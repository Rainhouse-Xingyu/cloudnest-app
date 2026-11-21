package mysql

import (
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	"context"
	"errors"
	"fmt"
	driver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// A unique upload_id arbitrates cancellation versus allocation. A missing task
// becomes a terminal v3 tombstone in one transaction with its audit. If an
// allocator wins that race, cancel its real reservation or return its receipt.
func (r *FileRepo) CancelUploadInitialization(ctx context.Context, uid int64, key, ip string) (contract.UploadInitializationCancellation, error) {
	id, err := contract.UploadInitializationID(uid, key)
	if err != nil {
		return contract.UploadInitializationCancellation{}, err
	}
	for attempt := 0; attempt < 4; attempt++ {
		var task model.UploadTask
		err := r.db.WithContext(ctx).First(&task, "upload_id=?", id).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				zero := int64(0)
				one, count := 1, 0
				task = model.UploadTask{UploadID: id, UserID: &uid, ContractVersion: 3, Status: 3, TransferProtocol: 1, ChunkProtocol: 1, TotalSize: &zero, ChunkSize: &one, TotalChunks: &count}
				if err := tx.Create(&task).Error; err != nil {
					return err
				}
				return tx.Create(&model.OperationLog{UserID: &uid, Action: "upload_initialization_cancel", Status: "success", Detail: fmt.Sprintf("upload_id=%s canceled before allocation", id), ClientIP: ip}).Error
			})
			if err == nil {
				return contract.UploadInitializationCancellation{UploadID: id, Status: 3}, nil
			}
			var duplicate *driver.MySQLError
			if errors.As(err, &duplicate) && duplicate.Number == 1062 {
				continue
			}
			return contract.UploadInitializationCancellation{}, err
		}
		if err != nil {
			return contract.UploadInitializationCancellation{}, err
		}
		if task.UserID == nil || *task.UserID != uid {
			return contract.UploadInitializationCancellation{}, contract.ErrForbidden
		}
		if _, err := contract.UploadQuotaUser(task); err != nil {
			return contract.UploadInitializationCancellation{}, err
		}
		if task.Status == 1 {
			file, err := contract.UploadReceipt(task, uid, task.ParentID)
			if err != nil {
				return contract.UploadInitializationCancellation{}, err
			}
			return contract.UploadInitializationCancellation{UploadID: id, Status: 1, File: &file, Version: contract.UploadVersionBinding(task)}, nil
		}
		if task.Status == 3 {
			return contract.UploadInitializationCancellation{UploadID: id, Status: 3, Version: contract.UploadVersionBinding(task)}, nil
		}
		if err := r.CancelUploadTask(ctx, uid, id, ip); err != nil {
			// A completion may have won after the first read. Only its stored receipt
			// can resolve that outcome; no fabricated success or guessed file lookup.
			if errors.Is(err, contract.ErrUploadContract) {
				continue
			}
			return contract.UploadInitializationCancellation{}, err
		}
		return contract.UploadInitializationCancellation{UploadID: id, Status: 3, Version: contract.UploadVersionBinding(task)}, nil
	}
	return contract.UploadInitializationCancellation{}, contract.ErrConflict
}
