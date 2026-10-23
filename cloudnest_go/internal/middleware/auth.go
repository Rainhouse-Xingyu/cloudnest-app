package middleware

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"cloudenest_go/internal/auth"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

const (
	ContextUserIDKey    = "user_id"
	ContextUsernameKey  = "username"
	ContextIsAdminKey   = "is_admin"
	ContextTokenKey     = "token"
	redisTokenKeyPrefix = "auth:token:"
)

// Identity is reloaded for each authenticated request so account/role changes
// do not depend on the expiry of an already issued token.
type Identity struct {
	AuthVersion uint64
	Username    string
	Enabled     bool
	IsAdmin     bool
}

// Authenticate validates the signature, Redis session owner, current account
// state and explicit method/path permissions. Public routes live outside it.
func Authenticate(rdb *redis.Client, jwtSecret string, loadPermissions func(context.Context, int64) ([]string, error), loadIdentity func(context.Context, int64) (Identity, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
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
		owner, err := rdb.Get(ctx, tokenKey).Result()
		if err == redis.Nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "token expired or logged out"})
			c.Abort()
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "redis error"})
			c.Abort()
			return
		}
		if owner != strconv.FormatInt(claims.UserID, 10) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid session owner"})
			c.Abort()
			return
		}
		identity, err := loadIdentity(ctx, claims.UserID)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "identity lookup failed"})
			c.Abort()
			return
		}
		if !identity.Enabled {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "user disabled or removed"})
			c.Abort()
			return
		}

		if claims.AuthVersion != identity.AuthVersion {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "session revoked"})
			return
		}

		c.Set(ContextUserIDKey, claims.UserID)
		c.Set(ContextUsernameKey, identity.Username)
		c.Set(ContextIsAdminKey, identity.IsAdmin)
		c.Set(ContextTokenKey, token)

		// These exact routes only operate on the authenticated caller's session.
		// Business grants must not prevent inspecting or revoking that session.
		if identity.IsAdmin || isSelfSessionRoute(c.Request.Method, c.FullPath()) {
			c.Next()
			return
		}

		if !checkPathPermission(ctx, claims.UserID, c.Request.Method, c.Request.URL.Path, c.FullPath(), loadPermissions) {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			c.Abort()
			return
		}

		c.Next()
	}
}

func isSelfSessionRoute(method, route string) bool {
	return method == http.MethodGet && route == "/api/auth/me" ||
		method == http.MethodPost && (route == "/api/auth/logout" || route == "/api/auth/logout-all")
}

// RequireAdmin函数是一个Gin中间件，用于检查当前用户是否具有管理员权限。它从上下文中获取管理员标志，如果不存在或为false，则返回403错误并终止请求处理，否则继续执行后续的处理函数。
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

