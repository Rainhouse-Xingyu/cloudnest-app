package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	mysqlDriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Read committed avoids stale conflict queries after waiting for a namespace
// mutex. Retry only deadlock victims of metadata-only transactions (no disk IO).
func fileMetadataTransaction(ctx context.Context, db *gorm.DB, fn func(*gorm.DB) error) error {
	for attempt := 0; attempt < 3; attempt++ {
		err := db.WithContext(ctx).Transaction(fn, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		var mysqlErr *mysqlDriver.MySQLError
		if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1213 {
			return normalizeFileNameConstraintError(err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return contract.ErrConflict
}

func fileNamespaceKey(ownerID, parentID int64) string {
	if parentID == 0 {
		return fmt.Sprintf("root:%d", ownerID)
	}
	return fmt.Sprintf("folder:%d", parentID)
}

func lockFileNamespaces(tx *gorm.DB, keys ...string) error {
	sort.Strings(keys)
	previous := ""
	for _, key := range keys {
		if key == previous {
			continue
		}
		previous = key
		row := model.FileNamespace{Key: key}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, "namespace_key = ?", key).Error; err != nil {
			return err
		}
	}
	return nil
}

func nameExists(tx *gorm.DB, ownerID, parentID int64, name string, excludeID int64) (bool, error) {
	q := tx.Model(&model.File{}).Where("parent_id = ? AND filename = ? AND is_deleted = 0", parentID, name)
	if parentID == 0 {
		q = q.Where("user_id = ?", ownerID)
	}
	if excludeID > 0 {
		q = q.Where("id <> ?", excludeID)
	}
	var count int64
	err := q.Count(&count).Error
	return count > 0, err
}

// Call while holding the namespace lock, or for an uncommitted new parent
// exclusively owned by this transaction. Explicit create/rename reject;
// copy/move/upload/restore preserve both using the first available numeric suffix.
func allocateFileName(tx *gorm.DB, ownerID, parentID int64, base string, folder bool, excludeID int64, keepBoth bool) (string, error) {
	if err := contract.ValidateFileName(base); err != nil {
		return "", err
	}
	stem, ext := base, ""
	if dot := strings.LastIndex(base, "."); !folder && dot > 0 {
		stem, ext = base[:dot], base[dot:]
	}
	for n := 0; n < 10000; n++ {
		candidate := base
		if n > 0 {
			if !keepBoth {
				return "", contract.ErrConflict
			}
			suffix := fmt.Sprintf(" (%d)", n)
			budget := 255 - utf8.RuneCountInString(ext) - len(suffix)
			if budget < 1 {
				return "", contract.ErrConflict
			}
			runes := []rune(stem)
			if len(runes) > budget {
				runes = runes[:budget]
			}
			candidate = string(runes) + suffix + ext
		}
		exists, err := nameExists(tx, ownerID, parentID, candidate, excludeID)
		if err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
	}
	return "", contract.ErrConflict
}

func requireFilePermission(tx *gorm.DB, uid int64, file model.File, write bool) error {
	if file.UserID == uid {
		return nil
	}
	permissions := []string{"read", "write"}
	if write {
		permissions = []string{"write"}
	}
	var grant model.FilePermission
	err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("file_id = ? AND user_id = ? AND permission IN ?", file.ID, uid, permissions).First(&grant).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return contract.ErrForbidden
	}
	return err
}

func requireWriteParent(tx *gorm.DB, uid, parentID int64) error {
	if parentID == 0 {
		return nil
	}
	var parent model.File
	err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&parent, "id = ? AND is_deleted = 0", parentID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return contract.ErrTargetNotFound
	}
	if err != nil {
		return err
	}
	if parent.IsFolder != 1 {
		return contract.ErrTargetNotFolder
	}
	return requireFilePermission(tx, uid, parent, true)
}

func lockFileWithNamespace(tx *gorm.DB, fileID int64, deleted int8, includeChildren bool) (model.File, error) {
	var snapshot, current model.File
	if err := tx.First(&snapshot, "id = ? AND is_deleted = ?", fileID, deleted).Error; err != nil {
		return current, err
	}
	keys := []string{fileNamespaceKey(snapshot.UserID, snapshot.ParentID)}
	if includeChildren && snapshot.IsFolder == 1 {
		keys = append(keys, fileNamespaceKey(snapshot.UserID, snapshot.ID))
	}
	if err := lockFileNamespaces(tx, keys...); err != nil {
		return current, err
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "id = ? AND is_deleted = ?", fileID, deleted).Error; err != nil {
		return current, err
	}
	if current.ParentID != snapshot.ParentID || current.UserID != snapshot.UserID {
		return current, contract.ErrConflict
	}
	return current, nil
}

// Only the known filename index is an API name conflict; other SQL errors keep
// their original failure classification.
func normalizeFileNameConstraintError(err error) error {
	var mysqlErr *mysqlDriver.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
		const marker = " for key '"
		if at := strings.LastIndex(mysqlErr.Message, marker); at >= 0 && strings.HasSuffix(mysqlErr.Message, "'") {
			key := mysqlErr.Message[at+len(marker) : len(mysqlErr.Message)-1]
			if dot := strings.LastIndex(key, "."); dot >= 0 {
				key = key[dot+1:]
			}
			if key == "uq_file_active_name_v1" {
				return contract.ErrConflict
			}
		}
	}
	return err
}
