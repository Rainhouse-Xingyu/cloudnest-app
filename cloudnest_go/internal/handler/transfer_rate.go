package handler

import (
	"context"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"cloudenest_go/internal/middleware"
	"cloudenest_go/internal/service/contract"
	"github.com/gin-gonic/gin"
)

func (h *FileHandler) transferRate(c *gin.Context, direction string) (int64, error) {
	key := "rate_kbps"
	if direction == "upload" {
		key = "upload_rate_kbps"
	}
	values := c.Request.URL.Query()[key]
	if len(values) > 1 {
		return 0, contract.ErrInvalidInput
	}
	requested := ""
	if len(values) == 1 {
		requested = strings.TrimSpace(values[0])
		if requested == "" {
			return 0, contract.ErrInvalidInput
		}
	}
	return h.Svc.TransferRate(c.Request.Context(), c.GetInt64(middleware.ContextUserIDKey), direction, requested)
}

// pacedResponseWriter caps every body write, including ServeContent's full,
// single-range and multipart-range bodies. Wrapping only the Reader allows
// WriterTo/sendfile optimizations to bypass pacing, so the writer is fenced.
// Wait BEFORE writing: a short response/each new request gets no free burst.
type transferPacer struct {
	ctx      context.Context
	rate     int64
	start    time.Time
	reserved int64
}

func (p *transferPacer) wait(n int) error {
	if p.rate <= 0 || p.rate > 10240*1024 || n < 0 || int64(n) > math.MaxInt64-p.reserved {
		return contract.ErrRatePolicy
	}
	p.reserved += int64(n)
	seconds, remainder := p.reserved/p.rate, p.reserved%p.rate
	fraction := remainder * int64(time.Second) / p.rate
	if seconds > (math.MaxInt64-fraction)/int64(time.Second) {
		return contract.ErrRatePolicy
	}
	target := time.Duration(seconds*int64(time.Second) + fraction)
	if wait := target - time.Since(p.start); wait > 0 {
		timer := time.NewTimer(wait)
		select {
		case <-p.ctx.Done():
			timer.Stop()
			return p.ctx.Err()
		case <-timer.C:
		}
	}
	return p.ctx.Err()
}

type pacedResponseWriter struct {
	gin.ResponseWriter
	pacer transferPacer
	err   error
}

func (w *pacedResponseWriter) Unwrap() http.ResponseWriter       { return w.ResponseWriter }
func (w *pacedResponseWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
func (w *pacedResponseWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	total := 0
	for len(p) > 0 {
		n := min(len(p), 16<<10)
		if err := w.pacer.wait(n); err != nil {
			w.err = err
			return total, err
		}
		written, err := w.ResponseWriter.Write(p[:n])
		total += written
		if err == nil && written != n {
			err = io.ErrShortWrite
		}
		if err != nil {
			w.err = err
			return total, err
		}
		p = p[n:]
	}
	return total, nil
}
func (h *FileHandler) sendFileWithRateLimit(c *gin.Context, filePath, downloadName string, bytesPerSec int64) error {
	if bytesPerSec <= 0 {
		return contract.ErrRatePolicy
	}
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	c.Header("Content-Disposition", `attachment; filename="`+sanitizeFileName(downloadName)+`"`)
	original := c.Writer
	writer := &pacedResponseWriter{ResponseWriter: original, pacer: transferPacer{ctx: c.Request.Context(), rate: bytesPerSec, start: time.Now()}}
	c.Writer = writer
	defer func() { c.Writer = original }()
	http.ServeContent(writer, c.Request, sanitizeFileName(downloadName), stat.ModTime(), f)
	return writer.err
}
func writeDownloadError(c *gin.Context, err error) {
	// Once bytes/headers were sent, appending JSON would corrupt the file/range
	// body. Terminate the stream so clients observe a truncated Content-Length.
	if c.Writer.Written() {
		c.Abort()
		panic(http.ErrAbortHandler)
	}
	writeFileMutationError(c, err, "download failed")
}