// extractBearerToken函数用于从Authorization头中提取Bearer token，如果格式不正确则返回空字符串。
func extractBearerToken(authHeader string) string {
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

// checkPathPermission uses the current database policy. A cached grant must not
// remain effective after revocation or a failed Redis invalidation.
func checkPathPermission(ctx context.Context, userID int64, method string, path string, route string, loadPermissions func(context.Context, int64) ([]string, error)) bool {
	perms, err := loadPermissions(ctx, userID)
	if err != nil {
		return false
	}

	// 未配置自定义权限时，只允许显式列出的业务方法和路由。
	if len(perms) == 0 {
		return defaultAllowPath(method, route)
	}

	for _, perm := range perms {
		if matchPermission(method, path, perm) {
			return true
		}
	}
	return false
}

// Explicit authenticated-user capabilities. Keys are Gin route templates, not
// URL prefixes. Unlisted routes/methods are denied; resource ACLs still apply.
var defaultUserRoutes = map[string]struct{}{
	"POST /api/auth/logout-all":                    {},
	"POST /api/auth/logout":                        {},
	"GET /api/auth/me":                             {},
	"GET /api/resource/health":                     {},
	"GET /api/files":                               {},
	"POST /api/files/folders":                      {},
	"PUT /api/files/:id/rename":                    {},
	"DELETE /api/files/:id":                        {},
	"POST /api/files/upload/pause":                 {},
	"POST /api/files/upload/resume":                {},
	"POST /api/files/upload/cancel":                {},
	"POST /api/files/upload/init":                  {},
	"POST /api/files/upload/chunk":                 {},
	"POST /api/files/upload/complete":              {},
	"GET /api/files/upload/progress":               {},
	"GET /api/files/search":                        {},
	"GET /api/files/:id/download":                  {},
	"GET /api/files/:id/download-zip":              {},
	"POST /api/files/batch-download":               {},
	"POST /api/files/batch-delete":                 {},
	"GET /api/files/:id/stream":                    {},
	"GET /api/files/:id/thumbnail":                 {},
	"POST /api/files/move":                         {},
	"POST /api/files/copy":                         {},
	"GET /api/files/recycle-bin":                   {},
	"POST /api/files/:id/restore":                  {},
	"DELETE /api/files/:id/permanent":              {},
	"GET /api/files/:id/preview":                   {},
	"POST /api/files/:id/share":                    {},
	"POST /api/files/:id/collaborators":            {},
	"GET /api/files/:id/collaborators":             {},
	"DELETE /api/files/:id/collaborators/:user_id": {},
	"POST /api/files/:id/versions":                 {},
	"GET /api/files/:id/versions":                  {},
	"POST /api/workspaces":                         {},
	"GET /api/workspaces":                          {},
	"POST /api/workspaces/:id/members":             {},
	"GET /api/workspaces/:id/members":              {},
	"PUT /api/workspaces/:id/members/:user_id":     {},
	"DELETE /api/workspaces/:id/members/:user_id":  {},
	"GET /api/shares":                              {},
	"DELETE /api/shares/:code":                     {},
	"GET /api/operation-logs":                      {},
	"GET /api/operation-logs/export":               {},
	"GET /api/settings":                            {},
	"GET /api/settings/rate-window":                {},
	"PUT /api/settings/rate-window":                {},
	"GET /api/storage/overview":                    {},
	"POST /api/frp/devices":                        {},
	"GET /api/frp/devices":                         {},
	"GET /api/frp/devices/:id":                     {},
	"PUT /api/frp/devices/:id":                     {},
	"DELETE /api/frp/devices/:id":                  {},
	"POST /api/frp/mappings":                       {},
	"GET /api/frp/devices/:id/mappings":            {},
	"PUT /api/frp/mappings/:id":                    {},
	"DELETE /api/frp/mappings/:id":                 {},
	"POST /api/frp/configs/preview":                {},
	"POST /api/frp/configs/save":                   {},
	"GET /api/frp/devices/:id/configs":             {},
	"POST /api/frp/configs/:id/rollback":           {},
	"POST /api/frp/devices/:id/deploy-config":      {},
	"POST /api/frp/devices/:id/reload":             {},
	"POST /api/frp/devices/:id/status/report":      {},
	"GET /api/frp/devices/:id/status":              {},
	"GET /api/frp/devices/:id/commands/next":       {},
	"POST /api/frp/commands/:command_id/result":    {},
	"GET /api/frp/devices/:id/commands":            {},
	"POST /api/live-photos/bind":                   {},
	"POST /api/live-photos/auto-bind":              {},
	"DELETE /api/live-photos/:id":                  {},
	"GET /api/live-photos/:id":                     {},
	"GET /api/live-photos":                         {},
	"GET /api/files/:id/live-photo":                {},
	"POST /api/ai/ocr":                             {},
	"GET /api/ai/search":                           {},
	"POST /api/ai/search/nl":                       {},
	"POST /api/ai/summarize":                       {},
	"POST /api/ai/assistant/ask":                   {},

	"GET /api/files/:id/versions/:version/download": {},
	"POST /api/files/:id/versions/:version/restore": {},
}

func defaultAllowPath(method string, route string) bool {
	_, allowed := defaultUserRoutes[method+" "+route]
	return allowed
}

// matchPermission函数用于检查一个权限字符串是否匹配给定的HTTP方法和路径。权限字符串的格式为"METHOD:PATH"，其中METHOD可以是具体的HTTP方法或通配符"*"，PATH可以是具体的路径、以"*"结尾的前缀路径或通配符"*"。函数首先解析权限字符串，然后根据规则进行匹配，如果权限方法不匹配则返回false，如果权限路径是通配符则返回true，如果权限路径以"*"结尾则检查前缀匹配，否则直接比较路径是否相等。
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
