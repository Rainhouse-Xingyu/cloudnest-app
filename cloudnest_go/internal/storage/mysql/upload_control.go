package mysql

import (
	"context"
	"fmt"
	"math"

	"cloudenest_go/internal/quota"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Controls serialize with every writer on the owner/task locks. Expected epoch
// prevents a delayed duplicate pause/resume from reversing a newer intention.
func (r *FileRepo) ControlUploadTask(ctx context.Context, uid int64, id string, expected int64, paused bool, ip string) (model.UploadTask, error) {
	var task model.UploadTask
	err := fileMetadataTransaction(ctx, r.db, func(tx *gorm.DB) error {
		if err := quota.Begin(tx, uid); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("upload_id=?", id).First(&task).Error; err != nil {
			return err
		}
		if task.UserID == nil || *task.UserID != uid {
			return contract.ErrForbidden
		}
		if task.TransferProtocol != 1 || task.ChunkProtocol != 1 || task.TransferEpoch < 0 || expected < 0 {
			return contract.ErrUploadContract
		}
		if err := quota.RequireRetainedTask(tx, task); err != nil {
			return err
		}
		target := int8(0)
		action := "upload_resume"
		if paused {
			target = 2
			action = "upload_pause"
		}
		if task.TransferEpoch != expected {
			if task.Status == target && expected < math.MaxInt64 && task.TransferEpoch == expected+1 {
				return nil
			}
			return contract.ErrUploadContract
		}
		if task.Status == target {
			return nil
		}
		if task.TransferEpoch == math.MaxInt64 {
			return contract.ErrUploadContract
		}
		if !paused {
			if err := requireWriteParent(tx, uid, task.ParentID); err != nil {
				return err
			}
		}
		next := task.TransferEpoch + 1
		result := tx.Model(&task).Where("status=? AND transfer_epoch=?", task.Status, task.TransferEpoch).Updates(map[string]any{"status": target, "transfer_epoch": next})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return contract.ErrUploadContract
		}
		task.Status = target
		task.TransferEpoch = next
		if err := quota.Refresh(tx, uid, !paused); err != nil {
			return err
		}
		return tx.Create(&model.OperationLog{UserID: &uid, Action: action, Status: "success", Detail: fmt.Sprintf("upload_id=%s epoch=%d", id, next), ClientIP: ip}).Error
	})
	return task, err
}
