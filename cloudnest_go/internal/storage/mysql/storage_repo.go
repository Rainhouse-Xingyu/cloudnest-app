package mysql

import (
	"context"
	"database/sql"

	"cloudenest_go/model"

	"gorm.io/gorm"
)

// StorageRepo 是存储容量概览仓储实现。
type StorageRepo struct {
	db *gorm.DB
}

func NewStorageRepo(db *gorm.DB) *StorageRepo {
	return &StorageRepo{db: db}
}

// GetUserUsedStorageBytes 汇总用户当前已使用容量（仅统计未删除普通文件）。
func (r *StorageRepo) GetUserUsedStorageBytes(ctx context.Context, userID int64) (int64, error) {
	var used sql.NullInt64
	err := r.db.WithContext(ctx).
		Model(&model.File{}).
		Select("COALESCE(SUM(size), 0)").
		Where("user_id = ? AND is_deleted = 0 AND is_folder = 0", userID).
		Scan(&used).Error
	if err != nil {
		return 0, err
	}
	if !used.Valid {
		return 0, nil
	}
	return used.Int64, nil
}

// GetSystemSettingValue 读取系统设置值。
func (r *StorageRepo) GetSystemSettingValue(ctx context.Context, key string) (string, error) {
	var row model.SystemSetting
	if err := r.db.WithContext(ctx).Where("setting_key = ?", key).First(&row).Error; err != nil {
		return "", err
	}
	return row.SettingValue, nil
}
