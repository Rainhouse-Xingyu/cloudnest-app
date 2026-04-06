package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"cloudenest_go/internal/auth"
	"cloudenest_go/internal/config"
	"cloudenest_go/internal/middleware"
	"cloudenest_go/model"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const redisTokenKeyPrefix = "auth:token:"
//AuthHandler结构体包含数据库连接和Redis客户端，以及配置对象，用于处理认证相关的HTTP请求。
type AuthHandler struct {
	DB  *gorm.DB
	RDB *redis.Client
	Cfg config.Config
}
//LoginRequest结构体定义了登录请求的JSON格式，包括用户名和密码字段。
type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}
//Login方法处理登录的HTTP请求，首先绑定请求数据并验证，然后查询数据库验证用户信息和状态，最后生成JWT并返回给客户端。
func (h *AuthHandler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
//查询数据库验证用户信息和状态，如果用户不存在、被禁用或密码错误，则返回相应的错误信息。
	var u model.User
	err := h.DB.Where("username = ?", req.Username).First(&u).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid username or password"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	if u.Status == 0 {
		c.JSON(http.StatusForbidden, gin.H{"error": "user disabled"})
		return
	}

	if !verifyPassword(req.Password, u.Password) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid username or password"})
		return
	}
//如果用户密码不是bcrypt哈希格式，则使用bcrypt重新哈希密码并更新数据库，增强安全性。
	if !isBcryptHash(u.Password) {
		hash, _ := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		h.DB.Model(&u).Update("password", string(hash))
	}
//生成JWT并返回给客户端，如果生成失败则返回500错误。
	isAdmin, err := isAdminUser(c.Request.Context(), h.DB, u.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
//使用auth.GenerateToken函数生成JWT，传入密钥、用户ID、用户名、管理员标志和过期时间等参数。
	token, expiresAt, err := auth.GenerateToken(h.Cfg.JWTSecret, u.ID, u.Username, isAdmin, h.Cfg.JWTTTL())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "generate token failed"})
		return
	}

	if err := h.RDB.Set(c.Request.Context(), redisTokenKeyPrefix+token, u.ID, time.Until(expiresAt)).Err(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "redis error"})
		return
	}
//返回生成的JWT字符串、过期时间和用户信息给客户端。
	c.JSON(http.StatusOK, gin.H{
		"token":      token,
		"token_type": "Bearer",
		"expires_at": expiresAt,
		"user": gin.H{
			"id":       u.ID,
			"username": u.Username,
			"email":    u.Email,
			"is_admin": isAdmin,
		},
	})
}
//Logout方法处理注销的HTTP请求，首先从上下文中获取JWT字符串，然后删除Redis中的相关缓存，最后返回注销成功的消息给客户端。
func (h *AuthHandler) Logout(c *gin.Context) {
	tokenAny, ok := c.Get(middleware.ContextTokenKey)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "not logged in"})
		return
	}
	token := tokenAny.(string)
	_ = h.RDB.Del(c.Request.Context(), redisTokenKeyPrefix+token).Err()
	c.JSON(http.StatusOK, gin.H{"message": "logged out"})
}
//Me方法处理获取当前用户信息的HTTP请求，首先从上下文中获取用户ID，然后查询数据库获取用户信息，最后返回用户信息给客户端。
func (h *AuthHandler) Me(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)

	var u model.User
	if err := h.DB.Select("id", "username", "email", "status", "create_time", "update_time").First(&u, uid).Error; err != nil {
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
//verifyPassword函数用于验证输入的密码是否与存储的密码匹配，如果存储的密码是bcrypt哈希格式，则使用bcrypt进行比较，否则直接比较字符串。
func verifyPassword(inputPassword string, storedPassword string) bool {
	if isBcryptHash(storedPassword) {
		return bcrypt.CompareHashAndPassword([]byte(storedPassword), []byte(inputPassword)) == nil
	}
	return inputPassword == storedPassword
}
//isBcryptHash函数用于判断一个字符串是否是bcrypt哈希格式，通过检查字符串的前缀来确定。
func isBcryptHash(raw string) bool {
	return strings.HasPrefix(raw, "$2a$") || strings.HasPrefix(raw, "$2b$") || strings.HasPrefix(raw, "$2y$")
}
//isAdminUser函数用于判断一个用户是否具有管理员角色，通过查询数据库中的用户角色关联表和角色表来确定，如果查询失败则返回错误信息。
func isAdminUser(ctx context.Context, db *gorm.DB, userID int64) (bool, error) {
	var count int64
	err := db.WithContext(ctx).
		Table("user_role ur").
		Joins("JOIN role r ON ur.role_id = r.id").
		Where("ur.user_id = ? AND r.role_name = ?", userID, "admin").
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}
