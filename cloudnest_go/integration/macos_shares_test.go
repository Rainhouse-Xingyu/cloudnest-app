//go:build integration && darwin

package integration

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"cloudenest_go/model"
)

func TestMacOSClientShares(t *testing.T) {
	if os.Getenv("CLOUDNEST_TEST_SWIFT") != "1" {
		t.Skip("set CLOUDNEST_TEST_SWIFT=1 for the real native client")
	}
	f := newFixture(t)
	var owner, peer model.User
	if err := f.db.First(&owner, f.aliceID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.First(&peer, f.bobID).Error; err != nil {
		t.Fatal(err)
	}
	file := f.upload(t, f.alice, "Native share source.txt", []byte("Native limited public share download.\n"))
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
	for _, role := range []string{"owner", "restricted"} {
		t.Run(role, func(t *testing.T) {
			user := owner
			if role == "restricted" {
				user = peer
				if err := f.db.Create(&model.UserPathPermission{UserID: peer.ID, Method: "GET", Path: "/api/files"}).Error; err != nil {
					t.Fatal(err)
				}
			}
			command := exec.Command("swift", "test", "--package-path", packagePath, "--filter", "realBackendShares")
			command.Env = append(os.Environ(), "CLOUDNEST_SWIFT_SHARES_TEST=1", "CLOUDNEST_SWIFT_ORIGIN="+server.URL,
				"CLOUDNEST_SWIFT_CERTIFICATE="+certificate, "CLOUDNEST_SWIFT_SHARES_ROLE="+role,
				"CLOUDNEST_SWIFT_USERNAME="+user.Username, "CLOUDNEST_SWIFT_PASSWORD=disposable-fixture-password",
				"CLOUDNEST_SWIFT_SHARES_PEER="+peer.Username, "CLOUDNEST_SWIFT_SHARES_FILE="+strconv.FormatInt(file.ID, 10))
			output, err := command.CombinedOutput()
			t.Log(string(output))
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
