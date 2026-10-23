package transfer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cloudenest_go/internal/blob"
	"cloudenest_go/internal/config"
	"cloudenest_go/internal/quota"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const leaseSeconds = 90
const DefaultLimit int64 = 8 << 30
const DefaultMinFree int64 = 64 << 20

type Manager struct {
	db        *gorm.DB
	rootPath  string
	seedLimit int64
}

func NewManager(db *gorm.DB, cfg config.Config) *Manager {
	base := strings.TrimSpace(cfg.DataDir)
	if base == "" {
		base = "data"
	}
	limit := cfg.TransferTempBytes
	if limit == 0 {
		limit = DefaultLimit
	}
	return &Manager{db: db, rootPath: filepath.Join(base, "storage"), seedLimit: limit}
}

type Usage struct {
	Charged   int64 `json:"charged_bytes"`
	Unwritten int64 `json:"unwritten_bytes"`
	Broken    int64 `json:"broken_records"`
}

func usage(tx *gorm.DB, volume string) (Usage, error) {
	var result Usage
	err := tx.Raw(`SELECT COALESCE(SUM(bytes),0) AS charged,COALESCE(SUM(IF(state IN ('intent','writing'),bytes,0)),0) AS unwritten,COALESCE(SUM(IF(bytes<=0 OR size<0 OR state NOT IN ('intent','writing','ready','chunk','discard','deleting','blocked'),1,0)),0) AS broken FROM transfer_artifact WHERE volume_id=? AND state NOT IN ('done','published')`, volume).Scan(&result).Error
	if err != nil || result.Charged < 0 || result.Unwritten < 0 || result.Unwritten > result.Charged {
		return result, contract.ErrTransferUnavailable
	}
	return result, nil
}
func (m *Manager) volume(tx *gorm.DB, root *rootScope, create bool) (model.TransferVolume, error) {
	if create {
		row := model.TransferVolume{ID: root.id, RootPath: root.path, LimitBytes: m.seedLimit, MinFreeBytes: DefaultMinFree}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return row, err
		}
	}
	var volume model.TransferVolume
	query := tx
	if create {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.First(&volume, "id=?", root.id).Error; err != nil {
		return volume, err
	}
	if volume.RootPath != root.path {
		return volume, contract.ErrTransferUnavailable
	}
	return volume, nil
}
func taskForUpdate(tx *gorm.DB, uid, taskID int64) (model.UploadTask, error) {
	var task model.UploadTask
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&task, taskID).Error; err != nil {
		return task, err
	}
	if task.UserID == nil || *task.UserID != uid {
		return task, contract.ErrForbidden
	}
	if task.TransferProtocol != 1 {
		return task, contract.ErrUploadContract
	}
	return task, nil
}
func live(tx *gorm.DB, task model.UploadTask) error { return quota.RequireLiveTask(tx, task) }
func record(tx *gorm.DB, uid int64, action, token string) error {
	return tx.Create(&model.OperationLog{UserID: &uid, Action: action, Status: "success", Detail: "artifact=" + token}).Error
}

