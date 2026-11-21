//go:build integration && darwin

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cloudenest_go/model"
)

// TestMacOSNativeUIFixture serves real disposable routes for the dedicated native
// UI harness. Normal CI skips this manual, bounded fixture.
func TestMacOSNativeUIFixture(t *testing.T) {
	dir := os.Getenv("CLOUDNEST_NATIVE_UI_FIXTURE_DIR")
	if dir == "" {
		t.Skip("set CLOUDNEST_NATIVE_UI_FIXTURE_DIR for native UI acceptance")
	}
	absolute, err := filepath.Abs(dir)
	parent := filepath.Dir(absolute)
	if err != nil || !strings.HasPrefix(filepath.Base(absolute), "cloudnest-native-ui-") ||
		(parent != "/tmp" && parent != "/private/tmp" && parent != filepath.Clean(os.TempDir())) {
		t.Fatal("a dedicated cloudnest-native-ui-* temporary directory is required")
	}
	if err := os.MkdirAll(absolute, 0700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("native UI fixture must be a real private directory")
	}
	if err := os.Chmod(absolute, 0700); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	if os.Getenv("CLOUDNEST_NATIVE_UI_LARGE_FILE") == "1" {
		rateSetting(t, f, "upload_speed_limit_kbps", "10240")
		rateSetting(t, f, "download_speed_limit_kbps", "10240")
	}
	slow := os.Getenv("CLOUDNEST_NATIVE_UI_SLOW_TRANSFERS") == "1"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if slow && r.URL.Path == "/api/files/upload/chunk" {
			r.Body = nativeUIReadCloser{ReadCloser: r.Body, context: r.Context()}
		}
		if slow && strings.HasSuffix(r.URL.Path, "/download") {
			w = &nativeUIWriter{ResponseWriter: w, context: r.Context()}
		}
		f.router.ServeHTTP(w, r)
	}))
	defer server.Close()
	certificate := filepath.Join(absolute, "server.der")
	if err := os.WriteFile(certificate, server.Certificate().Raw, 0600); err != nil {
		t.Fatal(err)
	}
	var user model.User
	if err := f.db.First(&user, f.aliceID).Error; err != nil {
		t.Fatal(err)
	}
	id := fixturePendingUpload(t, f, f.alice, "Preview.txt", []byte("CloudNest native preview acceptance.\nThis copy came from the real Go backend.\n"))
	seed := completedBlob(t, f, completeUploadRecorder(f, f.alice, id))
	fixtureFolder(t, f, f.alice, "Fixture folder", 0)
	versioned := fixtureNativeVersionHistory(t, f, "Version history.txt", []byte("Original native version.\n"), []byte("Replacement native version.\n"))
	if os.Getenv("CLOUDNEST_NATIVE_UI_VERSION_PAGES") == "1" {
		for base := 2; base < 56; base++ {
			f.json(t, "POST", fmt.Sprintf("/api/files/%d/versions/1/restore", versioned.ID), f.alice, map[string]int{"base_version": base}, 200)
		}
	}
	source := filepath.Join(absolute, "Upload from Finder.txt")
	if err := os.WriteFile(source, []byte("Uploaded through the native macOS file panel.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if slow {
		if err := os.WriteFile(filepath.Join(absolute, "Speed acceptance.bin"), bytes.Repeat([]byte{0x6e}, 9*1024*1024+173), 0600); err != nil {
			t.Fatal(err)
		}
	}
	manifest := map[string]any{
		"origin": server.URL, "certificate": certificate, "username": user.Username,
		"password": "disposable-fixture-password", "directory": absolute, "seed_file_id": seed.ID, "upload_source": source,
		"version_file_id": versioned.ID,
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(absolute, "fixture.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("Native UI fixture ready: %s", filepath.Join(absolute, "fixture.json"))
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(20 * time.Minute)
	defer timeout.Stop()
	for {
		select {
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(absolute, "stop")); err == nil {
				return
			}
		case <-timeout.C:
			t.Fatal("native UI fixture expired; all services will stop")
		case <-t.Context().Done():
			return
		}
	}
}

// Slow only this disposable fixture. Production transfer limits and routes do
// not depend on this environment variable or these wrappers.
func nativeUIPace(ctx context.Context) error {
	select {
	case <-time.After(50 * time.Millisecond):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type nativeUIReadCloser struct {
	io.ReadCloser
	context context.Context
}

func (r nativeUIReadCloser) Read(p []byte) (int, error) {
	if err := nativeUIPace(r.context); err != nil {
		return 0, err
	}
	return r.ReadCloser.Read(p[:min(len(p), 64<<10)])
}

type nativeUIWriter struct {
	http.ResponseWriter
	context context.Context
}

func (w *nativeUIWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *nativeUIWriter) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		if err := nativeUIPace(w.context); err != nil {
			return total, err
		}
		count := min(len(p), 64<<10)
		n, err := w.ResponseWriter.Write(p[:count])
		total += n
		p = p[n:]
		http.NewResponseController(w.ResponseWriter).Flush()
		if err != nil {
			return total, err
		}
		if n != count {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}
