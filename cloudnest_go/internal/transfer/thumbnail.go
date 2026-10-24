package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"time"

	"cloudenest_go/internal/blob"
	"cloudenest_go/internal/quota"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const MaxThumbnailBytes int64 = 8 << 20
const MaxThumbnailCacheBytes int64 = 512 << 20
const MaxThumbnailEntries int64 = 4096

func requestArtifact(kind string) bool { return kind == "archive" || kind == "thumbnail" }
func thumbnailCapacity(v model.TransferVolume) int64 {
	return min(MaxThumbnailCacheBytes, v.LimitBytes/4)
}
func thumbnailUsage(tx *gorm.DB, volume string) (count, charged int64, err error) {
	var result struct{ Entries, Charged int64 }
	err = tx.Raw("SELECT COUNT(*) AS entries,COALESCE(SUM(bytes),0) AS charged FROM transfer_artifact WHERE volume_id=? AND kind='thumbnail' AND state NOT IN ('done','published')", volume).Scan(&result).Error
	if err == nil && (result.Entries < 0 || result.Charged < 0) {
		err = contract.ErrTransferUnavailable
	}
	return result.Entries, result.Charged, err
}

// A keyed writer is charged from intent through retention and deletion. NULL
// cache keys on non-cache/retired rows allow a unique key per live generation.
func (m *Manager) CreateThumbnail(ctx context.Context, uid int64, key string) (*Handle, error) {
	for attempt := 0; attempt <= 16; attempt++ {
		h, err := m.create(ctx, uid, 0, "thumbnail", 1, 0, key)
		if !errors.Is(err, contract.ErrTransferSpace) || attempt == 16 {
			return h, err
		}
		removed, evictErr := m.evictThumbnail(ctx)
		if evictErr != nil {
			return nil, unavailable(evictErr)
		}
		if !removed {
			return nil, err
		}
	}
	return nil, contract.ErrTransferSpace
}

// SealThumbnail publishes only complete durable JPEG bytes. Holding the root
// gate fences cleanup while converting the builder lock to a reader lock.
func (h *Handle) SealThumbnail() error {
	h.mu.Lock()
	if h.artifact.Kind != "thumbnail" {
		h.mu.Unlock()
		return contract.ErrInvalidInput
	}
	size, digest := h.written, hex.EncodeToString(h.digest.Sum(nil))
	h.mu.Unlock()
	if size <= 0 || size > MaxThumbnailBytes {
		return contract.ErrTransferSpace
	}
	if err := h.Seal(size, digest); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	root, err := h.manager.openRoot(h.ctx, false)
	if err != nil {
		return unavailable(err)
	}
	defer root.Close()
	if err := lockFile(h.ctx, h.file, true, false); err != nil {
		return unavailable(err)
	}
	err = h.manager.db.WithContext(h.ctx).Transaction(func(tx *gorm.DB) error {
		if err := quota.Begin(tx, h.artifact.UserID); err != nil {
			return err
		}
		if _, err := h.manager.volume(tx, root, false); err != nil {
			return err
		}
		var row model.TransferArtifact
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("token=? AND state='ready' AND expires_at>UTC_TIMESTAMP(6)", h.Token()).First(&row).Error; err != nil {
			return err
		}
		if row.Kind != "thumbnail" || row.VolumeID != root.id || row.TaskID != 0 || row.TransferEpoch != 0 || row.UserID != h.artifact.UserID || row.CacheKey == nil || h.artifact.CacheKey == nil || *row.CacheKey != *h.artifact.CacheKey || row.Size != size || row.SHA256 != digest || row.Device != h.artifact.Device || row.Inode != h.artifact.Inode {
			return contract.ErrTransferUnavailable
		}
		if err := tx.Model(&row).Updates(map[string]any{"state": "cached", "expires_at": gorm.Expr("DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 24 HOUR)")}).Error; err != nil {
			return err
		}
		return record(tx, row.UserID, "transfer_thumbnail_cache", row.Token)
	})
	return unavailable(err)
}

type ThumbnailReader struct {
	File   *os.File
	Digest string
	Token  string
}

func (r *ThumbnailReader) Close() error { return r.File.Close() }