// Create registers and charges before creating a file. The root gate spans the
// filesystem creation/FD-lock handoff, including a lost database connection.
func (m *Manager) Create(ctx context.Context, uid, taskID int64, kind string, bytes int64) (*Handle, error) {
	parent := ctx
	operation, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	ctx = operation
	if m.seedLimit <= 0 || m.seedLimit > 4<<40 || bytes <= 0 || bytes > contract.MaxUploadFileBytes+1 || (kind != "chunk" && kind != "merge") {
		return nil, contract.ErrTransferUnavailable
	}
	root, err := m.openRoot(ctx, true)
	if err != nil {
		return nil, unavailable(err)
	}
	defer root.Close()
	token, err := blob.RandomToken()
	if err != nil {
		return nil, unavailable(err)
	}
	relative := artifactRelative(kind, token)
	if !validRelative(kind, token, relative) {
		return nil, contract.ErrTransferUnavailable
	}
	artifact := model.TransferArtifact{Token: token, VolumeID: root.id, RelativePath: relative, UserID: uid, TaskID: taskID, Kind: kind, State: "intent", Bytes: bytes, ExpiresAt: time.Now().UTC().Add(leaseSeconds * time.Second)}
	err = m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := quota.Begin(tx, uid); err != nil {
			return err
		}
		volume, err := m.volume(tx, root, true)
		if err != nil {
			return err
		}
		task, err := taskForUpdate(tx, uid, taskID)
		if err != nil {
			return err
		}
		if err := live(tx, task); err != nil {
			return err
		}
		if task.TransferVolume != "" && task.TransferVolume != root.id {
			return contract.ErrTransferUnavailable
		}
		if task.MergeArtifact != "" {
			return contract.ErrTransferBusy
		}
		if kind == "merge" {
			var active int64
			if err := tx.Model(&model.TransferArtifact{}).Where("volume_id=? AND kind='merge' AND state IN ('intent','writing','ready')", root.id).Count(&active).Error; err != nil {
				return err
			}
			if active >= 4 {
				return contract.ErrTransferBusy
			}
		}
		current, err := usage(tx, root.id)
		if err != nil {
			return err
		}
		if current.Broken != 0 || volume.LimitBytes <= 0 || volume.LimitBytes > 4<<40 || volume.MinFreeBytes < 0 {
			return contract.ErrTransferUnavailable
		}
		if current.Charged > math.MaxInt64-bytes || current.Charged+bytes > volume.LimitBytes {
			return contract.ErrTransferSpace
		}
		free, err := diskFree(root.path)
		if err != nil {
			return err
		}
		if volume.MinFreeBytes > free || current.Unwritten > free-volume.MinFreeBytes || bytes > free-volume.MinFreeBytes-current.Unwritten {
			return contract.ErrTransferSpace
		}
		if err := tx.Create(&artifact).Error; err != nil {
			return err
		}
		if err := tx.Model(&artifact).Update("expires_at", gorm.Expr("DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 90 SECOND)")).Error; err != nil {
			return err
		}
		updates := map[string]any{"transfer_volume": root.id}
		if kind == "merge" {
			updates["merge_artifact"] = token
		}
		if err := tx.Model(&task).Updates(updates).Error; err != nil {
			return err
		}
		return record(tx, uid, "transfer_reserve", token)
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, unavailable(err)
	}
	if err := blob.EnsurePrivateDir(root.root, filepath.Dir(relative)); err != nil {
		return nil, unavailable(errors.Join(err, m.abortNoFile(ctx, root, artifact)))
	}
	file, err := root.root.OpenFile(relative, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			err = errors.Join(err, m.abortNoFile(ctx, root, artifact))
		}
		return nil, unavailable(err)
	}
	owned := false
	defer func() {
		if !owned {
			file.Close()
		}
	}()
	if err := lockFile(ctx, file, false, false); err != nil {
		return nil, unavailable(err)
	}
	device, inode, err := fileIdentity(file)
	if err != nil {
		return nil, unavailable(err)
	}
	if err := errors.Join(file.Sync(), syncArtifactDirs(root.root, relative)); err != nil {
		return nil, unavailable(err)
	}
	err = m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := quota.Begin(tx, uid); err != nil {
			return err
		}
		if _, err := m.volume(tx, root, false); err != nil {
			return err
		}
		task, err := taskForUpdate(tx, uid, taskID)
		if err != nil {
			return err
		}
		if err := live(tx, task); err != nil {
			return err
		}
		update := tx.Model(&model.TransferArtifact{}).Where("token=? AND state='intent' AND expires_at>UTC_TIMESTAMP(6)", token).Updates(map[string]any{"state": "writing", "device": device, "inode": inode, "expires_at": gorm.Expr("DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 90 SECOND)")})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return contract.ErrUploadContract
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, unavailable(err)
	}
	artifact.State = "writing"
	artifact.Device = device
	artifact.Inode = inode
	handle := newHandle(m, artifact, file, root.path, parent)
	owned = true
	return handle, nil
}

