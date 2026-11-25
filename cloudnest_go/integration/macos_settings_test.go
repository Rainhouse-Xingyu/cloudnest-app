//go:build integration && darwin

package integration

import (
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"cloudenest_go/model"
)

func TestMacOSClientSettings(t *testing.T) {
	if os.Getenv("CLOUDNEST_TEST_SWIFT") != "1" {
		t.Skip("set CLOUDNEST_TEST_SWIFT=1 for the real native client")
	}
	f := newFixture(t)
	admin := model.Role{RoleName: "admin"}
	if err := f.db.Where("role_name=?", "admin").FirstOrCreate(&admin).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&model.UserRole{UserID: f.aliceID, RoleID: admin.ID}).Error; err != nil {
		t.Fatal(err)
	}
	keys := []string{"share_default_expire_hours", "peak_window_enabled", "peak_window_start", "peak_window_end"}
	for _, uid := range []int64{f.aliceID, f.bobID} {
		for _, key := range keys[1:4] {
			keys = append(keys, fmt.Sprintf("%s_user_%d", key, uid))
		}
	}
	preserveSettings(t, f, keys)
	if err := f.db.Where("setting_key IN ?", keys).Delete(&model.SystemSetting{}).Error; err != nil {
		t.Fatal(err)
	}
	rateSetting(t, f, "peak_window_enabled", "0")
	rateSetting(t, f, "peak_window_start", "08:00")
	rateSetting(t, f, "peak_window_end", "20:00")
	var owner, peer model.User
	if err := f.db.First(&owner, f.aliceID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.First(&peer, f.bobID).Error; err != nil {
		t.Fatal(err)
	}
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
	for _, role := range []string{"admin", "ordinary", "restricted"} {
		t.Run(role, func(t *testing.T) {
			user := owner
			if role != "admin" {
				user = peer
			}
			if role == "restricted" {
				if err := f.db.Create(&model.UserPathPermission{UserID: peer.ID, Method: "GET", Path: "/api/files"}).Error; err != nil {
					t.Fatal(err)
				}
			}
			command := exec.Command("swift", "test", "--package-path", packagePath, "--filter", "realBackendSettings")
			command.Env = append(os.Environ(), "CLOUDNEST_SWIFT_SETTINGS_TEST=1", "CLOUDNEST_SWIFT_ORIGIN="+server.URL, "CLOUDNEST_SWIFT_CERTIFICATE="+certificate, "CLOUDNEST_SWIFT_SETTINGS_ROLE="+role, "CLOUDNEST_SWIFT_USERNAME="+user.Username, "CLOUDNEST_SWIFT_PASSWORD=disposable-fixture-password")
			output, err := command.CombinedOutput()
			t.Log(string(output))
			if err != nil {
				t.Fatal(err)
			}
		})
	}
	var row model.SystemSetting
	if err := f.db.First(&row, "setting_key=?", "share_default_expire_hours").Error; err != nil || row.SettingValue != "72" {
		t.Fatal("native settings final state", row.SettingValue, err)
	}
	var count int64
	if err := f.db.Model(&model.OperationLog{}).Where("user_id=? AND action='settings_update'", f.aliceID).Count(&count).Error; err != nil || count != 2 {
		t.Fatal("native settings audit", count, err)
	}
}
