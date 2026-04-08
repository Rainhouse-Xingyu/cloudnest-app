package mysql

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"

	"gorm.io/gorm"
)

// FileRepo 是 FileRepository 的 MySQL 实现。
type FileRepo struct {
	db *gorm.DB
}

func NewFileRepo(db *gorm.DB) *FileRepo {
	return &FileRepo{db: db}
}

func (r *FileRepo) SearchFiles(ctx context.Context, uid int64, q contract.FileSearchQuery) ([]model.File, int64, error) {
	q.Name = strings.TrimSpace(q.Name)
	q.FileType = strings.ToLower(strings.TrimSpace(q.FileType))
	q.StartTime = strings.TrimSpace(q.StartTime)
	q.EndTime = strings.TrimSpace(q.EndTime)
	if q.Page <= 0 {
		q.Page = 1
	}
	if q.Limit <= 0 || q.Limit > 200 {
		q.Limit = 50
	}

	dbq := r.db.WithContext(ctx).
		Table("file f").
		Select("f.*").
		Joins("LEFT JOIN file_permission fp ON fp.file_id = f.id AND fp.user_id = ?", uid).
		Where("f.is_deleted = 0").
		Where("f.user_id = ? OR fp.permission IN ?", uid, []string{"read", "write"})

	if q.ParentID > 0 {
		dbq = dbq.Where("f.parent_id = ?", q.ParentID)
	}
	if q.Name != "" {
		dbq = dbq.Where("f.filename LIKE ?", "%"+q.Name+"%")
	}
	if q.FileType != "" {
		dbq = dbq.Where("LOWER(f.file_type) = ?", q.FileType)
	}
	if q.StartTime != "" {
		startAt, err := parseFlexibleTimeForFileRepo(q.StartTime)
		if err != nil {
			return nil, 0, fmt.Errorf("invalid start_time")
		}
		dbq = dbq.Where("f.update_time >= ?", startAt)
	}
	if q.EndTime != "" {
		endAt, err := parseFlexibleTimeForFileRepo(q.EndTime)
		if err != nil {
			return nil, 0, fmt.Errorf("invalid end_time")
		}
		dbq = dbq.Where("f.update_time <= ?", endAt)
	}

	var total int64
	if err := r.db.WithContext(ctx).Table("(?) as t", dbq).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	offset := (q.Page - 1) * q.Limit
	var rows []model.File
	if err := dbq.Order("f.is_folder DESC, f.filename ASC").Limit(q.Limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, 0, err
	}

	return rows, total, nil
}

func (r *FileRepo) ListFiles(ctx context.Context, uid int64, parentID int64) ([]model.File, error) {
	var rows []model.File
	err := r.db.WithContext(ctx).
		Table("file f").
		Select("f.*").
		Joins("LEFT JOIN file_permission fp ON fp.file_id = f.id AND fp.user_id = ?", uid).
		Where("f.parent_id = ? AND f.is_deleted = 0", parentID).
		Where("f.user_id = ? OR fp.permission IN ?", uid, []string{"read", "write"}).
		Order("f.is_folder DESC, f.filename ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *FileRepo) ListRecycleBin(ctx context.Context, uid int64) ([]model.File, error) {
	var rows []model.File
	if err := r.db.WithContext(ctx).Where("user_id = ? AND is_deleted = 1", uid).Order("update_time DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *FileRepo) CanAccessFile(ctx context.Context, userID int64, fileID int64, requireWrite bool) (bool, error) {
	return canAccessFileForFileRepo(ctx, r.db, userID, fileID, requireWrite)
}

func (r *FileRepo) GetActiveFileByID(ctx context.Context, fileID int64) (model.File, error) {
	var f model.File
	if err := r.db.WithContext(ctx).First(&f, "id = ? AND is_deleted = 0", fileID).Error; err != nil {
		return model.File{}, err
	}
	return f, nil
}

func (r *FileRepo) GetStorageByID(ctx context.Context, storageID int64) (model.Storage, error) {
	var storage model.Storage
	if err := r.db.WithContext(ctx).First(&storage, "id = ?", storageID).Error; err != nil {
		return model.Storage{}, err
	}
	return storage, nil
}

func (r *FileRepo) HasActiveNameConflict(ctx context.Context, parentID int64, name string, excludeID *int64) (bool, error) {
	q := r.db.WithContext(ctx).
		Model(&model.File{}).
		Where("parent_id = ? AND filename = ? AND is_deleted = 0", parentID, name)
	if excludeID != nil {
		q = q.Where("id <> ?", *excludeID)
	}

	var count int64
	if err := q.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *FileRepo) CreateFolderRecord(ctx context.Context, uid int64, name string, parentID int64) (model.File, error) {
	folder := model.File{UserID: uid, Filename: name, ParentID: parentID, IsFolder: 1}
	if err := r.db.WithContext(ctx).Create(&folder).Error; err != nil {
		return model.File{}, err
	}
	return folder, nil
}

func (r *FileRepo) UpdateFileName(ctx context.Context, fileID int64, name string) error {
	return r.db.WithContext(ctx).Model(&model.File{}).Where("id = ?", fileID).Update("filename", name).Error
}

func (r *FileRepo) CountActiveChildren(ctx context.Context, parentID int64) (int64, error) {
	var childCount int64
	err := r.db.WithContext(ctx).
		Model(&model.File{}).
		Where("parent_id = ? AND is_deleted = 0", parentID).
		Count(&childCount).Error
	if err != nil {
		return 0, err
	}
	return childCount, nil
}

func (r *FileRepo) SoftDeleteFileWithLivePhotoCleanup(ctx context.Context, fileID int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.File{}).Where("id = ?", fileID).Update("is_deleted", 1).Error; err != nil {
			return err
		}
		if err := tx.Where("image_file_id IN ? OR video_file_id IN ?", []int64{fileID}, []int64{fileID}).Delete(&model.LivePhoto{}).Error; err != nil {
			return err
		}
		return nil
	})
}

