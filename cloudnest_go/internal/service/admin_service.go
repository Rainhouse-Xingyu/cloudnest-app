package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"

	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
)

const redisPermKeyPrefix = "auth:perm:user:"

// PermissionInput 表示权限输入项。
type PermissionInput struct {
	Method string
	Path   string
}

// CreateUserInput 表示创建用户输入参数。
type CreateUserInput struct {
	Username    string
	Password    string
	Email       string
	RoleName    string
	Permissions []PermissionInput
}

// AdminService 承接管理员业务逻辑。
type AdminService struct {
	repo contract.AdminRepository
	rdb  *redis.Client
}

func NewAdminService(repo contract.AdminRepository, rdb *redis.Client) *AdminService {
	return &AdminService{repo: repo, rdb: rdb}
}

// CreateUser 创建用户、绑定默认角色并按需写入权限。
func (s *AdminService) CreateUser(ctx context.Context, req CreateUserInput) (model.User, error) {
	roleName := normalizeRoleName(req.RoleName)
	if roleName != "user" && roleName != "admin" {
		return model.User{}, contract.ErrInvalidInput
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return model.User{}, contract.ErrInternal
	}

	user := model.User{
		Username: req.Username,
		Password: string(hash),
		Email:    req.Email,
		Status:   1,
	}

	perms := make([]contract.MethodPathPermission, 0, len(req.Permissions))
	for _, p := range req.Permissions {
		perms = append(perms, contract.MethodPathPermission{Method: p.Method, Path: p.Path})
	}
	user, err = s.repo.CreateUserWithPermissions(ctx, user, roleName, perms)
	if err != nil {
		if errors.Is(err, contract.ErrConflict) {
			return model.User{}, contract.ErrConflict
		}
		return model.User{}, contract.ErrInternal
	}

	_ = s.rdb.Del(ctx, fmt.Sprintf("%s%d", redisPermKeyPrefix, user.ID)).Err()
	return user, nil
}

// SetUserPermissions 覆盖用户路径权限并记录审计日志。
func (s *AdminService) SetUserPermissions(ctx context.Context, adminID int64, userID int64, permissions []PermissionInput, clientIP string) error {
	repoPerms := make([]contract.MethodPathPermission, 0, len(permissions))
	for _, p := range permissions {
		repoPerms = append(repoPerms, contract.MethodPathPermission{Method: p.Method, Path: p.Path})
	}
	err := s.repo.ReplaceUserPermissionsWithAudit(ctx, adminID, userID, repoPerms, clientIP)
	if err != nil {
		return contract.ErrInternal
	}

	_ = s.rdb.Del(ctx, fmt.Sprintf("%s%d", redisPermKeyPrefix, userID)).Err()
	return nil
}

func normalizeMethod(method string) string {
	m := strings.TrimSpace(strings.ToUpper(method))
	if m == "" {
		return "*"
	}
	return m
}

func normalizeRoleName(role string) string {
	r := strings.TrimSpace(strings.ToLower(role))
	if r == "" {
		return "user"
	}
	return r
}
