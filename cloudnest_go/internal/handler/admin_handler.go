package handler

import (
	"net/http"
	"strconv"

	"cloudenest_go/internal/middleware"
	"cloudenest_go/internal/service"
	"cloudenest_go/internal/service/contract"

	"github.com/gin-gonic/gin"
)

// AdminHandler结构体包含数据库连接和Redis客户端，用于处理管理员相关的HTTP请求。
type AdminHandler struct {
	Svc *service.AdminService
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

	permissions := make([]service.PermissionInput, 0, len(req.Permissions))
	for _, p := range req.Permissions {
		permissions = append(permissions, service.PermissionInput{Method: p.Method, Path: p.Path})
	}

	user, err := h.Svc.CreateUser(c.Request.Context(), service.CreateUserInput{
		Username:    req.Username,
		Password:    req.Password,
		Email:       req.Email,
		Permissions: permissions,
	})
	//发生错误返回对应的http错误码。
	if err != nil {
		if err == contract.ErrConflict {
			c.JSON(http.StatusConflict, gin.H{"error": "username already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create user failed"})
		return
	}

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

	permissions := make([]service.PermissionInput, 0, len(req.Permissions))
	for _, p := range req.Permissions {
		permissions = append(permissions, service.PermissionInput{Method: p.Method, Path: p.Path})
	}

	err = h.Svc.SetUserPermissions(c.Request.Context(), adminID, userID, permissions, c.ClientIP())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save permissions failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "permissions updated"})
}
