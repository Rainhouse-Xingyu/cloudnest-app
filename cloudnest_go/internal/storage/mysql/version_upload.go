package mysql

import (
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	"context"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (r *FileRepo) ResolveVersionUploadTarget(ctx context.Context, uid, fileID int64, base int) (model.File, error) {
	var file model.File
	err := fileReadSnapshot(ctx, r.db, func(tx *gorm.DB) error {
		if err := tx.First(&file, "id=? AND is_deleted=0", fileID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return contract.ErrForbidden
			}
			return err
		}
		var count int64
		if err := fileAccessScope(tx.Table("file f"), "f", uid, true).Where("f.id=?", fileID).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return contract.ErrForbidden
		}
		if file.IsFolder != 0 {
			return contract.ErrInvalidInput
		}
		if file.CurrentVersion != base {
			return contract.ErrConflict
		}
		return nil
	})
	return file, err
}

// Called under the logical owner ledger and the upload task lock. Locking ACL
// reads ensure a revoked collaborator cannot commit bytes or publish a version.
func versionUploadTarget(tx *gorm.DB, task model.UploadTask) (model.File, error) {
	owner, err := contract.UploadQuotaUser(task)
	if err != nil || task.ContractVersion != 2 {
		return model.File{}, contract.ErrUploadContract
	}
	var file model.File
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&file, "id=? AND is_deleted=0", *task.VersionFileID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return file, contract.ErrForbidden
		}
		return file, err
	}
	if file.UserID != owner {
		return file, contract.ErrUploadContract
	}
	if err := requireFilePermission(tx, *task.UserID, file, true); err != nil {
		return file, err
	}
	if file.IsFolder != 0 {
		return file, contract.ErrInvalidInput
	}
	if file.CurrentVersion != task.VersionBase {
		return file, contract.ErrConflict
	}
	return file, nil
}

func (r *FileRepo) uploadLedgerForActor(ctx context.Context, uid int64, id string) (int64, error) {
	var task model.UploadTask
	if err := r.db.WithContext(ctx).First(&task, "upload_id=?", id).Error; err != nil {
		return 0, err
	}
	if task.UserID == nil || *task.UserID != uid {
		return 0, contract.ErrForbidden
	}
	if task.ContractVersion == 0 {
		return uid, nil
	} // Preserve old terminal cancellation behavior.
	return contract.UploadQuotaUser(task)
}
