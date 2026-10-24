package middleware

import (
	"log"
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"
)

// RecoverHTTP preserves net/http's stream-abort signal. Ordinary panics remain
// isolated; request headers, credentials and panic values are not logged.
func RecoverHTTP() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if failure := recover(); failure != nil {
				if failure == http.ErrAbortHandler {
					panic(failure)
				}
				log.Printf("request handler panic\n%s", debug.Stack())
				if c.Writer.Written() {
					panic(http.ErrAbortHandler)
				}
				c.AbortWithStatus(http.StatusInternalServerError)
			}
		}()
		c.Next()
	}
}
