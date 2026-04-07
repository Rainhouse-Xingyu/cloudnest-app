package service

import (
	"context"

	storageMysql "cloudenest_go/internal/storage/mysql"
)

// AuthzService 提供权限解析能力，供鉴权中间件调用。
type AuthzService struct {
	permissionRepo storageMysql.PermissionRepository
}

func NewAuthzService(permissionRepo storageMysql.PermissionRepository) *AuthzService {
	return &AuthzService{permissionRepo: permissionRepo}
}

func (s *AuthzService) ResolveUserPermissions(ctx context.Context, userID int64) ([]string, error) {
	rows, err := s.permissionRepo.ListUserPathPermissions(ctx, userID)
	if err != nil {
		return nil, err
	}
	return storageMysql.ToMethodPathPermissions(rows), nil
}
