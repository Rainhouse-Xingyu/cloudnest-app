package mysql

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"

	"gorm.io/gorm"
)

// AdminRepo 是管理员模块仓储实现。
type AdminRepo struct {
	db *gorm.DB
}

func NewAdminRepo(db *gorm.DB) *AdminRepo {
	return &AdminRepo{db: db}
}

func (r *AdminRepo) CreateUserWithPermissions(ctx context.Context, user model.User, roleName string, permissions []contract.MethodPathPermission) (model.User, error) {
	created := user
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&created).Error; err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
				return contract.ErrConflict
			}
			return err
		}

		selectedRole := strings.TrimSpace(strings.ToLower(roleName))
		if selectedRole == "" {
			selectedRole = "user"
		}

		var targetRole model.Role
		err := tx.Where("role_name = ?", selectedRole).First(&targetRole).Error
		if err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			desc := "normal user"
			if selectedRole == "admin" {
				desc = "system administrator"
			}
			targetRole = model.Role{RoleName: selectedRole, Description: desc}
			if err := tx.Create(&targetRole).Error; err != nil {
				return err
			}
		}

		if err := tx.Create(&model.UserRole{UserID: created.ID, RoleID: targetRole.ID}).Error; err != nil {
			return err
		}

		if len(permissions) == 0 {
			return nil
		}

		now := time.Now()
		rows := make([]model.UserPathPermission, 0, len(permissions))
		for _, p := range permissions {
			rows = append(rows, model.UserPathPermission{
				UserID:     created.ID,
				Method:     normalizeMethodForAdminRepo(p.Method),
				Path:       p.Path,
				CreateTime: now,
			})
		}
		return tx.Create(&rows).Error
	})
	if err != nil {
		return model.User{}, err
	}
	return created, nil
}

func (r *AdminRepo) ReplaceUserPermissionsWithAudit(ctx context.Context, adminID int64, userID int64, permissions []contract.MethodPathPermission, clientIP string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
				Method:     normalizeMethodForAdminRepo(p.Method),
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
			formatPermissionsForAdminRepo(oldRows),
			formatPermissionInputsForAdminRepo(permissions),
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
}

func normalizeMethodForAdminRepo(method string) string {
	m := strings.TrimSpace(strings.ToUpper(method))
	if m == "" {
		return "*"
	}
	return m
}

func formatPermissionsForAdminRepo(rows []model.UserPathPermission) string {
	if len(rows) == 0 {
		return "-"
	}
	b := strings.Builder{}
	for i, p := range rows {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(normalizeMethodForAdminRepo(p.Method))
		b.WriteString(":")
		b.WriteString(strings.TrimSpace(p.Path))
	}
	return b.String()
}

func formatPermissionInputsForAdminRepo(rows []contract.MethodPathPermission) string {
	if len(rows) == 0 {
		return "-"
	}
	b := strings.Builder{}
	for i, p := range rows {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(normalizeMethodForAdminRepo(p.Method))
		b.WriteString(":")
		b.WriteString(strings.TrimSpace(p.Path))
	}
	return b.String()
}
