//go:build integration

package integration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"cloudenest_go/internal/agentauth"
	"cloudenest_go/internal/agentcommand"
	"cloudenest_go/internal/agentstatus"
	"cloudenest_go/internal/service/contract"
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

func TestDiagnosticMappingIDsComeFromSavedVersion(t *testing.T) {
	f := newFixture(t)
	device := agentDevice(t, f)
	key := commandIdentity(t, f, device)
	ctx := t.Context()
	_, keys := frpTestKeys(t, "one", map[string][]byte{"one": bytes.Repeat([]byte{1}, 32)})
	repo := storageMysql.NewFrpRepo(f.db, keys)
	actor := contract.FrpActor{UserID: f.aliceID}
	mapping, err := repo.CreateMapping(ctx, contract.FrpCreateMappingInput{FrpActor: actor, DeviceID: device, LocalPort: 8080, Status: 1})
	if err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf("serverAddr: 127.0.0.1\nserverPort: 7000\nproxies:\n  - name: device_%d_map_%d\n    type: tcp\n  - name: device_%d_map_99\n    type: https\n", device, mapping.ID, device+1)
	config, err := repo.CreateConfig(ctx, f.aliceID, false, model.FrpConfig{DeviceID: &device, ConfigText: raw})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Where("id=?", config.ID).Delete(&model.FrpConfig{}) })
	m := agentcommand.New(f.db, keys)
	queued, err := m.Enqueue(ctx, f.aliceID, false, device, agentcommand.EnqueueInput{RequestID: strings.Repeat("a", 32), Action: "diagnose"}, "")
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := m.Claim(ctx, key, strings.Repeat("c", 32))
	if err != nil || delivery == nil || delivery.Command.ID != queued.ID {
		t.Fatal("missing diagnose lease", err)
	}
	report := agentstatus.Report{State: "degraded", ConfigID: config.ID, ObservedAt: time.Now(), Expected: 2, Registered: 1, Failed: 1, ProxyStates: []byte{6}}
	if err := m.FinishWithReport(ctx, key, queued.ID, delivery.LeaseID, false, "diagnostic_failed", &report); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.DeleteMapping(ctx, actor, mapping.ID); err != nil {
		t.Fatal(err)
	}
	newMapping, err := repo.CreateMapping(ctx, contract.FrpCreateMappingInput{FrpActor: actor, DeviceID: device, LocalPort: 8080, RemotePort: &mapping.RemotePort, Status: 1})
	if err != nil || newMapping.ID == mapping.ID {
		t.Fatal("mapping replacement fixture failed", err)
	}
	t.Cleanup(func() { f.db.Delete(&newMapping) })
	rows, err := m.List(ctx, f.aliceID, false, device, 20)
	if err != nil || len(rows) != 1 {
		t.Fatal("diagnostic list failed", err)
	}
	proxies := rows[0].DiagnosticProxies
	if len(proxies) != 2 || proxies[0].MappingID != mapping.ID || proxies[0].State != "failed" || proxies[1].MappingID != 0 || proxies[1].State != "registered" {
		t.Fatal("status did not match historical configuration", proxies)
	}
	runtime := report
	runtime.UpstreamStates = []byte{9}
	if _, err := agentauth.New(f.db).BeatWithReport(ctx, key, "cloudnest-agent-0.3.0", &runtime); err != nil {
		t.Fatal(err)
	}
	heartbeat, err := repo.GetAgentHeartbeat(ctx, f.aliceID, false, device)
	if err != nil || heartbeat == nil || len(heartbeat.RuntimeProxies) != 2 || heartbeat.RuntimeProxies[0] != (agentstatus.ProxyView{MappingID: mapping.ID, State: "failed", Upstream: "reachable"}) || heartbeat.RuntimeProxies[1] != (agentstatus.ProxyView{State: "registered", Upstream: "unreachable"}) {
		t.Fatal("automatic sample did not match historical configuration", heartbeat, err)
	}
	if _, err := repo.GetAgentHeartbeat(ctx, f.bobID, false, device); !errors.Is(err, contract.ErrForbidden) {
		t.Fatal("another owner read runtime mapping IDs", err)
	}
	heartbeat, err = storageMysql.NewFrpRepo(f.db).GetAgentHeartbeat(ctx, f.aliceID, false, device)
	if err != nil || heartbeat == nil || heartbeat.Runtime == nil || len(heartbeat.RuntimeProxies) != 0 {
		t.Fatal("automatic sample lost when key is missing", err)
	}
	rows, err = agentcommand.New(f.db, nil).List(ctx, f.aliceID, false, device, 20)
	if err != nil || len(rows) != 1 || rows[0].Diagnostic == nil || len(rows[0].DiagnosticProxies) != 0 {
		t.Fatal("old report unavailable when key is missing", err)
	}
}
