//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"cloudenest_go/internal/frpconfig"
	"cloudenest_go/internal/service/contract"
	storageMysql "cloudenest_go/internal/storage/mysql"
	"cloudenest_go/model"
	"gorm.io/gorm"
)

func frpTestKeys(t *testing.T, active string, keys map[string][]byte) (string, *frpconfig.Keyring) {
	t.Helper()
	var entries []map[string]string
	for id, key := range keys {
		entries = append(entries, map[string]string{"id": id, "key": base64.StdEncoding.EncodeToString(key)})
	}
	raw, err := json.Marshal(map[string]any{"version": 1, "active": active, "keys": entries})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "keyring.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	k, err := frpconfig.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, k
}

func TestFRPEncryptionExplicitCLIAndRotation(t *testing.T) {
	f := newFixture(t)
	id := agentDevice(t, f)
	t.Cleanup(func() {
		if err := f.db.Where("device_id = ?", id).Delete(&model.FrpConfig{}).Error; err != nil {
			t.Error(err)
		}
	})
	secret := "legacy-frps-secret-retained-only-in-test"
	legacy := model.FrpConfig{DeviceID: &id, ConfigText: "serverAddr: localhost\nauth:\n  token: " + secret + "\n"}
	if err := f.db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	k1 := bytes.Repeat([]byte{1}, 32)
	k2 := bytes.Repeat([]byte{2}, 32)
	path1, keys1 := frpTestKeys(t, "one", map[string][]byte{"one": k1})
	dir := t.TempDir()
	bin := filepath.Join(dir, "server")
	if out, err := exec.Command("go", "build", "-o", bin, "../cmd/server").CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v %s", err, out)
	}
	cli := func(command, keyPath string, wantSuccess bool) storageMysql.FRPEncryptionReport {
		t.Helper()
		cmd := exec.Command(bin, command)
		cmd.Dir = dir
		cmd.Env = []string{"MYSQL_DSN=" + f.cfg.MySQLDSN, "CLOUDNEST_FRP_KEY_FILE=" + keyPath}
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		if (err == nil) != wantSuccess {
			t.Fatalf("CLI %s unexpected success=%t", command, err == nil)
		}
		if strings.Contains(stdout.String()+stderr.String(), secret) {
			t.Fatal("CLI exposed credential")
		}
		var report storageMysql.FRPEncryptionReport
		if wantSuccess && json.Unmarshal(stdout.Bytes(), &report) != nil {
			t.Fatal("invalid CLI report")
		}
		return report
	}
	read := func() model.FrpConfig {
		t.Helper()
		var row model.FrpConfig
		if err := f.db.First(&row, legacy.ID).Error; err != nil {
			t.Fatal(err)
		}
		return row
	}
	if r := cli("audit-frp-configs", path1, true); r.Legacy != 1 || r.Rewritten != 0 || read().ConfigText != legacy.ConfigText {
		t.Fatal("audit mutated legacy row", r)
	}
	if r := cli("encrypt-frp-configs", path1, true); r.Rewritten != 1 {
		t.Fatal(r)
	}
	encrypted := read()
	if strings.Contains(encrypted.ConfigText, secret) {
		t.Fatal("plaintext remained")
	}
	if plain, err := keys1.Open(id, legacy.ID, encrypted.ConfigText); err != nil || plain != legacy.ConfigText {
		t.Fatal("migration lost original config", err)
	}
	if r := cli("encrypt-frp-configs", path1, true); r.Current != 1 || r.Rewritten != 0 || read().ConfigText != encrypted.ConfigText {
		t.Fatal("migration not idempotent", r)
	}
	missingPath, _ := frpTestKeys(t, "two", map[string][]byte{"two": k2})
	cli("encrypt-frp-configs", missingPath, false)
	if read().ConfigText != encrypted.ConfigText {
		t.Fatal("missing key changed row")
	}
	path2, keys2 := frpTestKeys(t, "two", map[string][]byte{"one": k1, "two": k2})
	if r := cli("encrypt-frp-configs", path2, true); r.OldKey != 1 || r.Rewritten != 1 {
		t.Fatal("rotation not applied", r)
	}
	rotated := read()
	if plain, err := keys2.Open(id, legacy.ID, rotated.ConfigText); err != nil || plain != legacy.ConfigText || rotated.ConfigText == encrypted.ConfigText {
		t.Fatal("rotation lost content", err)
	}
	if _, err := keys1.Open(id, legacy.ID, rotated.ConfigText); !errors.Is(err, frpconfig.ErrUnavailable) {
		t.Fatal("old keyring decrypted new key")
	}
	if r := cli("audit-frp-configs", missingPath, true); r.Current != 1 {
		t.Fatal("retiring old key failed", r)
	}
}

