//go:build integration && darwin

package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cloudenest_go/model"
)

// TestMacOSClientSession exercises the shipping Swift core against the real Go
// router, MySQL and Redis. The dedicated runner supplies disposable services.
func TestMacOSClientSession(t *testing.T) {
	if os.Getenv("CLOUDNEST_TEST_SWIFT") != "1" {
		t.Skip("run scripts/test-macos-backend.py on a macOS development runner")
	}
	f := newFixture(t)
	var lostInitializations sync.Map
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/files/upload/init" {
			body, err := io.ReadAll(io.LimitReader(r.Body, (16<<10)+1))
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
			var input struct {
				Filename string `json:"filename"`
			}
			if json.Unmarshal(body, &input) == nil && strings.HasPrefix(input.Filename, "lost-init-") {
				key := r.Header.Get("Idempotency-Key")
				if key == "" {
					t.Error("native transfer omitted durable initialization key")
					w.WriteHeader(400)
					return
				}
				if _, alreadyLost := lostInitializations.LoadOrStore(key, true); !alreadyLost {
					var disconnected atomic.Bool
					f.router.ServeHTTP(disconnectReceiptWriter{w, &disconnected}, r)
					if !disconnected.Load() {
						t.Error("native initialization did not reach response loss")
					}
					return
				}
			}
		}
		if r.URL.Path == "/api/files/upload/chunk" || strings.HasSuffix(r.URL.Path, "/download") {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
		f.router.ServeHTTP(w, r)
	}))
	defer server.Close()
	redirectServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/login" {
			t.Error("Swift followed the credential-bearing redirect")
			w.WriteHeader(http.StatusTeapot)
			return
		}
		http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
	}))
	defer redirectServer.Close()
	certificatePath := filepath.Join(t.TempDir(), "server.der")
	if err := os.WriteFile(certificatePath, server.Certificate().Raw, 0600); err != nil {
		t.Fatal(err)
	}
	response := f.json(t, "POST", "/api/files/folders", f.bob,
		map[string]any{"name": "Other-user-folder"}, 200)
	var otherFolder struct {
		Item model.File `json:"item"`
	}
	decode(t, response, &otherFolder)
	versioned := fixtureNativeVersionHistory(t, f, "Native version history.bin", bytes.Repeat([]byte{0x31}, 9*1024*1024+173), []byte("Current version for native history\n"))
	packagePath, err := filepath.Abs("../../cloudnest_macos")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		id         int64
		restricted string
	}{
		{"default-permissions", f.aliceID, "0"},
		{"custom-permissions", f.bobID, "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var user model.User
			if err := f.db.First(&user, tc.id).Error; err != nil {
				t.Fatal(err)
			}
			if tc.restricted == "1" {
				permission := model.UserPathPermission{UserID: user.ID, Method: "GET", Path: "/api/files"}
				if err := f.db.Create(&permission).Error; err != nil {
					t.Fatal(err)
				}
			}
			filter := os.Getenv("CLOUDNEST_SWIFT_TEST_FILTER")
			if filter == "" {
				filter = "realBackend"
			}
			command := exec.Command("swift", "test", "--package-path", packagePath,
				"--filter", filter)
			command.Env = append(os.Environ(),
				"CLOUDNEST_SWIFT_ORIGIN="+server.URL,
				"CLOUDNEST_SWIFT_REDIRECT_ORIGIN="+redirectServer.URL,
				"CLOUDNEST_SWIFT_USERNAME="+user.Username,
				"CLOUDNEST_SWIFT_PASSWORD=disposable-fixture-password",
				"CLOUDNEST_SWIFT_CERTIFICATE="+certificatePath,
				"CLOUDNEST_SWIFT_RESTRICTED="+tc.restricted,
				"CLOUDNEST_SWIFT_VERSION_FILE="+strconv.FormatInt(versioned.ID, 10),
				"CLOUDNEST_SWIFT_FORBIDDEN_FILE="+strconv.FormatInt(otherFolder.Item.ID, 10))
			output, err := command.CombinedOutput()
			t.Log(string(output))
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func fixtureNativeVersionHistory(t *testing.T, f *fixture, name string, original, current []byte) model.File {
	t.Helper()
	file := f.upload(t, f.alice, name, original)
	source := f.upload(t, f.alice, "Source for "+name, current)
	if source.StorageID == nil {
		t.Fatal("missing fixture storage")
	}
	f.json(t, "POST", "/api/files/"+strconv.FormatInt(file.ID, 10)+"/versions", f.alice,
		map[string]any{"storage_id": *source.StorageID, "size": source.Size, "base_version": 1, "comment": "Native history fixture replacement"}, 200)
	if err := f.db.First(&file, file.ID).Error; err != nil {
		t.Fatal(err)
	}
	return file
}
