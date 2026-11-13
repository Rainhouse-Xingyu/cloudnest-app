//go:build integration

package integration

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"cloudenest_go/internal/agentcommand"
	"cloudenest_go/internal/agentstatus"
	storageMysql "cloudenest_go/internal/storage/mysql"
	"cloudenest_go/internal/store"
	"cloudenest_go/model"
)

func TestDiagnosticReceiptImmutableValidatedAndMigrated(t *testing.T) {
	f := newFixture(t)
	device := agentDevice(t, f)
	key := commandIdentity(t, f, device)
	ctx := context.Background()
	manager := agentcommand.New(f.db, nil)
	queued, err := manager.Enqueue(ctx, f.aliceID, false, device, agentcommand.EnqueueInput{RequestID: strings.Repeat("d", 32), Action: "diagnose"}, "")
	if err != nil {
		t.Fatal(err)
	}
	// Exercise upgrade of an existing queue row, not just initial table creation.
	if err := f.db.Exec("ALTER TABLE agent_command DROP COLUMN diagnostic_data").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Migrate(f.db); err != nil {
			t.Error(err)
		}
	})
	if err := store.CheckAgentSchema(f.db); err == nil {
		t.Fatal("missing diagnostic column accepted")
	}
	if err := store.Migrate(f.db); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckAgentSchema(f.db); err != nil {
		t.Fatal(err)
	}
	leased, err := manager.Claim(ctx, key, strings.Repeat("a", 32))
	if err != nil || leased == nil || leased.Command.ID != queued.ID {
		t.Fatal("claim failed", err)
	}
	report := agentstatus.Report{State: "stopped", ObservedAt: time.Now().UTC()}
	finish := func(success bool, code string, r *agentstatus.Report) error {
		return manager.FinishWithReport(ctx, key, queued.ID, leased.LeaseID, success, code, r)
	}
	if err := finish(true, "ok", &report); !errors.Is(err, agentcommand.ErrInput) {
		t.Fatal("success with stopped status", err)
	}
	other := agentDevice(t, f)
	f.cfg.FRPKeyFile, f.cfg.FRPKeys = frpTestKeys(t, "one", map[string][]byte{"one": bytes.Repeat([]byte{1}, 32)})
	otherConfig, err := storageMysql.NewFrpRepo(f.db, f.cfg.FRPKeys).CreateConfig(ctx, f.aliceID, false, model.FrpConfig{DeviceID: &other, ConfigText: "synthetic other-device config"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Where("id=?", otherConfig.ID).Delete(&model.FrpConfig{}) })
	foreign := report
	foreign.ConfigID = otherConfig.ID
	if err := finish(false, "diagnostic_failed", &foreign); !errors.Is(err, agentcommand.ErrInput) {
		t.Fatal("unbound configuration accepted", err)
	}
	// A 512-proxy snapshot must fit the existing column and round-trip.
	config, err := storageMysql.NewFrpRepo(f.db, f.cfg.FRPKeys).CreateConfig(ctx, f.aliceID, false, model.FrpConfig{DeviceID: &device, ConfigText: "synthetic diagnostic fixture config"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Where("id=?", config.ID).Delete(&model.FrpConfig{}) })
	report = agentstatus.Report{
		State: "degraded", ConfigID: config.ID, ObservedAt: report.ObservedAt,
		Expected: 512, Failed: 512, ProxyStates: bytes.Repeat([]byte{0xaa}, 128),
	}
	if err := finish(false, "diagnostic_failed", &report); err != nil {
		t.Fatal(err)
	}
	if err := finish(false, "diagnostic_failed", &report); err != nil {
		t.Fatal("same observation retry", err)
	}
	changed := report
	changed.ProxyStates = append([]byte(nil), report.ProxyStates...)
	changed.ProxyStates[0] = 0xa9
	changed.Failed--
	changed.Registered++
	if err := finish(false, "diagnostic_failed", &changed); !errors.Is(err, agentcommand.ErrConflict) {
		t.Fatal("receipt proxy state overwritten", err)
	}
	changed = report
	changed.ObservedAt = changed.ObservedAt.Add(time.Second)
	if err := finish(false, "diagnostic_failed", &changed); !errors.Is(err, agentcommand.ErrConflict) {
		t.Fatal("receipt observation overwritten", err)
	}
	rows, err := manager.List(ctx, f.aliceID, false, device, 20)
	if err != nil || len(rows) != 1 || rows[0].Diagnostic == nil || !rows[0].Diagnostic.ObservedAt.Equal(report.ObservedAt) || !bytes.Equal(rows[0].Diagnostic.ProxyStates, report.ProxyStates) {
		t.Fatal("persisted observation missing", err)
	}
	if _, err := manager.List(ctx, f.bobID, false, device, 20); err == nil {
		t.Fatal("another owner read report")
	}
	var logs int64
	if err := f.db.Model(&model.OperationLog{}).Where("action=? AND detail=?", "agent.command.failed", "command_id="+queued.ID).Count(&logs).Error; err != nil || logs != 1 {
		t.Fatal("duplicate receipt audit", logs, err)
	}
	// Old agents may omit the extension; no invented diagnostic is returned.
	next, err := manager.Enqueue(ctx, f.aliceID, false, device, agentcommand.EnqueueInput{RequestID: strings.Repeat("e", 32), Action: "diagnose"}, "")
	if err != nil {
		t.Fatal(err)
	}
	del, err := manager.Claim(ctx, key, strings.Repeat("b", 32))
	if err != nil || del == nil {
		t.Fatal(err)
	}
	if err := manager.Finish(ctx, key, next.ID, del.LeaseID, false, "diagnostic_failed"); err != nil {
		t.Fatal(err)
	}
	rows, err = manager.List(ctx, f.aliceID, false, device, 20)
	if err != nil || len(rows) != 2 || rows[0].Diagnostic != nil {
		t.Fatal("legacy receipt invented status", err)
	}
}
