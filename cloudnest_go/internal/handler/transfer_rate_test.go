package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestDownloadPacingHasNoInitialBurstAndCancellationWritesNoBytes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	writer := &pacedResponseWriter{ResponseWriter: c.Writer, pacer: transferPacer{ctx: ctx, rate: 1, start: time.Now()}}
	start := time.Now()
	n, err := io.Copy(writer, strings.NewReader("must-not-escape"))
	if n != 0 || err == nil || recorder.Body.Len() != 0 || time.Since(start) > 250*time.Millisecond {
		t.Fatal(n, err, recorder.Body.String())
	}
}
func TestDownloadErrorAfterCommitAbortsWithoutAppendingJSON(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	_, _ = c.Writer.Write([]byte("partial-file"))
	defer func() {
		if recover() != http.ErrAbortHandler || recorder.Body.String() != "partial-file" {
			t.Fatal("download error did not abort original body")
		}
	}()
	writeDownloadError(c, io.ErrUnexpectedEOF)
}
