//go:build integration && darwin

package integration

import (
	"encoding/json"
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
	server := httptest.NewTLSServer(f.router)
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
	source := filepath.Join(absolute, "Upload from Finder.txt")
	if err := os.WriteFile(source, []byte("Uploaded through the native macOS file panel.\n"), 0600); err != nil {
		t.Fatal(err)
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