// Lookup grants no file authorization: callers must authorize and pin the
// current source first. Only this registry's immutable cache generations open.
func (m *Manager) OpenThumbnail(ctx context.Context, key string) (*ThumbnailReader, error) {
	if !blob.ValidToken(key) {
		return nil, contract.ErrInvalidInput
	}
	root, err := m.openRoot(ctx, true)
	if err != nil {
		return nil, unavailable(err)
	}
	var row model.TransferArtifact
	err = m.db.WithContext(ctx).Where("volume_id=? AND cache_key=?", root.id, key).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		root.Close()
		return nil, nil
	}
	if err != nil {
		root.Close()
		return nil, unavailable(err)
	}
	volume, err := m.volume(m.db.WithContext(ctx), root, false)
	if err != nil {
		root.Close()
		return nil, unavailable(err)
	}
	if volume.LimitBytes <= 0 || volume.LimitBytes > 4<<40 || volume.MinFreeBytes < 0 {
		root.Close()
		return nil, contract.ErrTransferUnavailable
	}
	if row.Kind != "thumbnail" || row.TaskID != 0 || row.TransferEpoch != 0 || !validRelative(row.Kind, row.Token, row.RelativePath) {
		root.Close()
		return nil, contract.ErrTransferUnavailable
	}
	var live int64
	if err := m.db.WithContext(ctx).Model(&model.TransferArtifact{}).Where("token=? AND expires_at>UTC_TIMESTAMP(6)", row.Token).Count(&live).Error; err != nil {
		root.Close()
		return nil, unavailable(err)
	}
	if live == 0 && row.State != "blocked" {
		root.Close()
		result, err := m.reconcileOne(ctx, row.Token)
		if err != nil {
			return nil, unavailable(err)
		}
		if result == "removed" {
			return nil, nil
		}
		if result == "busy" {
			return nil, contract.ErrTransferBusy
		}
		return nil, contract.ErrTransferUnavailable
	}
	if row.State != "cached" {
		root.Close()
		if row.State == "intent" || row.State == "writing" || row.State == "ready" {
			return nil, contract.ErrTransferBusy
		}
		return nil, contract.ErrTransferUnavailable
	}
	if row.Size <= 0 || row.Size > MaxThumbnailBytes || row.Bytes != row.Size || !blob.ValidToken(row.SHA256) {
		root.Close()
		return nil, contract.ErrStorageIntegrity
	}
	if err := blob.RegularPath(root.root, row.RelativePath); err != nil {
		root.Close()
		return nil, contract.ErrStorageIntegrity
	}
	file, err := root.root.Open(row.RelativePath)
	if err != nil {
		root.Close()
		return nil, unavailable(err)
	}
	if err := lockFile(ctx, file, true, false); err != nil {
		file.Close()
		root.Close()
		if errors.Is(err, errLocked) {
			return nil, contract.ErrTransferBusy
		}
		return nil, unavailable(err)
	}
	device, inode, err := fileIdentity(file)
	root.Close()
	if err != nil || device != row.Device || inode != row.Inode {
		file.Close()
		return nil, contract.ErrStorageIntegrity
	}
	digest, err := verifyArtifactFile(ctx, file, row)
	if err != nil {
		file.Close()
		return nil, err
	}
	return &ThumbnailReader{File: file, Digest: digest, Token: row.Token}, nil
}

func verifyArtifactFile(ctx context.Context, file *os.File, row model.TransferArtifact) (string, error) {
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	device, inode, err := fileIdentity(file)
	if err != nil || device != row.Device || inode != row.Inode || !info.Mode().IsRegular() || info.Size() != row.Size {
		return "", contract.ErrStorageIntegrity
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	hash := sha256.New()
	n, err := io.CopyBuffer(hash, archiveContextReader{ctx: ctx, in: file}, make([]byte, 64<<10))
	if err != nil {
		return "", err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if n != row.Size || digest != row.SHA256 {
		return "", contract.ErrStorageIntegrity
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return digest, nil
}

// Pressure eviction is FIFO and bounded, not a directory scan. A reader lock
// prevents even retirement; uncertain identities retain both bytes and key.
func (m *Manager) evictThumbnail(parent context.Context) (bool, error) {
	ctx, stop := context.WithTimeout(parent, 5*time.Second)
	defer stop()
	root, err := m.openRoot(ctx, false)
	if err != nil {
		return false, err
	}
	var candidates []model.TransferArtifact
	err = m.db.WithContext(ctx).Where("volume_id=? AND kind='thumbnail' AND state='cached'", root.id).Order("created_at,token").Limit(16).Find(&candidates).Error
	if err != nil {
		root.Close()
		return false, err
	}
	for _, row := range candidates {
		if !validRelative(row.Kind, row.Token, row.RelativePath) {
			continue
		}
		if err := blob.RegularPath(root.root, row.RelativePath); err != nil {
			continue
		}
		file, err := root.root.Open(row.RelativePath)
		if err != nil {
			continue
		}
		if err := lockFile(ctx, file, false, false); err != nil {
			file.Close()
			if errors.Is(err, errLocked) {
				continue
			}
			root.Close()
			return false, err
		}
		info, statErr := file.Stat()
		device, inode, idErr := fileIdentity(file)
		if statErr != nil || idErr != nil || device != row.Device || inode != row.Inode || !info.Mode().IsRegular() || info.Size() != row.Size {
			file.Close()
			continue
		}
		retired := false
		err = m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := quota.Begin(tx, row.UserID); err != nil {
				return err
			}
			volume, err := m.volume(tx, root, false)
			if err != nil {
				return err
			}
			if volume.LimitBytes <= 0 || volume.LimitBytes > 4<<40 || volume.MinFreeBytes < 0 {
				return contract.ErrTransferUnavailable
			}
			var current model.TransferArtifact
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "token=?", row.Token).Error; err != nil {
				return err
			}
			if current.State != "cached" {
				return nil
			}
			if current.Kind != "thumbnail" || current.TaskID != 0 || current.TransferEpoch != 0 || current.VolumeID != root.id || current.RelativePath != row.RelativePath || current.UserID != row.UserID || current.Device != device || current.Inode != inode || current.Size != info.Size() {
				return contract.ErrTransferUnavailable
			}
			if err := tx.Model(&current).Updates(map[string]any{"state": "discard", "cache_key": nil}).Error; err != nil {
				return err
			}
			if err := record(tx, current.UserID, "transfer_thumbnail_evict", current.Token); err != nil {
				return err
			}
			retired = true
			return nil
		})
		file.Close()
		if err != nil {
			root.Close()
			return false, err
		}
		if retired {
			root.Close()
			result, err := m.reconcileOne(ctx, row.Token)
			return result == "removed", err
		}
	}
	root.Close()
	return false, nil
}
