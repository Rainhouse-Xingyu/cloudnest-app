package middleware

import (
	"crypto/sha256"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

const loginAttemptLimit = 30
const loginAttemptWindow = time.Minute

// Count all attempts (including malformed requests and successful logins) before
// parsing credentials or accessing MySQL/bcrypt. One atomic Redis script shares
// the budget across instances and never extends a blocked peer's cooldown.
var loginAttemptScript = redis.NewScript(`
local count = tonumber(redis.call('GET', KEYS[1]) or '0')
if count >= tonumber(ARGV[1]) then
    return {0, redis.call('PTTL', KEYS[1])}
end
count = redis.call('INCR', KEYS[1])
if count == 1 then
    redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return {1, redis.call('PTTL', KEYS[1])}
`)

// LoginRateLimit uses only the actual TCP peer, never an untrusted forwarding
// header. A reverse proxy's clients consequently share a budget; deployments
// requiring per-client limits must enforce those at their trusted ingress.
func LoginRateLimit(rdb *redis.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		key, ok := loginPeerKey(c.Request.RemoteAddr)
		if !ok || rdb == nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "login protection unavailable"})
			return
		}
		result, err := loginAttemptScript.Run(c.Request.Context(), rdb, []string{key}, loginAttemptLimit, loginAttemptWindow.Milliseconds()).Int64Slice()
		if err != nil || len(result) != 2 || result[1] < 0 {
			// Redis errors must not silently remove brute-force protection.
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "login protection unavailable"})
			return
		}
		if result[0] == 0 {
			retrySeconds := max(int64(1), (result[1]+999)/1000)
			c.Header("Retry-After", strconv.FormatInt(retrySeconds, 10))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "too many login attempts; retry later"})
			return
		}
		c.Next()
	}
}

func loginPeerKey(remoteAddr string) (string, bool) {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return "", false
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return "", false
	}
	// IPv4-mapped IPv6 and IPv6 textual variants must share the same budget.
	peer := ip.Unmap().WithZone("").String()
	return fmt.Sprintf("auth:login-rate:%x", sha256.Sum256([]byte(peer))), true
}
