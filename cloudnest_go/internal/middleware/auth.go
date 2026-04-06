package middleware

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"cloudenest_go/internal/auth"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

const (
	ContextUserIDKey    = "user_id"
	ContextUsernameKey  = "username"
	ContextIsAdminKey   = "is_admin"
	ContextTokenKey     = "token"
	permissionCacheTTL  = 10 * time.Minute
	redisTokenKeyPrefix = "auth:token:"
	redisPermKeyPrefix  = "auth:perm:user:"
)
//Authenticate函数是一个Gin中间件，用于验证请求中的JWT并检查用户权限。它首先检查请求路径是否在跳过列表中，如果是则直接放行。然后从Authorization头中提取Bearer token，并使用auth.ParseToken函数解析JWT。如果解析失败，则返回401错误。接着检查Redis中是否存在该token的缓存，如果不存在则说明token已过期或已注销，返回401错误。最后将用户信息存储在上下文中，并根据用户是否为管理员来决定是否继续检查权限。
func Authenticate(rdb *redis.Client, jwtSecret string, loadPermissions func(context.Context, int64) ([]string, error), skipPaths map[string]bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if skipPaths[c.Request.URL.Path] {
			c.Next()
			return
		}

		token := extractBearerToken(c.GetHeader("Authorization"))
		if token == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "missing bearer token"})
			c.Abort()
			return
		}

		claims, err := auth.ParseToken(jwtSecret, token)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			c.Abort()
			return
		}

		ctx := c.Request.Context()
		tokenKey := redisTokenKeyPrefix + token
		exists, err := rdb.Exists(ctx, tokenKey).Result()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "redis error"})
			c.Abort()
			return
		}
		if exists == 0 {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "token expired or logged out"})
			c.Abort()
			return
		}

		c.Set(ContextUserIDKey, claims.UserID)
		c.Set(ContextUsernameKey, claims.Username)
		c.Set(ContextIsAdminKey, claims.IsAdmin)
		c.Set(ContextTokenKey, token)

		if claims.IsAdmin {
			c.Next()
			return
		}

		if !checkPathPermission(ctx, rdb, claims.UserID, c.Request.Method, c.Request.URL.Path, loadPermissions) {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			c.Abort()
			return
		}

		c.Next()
	}
}
//RequireAdmin函数是一个Gin中间件，用于检查当前用户是否具有管理员权限。它从上下文中获取管理员标志，如果不存在或为false，则返回403错误并终止请求处理，否则继续执行后续的处理函数。
func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		isAdmin, ok := c.Get(ContextIsAdminKey)
		if !ok || !isAdmin.(bool) {
			c.JSON(http.StatusForbidden, gin.H{"error": "admin only"})
			c.Abort()
			return
		}
		c.Next()
	}
}
//extractBearerToken函数用于从Authorization头中提取Bearer token，如果格式不正确则返回空字符串。
func extractBearerToken(authHeader string) string {
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}
//checkPathPermission函数用于检查用户是否具有访问特定路径和方法的权限。它首先尝试从Redis缓存中获取用户的权限列表，如果缓存不存在或为空，则调用loadPermissions函数从数据库加载权限并缓存到Redis中。然后遍历权限列表，使用matchPermission函数检查是否有匹配的权限，如果找到匹配的权限则返回true，否则返回false。
func checkPathPermission(ctx context.Context, rdb *redis.Client, userID int64, method string, path string, loadPermissions func(context.Context, int64) ([]string, error)) bool {
	key := fmt.Sprintf("%s%d", redisPermKeyPrefix, userID)
	perms, err := rdb.SMembers(ctx, key).Result()
	if err != nil || len(perms) == 0 {
		perms, err = loadPermissions(ctx, userID)
		if err != nil {
			return false
		}
		if len(perms) > 0 {
			values := make([]interface{}, 0, len(perms))
			for _, p := range perms {
				values = append(values, p)
			}
			_ = rdb.SAdd(ctx, key, values...).Err()
			_ = rdb.Expire(ctx, key, permissionCacheTTL).Err()
		}
	}

	for _, perm := range perms {
		if matchPermission(method, path, perm) {
			return true
		}
	}
	return false
}
//matchPermission函数用于检查一个权限字符串是否匹配给定的HTTP方法和路径。权限字符串的格式为"METHOD:PATH"，其中METHOD可以是具体的HTTP方法或通配符"*"，PATH可以是具体的路径、以"*"结尾的前缀路径或通配符"*"。函数首先解析权限字符串，然后根据规则进行匹配，如果权限方法不匹配则返回false，如果权限路径是通配符则返回true，如果权限路径以"*"结尾则检查前缀匹配，否则直接比较路径是否相等。
func matchPermission(method string, path string, permission string) bool {
	parts := strings.SplitN(permission, ":", 2)
	if len(parts) != 2 {
		return false
	}
	permMethod := strings.ToUpper(strings.TrimSpace(parts[0]))
	permPath := strings.TrimSpace(parts[1])
	method = strings.ToUpper(method)

	if permMethod != "*" && permMethod != method {
		return false
	}

	if permPath == "*" {
		return true
	}

	if strings.HasSuffix(permPath, "*") {
		prefix := strings.TrimSuffix(permPath, "*")
		return strings.HasPrefix(path, prefix)
	}

	return path == permPath
}
