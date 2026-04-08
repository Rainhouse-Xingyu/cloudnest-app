package service

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"cloudenest_go/model"

	"gorm.io/gorm"
)

// SettingsService 承接设置模块业务逻辑。
type SettingsService struct {
	db *gorm.DB
}

func NewSettingsService(db *gorm.DB) *SettingsService {
	return &SettingsService{db: db}
}

// SettingUpsertItem 表示设置更新项。
type SettingUpsertItem struct {
	SettingKey   string
	SettingValue string
	Description  string
}

// PermissionScopeView 表示权限汇总范围。
type PermissionScopeView struct {
	All        bool  `json:"all"`
	CurrentUID int64 `json:"current_uid"`
}

// UserRoleView 表示用户角色视图。
type UserRoleView struct {
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
	RoleID   int64  `json:"role_id"`
	RoleName string `json:"role_name"`
}

// PermissionSummaryResult 表示权限汇总结果。
type PermissionSummaryResult struct {
	Scope           PermissionScopeView    `json:"scope"`
	Roles           []model.Role           `json:"roles"`
	UserRoles       []UserRoleView         `json:"user_roles"`
	WorkspaceRoles  []model.WorkspaceUser  `json:"workspace_roles"`
	FilePermissions []model.FilePermission `json:"file_permissions"`
}

// RateWindowResult 表示用户速率时间窗配置。
type RateWindowResult struct {
	Enabled bool   `json:"enabled"`
	Start   string `json:"start"`
	End     string `json:"end"`
	Source  string `json:"source"`
}

var allowedSystemSettingKeys = map[string]struct{}{
	"upload_chunk_threshold_mb":      {},
	"upload_default_chunk_size_mb":   {},
	"share_default_expire_hours":     {},
	"upload_speed_limit_kbps":        {},
	"download_speed_limit_kbps":      {},
	"upload_peak_speed_limit_kbps":   {},
	"download_peak_speed_limit_kbps": {},
	"peak_window_enabled":            {},
	"peak_window_start":              {},
	"peak_window_end":                {},
	"upload_max_concurrency":         {},
	"recycle_retention_days":         {},
}

// GetSettings 返回 system_setting 全量配置（非管理员脱敏）。
func (s *SettingsService) GetSettings(ctx context.Context, isAdmin bool) ([]model.SystemSetting, error) {
	var rows []model.SystemSetting
	if err := s.db.WithContext(ctx).Order("setting_key ASC").Find(&rows).Error; err != nil {
		return nil, err
	}

	for i := range rows {
		if !isAdmin && isSensitiveSettingKey(rows[i].SettingKey) {
			rows[i].SettingValue = maskSettingValue(rows[i].SettingValue)
		}
	}

	return rows, nil
}

