package handler

import (
	"net/http"

	"cloudenest_go/internal/middleware"
	"cloudenest_go/internal/service"
	"cloudenest_go/internal/service/contract"

	"github.com/gin-gonic/gin"
)

// AuthHandler结构体通过注入服务处理认证相关的HTTP请求。
type AuthHandler struct {
	Svc *service.AuthService
}

// LoginRequest结构体定义了登录请求的JSON格式，包括用户名和密码字段。
type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// Login方法处理登录的HTTP请求，首先绑定请求数据并验证，然后查询数据库验证用户信息和状态，最后生成JWT并返回给客户端。
func (h *AuthHandler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	res, err := h.Svc.Login(c.Request.Context(), req.Username, req.Password)
	if err != nil {
		if err == service.ErrInvalidCredentials {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid username or password"})
			return
		}
		if err == service.ErrUserDisabled {
			c.JSON(http.StatusForbidden, gin.H{"error": "user disabled"})
			return
		}
		if err == contract.ErrInternal {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "login failed"})
		return
	}

	if res.User.Status == 0 {
		c.JSON(http.StatusForbidden, gin.H{"error": "user disabled"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"token":      res.Token,
		"token_type": "Bearer",
		"expires_at": res.ExpiresAt,
		"user": gin.H{
			"id":       res.User.ID,
			"username": res.User.Username,
			"email":    res.User.Email,
			"is_admin": res.IsAdmin,
		},
	})
}

// Logout方法处理注销的HTTP请求，首先从上下文中获取JWT字符串，然后删除Redis中的相关缓存，最后返回注销成功的消息给客户端。
func (h *AuthHandler) Logout(c *gin.Context) {
	tokenAny, ok := c.Get(middleware.ContextTokenKey)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "not logged in"})
		return
	}
	token := tokenAny.(string)
	h.Svc.Logout(c.Request.Context(), token)
	c.JSON(http.StatusOK, gin.H{"message": "logged out"})
}

// Me方法处理获取当前用户信息的HTTP请求，首先从上下文中获取用户ID，然后查询数据库获取用户信息，最后返回用户信息给客户端。
func (h *AuthHandler) Me(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	u, err := h.Svc.GetUserProfile(c.Request.Context(), uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	isAdminAny, _ := c.Get(middleware.ContextIsAdminKey)
	isAdmin, _ := isAdminAny.(bool)
	c.JSON(http.StatusOK, gin.H{"user": gin.H{
		"id":          u.ID,
		"username":    u.Username,
		"email":       u.Email,
		"status":      u.Status,
		"create_time": u.CreateTime,
		"update_time": u.UpdateTime,
		"is_admin":    isAdmin,
	}})
}