func TestFRPEncryptionIdentityAuditAndFailureIsolation(t *testing.T) {
	f := newFixture(t)
	id := agentDevice(t, f)
	t.Cleanup(func() {
		if err := f.db.Where("device_id = ?", id).Delete(&model.FrpConfig{}).Error; err != nil {
			t.Error(err)
		}
	})
	ctx := context.Background()
	_, keys := frpTestKeys(t, "one", map[string][]byte{"one": bytes.Repeat([]byte{1}, 32)})
	r := storageMysql.NewFrpRepo(f.db, keys)
	count := func() int64 {
		t.Helper()
		var n int64
		if err := f.db.Model(&model.FrpConfig{}).Where("device_id = ?", id).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	input := model.FrpConfig{DeviceID: &id, ConfigText: "sensitive test config"}
	if _, err := storageMysql.NewFrpRepo(f.db).CreateConfig(ctx, f.aliceID, false, input); !errors.Is(err, frpconfig.ErrUnavailable) || count() != 0 {
		t.Fatal("missing key created version")
	}
	created, err := r.CreateConfig(ctx, f.aliceID, false, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.RollbackConfig(ctx, f.bobID, false, created.ID); !errors.Is(err, contract.ErrForbidden) {
		t.Fatal("cross-user rollback accepted")
	}
	copyRow := model.FrpConfig{DeviceID: &id, ConfigText: created.ConfigText}
	if err := f.db.Create(&copyRow).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.RollbackConfig(ctx, f.aliceID, false, copyRow.ID); !errors.Is(err, frpconfig.ErrInvalid) || count() != 2 {
		t.Fatal("ciphertext transplant accepted")
	}
	legacy := model.FrpConfig{DeviceID: &id, ConfigText: input.ConfigText}
	if err := f.db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.RollbackConfig(ctx, f.aliceID, false, legacy.ID); !errors.Is(err, frpconfig.ErrLegacy) {
		t.Fatal("plaintext rollback fallback")
	}
	const hook = "test:frp_audit_failure"
	if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if log, ok := tx.Statement.Dest.(*model.OperationLog); ok && strings.HasPrefix(log.Action, "frp_config_") {
			tx.AddError(errors.New("injected audit failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Callback().Create().Remove(hook) })
	if _, err := r.CreateConfig(ctx, f.aliceID, false, input); err == nil || count() != 3 {
		t.Fatal("save survived audit failure")
	}
	if _, _, err := r.RollbackConfig(ctx, f.aliceID, false, created.ID); err == nil || count() != 3 {
		t.Fatal("rollback survived audit failure")
	}
	// The earlier corrupted row stops migration, preserving all later rows.
	if report, err := r.ReencryptConfigs(ctx, false); err == nil || report.Current != 1 || report.Rewritten != 0 {
		t.Fatal("tamper did not stop migration", report, err)
	}
	if err := f.db.Model(&copyRow).Update("config_text", input.ConfigText).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReencryptConfigs(ctx, false); err == nil {
		t.Fatal("migration ignored audit failure")
	}
	var retained model.FrpConfig
	if err := f.db.First(&retained, copyRow.ID).Error; err != nil {
		t.Fatal(err)
	}
	if retained.ConfigText != input.ConfigText {
		t.Fatal("migration lost plaintext before audit commit")
	}
	if err := f.db.Callback().Create().Remove(hook); err != nil {
		t.Fatal(err)
	}
	if report, err := r.ReencryptConfigs(ctx, false); err != nil || report.Rewritten != 2 {
		t.Fatal("migration could not resume", report, err)
	}
}