func (r *FileRepo) MoveFiles(ctx context.Context, uid int64, fileIDs []int64, targetParentID int64) ([]int64, error) {
	if targetParentID > 0 {
		canWrite, err := canAccessFileForFileRepo(ctx, r.db, uid, targetParentID, true)
		if err != nil {
			return nil, err
		}
		if !canWrite {
			return nil, contract.ErrForbidden
		}

		var target model.File
		if err := r.db.WithContext(ctx).First(&target, "id = ? AND is_deleted = 0", targetParentID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, contract.ErrTargetNotFound
			}
			return nil, err
		}
		if target.IsFolder != 1 {
			return nil, contract.ErrTargetNotFolder
		}
	}

	updated := make([]int64, 0, len(fileIDs))
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, fileID := range uniqueInt64ForFileRepo(fileIDs) {
			if fileID <= 0 {
				return contract.ErrInvalidInput
			}

			canWrite, err := canAccessFileForFileRepo(tx.Statement.Context, tx, uid, fileID, true)
			if err != nil {
				return err
			}
			if !canWrite {
				return contract.ErrForbidden
			}

			var src model.File
			if err := tx.First(&src, "id = ? AND is_deleted = 0", fileID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return contract.ErrNotFound
				}
				return err
			}

			if src.ID == targetParentID {
				return contract.ErrCannotMoveToItself
			}

			if src.IsFolder == 1 && targetParentID > 0 {
				isDescendant, err := isDescendantFolderForFileRepo(tx.Statement.Context, tx, targetParentID, src.ID)
				if err != nil {
					return err
				}
				if isDescendant {
					return contract.ErrCannotMoveToDesc
				}
			}

			finalName, err := ensureUniqueFilenameForFileRepo(tx, targetParentID, src.Filename)
			if err != nil {
				return err
			}

			if err := tx.Model(&src).Updates(map[string]interface{}{"parent_id": targetParentID, "filename": finalName}).Error; err != nil {
				return err
			}
			updated = append(updated, src.ID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return updated, nil
}

