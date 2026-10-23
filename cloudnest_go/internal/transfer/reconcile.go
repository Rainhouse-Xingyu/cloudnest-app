package transfer

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"

	"cloudenest_go/internal/blob"
	"cloudenest_go/internal/quota"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/internal/store"
	"cloudenest_go/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type cleanupContractKey struct{}

var ErrPending = errors.New("transfer reconciliation has retained or blocked records")

type Report struct {
	Checked    int    `json:"checked"`
	Removed    int    `json:"removed"`
	Busy       int    `json:"busy"`
	Blocked    int    `json:"blocked"`
	VolumeID   string `json:"volume_id"`
	LimitBytes int64  `json:"limit_bytes"`
	Usage      Usage  `json:"usage"`
}

func (m *Manager) Reconcile(ctx context.Context) (Report, error) {
	var report Report
	if err := store.CheckTransferCleanupContract(m.db.WithContext(ctx)); err != nil {
		return report, unavailable(err)
	}
	ctx = context.WithValue(ctx, cleanupContractKey{}, true)
	root, err := m.openRoot(ctx, false)
	if err != nil {
		return report, unavailable(err)
	}
	id, path := root.id, root.path
	root.Close()
	report.VolumeID = id
	var volume model.TransferVolume
	if err := m.db.WithContext(ctx).First(&volume, "id=? AND root_path=?", id, path).Error; err != nil {
		return report, unavailable(err)
	}
	report.LimitBytes = volume.LimitBytes
	var blocked int64
	if err := m.db.WithContext(ctx).Model(&model.TransferArtifact{}).Where("volume_id=? AND state='blocked'", id).Count(&blocked).Error; err != nil {
		return report, unavailable(err)
	}
	report.Blocked = int(blocked)
	var tokens []string
	if err := m.db.WithContext(ctx).Table("transfer_artifact AS a").Joins("LEFT JOIN upload_task AS t ON t.id=a.task_id").Where("a.volume_id=? AND a.state NOT IN ('done','published','blocked') AND (a.state IN ('discard','deleting') OR (a.state<>'chunk' AND a.expires_at<=UTC_TIMESTAMP(6)) OR t.id IS NULL OR t.status NOT IN (0,2) OR t.expires_at<=UTC_TIMESTAMP(6))", id).Order("a.created_at,a.token").Limit(64).Pluck("a.token", &tokens).Error; err != nil {
		return report, unavailable(err)
	}
	for _, token := range tokens {
		result, err := m.reconcileOne(ctx, token)
		report.Checked++
		if err != nil {
			return report, unavailable(err)
		}
		switch result {
		case "removed":
			report.Removed++
		case "busy":
			report.Busy++
		case "blocked":
			report.Blocked++
		}
	}
	report.Usage, err = usage(m.db.WithContext(ctx), id)
	if err != nil {
		return report, err
	}
	if volume.LimitBytes <= 0 || volume.LimitBytes > 4<<40 || volume.MinFreeBytes < 0 {
		report.Usage.Broken++
	}
	if report.Blocked > 0 || report.Busy > 0 || report.Usage.Broken > 0 {
		return report, ErrPending
	}
	return report, nil
}

