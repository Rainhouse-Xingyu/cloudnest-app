//go:build integration && frpupgrade

package integration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"cloudenest_go/internal/agentauth"
	"cloudenest_go/internal/agentcommand"
	"cloudenest_go/internal/service/contract"
	repo "cloudenest_go/internal/storage/mysql"
	"cloudenest_go/internal/store"
	"cloudenest_go/model"
	"golang.org/x/crypto/bcrypt"
)

func TestFRPUpgradeFromPreLedgerRelease(t *testing.T) {
	cfg := disposableConfig(t)
	before := os.Getenv("CLOUDNEST_TEST_FRP_BEFORE")
	after := os.Getenv("CLOUDNEST_TEST_FRP_AFTER")
	if !filepath.IsAbs(before) || !filepath.IsAbs(after) {
		t.Fatal("run scripts/test-frp-upgrade.sh")
	}
	cfg.FRPKeyFile, cfg.FRPKeys = frpTestKeys(t, "test", map[string][]byte{"test": bytes.Repeat([]byte{7}, 32)})
	work := t.TempDir()
	invoke := func(bin string, args ...string) []byte {
		t.Helper()
		c := exec.CommandContext(t.Context(), bin, args...)
		c.Dir = work
		c.Env = []string{"PATH=" + os.Getenv("PATH"), "MYSQL_DSN=" + cfg.MySQLDSN, "JWT_SECRET=" + cfg.JWTSecret, "CLOUDNEST_FRP_KEY_FILE=" + cfg.FRPKeyFile}
		output, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("maintenance %s failed: %v\n%s", args[0], err, output)
		}
		return output
	}
	invoke(before, "migrate")
	db, err := store.NewMySQL(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if db.Migrator().HasTable(&model.FrpReservation{}) {
		t.Fatal("expected a database created before the reservation ledger")
	}
	ctx := context.Background()
	password := "disposable-fixture-password"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	owner := model.User{Username: "upgrade-fixture", Password: string(hash), Status: 1}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	device := model.Device{UserID: owner.ID, DeviceName: "upgrade fixture", Status: 1}
	if err := db.Create(&device).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO frp_mapping (device_id,local_port,remote_port,domain,status) VALUES (?,8080,32442,'',1)", device.ID).Error; err != nil {
		t.Fatal(err)
	}
	auth := agentauth.New(db)
	issue, err := auth.Issue(ctx, owner.ID, device.ID, password)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := auth.Exchange(ctx, issue.Enrollment)
	if err != nil {
		t.Fatal(err)
	}
	r := repo.NewFrpRepo(db, cfg.FRPKeys)
	plain := "serverAddr: 127.0.0.1\nserverPort: 7000\nproxies:\n  - name: upgrade\n    type: tcp\n    localIP: 127.0.0.1\n    localPort: 8080\n    remotePort: 32442\n"
	saved, err := r.CreateConfig(ctx, owner.ID, false, model.FrpConfig{DeviceID: &device.ID, ConfigText: plain})
	if err != nil {
		t.Fatal(err)
	}
	var binding model.AgentBinding
	if err := db.First(&binding, "device_id=?", device.ID).Error; err != nil {
		t.Fatal(err)
	}
	// Seed a lease issued by the old queue, which had no per-command resource claim.
	for i, action := range []string{"deploy_config", "start", "reload", "stop", "diagnose", "forget_config"} {
		id := fmt.Sprintf("%032x", i+1)
		if err := db.Exec(`INSERT INTO agent_command (id,device_id,credential_id,request_id,action,requested_config_id,config_id,config_sha256,status,attempts,claim_id,lease_id,lease_expires,expires_at,created_at,result_code)
  VALUES (?,?,?,?,?,?,?,'','leased',1,?,?,DATE_ADD(UTC_TIMESTAMP(6), INTERVAL 5 MINUTE),DATE_ADD(UTC_TIMESTAMP(6), INTERVAL 1 HOUR),UTC_TIMESTAMP(6),'')`, id, device.ID, *binding.CredentialID, id, action, saved.ID, saved.ID, id, id).Error; err != nil {
			t.Fatal(err)
		}
	}
	invoke(after, "migrate")
	invoke(after, "migrate")
	if err := store.CheckAgentSchema(db); err != nil {
		t.Fatal(err)
	}
	var mapping model.FrpMapping
	if err := db.Where("device_id=?", device.ID).First(&mapping).Error; err != nil || mapping.ServerID != 0 || mapping.RemotePort != 32442 {
		t.Fatal("legacy mapping changed", err)
	}
	var history model.FrpConfig
	if err := db.First(&history, saved.ID).Error; err != nil || history.ConfigText != saved.ConfigText {
		t.Fatal("encrypted configuration changed", err)
	}
	if raw, err := cfg.FRPKeys.OpenConfig(device.ID, history.ID, history.ServerID, history.ServerRevisionID, history.ConfigText); err != nil || raw != plain {
		t.Fatal("previous configuration cannot be decrypted", err)
	}
	if _, err := auth.Beat(ctx, identity.Credential, "cloudnest-agent-0.3.0"); err != nil {
		t.Fatal("previous Agent credential no longer works", err)
	}
	var commands []model.AgentCommand
	if err := db.Where("device_id=?", device.ID).Order("id").Find(&commands).Error; err != nil || len(commands) != 6 {
		t.Fatal("command history changed", err)
	}
	for i, row := range commands {
		if i < 3 && (row.Status != "cancelled" || row.ResultCode != "upgrade_requeue" || row.CompletedAt == nil) {
			t.Fatal("old activation command remained executable", row.Action)
		}
		if i >= 3 && row.Status != "leased" {
			t.Fatal("safe command altered", row.Action)
		}
	}
	var holds []model.FrpReservation
	if err := db.Where("device_id=?", device.ID).Find(&holds).Error; err != nil || len(holds) != 1 || holds[0].Kind != "unknown" || holds[0].ConfigID != saved.ID {
		t.Fatal("old runtime history missing or duplicated", err)
	}
	hold := fmt.Sprint(holds[0].ID)
	output := invoke(after, "reconcile-frp-reservation", hold)
	if !bytes.Contains(output, []byte(`"eligibility":"historical"`)) {
		t.Fatal("unexpected dry-run result")
	}
	invoke(after, "reconcile-frp-reservation", hold, "--confirm-decommissioned", "TEST-UPGRADE-42")
	invoke(after, "migrate")
	if _, err := r.CreateMapping(ctx, contract.FrpCreateMappingInput{FrpActor: contract.FrpActor{UserID: owner.ID}, DeviceID: device.ID, LocalPort: 8081, Status: 1}); err != nil {
		t.Fatal("reconciled claim blocked fresh allocation", err)
	}
	m := agentcommand.New(db, cfg.FRPKeys)
	for _, row := range commands[:3] {
		if err := m.Finish(ctx, identity.Credential, row.ID, row.LeaseID, true, "ok"); !errors.Is(err, agentcommand.ErrConflict) {
			t.Fatal("obsolete lease could still acknowledge execution", row.Action, err)
		}
	}
	for _, row := range commands[3:] {
		if err := m.Finish(ctx, identity.Credential, row.ID, row.LeaseID, true, "ok"); err != nil {
			t.Fatal("preserved command could not complete", row.Action, err)
		}
	}
	fresh, err := m.Enqueue(ctx, owner.ID, false, device.ID, agentcommand.EnqueueInput{RequestID: fmt.Sprintf("%032x", 99), Action: "deploy_config", ConfigID: saved.ID}, "")
	if err != nil {
		t.Fatal("fresh claimed deployment failed", err)
	}
	var claim model.FrpReservation
	if err := db.Where("reference=?", fresh.ID).First(&claim).Error; err != nil || claim.Kind != "tcp" || claim.Resource != "32442" || claim.ReconciledAt != nil {
		t.Fatal("fresh deployment did not reserve its resource", err)
	}
}
