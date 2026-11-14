package mysql

import (
	"context"
	"strings"

	"cloudenest_go/internal/quota"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"

	"gorm.io/gorm"
)

// SettingsRepo 是设置模块仓储实现。
type SettingsRepo struct {
	db *gorm.DB
}

func NewSettingsRepo(db *gorm.DB) *SettingsRepo {
	return &SettingsRepo{db: db}
}

func (r *SettingsRepo) ListSystemSettings(ctx context.Context) ([]model.SystemSetting, error) {
	rows := make([]model.SystemSetting, 0)
	if err := r.db.WithContext(ctx).Order("setting_key ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *SettingsRepo) UpsertSystemSettings(ctx context.Context, uid int64, items []contract.SettingUpsertItem, action, clientIP string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := quota.LockPolicy(tx, true); err != nil {
			return err
		}
		keys := make([]string, 0, len(items))
		for _, item := range items {
			if err := saveSystemSetting(tx, item); err != nil {
				return err
			}
			keys = append(keys, item.SettingKey)
		}
		return tx.Create(&model.OperationLog{
			UserID: &uid, Action: action, Status: "success", ClientIP: clientIP,
			Detail: "keys=" + strings.Join(keys, ","),
		}).Error
	})
}

func saveSystemSetting(tx *gorm.DB, item contract.SettingUpsertItem) error {
	var row model.SystemSetting
	err := tx.Where("setting_key = ?", item.SettingKey).First(&row).Error
	if err == gorm.ErrRecordNotFound {
		row = model.SystemSetting{SettingKey: item.SettingKey, SettingValue: item.SettingValue, Description: item.Description}
		return tx.Create(&row).Error
	}
	if err != nil {
		return err
	}
	updates := map[string]any{"setting_value": item.SettingValue}
	if item.Description != "" {
		updates["description"] = item.Description
	}
	return tx.Model(&row).Updates(updates).Error
}

func (r *SettingsRepo) ListRoles(ctx context.Context) ([]model.Role, error) {
	rows := make([]model.Role, 0)
	if err := r.db.WithContext(ctx).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *SettingsRepo) ListUserRoles(ctx context.Context, uid int64, all bool) ([]contract.UserRoleRow, error) {
	rows := make([]contract.UserRoleRow, 0)
	q := r.db.WithContext(ctx).Table("user_role ur").
		Select("ur.user_id, u.username, ur.role_id, r.role_name").
		Joins("JOIN user u ON u.id = ur.user_id").
		Joins("JOIN role r ON r.id = ur.role_id")
	if !all {
		q = q.Where("ur.user_id = ?", uid)
	}
	if err := q.Order("ur.user_id ASC, ur.role_id ASC").Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *SettingsRepo) ListWorkspaceRoles(ctx context.Context, uid int64, all bool) ([]model.WorkspaceUser, error) {
	rows := make([]model.WorkspaceUser, 0)
	q := r.db.WithContext(ctx).Order("workspace_id ASC, user_id ASC")
	if !all {
		q = q.Where("user_id = ?", uid)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *SettingsRepo) ListFilePermissions(ctx context.Context, uid int64, all bool) ([]model.FilePermission, error) {
	rows := make([]model.FilePermission, 0)
	q := r.db.WithContext(ctx).Order("file_id ASC, user_id ASC")
	if !all {
		q = q.Where("user_id = ?", uid)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *SettingsRepo) GetSystemSettingValue(ctx context.Context, key string) (string, error) {
	var row model.SystemSetting
	err := r.db.WithContext(ctx).Where("setting_key = ?", key).First(&row).Error
	if err != nil {
		return "", err
	}
	return row.SettingValue, nil
}

func (r *SettingsRepo) UpsertSystemSetting(ctx context.Context, key string, value string, desc string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := quota.LockPolicy(tx, true); err != nil {
			return err
		}
		var row model.SystemSetting
		err := tx.Where("setting_key = ?", key).First(&row).Error
		if err != nil {
			if err != gorm.ErrRecordNotFound {
				return err
			}
			row = model.SystemSetting{SettingKey: key, SettingValue: value, Description: desc}
			return tx.Create(&row).Error
		}
		return tx.Model(&row).Updates(map[string]interface{}{"setting_value": value, "description": desc}).Error
	})
}