func (r *FileRepo) CopyFiles(ctx context.Context, uid int64, fileIDs []int64, targetParentID int64) ([]int64, error) {
	if targetParentID > 0 {
		canWrite, err := canAccessFileForFileRepo(ctx, r.db, uid, targetParentID, true)
		if err != nil {
			return nil, err
		}
		if !canWrite {
			return nil, contract.ErrForbidden
		}
	}

	newIDs := make([]int64, 0)
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, fileID := range uniqueInt64ForFileRepo(fileIDs) {
			if fileID <= 0 {
				return contract.ErrInvalidInput
			}

			canRead, err := canAccessFileForFileRepo(tx.Statement.Context, tx, uid, fileID, false)
			if err != nil {
				return err
			}
			if !canRead {
				return contract.ErrForbidden
			}

			var src model.File
			if err := tx.First(&src, "id = ? AND is_deleted = 0", fileID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return contract.ErrNotFound
				}
				return err
			}

			copiedID, err := copyNodeForFileRepo(tx, uid, src, targetParentID)
			if err != nil {
				return err
			}
			newIDs = append(newIDs, copiedID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return newIDs, nil
}

func (r *FileRepo) RestoreFromRecycleBin(ctx context.Context, uid int64, fileID int64) (model.File, error) {
	var restored model.File
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&restored, "id = ? AND is_deleted = 1", fileID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return contract.ErrRecycleItemNotFound
			}
			return err
		}
		if restored.UserID != uid {
			return contract.ErrForbidden
		}

		finalName, err := ensureUniqueFilenameForFileRepo(tx, restored.ParentID, restored.Filename)
		if err != nil {
			return err
		}

		if err := tx.Model(&restored).Updates(map[string]interface{}{"is_deleted": 0, "filename": finalName}).Error; err != nil {
			return err
		}
		restored.Filename = finalName
		restored.IsDeleted = 0
		return nil
	})
	if err != nil {
		return model.File{}, err
	}

	return restored, nil
}

func (r *FileRepo) PermanentDeleteFromRecycleBin(ctx context.Context, uid int64, fileID int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var root model.File
		if err := tx.First(&root, "id = ? AND is_deleted = 1", fileID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return contract.ErrRecycleItemNotFound
			}
			return err
		}
		if root.UserID != uid {
			return contract.ErrForbidden
		}

		ids, err := collectDescendantIDsForFileRepo(tx.Statement.Context, tx, root.ID)
		if err != nil {
			return err
		}
		ids = append(ids, root.ID)

		if err := tx.Where("image_file_id IN ? OR video_file_id IN ?", ids, ids).Delete(&model.LivePhoto{}).Error; err != nil {
			return err
		}

		return tx.Where("id IN ?", ids).Delete(&model.File{}).Error
	})
}

func (r *FileRepo) ResolveValidShare(ctx context.Context, code string) (model.Share, model.File, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return model.Share{}, model.File{}, gorm.ErrRecordNotFound
	}

	var share model.Share
	if err := r.db.WithContext(ctx).Where("share_code = ?", code).First(&share).Error; err != nil {
		return model.Share{}, model.File{}, err
	}
	if share.ExpireTime != nil && time.Now().After(*share.ExpireTime) {
		return model.Share{}, model.File{}, contract.ErrShareExpired
	}
	if share.FileID == nil {
		return model.Share{}, model.File{}, gorm.ErrRecordNotFound
	}

	var file model.File
	if err := r.db.WithContext(ctx).First(&file, "id = ? AND is_deleted = 0", *share.FileID).Error; err != nil {
		return model.Share{}, model.File{}, err
	}

	return share, file, nil
}

func (r *FileRepo) CollectBatchDownloadEntries(ctx context.Context, uid int64, fileIDs []int64) ([]contract.BatchDownloadEntry, error) {
	entries := make([]contract.BatchDownloadEntry, 0)
	for _, fileID := range uniqueInt64ForFileRepo(fileIDs) {
		if fileID <= 0 {
			continue
		}

		canRead, err := canAccessFileForFileRepo(ctx, r.db, uid, fileID, false)
		if err != nil || !canRead {
			continue
		}

		var f model.File
		if err := r.db.WithContext(ctx).First(&f, "id = ? AND is_deleted = 0", fileID).Error; err != nil {
			continue
		}

		if f.IsFolder == 1 {
			entries = append(entries, contract.BatchDownloadEntry{File: f, IsFolderZip: true})
			continue
		}

		if f.StorageID == nil {
			continue
		}

		var storage model.Storage
		if err := r.db.WithContext(ctx).First(&storage, "id = ?", *f.StorageID).Error; err != nil {
			continue
		}
		if _, err := os.Stat(storage.FilePath); err != nil {
			continue
		}

		entries = append(entries, contract.BatchDownloadEntry{File: f, StoragePath: storage.FilePath})
	}

	return entries, nil
}

