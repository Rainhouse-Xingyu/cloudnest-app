package handler

import (
	"net"
	"net/http"
	"sort"
	"strings"

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
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
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
		if err == service.ErrPasswordResetRequired {
			c.JSON(http.StatusForbidden, gin.H{"error": "legacy default administrator password must be reset by the operator"})
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

	requestHost := strings.TrimSpace(c.Request.Host)
	lanIPs := detectServerLANIPv4()

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
		"server_hosts": gin.H{
			"request_host": requestHost,
			"lan_ips":      lanIPs,
		},
	})
}

// detectServerLANIPv4 收集服务端可用于内网直连的 IPv4 地址。
func detectServerLANIPv4() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return []string{}
	}

	set := make(map[string]struct{})
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, addrErr := iface.Addrs()
		if addrErr != nil {
			continue
		}
		for _, addr := range addrs {
			ip, _, parseErr := net.ParseCIDR(addr.String())
			if parseErr != nil || ip == nil {
				continue
			}
			v4 := ip.To4()
			if v4 == nil {
				continue
			}
			if !isPrivateIPv4(v4) {
				continue
			}
			set[v4.String()] = struct{}{}
		}
	}

	rows := make([]string, 0, len(set))
	for ip := range set {
		rows = append(rows, ip)
	}
	sort.Strings(rows)
	return rows
}

func isPrivateIPv4(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}

	// RFC1918 私网段 + 链路本地地址
	if ip[0] == 10 {
		return true
	}
	if ip[0] == 172 && ip[1] >= 16 && ip[1] <= 31 {
		return true
	}
	if ip[0] == 192 && ip[1] == 168 {
		return true
	}
	if ip[0] == 169 && ip[1] == 254 {
		return true
	}
	return false
}

// Logout方法处理注销的HTTP请求，首先从上下文中获取JWT字符串，然后删除Redis中的相关缓存，最后返回注销成功的消息给客户端。
func (h *AuthHandler) Logout(c *gin.Context) {
	tokenAny, ok := c.Get(middleware.ContextTokenKey)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "not logged in"})
		return
	}
	token := tokenAny.(string)
	if err := h.Svc.Logout(c.Request.Context(), token); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "could not revoke session; retry logout"})
		return
	}
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

// LogoutAll only affects the authenticated account; never bind a target user ID.
func (h *AuthHandler) LogoutAll(c *gin.Context) {
	if err := h.Svc.LogoutAll(c.Request.Context(), c.GetInt64(middleware.ContextUserIDKey), c.ClientIP()); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "could not revoke all sessions; retry"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "all sessions revoked; sign in again"})
}
