package middleware

import (
	"log"
	"net/http"

	"cloudenest_go/internal/blob"
	"github.com/gin-gonic/gin"
)

// ProtectBlobReads covers public and authenticated routes. No database lease is
// created until an authorized consumer pins actual storage bytes.
func ProtectBlobReads(manager *blob.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, scope := manager.Begin(c.Request.Context())
		c.Request = c.Request.WithContext(ctx)
		originalWriter := c.Writer
		c.Writer = &leaseResponseWriter{ResponseWriter: originalWriter, scope: scope}
		defer func() {
			// Gin may render its default 404 after middleware returns. That output
			// has no object reader and must not use an already closed scope.
			c.Writer = originalWriter
			if err := scope.Close(); err != nil {
				log.Print("storage read lease cleanup pending")
			}
		}()
		c.Next()
	}
}

type leaseResponseWriter struct {
	gin.ResponseWriter
	scope *blob.Scope
}

func (w *leaseResponseWriter) allow() error {
	if err := w.scope.Check(); err != nil {
		if !w.ResponseWriter.Written() {
			w.ResponseWriter.Header().Del("Content-Length")
			w.ResponseWriter.Header().Del("Content-Disposition")
			w.ResponseWriter.Header().Del("Content-Range")
			w.ResponseWriter.Header().Set("Content-Type", "application/json")
			w.ResponseWriter.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.ResponseWriter.Write([]byte(`{"error":"storage read unavailable"}`))
		}
		return err
	}
	return nil
}
func (w *leaseResponseWriter) Write(p []byte) (int, error) {
	if err := w.allow(); err != nil {
		return 0, err
	}
	return w.ResponseWriter.Write(p)
}
func (w *leaseResponseWriter) WriteString(s string) (int, error) {
	if err := w.allow(); err != nil {
		return 0, err
	}
	return w.ResponseWriter.WriteString(s)
}
func (w *leaseResponseWriter) Flush() {
	if w.allow() == nil {
		w.ResponseWriter.Flush()
	}
}