func parseFlexibleTimeForFileRepo(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	layouts := []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"}
	for _, layout := range layouts {
		t, err := time.ParseInLocation(layout, raw, time.Local)
		if err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time format")
}

func uniqueInt64ForFileRepo(input []int64) []int64 {
	seen := make(map[int64]bool, len(input))
	result := make([]int64, 0, len(input))
	for _, v := range input {
		if seen[v] {
			continue
		}
		seen[v] = true
		result = append(result, v)
	}
	return result
}

func canAccessFileForFileRepo(ctx context.Context, db *gorm.DB, userID int64, fileID int64, requireWrite bool) (bool, error) {
	var f model.File
	err := db.WithContext(ctx).First(&f, "id = ? AND is_deleted = 0", fileID).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return false, nil
		}
		return false, err
	}
	if f.UserID == userID {
		return true, nil
	}

	permissions := []string{"read", "write"}
	if requireWrite {
		permissions = []string{"write"}
	}

	var count int64
	err = db.WithContext(ctx).
		Model(&model.FilePermission{}).
		Where("file_id = ? AND user_id = ? AND permission IN ?", fileID, userID, permissions).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func ensureUniqueFilenameForFileRepo(tx *gorm.DB, parentID int64, baseName string) (string, error) {
	finalName := baseName
	for i := 0; i < 100; i++ {
		var count int64
		err := tx.Model(&model.File{}).
			Where("parent_id = ? AND filename = ? AND is_deleted = 0", parentID, finalName).
			Count(&count).Error
		if err != nil {
			return "", err
		}
		if count == 0 {
			return finalName, nil
		}
		finalName = appendNameSuffixForFileRepo(baseName, time.Now().Format("20060102150405"))
	}
	return "", fmt.Errorf("failed to allocate unique filename")
}

func appendNameSuffixForFileRepo(name string, suffix string) string {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	if ext == "" {
		return fmt.Sprintf("%s_%s", base, suffix)
	}
	return fmt.Sprintf("%s_%s%s", base, suffix, ext)
}

func isDescendantFolderForFileRepo(ctx context.Context, db *gorm.DB, candidateID int64, ancestorID int64) (bool, error) {
	if candidateID <= 0 || ancestorID <= 0 {
		return false, nil
	}

	current := candidateID
	for current > 0 {
		if current == ancestorID {
			return true, nil
		}
		var f model.File
		err := db.WithContext(ctx).Select("id", "parent_id").First(&f, "id = ? AND is_deleted = 0", current).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return false, nil
			}
			return false, err
		}
		if f.ParentID == current {
			return false, nil
		}
		current = f.ParentID
	}

	return false, nil
}

func collectDescendantIDsForFileRepo(ctx context.Context, db *gorm.DB, rootID int64) ([]int64, error) {
	ids := make([]int64, 0)
	queue := []int64{rootID}

	for len(queue) > 0 {
		parentID := queue[0]
		queue = queue[1:]

		var children []model.File
		err := db.WithContext(ctx).Select("id", "is_folder").Where("parent_id = ?", parentID).Find(&children).Error
		if err != nil {
			return nil, err
		}

		for _, child := range children {
			ids = append(ids, child.ID)
			if child.IsFolder == 1 {
				queue = append(queue, child.ID)
			}
		}
	}

	return ids, nil
}

func copyNodeForFileRepo(tx *gorm.DB, newOwnerID int64, src model.File, targetParentID int64) (int64, error) {
	finalName, err := ensureUniqueFilenameForFileRepo(tx, targetParentID, src.Filename)
	if err != nil {
		return 0, err
	}

	newNode := model.File{
		UserID:         newOwnerID,
		Filename:       finalName,
		ParentID:       targetParentID,
		IsFolder:       src.IsFolder,
		FileType:       src.FileType,
		Size:           src.Size,
		StorageID:      src.StorageID,
		WorkspaceID:    src.WorkspaceID,
		CurrentVersion: src.CurrentVersion,
		IsDeleted:      0,
	}
	if err := tx.Create(&newNode).Error; err != nil {
		return 0, err
	}

	if src.IsFolder == 1 {
		var children []model.File
		if err := tx.Where("parent_id = ? AND is_deleted = 0", src.ID).Order("id ASC").Find(&children).Error; err != nil {
			return 0, err
		}
		for _, child := range children {
			if _, err := copyNodeForFileRepo(tx, newOwnerID, child, newNode.ID); err != nil {
				return 0, err
			}
		}
	}

	return newNode.ID, nil
}

var _ contract.FileRepository = (*FileRepo)(nil)
