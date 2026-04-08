package mysql

import (
	"context"
	"fmt"
	"os"
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

var _ contract.FileRepository = (*FileRepo)(nil)
