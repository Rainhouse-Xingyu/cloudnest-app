package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"cloudenest_go/internal/blob"
	"cloudenest_go/internal/quota"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	"gorm.io/gorm"
)

type Handle struct {
	digest         hash.Hash
	manager        *Manager
	artifact       model.TransferArtifact
	file           *os.File
	root           string
	ctx            context.Context
	cancel         context.CancelCauseFunc
	done           chan struct{}
	mu             sync.Mutex
	written        int64
	sealed, closed bool
}

func newHandle(m *Manager, a model.TransferArtifact, f *os.File, root string, parent context.Context) *Handle {
	ctx, cancel := context.WithCancelCause(parent)
	h := &Handle{digest: sha256.New(), manager: m, artifact: a, file: f, root: root, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	go h.renew()
	return h
}
func (h *Handle) Context() context.Context { return h.ctx }
func (h *Handle) Token() string            { return h.artifact.Token }
func (h *Handle) Path() string             { return filepath.Join(h.root, h.artifact.RelativePath) }
func (h *Handle) Root() string             { return h.root }
func (h *Handle) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.sealed {
		return 0, io.ErrClosedPipe
	}
	if err := h.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(p)) > h.artifact.Bytes-h.written {
		return 0, contract.ErrTransferSpace
	}
	n, err := h.file.Write(p)
	h.written += int64(n)
	if n > 0 {
		_, _ = h.digest.Write(p[:n])
	}
	return n, err
}
func (h *Handle) Seal(size int64, digest string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.sealed || size != h.written || size < 0 || !blob.ValidToken(digest) || hex.EncodeToString(h.digest.Sum(nil)) != digest {
		return contract.ErrInvalidInput
	}
	if err := h.ctx.Err(); err != nil {
		return err
	}
	if err := h.file.Sync(); err != nil {
		return err
	}
	root, err := os.OpenRoot(h.root)
	if err != nil {
		return err
	}
	err = syncArtifactDirs(root, h.artifact.RelativePath)
	err = errors.Join(err, root.Close())
	if err != nil {
		return err
	}
	err = h.manager.db.WithContext(h.ctx).Transaction(func(tx *gorm.DB) error {
		if err := quota.Begin(tx, h.artifact.UserID); err != nil {
			return err
		}
		scope := &rootScope{id: h.artifact.VolumeID, path: h.root}
		if _, err := h.manager.volume(tx, scope, false); err != nil {
			return err
		}
		task, err := taskForUpdate(tx, h.artifact.UserID, h.artifact.TaskID)
		if err != nil {
			return err
		}
		if err := live(tx, task); err != nil {
			return err
		}
		if task.TransferEpoch != h.artifact.TransferEpoch {
			return contract.ErrUploadContract
		}
		result := tx.Model(&model.TransferArtifact{}).Where("token=? AND state='writing' AND expires_at>UTC_TIMESTAMP(6)", h.Token()).Updates(map[string]any{"state": "ready", "size": size, "bytes": max(size, int64(1)), "sha256": digest})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return contract.ErrUploadContract
		}
		return nil
	})
	if err != nil {
		return unavailable(err)
	}
	h.sealed = true
	return nil
}
func (h *Handle) renew() {
	defer close(h.done)
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-h.ctx.Done():
			return
		case <-ticker.C:
			ctx, stop := context.WithTimeout(h.ctx, 5*time.Second)
			result := h.manager.db.WithContext(ctx).Model(&model.TransferArtifact{}).Where("token=? AND state IN ('writing','ready') AND expires_at>UTC_TIMESTAMP(6) AND EXISTS (SELECT 1 FROM upload_task WHERE id=? AND status=0 AND transfer_epoch=? AND expires_at>UTC_TIMESTAMP(6))", h.Token(), h.artifact.TaskID, h.artifact.TransferEpoch).Update("expires_at", gorm.Expr("DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 90 SECOND)"))
			stop()
			if result.Error != nil || result.RowsAffected != 1 {
				cause := contract.ErrUploadContract
				if result.Error != nil {
					cause = contract.ErrTransferUnavailable
				}
				h.cancel(cause)
				return
			}
		}
	}
}

// Close never unlinks an open descriptor. Known-unused artifacts are durably
// discarded, then use the same bounded reconciler as crash recovery.
func (h *Handle) Close(discard bool) error {
	defer h.manager.untrack(h)
	h.cancel(nil)
	<-h.done
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	err := h.file.Close()
	h.mu.Unlock()
	if err != nil {
		return err
	}
	if discard {
		ctx, stop := context.WithTimeout(context.WithoutCancel(h.ctx), 5*time.Second)
		defer stop()
		if err := h.manager.discard(ctx, h.artifact); err != nil {
			return unavailable(err)
		}
		_, err := h.manager.reconcileOne(ctx, h.Token())
		return err
	}
	return nil
}
