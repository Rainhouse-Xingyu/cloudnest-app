package contract

import (
	"context"

	"cloudenest_go/model"
)

// MethodPathPermission 定义方法+路径权限输入。
type MethodPathPermission struct {
	Method string
	Path   string
}

// SettingUpsertItem 定义系统设置更新项。
type SettingUpsertItem struct {
	SettingKey   string
	SettingValue string
	Description  string
}

// UserRoleRow 定义用户角色聚合行。
type UserRoleRow struct {
	UserID   int64
	Username string
	RoleID   int64
	RoleName string
}

// AuthRepository 定义认证模块存储访问能力。
type AuthRepository interface {
	GetUserByUsername(ctx context.Context, username string) (model.User, error)
	RevokeUserSessions(ctx context.Context, userID int64, clientIP string) error
	ResetUserPassword(ctx context.Context, userID int64, expectedUsername, passwordHash string) error
	CheckUserIsAdmin(ctx context.Context, userID int64) (bool, error)
	GetUserProfile(ctx context.Context, userID int64) (model.User, error)
}

// AdminRepository 定义管理员模块存储访问能力。
type AdminRepository interface {
	CreateUserWithPermissions(ctx context.Context, user model.User, roleName string, permissions []MethodPathPermission) (model.User, error)
	ReplaceUserPermissionsWithAudit(ctx context.Context, adminID int64, userID int64, permissions []MethodPathPermission, clientIP string) error
}

// SettingsRepository 定义设置模块存储访问能力。
type SettingsRepository interface {
	ListSystemSettings(ctx context.Context) ([]model.SystemSetting, error)
	UpsertSystemSettings(ctx context.Context, items []SettingUpsertItem) error
	ListRoles(ctx context.Context) ([]model.Role, error)
	ListUserRoles(ctx context.Context, uid int64, all bool) ([]UserRoleRow, error)
	ListWorkspaceRoles(ctx context.Context, uid int64, all bool) ([]model.WorkspaceUser, error)
	ListFilePermissions(ctx context.Context, uid int64, all bool) ([]model.FilePermission, error)
	GetSystemSettingValue(ctx context.Context, key string) (string, error)
	UpsertSystemSetting(ctx context.Context, key string, value string, desc string) error
}
