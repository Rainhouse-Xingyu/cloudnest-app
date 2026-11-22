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
	"sync"
	"testing"
	"time"

	"cloudenest_go/internal/app"
	"cloudenest_go/model"
)

// Populated only by the opt-in frpnative build; ordinary UI/CI needs no FRP binary.
var nativeUIFRPAgentSetup func(*testing.T, *fixture, *httptest.Server, string, map[string]any, *func())

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
	aiLive := os.Getenv("CLOUDNEST_NATIVE_UI_AI") == "1"
	if aiLive {
		f.cfg.AIOllamaBaseURL = "http://127.0.0.1:11434"
		f.cfg.AIEmbeddingModel = "qwen3-embedding:0.6b"
		f.router = fixtureRouter(app.NewContainer(f.cfg, f.db, f.rdb).NewRouter())
	}
	if os.Getenv("CLOUDNEST_NATIVE_UI_LARGE_FILE") == "1" {
		rateSetting(t, f, "upload_speed_limit_kbps", "10240")
		rateSetting(t, f, "download_speed_limit_kbps", "10240")
	}
	slow := os.Getenv("CLOUDNEST_NATIVE_UI_SLOW_TRANSFERS") == "1"
	var traceMu sync.Mutex
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/download") {
			if _, err := os.Stat(filepath.Join(absolute, "trace-downloads")); err == nil {
				observed := &nativeUIResponseWriter{ResponseWriter: w}
				w = observed
				defer func() {
					traceMu.Lock()
					defer traceMu.Unlock()
					data, err := json.Marshal(map[string]any{"path": r.URL.Path, "range": r.Header.Get("Range"),
						"if_range": r.Header.Get("If-Range"), "status": observed.status, "written_bytes": observed.written,
						"etag": observed.Header().Get("ETag"), "utc": time.Now().UTC().Format(time.RFC3339Nano)})
					if err != nil {
						t.Error(err)
						return
					}
					file, err := os.OpenFile(filepath.Join(absolute, "download-trace.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
					if err != nil {
						t.Error(err)
						return
					}
					if _, err := file.Write(append(data, '\n')); err != nil {
						t.Error(err)
					}
					if err := file.Close(); err != nil {
						t.Error(err)
					}
				}()
			}
		}
		if slow && r.URL.Path == "/api/files/upload/chunk" {
			r.Body = nativeUIReadCloser{ReadCloser: r.Body, context: r.Context()}
		}
		if slow && strings.HasSuffix(r.URL.Path, "/download") {
			w = &nativeUIWriter{ResponseWriter: w, context: r.Context()}
		}
		if strings.HasSuffix(r.URL.Path, "/download") && os.Remove(filepath.Join(absolute, "interrupt-next-download")) == nil {
			writer := &nativeUIInterruptWriter{ResponseWriter: w, remaining: 5 << 20}
			defer func() {
				data, err := json.Marshal(map[string]any{"path": r.URL.Path, "range": r.Header.Get("Range"),
					"if_range": r.Header.Get("If-Range"), "written_bytes": (5 << 20) - writer.remaining, "interrupted": writer.interrupted})
				if err != nil {
					t.Error(err)
					return
				}
				if err := os.WriteFile(filepath.Join(absolute, "interrupted-download.json"), data, 0600); err != nil {
					t.Error(err)
				}
			}()
			w = writer
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
	var runtimeCleanup func()
	defer func() {
		if runtimeCleanup != nil {
			runtimeCleanup()
		}
	}()
	if os.Getenv("CLOUDNEST_NATIVE_UI_FRP_AGENT") == "1" {
		if nativeUIFRPAgentSetup == nil {
			t.Fatal("live Agent UI requires --go-tags integration,frpnative")
		}
		nativeUIFRPAgentSetup(t, f, server, absolute, manifest, &runtimeCleanup)
	} else if os.Getenv("CLOUDNEST_NATIVE_UI_FRP") == "1" {
		seed := fixtureNativeFRP(t, f)
		f.router = fixtureRouter(app.NewContainer(f.cfg, f.db, f.rdb).NewRouter())
		manifest["frp_device_id"] = seed.Devices[0]
		manifest["frp_mapping_id"] = seed.Mapping.ID
		manifest["frp_config_id"] = seed.Config.ID
		manifest["frp_command_id"] = seed.Command.ID
	}
	if aiLive {
		content := "数据库迁移方案：MySQL 升级前先备份，并验证迁移后的数据一致性。迁移失败时回滚到已验证的备份。"
		file := f.upload(t, f.alice, "Project Atlas.md", []byte(content))
		f.upload(t, f.bob, "Private Atlas.md", []byte(content))
		worker := textWorker(t, f, f.cfg.AIOllamaBaseURL)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		for {
			worked, err := worker.Step(ctx)
			if err != nil {
				t.Fatal("real UI model indexing required", err)
			}
			if !worked {
				break
			}
		}
		done := make(chan struct{})
		go func() { defer close(done); worker.Run(ctx) }()
		defer func() { cancel(); <-done }()
		manifest["ai_file_id"] = file.ID
		manifest["ai_model"] = f.cfg.AIEmbeddingModel
	}
	if os.Getenv("CLOUDNEST_NATIVE_UI_PERMISSION_ADMIN") == "1" {
		admin := model.Role{RoleName: "admin"}
		if err := f.db.Where("role_name = ?", "admin").FirstOrCreate(&admin).Error; err != nil {
			t.Fatal(err)
		}
		if err := f.db.Create(&model.UserRole{UserID: f.bobID, RoleID: admin.ID}).Error; err != nil {
			t.Fatal(err)
		}
		var operator model.User
		if err := f.db.First(&operator, f.bobID).Error; err != nil {
			t.Fatal(err)
		}
		manifest["permission_admin_username"] = operator.Username
		manifest["permission_user_id"] = user.ID
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
