//go:build integration && darwin

package integration

import (
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"cloudenest_go/model"
)

func TestMacOSClientWorkspaceAccess(t *testing.T) {
	if os.Getenv("CLOUDNEST_TEST_SWIFT") != "1" {
		t.Skip("set CLOUDNEST_TEST_SWIFT=1 for the real native client")
	}
	f := newFixture(t)
	var owner, viewer model.User
	if err := f.db.First(&owner, f.aliceID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.First(&viewer, f.bobID).Error; err != nil {
		t.Fatal(err)
	}
	third := model.User{Username: fmt.Sprintf("native-workspace-third-%d", f.aliceID), Password: owner.Password, Status: 1}
	if err := f.db.Create(&third).Error; err != nil {
		t.Fatal(err)
	}
	legacy := model.Workspace{Name: "Legacy native workspace", OwnerID: f.aliceID}
	if err := f.db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&model.WorkspaceUser{WorkspaceID: legacy.ID, UserID: f.aliceID, Role: "owner"}).Error; err != nil {
		t.Fatal(err)
	}
	personal := f.upload(t, f.alice, "Personal workspace copy source.txt", []byte("Native workspace copy source.\n"))
	server := httptest.NewTLSServer(f.router)
	defer server.Close()
	certificate := filepath.Join(t.TempDir(), "server.der")
	if err := os.WriteFile(certificate, server.Certificate().Raw, 0600); err != nil {
		t.Fatal(err)
	}
	packagePath, err := filepath.Abs("../../cloudnest_macos")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		user model.User
	}{{"owner", owner}, {"restricted", viewer}} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "restricted" {
				if err := f.db.Create(&model.UserPathPermission{UserID: viewer.ID, Method: "GET", Path: "/api/files"}).Error; err != nil {
					t.Fatal(err)
				}
			}
			command := exec.Command("swift", "test", "--package-path", packagePath, "--filter", "realBackendWorkspace")
			command.Env = append(os.Environ(), "CLOUDNEST_SWIFT_WORKSPACE_TEST=1", "CLOUDNEST_SWIFT_ORIGIN="+server.URL,
				"CLOUDNEST_SWIFT_CERTIFICATE="+certificate, "CLOUDNEST_SWIFT_WORKSPACE_ROLE="+tc.name,
				"CLOUDNEST_SWIFT_USERNAME="+tc.user.Username, "CLOUDNEST_SWIFT_PASSWORD=disposable-fixture-password",
				"CLOUDNEST_SWIFT_WORKSPACE_VIEWER="+viewer.Username, "CLOUDNEST_SWIFT_WORKSPACE_THIRD="+third.Username,
				"CLOUDNEST_SWIFT_WORKSPACE_LEGACY="+strconv.FormatInt(legacy.ID, 10),
				"CLOUDNEST_SWIFT_WORKSPACE_SOURCE="+strconv.FormatInt(personal.ID, 10))
			output, err := command.CombinedOutput()
			t.Log(string(output))
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
