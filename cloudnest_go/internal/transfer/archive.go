package transfer

import (
	"context"
	"encoding/hex"
	"io"
	"os"
	"time"

	"cloudenest_go/internal/quota"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const archiveQuantum int64 = 1 << 20
const maxArchiveBytes int64 = 1 << 40

func artifactTaskForUpdate(tx *gorm.DB, artifact model.TransferArtifact) (model.UploadTask, error) {
	if requestArtifact(artifact.Kind) {
		if artifact.TaskID != 0 || artifact.TransferEpoch != 0 {
			return model.UploadTask{}, contract.ErrTransferUnavailable
		}
		return model.UploadTask{}, nil
	}
	var task model.UploadTask
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&task, artifact.TaskID).Error
	return task, err
}

// Called with the handle mutex held. The capacity change commits before any
// payload write, and unknown commit outcomes leave durable charges in place.
func (h *Handle) growTemporary(add int64) error {
	maximum, quantum := maxArchiveBytes, archiveQuantum
	if h.artifact.Kind == "thumbnail" {
		maximum, quantum = MaxThumbnailBytes, 64<<10
	}
	if add < 0 || add > maximum-h.written {
		return contract.ErrTransferSpace
	}
	need := h.written + add
	target := min(((need+quantum-1)/quantum)*quantum, maximum)
	ctx, stop := context.WithTimeout(h.ctx, 5*time.Second)
	defer stop()
	err := h.manager.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := quota.Begin(tx, h.artifact.UserID); err != nil {
			return err
		}
		root := &rootScope{id: h.artifact.VolumeID, path: h.root}
		volume, err := h.manager.volume(tx.Clauses(clause.Locking{Strength: "UPDATE"}), root, false)
		if err != nil {
			return err
		}
		var row model.TransferArtifact
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("token=? AND state='writing' AND expires_at>UTC_TIMESTAMP(6)", h.Token()).First(&row).Error; err != nil {
			return err
		}
		if row.Kind != h.artifact.Kind || row.TaskID != 0 || row.TransferEpoch != 0 || row.VolumeID != h.artifact.VolumeID || row.UserID != h.artifact.UserID || row.Bytes != h.artifact.Bytes || row.Device != h.artifact.Device || row.Inode != h.artifact.Inode {
			return contract.ErrTransferUnavailable
		}
		usage, err := usage(tx, row.VolumeID)
		if err != nil {
			return err
		}
		if usage.Broken != 0 || volume.LimitBytes <= 0 || volume.LimitBytes > 4<<40 || volume.MinFreeBytes < 0 || usage.Charged < row.Bytes || usage.Unwritten < row.Bytes {
			return contract.ErrTransferUnavailable
		}
		free, err := diskFree(h.root)
		if err != nil {
			return err
		}
		if volume.MinFreeBytes > free || usage.Unwritten-row.Bytes > free-volume.MinFreeBytes {
			return contract.ErrTransferSpace
		}
		capacity := min(volume.LimitBytes-(usage.Charged-row.Bytes), free-volume.MinFreeBytes-(usage.Unwritten-row.Bytes))
		if row.Kind == "thumbnail" {
			_, charged, err := thumbnailUsage(tx, row.VolumeID)
			if err != nil {
				return err
			}
			capacity = min(capacity, thumbnailCapacity(volume)-(charged-row.Bytes))
		}
		target = min(target, capacity)
		if target < need {
			return contract.ErrTransferSpace
		}
		if err := tx.Model(&row).Update("bytes", target).Error; err != nil {
			return err
		}
		return record(tx, row.UserID, "transfer_"+row.Kind+"_grow", row.Token)
	})
	if err != nil {
		return unavailable(err)
	}
	h.artifact.Bytes = target
	return nil
}

// SealArchive seals the bytes written by archive/zip after its final Close.
func (h *Handle) SealArchive() error {
	h.mu.Lock()
	if h.artifact.Kind != "archive" {
		h.mu.Unlock()
		return contract.ErrInvalidInput
	}
	size, digest := h.written, hex.EncodeToString(h.digest.Sum(nil))
	h.mu.Unlock()
	return h.Seal(size, digest)
}

type archiveContextReader struct {
	ctx context.Context
	in  io.Reader
}

func (r archiveContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.in.Read(p)
}

// ArchiveReader retains ownership in Handle. Callers must keep the handle open
// through the HTTP response; the kernel lock protects even a stale DB lease.
func (h *Handle) ArchiveReader() (*os.File, string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || !h.sealed || h.artifact.Kind != "archive" {
		return nil, "", contract.ErrTransferUnavailable
	}
	if err := h.ctx.Err(); err != nil {
		return nil, "", context.Cause(h.ctx)
	}
	digest, err := verifyArtifactFile(h.ctx, h.file, h.artifact)
	if err != nil {
		return nil, "", err
	}
	return h.file, digest, nil
}
