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

func TestDownloadErrorBeforeBodyClearsRepresentationHeaders(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	for key, value := range map[string]string{"Content-Length": "999", "Content-Range": "bytes 1-9/10", "ETag": `"sha256-file"`, "Repr-Digest": "sha-256=:AAAA:", "Content-Disposition": "attachment", "Content-Type": "application/octet-stream"} {
		c.Header(key, value)
	}
	writeDownloadError(c, io.ErrUnexpectedEOF)
	if recorder.Code != 500 || recorder.Header().Get("Content-Length") != "" || recorder.Header().Get("Content-Range") != "" || recorder.Header().Get("ETag") != "" || recorder.Header().Get("Repr-Digest") != "" || !strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/json") {
		t.Fatal("error mislabeled as original file", recorder.Code, recorder.Header())
	}
}
