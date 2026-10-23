package middleware

import (
	"context"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"cloudenest_go/internal/blob"
	"cloudenest_go/internal/service/contract"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

const UploadBodyTimeout = 4 * time.Minute
const MaxUploadBodyBytes = contract.MaxUploadChunkBytes + 64*1024
const UploadBodyGlobalKey = "cloudnest:upload-body:v1:all"

func UploadBodyUserKey(uid int64) string {
	return "cloudnest:upload-body:v1:user:" + strconv.FormatInt(uid, 10)
}

const admitUploadBody = `local t=redis.call('TIME'); local now=t[1]*1000+math.floor(t[2]/1000)
redis.call('ZREMRANGEBYSCORE',KEYS[1],'-inf',now);redis.call('ZREMRANGEBYSCORE',KEYS[2],'-inf',now)
if redis.call('ZCARD',KEYS[2])>=2 or redis.call('ZCARD',KEYS[1])>=16 then return 0 end
local expiry=now+tonumber(ARGV[2]);redis.call('ZADD',KEYS[1],expiry,ARGV[1]);redis.call('ZADD',KEYS[2],expiry,ARGV[1])
redis.call('PEXPIRE',KEYS[1],ARGV[2]);redis.call('PEXPIRE',KEYS[2],ARGV[2]);return 1`
const releaseUploadBody = `redis.call('ZREM',KEYS[1],ARGV[1]);redis.call('ZREM',KEYS[2],ARGV[1]);return 1`

// Authenticated requests share a fail-closed distributed admission gate before
// any multipart parsing. Slots outlive the enforced body deadline by 30 seconds.
func UploadBodyBudget(rdb *redis.Client, timeout time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		controller := http.NewResponseController(c.Writer)
		reject := func(status int, message string) {
			if c.Request.ProtoMajor < 2 {
				c.Header("Connection", "close")
				_ = controller.SetReadDeadline(time.Now())
			}
			c.AbortWithStatusJSON(status, gin.H{"error": message})
		}
		if timeout <= 0 || timeout > UploadBodyTimeout {
			reject(503, "upload deadline unavailable")
			return
		}
		if c.Request.ContentLength > MaxUploadBodyBytes {
			reject(413, "upload body too large")
			return
		}
		uid := c.GetInt64(ContextUserIDKey)
		if uid <= 0 {
			reject(401, "unauthorized")
			return
		}
		token, err := blob.RandomToken()
		if err != nil {
			reject(503, "upload admission unavailable")
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
		defer cancel()
		keys := []string{UploadBodyGlobalKey, UploadBodyUserKey(uid)}
		defer func() {
			cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			defer stop()
			if err := rdb.Eval(cleanup, releaseUploadBody, keys, token).Err(); err != nil {
				log.Print("upload admission release pending; token will expire")
			}
		}()
		admission, stop := context.WithTimeout(ctx, 3*time.Second)
		accepted, err := rdb.Eval(admission, admitUploadBody, keys, token, (timeout + 30*time.Second).Milliseconds()).Int()
		stop()
		if err != nil {
			reject(503, "upload admission unavailable")
			return
		}
		if accepted != 1 {
			c.Header("Retry-After", "1")
			reject(429, "too many active upload bodies")
			return
		}
		deadline, _ := ctx.Deadline()
		if err := controller.SetReadDeadline(deadline); err != nil {
			reject(503, "upload deadline unavailable")
			return
		}
		originalBody := c.Request.Body
		tracked := &uploadBodyCompletion{ReadCloser: originalBody}
		// Do not clear the current request deadline before net/http finishes
		// draining unread HTTP/1 bodies. Its next-request/idle loop resets the
		// deadline itself. HTTP/2 retains its per-stream deadline until closure.
		defer func() {
			if c.Request.ProtoMajor < 2 && !tracked.eof {
				if err := controller.SetReadDeadline(time.Now()); err != nil {
					log.Print("upload post-handler read deadline failed")
				}
			}
		}()
		stopClose := context.AfterFunc(ctx, func() { _ = originalBody.Close() })
		defer stopClose()
		c.Request = c.Request.WithContext(ctx)
		c.Request.Body = http.MaxBytesReader(c.Writer, tracked, MaxUploadBodyBytes)
		c.Next()
	}
}

// Accessed by the handler goroutine only; Close does not mutate EOF state.
type uploadBodyCompletion struct {
	io.ReadCloser
	eof bool
}

func (r *uploadBodyCompletion) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if err == io.EOF {
		r.eof = true
	}
	return n, err
}
