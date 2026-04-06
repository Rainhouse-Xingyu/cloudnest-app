package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cloudenest_go/internal/middleware"
	"cloudenest_go/model"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const redisPermKeyPrefix = "auth:perm:user:"

// AdminHandler结构体包含数据库连接和Redis客户端，用于处理管理员相关的HTTP请求。
type AdminHandler struct {
	DB  *gorm.DB
	RDB *redis.Client
}

// CreateUserRequest结构体定义了创建用户请求的JSON格式，包括用户名、密码、邮箱和权限列表等字段。
type CreateUserRequest struct {
	Username    string              `json:"username" binding:"required"`
	Password    string              `json:"password" binding:"required"`
	Email       string              `json:"email"`
	Permissions []PermissionRequest `json:"permissions"`
}

// PermissionRequest结构体定义了权限请求的JSON格式，包括HTTP方法和路径等字段。
type PermissionRequest struct {
	Method string `json:"method" binding:"required"`
	Path   string `json:"path" binding:"required"`
}

// SetUserPermissionsRequest结构体定义了设置用户权限请求的JSON格式，包括权限列表字段。
type SetUserPermissionsRequest struct {
	Permissions []PermissionRequest `json:"permissions" binding:"required"`
}

/*CreateUser方法处理创建用户的HTTP请求，
首先绑定请求数据并验证，然后使用bcrypt哈希密码，
最后在数据库中创建用户记录和关联的权限记录，
并清除相关的Redis缓存。*/

func (h *AdminHandler) CreateUser(c *gin.Context) {
	var req CreateUserRequest
	//绑定请求数据并验证，如果失败则返回400错误。
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	//使用bcrypt哈希密码，如果失败则返回500错误。
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "hash password failed"})
		return
	}
	//在数据库中创建用户记录和关联的权限记录，如果失败则返回相应的错误信息。
	user := model.User{
		Username: req.Username,
		Password: string(hash),
		Email:    req.Email,
		Status:   1,
	}
	//使用数据库事务确保用户和权限记录的一致性，如果发生错误则回滚事务。
	err = h.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&user).Error; err != nil {
			return err
		}
		//查询或创建普通用户角色，并关联到新创建的用户。
		var normalRole model.Role
		err := tx.Where("role_name = ?", "user").First(&normalRole).Error
		//如果查询角色失败且不是记录未找到错误，则返回错误；如果是记录未找到错误，则创建一个新的普通用户角色。
		if err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			normalRole = model.Role{RoleName: "user", Description: "normal user"}
			if err := tx.Create(&normalRole).Error; err != nil {
				return err
			}
		}
		//将新创建的用户与普通用户角色关联起来。
		if err := tx.Create(&model.UserRole{UserID: user.ID, RoleID: normalRole.ID}).Error; err != nil {
			return err
		}
		//如果请求中没有权限列表，则直接返回成功。
		if len(req.Permissions) == 0 {
			return nil
		}
		//将请求中的权限列表转换为UserPathPermission记录，并批量插入数据库。
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
	//发生错误返回对应的http错误码
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			c.JSON(http.StatusConflict, gin.H{"error": "username already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create user failed"})
		return
	}
	//清除相关的Redis缓存，确保权限更新后能够生效。
	_ = h.RDB.Del(c.Request.Context(), fmt.Sprintf("%s%d", redisPermKeyPrefix, user.ID)).Err()
	c.JSON(http.StatusOK, gin.H{"id": user.ID, "username": user.Username})
}

/*
SetUserPermissions方法处理设置用户权限的HTTP请求，首先解析用户ID并验证，然后绑定请求数据并验证，
最后在数据库中更新用户的权限记录，并清除相关的Redis缓存。
*/
func (h *AdminHandler) SetUserPermissions(c *gin.Context) {
	adminID := c.GetInt64(middleware.ContextUserIDKey)
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || userID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user id"})
		return
	}
	//绑定请求数据并验证，如果失败则返回400错误。
	var req SetUserPermissionsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	//在数据库中更新用户的权限记录，如果失败则返回500错误。
	err = h.DB.Transaction(func(tx *gorm.DB) error {
		var oldRows []model.UserPathPermission
		if err := tx.Where("user_id = ?", userID).Find(&oldRows).Error; err != nil {
			return err
		}

		if err := tx.Where("user_id = ?", userID).Delete(&model.UserPathPermission{}).Error; err != nil {
			return err
		}
		rows := make([]model.UserPathPermission, 0, len(req.Permissions))
		now := time.Now()
		//将请求中的权限列表转换为UserPathPermission记录，并批量插入数据库。
		for _, p := range req.Permissions {
			rows = append(rows, model.UserPathPermission{
				UserID:     userID,
				Method:     normalizeMethod(p.Method),
				Path:       p.Path,
				CreateTime: now,
			})
		}
		//如果权限列表不为空，则批量插入新的权限记录。
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
			formatPermissionRequests(req.Permissions),
		)
		if len(detail) > 500 {
			detail = detail[:500]
		}

		actor := adminID
		if err := tx.Create(&model.OperationLog{
			UserID:   &actor,
			Action:   "admin_permission_change",
			Status:   "success",
			Detail:   detail,
			ClientIP: c.ClientIP(),
		}).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save permissions failed"})
		return
	}
	//清除相关的Redis缓存，确保权限更新后能够生效。
	_ = h.RDB.Del(c.Request.Context(), fmt.Sprintf("%s%d", redisPermKeyPrefix, userID)).Err()
	c.JSON(http.StatusOK, gin.H{"message": "permissions updated"})
}

// normalizeMethod函数用于规范化HTTP方法，将其转换为大写并去除空格，如果方法为空则返回通配符"*"。
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

func formatPermissionRequests(rows []PermissionRequest) string {
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
