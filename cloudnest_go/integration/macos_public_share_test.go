//go:build integration && darwin

package integration

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cloudenest_go/model"
)

type publicShareCorruptWriter struct {
	http.ResponseWriter
	changed bool
}

type publicShareCountingWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *publicShareCountingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *publicShareCountingWriter) Flush()                      { _ = http.NewResponseController(w.ResponseWriter).Flush() }
func (w *publicShareCountingWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *publicShareCountingWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, err
}

func (w *publicShareCorruptWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *publicShareCorruptWriter) Flush()                      { _ = http.NewResponseController(w.ResponseWriter).Flush() }
func (w *publicShareCorruptWriter) Write(p []byte) (int, error) {
	if !w.changed && len(p) > 0 {
		w.changed = true
		copy := append([]byte(nil), p...)
		copy[0] ^= 0xff
		return w.ResponseWriter.Write(copy)
	}
	return w.ResponseWriter.Write(p)
}

func TestMacOSPublicShareReceiver(t *testing.T) {
	if os.Getenv("CLOUDNEST_TEST_SWIFT") != "1" {
		t.Skip("set CLOUDNEST_TEST_SWIFT=1 for the real native client")
	}
	f := newFixture(t)
	var owner model.User
	if err := f.db.First(&owner, f.aliceID).Error; err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 16*1024*1024+173)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	file := f.upload(t, f.alice, "Public native large.bin", payload)
	empty := emptyComplete(t, f, f.alice, emptyInit(t, f, f.alice, "Public empty.txt"))
	var folder struct{ Item model.File }
	decode(t, f.json(t, "POST", "/api/files/folders", f.alice, map[string]any{"name": "Public folder"}, 200), &folder)
	f.uploadTo(t, f.alice, "Shared child.txt", []byte("Verified public ZIP child.\n"), folder.Item.ID)
	create := func(id int64, limit int) string {
		t.Helper()
		body, err := json.Marshal(map[string]any{"passcode": "fixture-code", "max_downloads": limit, "expire_hours": 1})
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Code string `json:"share_code"`
		}
		decode(t, f.request(t, "POST", fmt.Sprintf("https://cloudnest.test/api/files/%d/share", id), f.alice, "application/json", bytes.NewReader(body), 200), &result)
		if result.Code == "" {
			t.Fatal("missing fixture share capability")
		}
		return result.Code
	}
	code, slow, corrupt := create(file.ID, 1), create(file.ID, 0), create(file.ID, 0)
	emptyCode, folderCode, expired := create(empty.ID, 0), create(folder.Item.ID, 0), create(file.ID, 0)
	if err := f.db.Model(&model.Share{}).Where("share_code=?", expired).Update("expire_time", time.Now().Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	var redirects atomic.Int32
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirects.Add(1); w.WriteHeader(200) }))
	defer other.Close()
	type partialResponse struct {
		status           int
		bytes            int64
		elapsed          time.Duration
		contextCancelled bool
	}
	cancelled := make(chan partialResponse, 1)
	var downloads atomic.Int32
	var slowRequests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/public/shares/") {
			if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Proxy-Authorization") != "" || r.Header.Get("Range") != "" || r.URL.RawQuery != "" {
				t.Error("anonymous native request leaked account state or introduced a range/query")
				http.Error(w, "invalid anonymous request", 400)
				return
			}
			if r.URL.Path == "/api/public/shares/Redirect1234" {
				http.Redirect(w, r, other.URL+"/capture", http.StatusFound)
				return
			}
			if r.URL.Path == "/api/public/shares/Unauthorized1" {
				w.WriteHeader(401)
				return
			}
			if strings.HasSuffix(r.URL.Path, "/download") {
				downloads.Add(1)
			}
		}
		if r.URL.Path == "/api/public/shares/"+corrupt+"/download" {
			f.router.ServeHTTP(&publicShareCorruptWriter{ResponseWriter: w}, r)
			return
		}
		if r.URL.Path == "/api/public/shares/"+slow+"/download" {
			slowRequests.Add(1)
			t.Log("Slow anonymous HTTP body started")
			// Explicit test-only pacing uses the real production handler/writer.
			clone := r.Clone(r.Context())
			url := *r.URL
			url.RawQuery = "rate_kbps=16"
			clone.URL = &url
			started := time.Now()
			counter := &publicShareCountingWriter{ResponseWriter: w}
			// A cancelled response deliberately panics with http.ErrAbortHandler
			// after committing headers. Observe completion while unwinding too;
			// leave the transport abort for net/http to handle unchanged.
			defer func() {
				cancelled <- partialResponse{counter.status, counter.bytes, time.Since(started), r.Context().Err() != nil}
			}()
			f.router.ServeHTTP(counter, clone)
			return
		}
		f.router.ServeHTTP(w, r)
	}))
	defer server.Close()
	directory := t.TempDir()
	certificate := filepath.Join(directory, "server.der")
	if err := os.WriteFile(certificate, server.Certificate().Raw, 0600); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{"origin": server.URL, "certificate": certificate, "username": owner.Username,
		"password": "disposable-fixture-password", "passcode": "fixture-code", "code": code, "slow_code": slow,
		"corrupt_code": corrupt, "empty_code": emptyCode, "folder_code": folderCode, "expired_code": expired,
		"source_size": len(payload), "source_sha256": fmt.Sprintf("%x", sha256.Sum256(payload)), "directory": directory}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(directory, "public-fixture.json")
	if err := os.WriteFile(manifestPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	packagePath, err := filepath.Abs("../../cloudnest_macos")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("swift", "test", "--package-path", packagePath, "--filter", "realBackendPublicShareReceiver")
	command.Env = append(os.Environ(), "CLOUDNEST_SWIFT_PUBLIC_SHARE_FIXTURE="+manifestPath)
	output, err := command.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatal(err)
	}
	if redirects.Load() != 0 {
		t.Fatal("anonymous share followed a redirect")
	}
	select {
	case result := <-cancelled:
		t.Logf("Slow anonymous response stopped: status=%d bytes=%d elapsed=%v parent_context_cancelled=%v", result.status, result.bytes, result.elapsed, result.contextCancelled)
		if result.status != 200 || result.bytes <= 0 || result.bytes >= int64(len(payload)) {
			t.Fatal("cancelled native response did not stop partway through the real body")
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("native cancellation did not disconnect the real HTTP body: download_requests=%d slow_requests=%d", downloads.Load(), slowRequests.Load())
	}
	waitNoReadLeases(t, f, *file.StorageID)
	var counted model.Share
	if err := f.db.First(&counted, "share_code=?", code).Error; err != nil || counted.DownloadCount != 1 {
		t.Fatal("native download replayed or overspent quota", err)
	}
	if downloads.Load() != 6 {
		t.Fatal("unexpected automatic anonymous download/retry count", downloads.Load())
	}
	archive, err := zip.OpenReader(filepath.Join(directory, "Folder.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if len(archive.File) != 2 {
		t.Fatal("unexpected public ZIP entries", len(archive.File))
	}
	var childEntry *zip.File
	for _, entry := range archive.File {
		switch entry.Name {
		case "Public folder/":
			if !entry.FileInfo().IsDir() {
				t.Fatal("public ZIP root is not a directory")
			}
		case "Public folder/Shared child.txt":
			childEntry = entry
		default:
			t.Fatal("unexpected public ZIP path")
		}
	}
	if childEntry == nil {
		t.Fatal("public ZIP child missing")
	}
	in, err := childEntry.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	child, err := io.ReadAll(in)
	if err != nil || string(child) != "Verified public ZIP child.\n" {
		t.Fatal("public ZIP body mismatch", err)
	}
}
