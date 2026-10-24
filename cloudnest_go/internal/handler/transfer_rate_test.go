package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

type flushFailureWriter struct{ *httptest.ResponseRecorder }

func (w *flushFailureWriter) FlushError() error { return io.ErrClosedPipe }

func TestDownloadReportsFinalBufferedFlushFailure(t *testing.T) {
	transport := &flushFailureWriter{httptest.NewRecorder()}
	c, _ := gin.CreateTestContext(transport)
	w := &pacedResponseWriter{ResponseWriter: c.Writer, pacer: transferPacer{ctx: context.Background(), rate: 10240 * 1024, start: time.Now()}}
	if n, err := w.Write([]byte("last bytes")); n != 10 || err != io.ErrClosedPipe || w.finish() != io.ErrClosedPipe {
		t.Fatal("final transport flush error was hidden", n, err)
	}
}

type observedDownloadWriter struct {
	http.ResponseWriter
	entered atomic.Int64
}

func (w *observedDownloadWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *observedDownloadWriter) Write(p []byte) (int, error) {
	w.entered.Store(time.Now().UnixNano())
	defer w.entered.Store(0)
	return w.ResponseWriter.Write(p)
}
func (w *observedDownloadWriter) FlushError() error {
	w.entered.Store(time.Now().UnixNano())
	defer w.entered.Store(0)
	return http.NewResponseController(w.ResponseWriter).Flush()
}

type zeroDownloadReader struct{}

func (zeroDownloadReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestDownloadCancellationInterruptsBlockedHTTP2Transport(t *testing.T) {
	observed := make(chan *observedDownloadWriter, 1)
	cancels := make(chan context.CancelFunc, 1)
	done := make(chan error, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(raw http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		transport := &observedDownloadWriter{ResponseWriter: raw}
		observed <- transport
		cancels <- cancel
		c, _ := gin.CreateTestContext(transport)
		c.Header("Content-Length", "67108864")
		w := &pacedResponseWriter{ResponseWriter: c.Writer, writeTimeout: 10 * time.Second, pacer: transferPacer{ctx: ctx, rate: 10240 * 1024, start: time.Now()}}
		_, err := io.CopyN(w, zeroDownloadReader{}, 64<<20)
		if err == nil {
			err = w.finish()
		}
		done <- err
		if err != nil {
			panic(http.ErrAbortHandler)
		}
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	defer server.CloseClientConnections()
	response, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.ProtoMajor != 2 {
		t.Fatal("HTTP/2 not negotiated")
	}
	transport, cancel := <-observed, <-cancels
	defer cancel()
	until := time.Now().Add(5 * time.Second)
	blocked := false
	for time.Now().Before(until) {
		if entered := transport.entered.Load(); entered != 0 && time.Since(time.Unix(0, entered)) > 100*time.Millisecond {
			blocked = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("test never blocked in transport IO")
	}
	cancel() // server-side scope cancellation, not a client disconnect
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled blocked stream reported success")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation waited for the ten-second write deadline")
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