// UpsertSettings 批量新增/更新系统设置。
func (s *SettingsService) UpsertSettings(ctx context.Context, items []SettingUpsertItem) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, item := range items {
			key := strings.TrimSpace(item.SettingKey)
			value := strings.TrimSpace(item.SettingValue)
			desc := strings.TrimSpace(item.Description)
			if key == "" || value == "" {
				return gorm.ErrInvalidData
			}
			if _, ok := allowedSystemSettingKeys[key]; !ok {
				return gorm.ErrInvalidField
			}
			if err := validateSettingValue(key, value); err != nil {
				return err
			}

			var row model.SystemSetting
			err := tx.Where("setting_key = ?", key).First(&row).Error
			if err != nil {
				if err != gorm.ErrRecordNotFound {
					return err
				}
				row = model.SystemSetting{SettingKey: key, SettingValue: value, Description: desc}
				if err := tx.Create(&row).Error; err != nil {
					return err
				}
				continue
			}

			updates := map[string]interface{}{"setting_value": value}
			if desc != "" {
				updates["description"] = desc
			}
			if err := tx.Model(&row).Updates(updates).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// GetPermissionSummary 聚合展示角色、空间角色与文件授权规则。
func (s *SettingsService) GetPermissionSummary(ctx context.Context, uid int64, all bool) (PermissionSummaryResult, error) {
	res := PermissionSummaryResult{Scope: PermissionScopeView{All: all, CurrentUID: uid}}

	if err := s.db.WithContext(ctx).Order("id ASC").Find(&res.Roles).Error; err != nil {
		return PermissionSummaryResult{}, err
	}

	roleSQL := s.db.WithContext(ctx).Table("user_role ur").
		Select("ur.user_id, u.username, ur.role_id, r.role_name").
		Joins("JOIN user u ON u.id = ur.user_id").
		Joins("JOIN role r ON r.id = ur.role_id")
	if !all {
		roleSQL = roleSQL.Where("ur.user_id = ?", uid)
	}
	if err := roleSQL.Order("ur.user_id ASC, ur.role_id ASC").Scan(&res.UserRoles).Error; err != nil {
		return PermissionSummaryResult{}, err
	}

	workspaceSQL := s.db.WithContext(ctx).Order("workspace_id ASC, user_id ASC")
	if !all {
		workspaceSQL = workspaceSQL.Where("user_id = ?", uid)
	}
	if err := workspaceSQL.Find(&res.WorkspaceRoles).Error; err != nil {
		return PermissionSummaryResult{}, err
	}

	filePermSQL := s.db.WithContext(ctx).Order("file_id ASC, user_id ASC")
	if !all {
		filePermSQL = filePermSQL.Where("user_id = ?", uid)
	}
	if err := filePermSQL.Find(&res.FilePermissions).Error; err != nil {
		return PermissionSummaryResult{}, err
	}

	return res, nil
}

// GetMyRateWindow 返回当前用户高峰时段配置（无用户配置时回落全局配置）。
func (s *SettingsService) GetMyRateWindow(ctx context.Context, uid int64) (RateWindowResult, error) {
	enabledKey := "peak_window_enabled_user_" + strconv.FormatInt(uid, 10)
	startKey := "peak_window_start_user_" + strconv.FormatInt(uid, 10)
	endKey := "peak_window_end_user_" + strconv.FormatInt(uid, 10)

	enabled, err := s.readSettingValue(ctx, enabledKey)
	if err != nil {
		return RateWindowResult{}, err
	}
	start, err := s.readSettingValue(ctx, startKey)
	if err != nil {
		return RateWindowResult{}, err
	}
	end, err := s.readSettingValue(ctx, endKey)
	if err != nil {
		return RateWindowResult{}, err
	}

	source := "user"
	if enabled == "" && start == "" && end == "" {
		enabled, err = s.readSettingValue(ctx, "peak_window_enabled")
		if err != nil {
			return RateWindowResult{}, err
		}
		start, err = s.readSettingValue(ctx, "peak_window_start")
		if err != nil {
			return RateWindowResult{}, err
		}
		end, err = s.readSettingValue(ctx, "peak_window_end")
		if err != nil {
			return RateWindowResult{}, err
		}
		source = "global"
	}

	return RateWindowResult{
		Enabled: strings.TrimSpace(enabled) == "1",
		Start:   strings.TrimSpace(start),
		End:     strings.TrimSpace(end),
		Source:  source,
	}, nil
}

// UpdateMyRateWindow 更新当前用户高峰时段配置。
func (s *SettingsService) UpdateMyRateWindow(ctx context.Context, uid int64, enabled bool, start string, end string) error {
	enabledKey := "peak_window_enabled_user_" + strconv.FormatInt(uid, 10)
	startKey := "peak_window_start_user_" + strconv.FormatInt(uid, 10)
	endKey := "peak_window_end_user_" + strconv.FormatInt(uid, 10)

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.upsertSetting(tx, enabledKey, boolToFlag(enabled), "用户自定义高峰时段开关"); err != nil {
			return err
		}
		if enabled {
			if err := s.upsertSetting(tx, startKey, start, "用户自定义高峰开始时刻 HH:MM"); err != nil {
				return err
			}
			if err := s.upsertSetting(tx, endKey, end, "用户自定义高峰结束时刻 HH:MM"); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *SettingsService) upsertSetting(tx *gorm.DB, key, value, desc string) error {
	var row model.SystemSetting
	err := tx.Where("setting_key = ?", key).First(&row).Error
	if err != nil {
		if err != gorm.ErrRecordNotFound {
			return err
		}
		row = model.SystemSetting{SettingKey: key, SettingValue: value, Description: desc}
		return tx.Create(&row).Error
	}
	return tx.Model(&row).Updates(map[string]interface{}{
		"setting_value": value,
		"description":   desc,
	}).Error
}

func (s *SettingsService) readSettingValue(ctx context.Context, key string) (string, error) {
	var row model.SystemSetting
	err := s.db.WithContext(ctx).Where("setting_key = ?", key).First(&row).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return "", nil
		}
		return "", err
	}
	return row.SettingValue, nil
}

func isValidHHMM(v string) bool {
	v = strings.TrimSpace(v)
	if len(v) != 5 || v[2] != ':' {
		return false
	}
	h, errH := strconv.Atoi(v[:2])
	m, errM := strconv.Atoi(v[3:])
	if errH != nil || errM != nil {
		return false
	}
	return h >= 0 && h <= 23 && m >= 0 && m <= 59
}

func boolToFlag(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func validateSettingValue(key, value string) error {
	value = strings.TrimSpace(value)
	key = strings.TrimSpace(key)
	if key == "" || value == "" {
		return gorm.ErrInvalidData
	}

	intInRange := func(min, max int64) error {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n < min || n > max {
			return errInvalidSettingValue(key)
		}
		return nil
	}

	switch key {
	case "upload_chunk_threshold_mb":
		return intInRange(1, 10240)
	case "upload_default_chunk_size_mb":
		return intInRange(1, 1024)
	case "share_default_expire_hours":
		return intInRange(0, 8760)
	case "upload_speed_limit_kbps", "download_speed_limit_kbps", "upload_peak_speed_limit_kbps", "download_peak_speed_limit_kbps":
		return intInRange(64, 10240)
	case "peak_window_enabled":
		if value != "0" && value != "1" {
			return errInvalidSettingValue(key)
		}
		return nil
	case "peak_window_start", "peak_window_end":
		if !isValidHHMM(value) {
			return errInvalidSettingValue(key)
		}
		return nil
	case "upload_max_concurrency":
		return intInRange(1, 32)
	case "recycle_retention_days":
		return intInRange(1, 3650)
	default:
		return nil
	}
}

func errInvalidSettingValue(key string) error {
	return errors.New("invalid value for setting_key: " + key)
}

func isSensitiveSettingKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	return strings.Contains(k, "token") || strings.Contains(k, "secret") || strings.Contains(k, "password") || strings.Contains(k, "private")
}

func maskSettingValue(value string) string {
	v := strings.TrimSpace(value)
	if v == "" {
		return ""
	}
	if len(v) <= 4 {
		return "****"
	}
	return v[:2] + "****" + v[len(v)-2:]
}
