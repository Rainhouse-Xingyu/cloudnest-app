package mysql

import (
	"context"
	"strings"

	"cloudenest_go/model"

	"gorm.io/gorm"
)

// PermissionRepository 定义权限数据访问能力。
type PermissionRepository interface {
	ListUserPathPermissions(ctx context.Context, userID int64) ([]model.UserPathPermission, error)
}

// PermissionRepo 是 MySQL 权限仓储实现。
type PermissionRepo struct {
	db *gorm.DB
}

func NewPermissionRepo(db *gorm.DB) *PermissionRepo {
	return &PermissionRepo{db: db}
}

func (r *PermissionRepo) ListUserPathPermissions(ctx context.Context, userID int64) ([]model.UserPathPermission, error) {
	rows := make([]model.UserPathPermission, 0)
	if err := r.db.WithContext(ctx).Where("user_id = ?", userID).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ToMethodPathPermissions 将权限行映射为 middleware 所需的 method:path 字符串格式。
func ToMethodPathPermissions(rows []model.UserPathPermission) []string {
	perms := make([]string, 0, len(rows))
	for _, p := range rows {
		method := strings.ToUpper(strings.TrimSpace(p.Method))
		if method == "" {
			method = "*"
		}
		perms = append(perms, method+":"+p.Path)
	}
	return perms
}
