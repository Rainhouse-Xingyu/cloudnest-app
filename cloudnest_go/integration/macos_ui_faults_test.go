//go:build integration && darwin

package integration

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type nativeUIResponseWriter struct {
	http.ResponseWriter
	status  int
	written int64
}

func (w *nativeUIResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *nativeUIResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *nativeUIResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(data)
	w.written += int64(n)
	return n, err
}

// Test-only wire interruption after a bounded amount of the real response.
// Preserve ResponseController access to the underlying network connection.
type nativeUIInterruptWriter struct {
	http.ResponseWriter
	remaining   int64
	interrupted bool
}

func (w *nativeUIInterruptWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *nativeUIInterruptWriter) Write(data []byte) (int, error) {
	if w.remaining == 0 {
		w.interrupted = true
		return 0, io.ErrUnexpectedEOF
	}
	count := int64(len(data))
	if count > w.remaining {
		count = w.remaining
	}
	n, err := w.ResponseWriter.Write(data[:count])
	w.remaining -= int64(n)
	if err == nil && n < len(data) {
		w.interrupted = true
		err = io.ErrUnexpectedEOF
	}
	return n, err
}

func TestMacOSNativeUIInterruptsActualHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed := &nativeUIResponseWriter{ResponseWriter: w}
		writer := &nativeUIInterruptWriter{ResponseWriter: observed, remaining: 4096}
		observed.Header().Set("Content-Length", "8192")
		observed.Header().Set("Content-Range", "bytes 0-8191/8192")
		observed.WriteHeader(http.StatusPartialContent)
		if _, err := io.Copy(writer, strings.NewReader(strings.Repeat("x", 8192))); err != nil {
			if observed.status != http.StatusPartialContent || observed.written != 4096 || !writer.interrupted {
				t.Error("response observer did not capture the actual interrupted representation")
			}
			// Same abort mechanism used by the shipping download handler when
			// a write fails after headers have been sent.
			if err := http.NewResponseController(writer).Flush(); err != nil {
				t.Error(err)
			}
			panic(http.ErrAbortHandler)
		}
		t.Error("fault did not interrupt the response")
	}))
	defer server.Close()
	response, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	written, err := io.Copy(io.Discard, response.Body)
	if response.StatusCode != http.StatusPartialContent || written != 4096 || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("real wire interruption: status=%d bytes=%d error=%v", response.StatusCode, written, err)
	}
}
