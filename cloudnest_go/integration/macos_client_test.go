//go:build integration && darwin

package integration

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			command := exec.Command("swift", "test", "--package-path", packagePath,
				"--filter", "realBackend")
			command.Env = append(os.Environ(),
				"CLOUDNEST_SWIFT_ORIGIN="+server.URL,
				"CLOUDNEST_SWIFT_REDIRECT_ORIGIN="+redirectServer.URL,
				"CLOUDNEST_SWIFT_USERNAME="+user.Username,
				"CLOUDNEST_SWIFT_PASSWORD=disposable-fixture-password",
				"CLOUDNEST_SWIFT_CERTIFICATE="+certificatePath,
				"CLOUDNEST_SWIFT_RESTRICTED="+tc.restricted,
				"CLOUDNEST_SWIFT_FORBIDDEN_FILE="+strconv.FormatInt(otherFolder.Item.ID, 10))
			output, err := command.CombinedOutput()
			t.Log(string(output))
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
