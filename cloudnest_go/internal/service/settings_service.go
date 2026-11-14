package service

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"

	"gorm.io/gorm"
)

// SettingsService 承接设置模块业务逻辑。
type SettingsService struct {
	repo contract.SettingsRepository
}

func NewSettingsService(repo contract.SettingsRepository) *SettingsService {
	return &SettingsService{repo: repo}
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
	"storage_quota_bytes":            {},
	"storage_default_quota_bytes":    {},
}

// GetSettings only exposes public setting keys to ordinary users.
func (s *SettingsService) GetSettings(ctx context.Context, isAdmin bool) ([]model.SystemSetting, error) {
	rows, err := s.repo.ListSystemSettings(ctx)
	if err != nil {
		return nil, err
	}

	if isAdmin {
		return rows, nil
	}
	visible := make([]model.SystemSetting, 0, len(rows))
	for _, row := range rows {
		if _, ok := allowedSystemSettingKeys[row.SettingKey]; ok {
			visible = append(visible, row)
		}
	}
	return visible, nil
}

// UpsertSettings 批量新增/更新系统设置。
func (s *SettingsService) UpsertSettings(ctx context.Context, uid int64, items []SettingUpsertItem, clientIP string) error {
	if len(items) == 0 || len(items) > len(allowedSystemSettingKeys) {
		return gorm.ErrInvalidData
	}
	seen := make(map[string]bool, len(items))
	repoItems := make([]contract.SettingUpsertItem, 0, len(items))
	for _, item := range items {
		normalized, err := normalizeSystemSetting(item)
		if err != nil {
			return err
		}
		if seen[normalized.SettingKey] {
			return gorm.ErrInvalidData
		}
		seen[normalized.SettingKey] = true
		repoItems = append(repoItems, normalized)
	}
	return s.repo.UpsertSystemSettings(ctx, uid, repoItems, "settings_update", clientIP)
}

// GetPermissionSummary 聚合展示角色、空间角色与文件授权规则。
func (s *SettingsService) GetPermissionSummary(ctx context.Context, uid int64, all bool) (PermissionSummaryResult, error) {
	res := PermissionSummaryResult{Scope: PermissionScopeView{All: all, CurrentUID: uid}}

	roles, err := s.repo.ListRoles(ctx)
	if err != nil {
		return PermissionSummaryResult{}, err
	}
	res.Roles = roles

	roleRows, err := s.repo.ListUserRoles(ctx, uid, all)
	if err != nil {
		return PermissionSummaryResult{}, err
	}
	res.UserRoles = make([]UserRoleView, 0, len(roleRows))
	for _, row := range roleRows {
		res.UserRoles = append(res.UserRoles, UserRoleView{UserID: row.UserID, Username: row.Username, RoleID: row.RoleID, RoleName: row.RoleName})
	}

	workspaceRoles, err := s.repo.ListWorkspaceRoles(ctx, uid, all)
	if err != nil {
		return PermissionSummaryResult{}, err
	}
	res.WorkspaceRoles = workspaceRoles

	filePerms, err := s.repo.ListFilePermissions(ctx, uid, all)
	if err != nil {
		return PermissionSummaryResult{}, err
	}
	res.FilePermissions = filePerms

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
func (s *SettingsService) UpdateMyRateWindow(ctx context.Context, uid int64, enabled bool, start, end, clientIP string) error {
	enabledKey := "peak_window_enabled_user_" + strconv.FormatInt(uid, 10)
	startKey := "peak_window_start_user_" + strconv.FormatInt(uid, 10)
	endKey := "peak_window_end_user_" + strconv.FormatInt(uid, 10)

	items := []contract.SettingUpsertItem{{SettingKey: enabledKey, SettingValue: boolToFlag(enabled), Description: "用户自定义高峰时段开关"}}
	if enabled {
		if !isValidHHMM(start) || !isValidHHMM(end) {
			return contract.ErrInvalidInput
		}
		items = append(items, contract.SettingUpsertItem{SettingKey: startKey, SettingValue: start, Description: "用户自定义高峰开始时刻 HH:MM"}, contract.SettingUpsertItem{SettingKey: endKey, SettingValue: end, Description: "用户自定义高峰结束时刻 HH:MM"})
	}
	return s.repo.UpsertSystemSettings(ctx, uid, items, "rate_window_update", clientIP)
}

func (s *SettingsService) readSettingValue(ctx context.Context, key string) (string, error) {
	v, err := s.repo.GetSystemSettingValue(ctx, key)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return "", nil
		}
		return "", err
	}
	return v, nil
}

func isValidHHMM(v string) bool { _, ok := parseHHMMToMinuteInService(v); return ok }

func boolToFlag(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func normalizeSystemSetting(item SettingUpsertItem) (contract.SettingUpsertItem, error) {
	key := strings.TrimSpace(item.SettingKey)
	value := strings.TrimSpace(item.SettingValue)
	desc := strings.TrimSpace(item.Description)
	if key == "" || value == "" || len(desc) > 255 {
		return contract.SettingUpsertItem{}, gorm.ErrInvalidData
	}
	if _, ok := allowedSystemSettingKeys[key]; !ok {
		return contract.SettingUpsertItem{}, gorm.ErrInvalidField
	}
	if err := validateSettingValue(key, value); err != nil {
		return contract.SettingUpsertItem{}, err
	}
	return contract.SettingUpsertItem{SettingKey: key, SettingValue: value, Description: desc}, nil
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
	case "storage_quota_bytes", "storage_default_quota_bytes":
		return intInRange(1, 1<<62)
	default:
		return nil
	}
}

func errInvalidSettingValue(key string) error {
	return errors.New("invalid value for setting_key: " + key)
}
