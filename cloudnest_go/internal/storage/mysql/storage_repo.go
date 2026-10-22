package mysql

import (
	"cloudenest_go/internal/quota"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	"context"
	"database/sql"
	"gorm.io/gorm"
)

type StorageRepo struct{ db *gorm.DB }

func NewStorageRepo(db *gorm.DB) *StorageRepo { return &StorageRepo{db: db} }

// Overview is a read-only consistent snapshot, not a blind ledger-cache read.
func (r *StorageRepo) GetStorageUsage(ctx context.Context, uid int64) (contract.StorageUsage, error) {
	var usage contract.StorageUsage
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		usage, err = quota.Snapshot(tx, uid)
		if err != nil {
			return err
		}
		if usage.BrokenRefs != 0 {
			return contract.ErrQuotaAccounting
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return usage, err
}
func (r *StorageRepo) GetSystemSettingValue(ctx context.Context, key string) (string, error) {
	var row model.SystemSetting
	if err := r.db.WithContext(ctx).Where("setting_key=?", key).First(&row).Error; err != nil {
		return "", err
	}
	return row.SettingValue, nil
}
