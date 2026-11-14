package mysql

import (
	"context"
	"errors"

	"cloudenest_go/internal/blob"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func verifyStorageObject(ctx context.Context, storage model.Storage) error {
	return blob.VerifyStorageObject(ctx, storage)
}

// Preserve an observed current version before replacing its pointer. Missing
// historical versions are not invented; legacy snapshots have no claimed author.
func preserveCurrentVersion(tx *gorm.DB, file model.File, creator *int64, comment string) error {
	if file.IsFolder != 0 || file.StorageID == nil || file.CurrentVersion <= 0 {
		return contract.ErrConflict
	}
	var storage model.Storage
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&storage, *file.StorageID).Error; err != nil {
		return err
	}
	if err := verifyStorageObject(tx.Statement.Context, storage); err != nil {
		return err
	}
	if file.Size != *storage.FileSize {
		return contract.ErrStorageIntegrity
	}
	var version model.FileVersion
	err := tx.Where("file_id=? AND version=?", file.ID, file.CurrentVersion).First(&version).Error
	if err == nil {
		if version.StorageID != storage.ID || version.Size == nil || *version.Size != file.Size {
			return contract.ErrStorageIntegrity
		}
		// This content is already the authorized current file. Preserve that
		// capability when an explicit version write snapshots legacy metadata.
		if !version.SourceVerified {
			return tx.Model(&version).Update("source_verified", true).Error
		}
		return nil
	}
	if err != gorm.ErrRecordNotFound {
		return err
	}
	size := file.Size
	return tx.Create(&model.FileVersion{FileID: file.ID, Version: file.CurrentVersion, StorageID: storage.ID, Size: &size, FileType: file.FileType, SourceVerified: true, CreateUser: creator, Comment: comment}).Error
}

// Pre-G01 rows may contain unauthorized storage IDs. Association alone is not
// provenance. Unknown history can only disclose bytes the caller can read now;
// new verified rows retain ordinary file-history permissions after source expiry.
func requireHistoricalSource(tx *gorm.DB, uid int64, file model.File, historical model.FileVersion, lockSource bool) error {
	if historical.SourceVerified || (file.StorageID != nil && *file.StorageID == historical.StorageID) {
		return nil
	}
	var source model.File
	candidates := fileAccessScope(tx.Table("file f").Select("f.*"), "f", uid, false).
		Where("f.storage_id=? AND f.is_deleted=0 AND f.is_folder=0", historical.StorageID)
	if lockSource {
		// Restoring changes the destination's bytes. An unverified row can only
		// source a workspace file if the destination is in that same workspace.
		candidates = candidates.Where("(f.workspace_id <=> ? OR NOT EXISTS (SELECT 1 FROM workspace w WHERE w.id=f.workspace_id AND w.root_file_id IS NOT NULL))", file.WorkspaceID)
	}
	err := candidates.Order("f.id").Take(&source).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return contract.ErrForbidden
	}
	if err != nil {
		return err
	}
	if lockSource {
		var current model.File
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&current, "id=? AND is_deleted=0 AND storage_id=?", source.ID, historical.StorageID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return contract.ErrForbidden
			}
			return err
		}
		return requireFilePermission(tx, uid, current, false)
	}
	return nil
}
