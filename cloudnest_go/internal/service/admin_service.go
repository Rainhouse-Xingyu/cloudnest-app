package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"

	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
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
	Permissions []PermissionInput
}

// AdminService 承接管理员业务逻辑。
type AdminService struct {
	db  *gorm.DB
	rdb *redis.Client
}

func NewAdminService(db *gorm.DB, rdb *redis.Client) *AdminService {
	return &AdminService{db: db, rdb: rdb}
}

// CreateUser 创建用户、绑定默认角色并按需写入权限。
func (s *AdminService) CreateUser(ctx context.Context, req CreateUserInput) (model.User, error) {
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

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&user).Error; err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
				return contract.ErrConflict
			}
			return err
		}

		var normalRole model.Role
		err := tx.Where("role_name = ?", "user").First(&normalRole).Error
		if err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			normalRole = model.Role{RoleName: "user", Description: "normal user"}
			if err := tx.Create(&normalRole).Error; err != nil {
				return err
			}
		}

		if err := tx.Create(&model.UserRole{UserID: user.ID, RoleID: normalRole.ID}).Error; err != nil {
			return err
		}

		if len(req.Permissions) == 0 {
			return nil
		}

		now := time.Now()
		rows := make([]model.UserPathPermission, 0, len(req.Permissions))
		for _, p := range req.Permissions {
			rows = append(rows, model.UserPathPermission{
				UserID:     user.ID,
				Method:     normalizeMethod(p.Method),
				Path:       p.Path,
				CreateTime: now,
			})
		}
		return tx.Create(&rows).Error
	})
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
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var oldRows []model.UserPathPermission
		if err := tx.Where("user_id = ?", userID).Find(&oldRows).Error; err != nil {
			return err
		}

		if err := tx.Where("user_id = ?", userID).Delete(&model.UserPathPermission{}).Error; err != nil {
			return err
		}

		rows := make([]model.UserPathPermission, 0, len(permissions))
		now := time.Now()
		for _, p := range permissions {
			rows = append(rows, model.UserPathPermission{
				UserID:     userID,
				Method:     normalizeMethod(p.Method),
				Path:       p.Path,
				CreateTime: now,
			})
		}
		if len(rows) > 0 {
			if err := tx.Create(&rows).Error; err != nil {
				return err
			}
		}

		detail := fmt.Sprintf(
			"target_user=%d old_count=%d new_count=%d old=%s new=%s",
			userID,
			len(oldRows),
			len(rows),
			formatPermissions(oldRows),
			formatPermissionInputs(permissions),
		)
		if len(detail) > 500 {
			detail = detail[:500]
		}

		actor := adminID
		return tx.Create(&model.OperationLog{
			UserID:   &actor,
			Action:   "admin_permission_change",
			Status:   "success",
			Detail:   detail,
			ClientIP: clientIP,
		}).Error
	})
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

func formatPermissions(rows []model.UserPathPermission) string {
	if len(rows) == 0 {
		return "-"
	}
	b := strings.Builder{}
	for i, p := range rows {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(normalizeMethod(p.Method))
		b.WriteString(":")
		b.WriteString(strings.TrimSpace(p.Path))
	}
	return b.String()
}

func formatPermissionInputs(rows []PermissionInput) string {
	if len(rows) == 0 {
		return "-"
	}
	b := strings.Builder{}
	for i, p := range rows {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(normalizeMethod(p.Method))
		b.WriteString(":")
		b.WriteString(strings.TrimSpace(p.Path))
	}
	return b.String()
}
