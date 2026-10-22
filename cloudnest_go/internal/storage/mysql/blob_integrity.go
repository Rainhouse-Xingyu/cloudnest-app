package mysql

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"

	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Published objects are never opened for writing. Stream verification is bounded
// in memory and observes cancellation. It is not a lease or a GC protocol.
func verifyStorageObject(ctx context.Context, storage model.Storage) error {
	if storage.FileSize == nil || *storage.FileSize < 0 || len(storage.FileHash) != 64 {
		return contract.ErrStorageIntegrity
	}
	if _, err := hex.DecodeString(storage.FileHash); err != nil {
		return contract.ErrStorageIntegrity
	}
	in, err := os.Open(storage.FilePath)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != *storage.FileSize {
		return contract.ErrStorageIntegrity
	}
	hash := sha256.New()
	buffer := make([]byte, 64*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := in.Read(buffer)
		total += int64(n)
		if total > *storage.FileSize {
			return contract.ErrStorageIntegrity
		}
		if n > 0 {
			_, _ = hash.Write(buffer[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if total != *storage.FileSize || !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), storage.FileHash) {
		return contract.ErrStorageIntegrity
	}
	return nil
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
	err := tx.Table("file f").Select("f.*").Joins("LEFT JOIN file_permission fp ON fp.file_id=f.id AND fp.user_id=?", uid).Where("f.storage_id=? AND f.is_deleted=0 AND f.is_folder=0", historical.StorageID).Where("f.user_id=? OR fp.permission IN ?", uid, []string{"read", "write"}).Order("f.id").First(&source).Error
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