// OpenChunk obtains a shared kernel lock while holding the same root gate used
// by cleanup. No unregistered path or cross-volume manifest can be adopted.
func (m *Manager) OpenChunk(ctx context.Context, uid, taskID int64, row model.UploadChunk) (*os.File, error) {
	root, err := m.openRoot(ctx, false)
	if err != nil {
		return nil, unavailable(err)
	}
	defer root.Close()
	var task model.UploadTask
	if err := m.db.WithContext(ctx).First(&task, taskID).Error; err != nil {
		return nil, unavailable(err)
	}
	if task.UserID == nil || *task.UserID != uid {
		return nil, contract.ErrForbidden
	}
	if task.Status != 0 || task.TransferProtocol != 1 || task.TransferVolume != root.id {
		return nil, contract.ErrUploadContract
	}
	var artifact model.TransferArtifact
	if err := m.db.WithContext(ctx).First(&artifact, "token=?", row.ArtifactToken).Error; err != nil {
		return nil, unavailable(err)
	}
	if artifact.Kind != "chunk" || artifact.State != "chunk" || artifact.UserID != uid || artifact.TaskID != taskID || artifact.VolumeID != root.id || row.ChunkSize == nil || artifact.Size != *row.ChunkSize || artifact.SHA256 != row.SHA256 || filepath.Join(root.path, artifact.RelativePath) != row.ChunkPath || !validRelative(artifact.Kind, artifact.Token, artifact.RelativePath) {
		return nil, contract.ErrStorageIntegrity
	}
	if err := blob.RegularPath(root.root, artifact.RelativePath); err != nil {
		return nil, contract.ErrStorageIntegrity
	}
	file, err := root.root.Open(artifact.RelativePath)
	if err != nil {
		return nil, unavailable(err)
	}
	if err := lockFile(ctx, file, true, false); err != nil {
		file.Close()
		return nil, unavailable(err)
	}
	device, inode, err := fileIdentity(file)
	if err != nil || device != artifact.Device || inode != artifact.Inode {
		file.Close()
		return nil, contract.ErrStorageIntegrity
	}
	return file, nil
}

func (m *Manager) discard(ctx context.Context, artifact model.TransferArtifact) error {
	root, err := m.openRoot(ctx, false)
	if err != nil {
		return err
	}
	defer root.Close()
	return m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := quota.Begin(tx, artifact.UserID); err != nil {
			return err
		}
		if _, err := m.volume(tx, root, false); err != nil {
			return err
		}
		var task model.UploadTask
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&task, artifact.TaskID).Error; err != nil {
			return err
		}
		var current model.TransferArtifact
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "token=?", artifact.Token).Error; err != nil {
			return err
		}
		if current.VolumeID != root.id || current.UserID != artifact.UserID || current.TaskID != artifact.TaskID {
			return contract.ErrTransferUnavailable
		}
		if current.State == "chunk" || current.State == "published" || current.State == "done" {
			return nil
		}
		var refs int64
		if err := tx.Model(&model.UploadChunk{}).Where("artifact_token=?", artifact.Token).Count(&refs).Error; err != nil {
			return err
		}
		if refs != 0 {
			return contract.ErrTransferUnavailable
		}
		if err := tx.Model(&current).Update("state", "discard").Error; err != nil {
			return err
		}
		return record(tx, current.UserID, "transfer_discard", current.Token)
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
}
func transactionError(err error) error {
	if err != nil {
		return fmt.Errorf("%w: %w", contract.ErrTransferUnavailable, err)
	}
	return nil
}

// Only the creating call can prove it never opened a payload file. A later
// reconciler cannot infer that proof from an intent or filename after a crash.
func (m *Manager) abortNoFile(parent context.Context, root *rootScope, artifact model.TransferArtifact) error {
	ctx, stop := context.WithTimeout(context.WithoutCancel(parent), 3*time.Second)
	defer stop()
	return m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := quota.Begin(tx, artifact.UserID); err != nil {
			return err
		}
		task, err := taskForUpdate(tx, artifact.UserID, artifact.TaskID)
		if err != nil {
			return err
		}
		var current model.TransferArtifact
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "token=?", artifact.Token).Error; err != nil {
			return err
		}
		if current.VolumeID != root.id || current.State != "intent" || current.Inode != 0 {
			return contract.ErrTransferUnavailable
		}
		if err := tx.Model(&current).Update("state", "done").Error; err != nil {
			return err
		}
		if task.MergeArtifact == artifact.Token {
			if err := tx.Model(&task).Update("merge_artifact", "").Error; err != nil {
				return err
			}
		}
		return record(tx, current.UserID, "transfer_no_file", current.Token)
	})
}