// reconcileOne obtains an exclusive kernel lock before recording deletion. It
// never frees a reservation while any compliant process still has an IO lock.
func (m *Manager) reconcileOne(ctx context.Context, token string) (string, error) {
	if ctx.Value(cleanupContractKey{}) != true {
		if err := store.CheckTransferCleanupContract(m.db.WithContext(ctx)); err != nil {
			return "", unavailable(err)
		}
	}
	root, err := m.openRoot(ctx, false)
	if err != nil {
		return "", err
	}
	defer root.Close()
	var before model.TransferArtifact
	if err := m.db.WithContext(ctx).First(&before, "token=?", token).Error; err != nil {
		return "", err
	}
	if before.VolumeID != root.id || !validRelative(before.Kind, before.Token, before.RelativePath) {
		return "blocked", nil
	}
	if before.State == "done" || before.State == "published" {
		return "retained", nil
	}
	var file *os.File
	missing := false
	if err := blob.RegularPath(root.root, before.RelativePath); err != nil {
		if isMissing(err) {
			missing = true
		} else {
			return m.block(ctx, root, before)
		}
	} else {
		file, err = root.root.OpenFile(before.RelativePath, os.O_RDWR, 0)
		if err != nil {
			return "", err
		}
		defer file.Close()
		if err := lockFile(ctx, file, false, false); err != nil {
			if errors.Is(err, errLocked) {
				return "busy", nil
			}
			return "", err
		}
		device, inode, err := fileIdentity(file)
		if err != nil {
			return "", err
		}
		info, err := file.Stat()
		if err != nil {
			return "", err
		}
		// An intent with no persisted inode is never adopted, even if a file exists
		// at its reserved random name. A partial registration is operator-visible.
		if before.Inode == 0 || device != before.Device || inode != before.Inode || info.Size() < 0 || info.Size() > before.Bytes {
			return m.block(ctx, root, before)
		}

	}
	eligible, blocked := false, false
	err = m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := quota.Begin(tx, before.UserID); err != nil {
			return err
		}
		if _, err := m.volume(tx, root, false); err != nil {
			return err
		}
		var task model.UploadTask
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&task, before.TaskID).Error; err != nil {
			return err
		}
		var current model.TransferArtifact
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "token=?", token).Error; err != nil {
			return err
		}
		if current.VolumeID != root.id || current.RelativePath != before.RelativePath || current.UserID != before.UserID || current.TaskID != before.TaskID {
			return contract.ErrTransferUnavailable
		}
		if current.State == "done" || current.State == "published" {
			return nil
		}
		var taskLive, leaseLive int64
		if err := tx.Model(&model.UploadTask{}).Where("id=? AND status IN (0,2) AND expires_at>UTC_TIMESTAMP(6)", task.ID).Count(&taskLive).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.TransferArtifact{}).Where("token=? AND expires_at>UTC_TIMESTAMP(6)", token).Count(&leaseLive).Error; err != nil {
			return err
		}
		if current.State == "chunk" && taskLive != 0 {
			return nil
		}
		if (current.State == "intent" || current.State == "writing" || current.State == "ready") && leaseLive != 0 && taskLive != 0 {
			return nil
		}
		if current.State == "blocked" {
			return nil
		}
		// Never consume a path after handoff, including aliases created outside the
		// normal publisher. Unknown metadata is retained rather than guessed away.
		var storageRefs int64
		if err := tx.Model(&model.Storage{}).Where("file_path=?", filepath.Join(root.path, current.RelativePath)).Count(&storageRefs).Error; err != nil {
			return err
		}
		if storageRefs != 0 {
			if err := tx.Model(&current).Update("state", "blocked").Error; err != nil {
				return err
			}
			blocked = true
			return record(tx, current.UserID, "transfer_blocked", token)
		}
		if current.State == "chunk" {
			if err := tx.Where("artifact_token=? AND upload_id=?", token, task.UploadID).Delete(&model.UploadChunk{}).Error; err != nil {
				return err
			}
		}
		var refs int64
		if err := tx.Model(&model.UploadChunk{}).Where("artifact_token=? OR chunk_path=?", token, filepath.Join(root.path, current.RelativePath)).Count(&refs).Error; err != nil {
			return err
		}
		if refs != 0 {
			if err := tx.Model(&current).Update("state", "blocked").Error; err != nil {
				return err
			}
			blocked = true
			return record(tx, current.UserID, "transfer_blocked", token)
		}
		if err := tx.Model(&current).Update("state", "deleting").Error; err != nil {
			return err
		}
		if err := record(tx, current.UserID, "transfer_delete_intent", token); err != nil {
			return err
		}
		eligible = true
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return "", err
	}
	if !eligible {
		if blocked || before.State == "blocked" {
			return "blocked", nil
		}
		return "retained", nil
	}
	// The committed deleting state fences every publisher; the file remains locked
	// until unlink+directory sync and durable budget release complete.
	if !missing {
		if err := root.root.Remove(before.RelativePath); err != nil && !isMissing(err) {
			return "", err
		}
	}
	if err := syncDirs(root.root, []string{filepath.Dir(before.RelativePath)}); err != nil {
		if !isMissing(err) {
			return "", err
		}
	}
	err = m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := quota.Begin(tx, before.UserID); err != nil {
			return err
		}
		if _, err := m.volume(tx, root, false); err != nil {
			return err
		}
		var task model.UploadTask
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&task, before.TaskID).Error; err != nil {
			return err
		}
		var current model.TransferArtifact
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "token=?", token).Error; err != nil {
			return err
		}
		if current.State != "deleting" {
			return contract.ErrTransferUnavailable
		}
		if err := tx.Model(&current).Update("state", "done").Error; err != nil {
			return err
		}
		if task.MergeArtifact == token {
			if err := tx.Model(&task).Update("merge_artifact", "").Error; err != nil {
				return err
			}
		}
		return record(tx, current.UserID, "transfer_delete_done", token)
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return "", err
	}
	return "removed", nil
}
func (m *Manager) block(ctx context.Context, root *rootScope, before model.TransferArtifact) (string, error) {
	err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := quota.Begin(tx, before.UserID); err != nil {
			return err
		}
		if _, err := m.volume(tx, root, false); err != nil {
			return err
		}
		var task model.UploadTask
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&task, before.TaskID).Error; err != nil {
			return err
		}
		var current model.TransferArtifact
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "token=?", before.Token).Error; err != nil {
			return err
		}
		if current.State == "done" || current.State == "published" {
			return nil
		}
		if err := tx.Model(&current).Update("state", "blocked").Error; err != nil {
			return err
		}
		return record(tx, current.UserID, "transfer_blocked", current.Token)
	})
	return "blocked", err
}
