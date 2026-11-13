//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"cloudenest_go/internal/agentauth"
	"cloudenest_go/internal/agentcommand"
	"cloudenest_go/internal/service"
	"cloudenest_go/internal/service/contract"
	repo "cloudenest_go/internal/storage/mysql"
	"cloudenest_go/internal/store"
	"cloudenest_go/model"
	"gorm.io/gorm"
)

func finishClaim(t *testing.T, m *agentcommand.Manager, key string, n int) agentcommand.Delivery {
	t.Helper()
	d, err := m.Claim(context.Background(), key, fmt.Sprintf("%032x", n))
	if err != nil || d == nil {
		t.Fatal("missing claim", err)
	}
	if err := m.Finish(context.Background(), key, d.Command.ID, d.LeaseID, true, "ok"); err != nil {
		t.Fatal(err)
	}
	return *d
}
func TestFRPReservationRetainsDeletedMappingUntilAtomicForget(t *testing.T) {
	f, r, device, server, _ := serverFixture(t)
	ctx := context.Background()
	actor := contract.FrpActor{UserID: f.aliceID}
	key := commandIdentity(t, f, device)
	if _, err := agentauth.New(f.db).Beat(ctx, key, "cloudnest-agent-0.3.0"); err != nil {
		t.Fatal(err)
	}
	mappings, err := r.ListMappingsByDeviceID(ctx, device)
	if err != nil || len(mappings) != 1 {
		t.Fatal(err)
	}
	mapping := mappings[0]
	port := mapping.RemotePort
	cfg, err := service.NewFrpService(r, f.rdb).SaveConfig(ctx, f.aliceID, false, service.SaveConfigInput{DeviceID: device, ServerID: server.ID, ServerRevisionID: server.RevisionID})
	if err != nil {
		t.Fatal(err)
	}
	m := agentcommand.New(f.db, f.cfg.FRPKeys)
	deploy := func(n int) error {
		_, err := m.Enqueue(ctx, f.aliceID, false, device, agentcommand.EnqueueInput{RequestID: fmt.Sprintf("%032x", n), Action: "deploy_config", ConfigID: cfg.ID}, "")
		return err
	}
	if err := deploy(1); err != nil {
		t.Fatal(err)
	}
	finishClaim(t, m, key, 2)
	if _, err := r.DeleteMapping(ctx, actor, mapping.ID); err != nil {
		t.Fatal(err)
	}
	other := mappingDevice(t, f, f.bobID)
	claimPort := func() error {
		_, err := r.CreateMapping(ctx, contract.FrpCreateMappingInput{FrpActor: contract.FrpActor{UserID: f.bobID}, DeviceID: other, LocalPort: 8081, RemotePort: &port, Status: 1})
		return err
	}
	if err := claimPort(); !errors.Is(err, contract.ErrFrpMappingConflict) {
		t.Fatal("deleted mapping freed live port", err)
	}
	if _, err := m.Enqueue(ctx, f.aliceID, false, device, agentcommand.EnqueueInput{RequestID: fmt.Sprintf("%032x", 3), Action: "stop"}, ""); err != nil {
		t.Fatal(err)
	}
	finishClaim(t, m, key, 4)
	if err := claimPort(); !errors.Is(err, contract.ErrFrpMappingConflict) {
		t.Fatal("stop freed restartable port", err)
	}
	if _, err := m.Enqueue(ctx, f.aliceID, false, device, agentcommand.EnqueueInput{RequestID: fmt.Sprintf("%032x", 5), Action: "forget_config"}, ""); err != nil {
		t.Fatal(err)
	}
	forgotten, err := m.Claim(ctx, key, fmt.Sprintf("%032x", 6))
	if err != nil || forgotten == nil {
		t.Fatal(err)
	}
	hook := "test:reservation_forget_audit"
	if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if log, ok := tx.Statement.Dest.(*model.OperationLog); ok && log.Action == "agent.command.succeeded" {
			tx.AddError(errors.New("injected receipt audit failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	err = m.Finish(ctx, key, forgotten.Command.ID, forgotten.LeaseID, true, "ok")
	f.db.Callback().Create().Remove(hook)
	if err == nil {
		t.Fatal("audit failure accepted")
	}
	if err := claimPort(); !errors.Is(err, contract.ErrFrpMappingConflict) {
		t.Fatal("audit failure released hold", err)
	}
	if err := m.Finish(ctx, key, forgotten.Command.ID, forgotten.LeaseID, true, "ok"); err != nil {
		t.Fatal(err)
	}
	// A duplicate old successful receipt cannot erase claims acquired later.
	if err := deploy(7); err != nil {
		t.Fatal(err)
	}
	if err := m.Finish(ctx, key, forgotten.Command.ID, forgotten.LeaseID, true, "ok"); err != nil {
		t.Fatal(err)
	}
	if err := claimPort(); !errors.Is(err, contract.ErrFrpMappingConflict) {
		t.Fatal("old receipt deleted new claim", err)
	}
	finishClaim(t, m, key, 8)
	if _, err := m.Enqueue(ctx, f.aliceID, false, device, agentcommand.EnqueueInput{RequestID: fmt.Sprintf("%032x", 9), Action: "forget_config"}, ""); err != nil {
		t.Fatal(err)
	}
	finishClaim(t, m, key, 10)
	if err := claimPort(); err != nil {
		t.Fatal("confirmed forget did not free unmapped port", err)
	}
	if err := deploy(11); !errors.Is(err, contract.ErrFrpMappingConflict) {
		t.Fatal("old config stole reallocated port", err)
	}
}

func TestFRPReservationNewCredentialCannotClearOldGeneration(t *testing.T) {
	f, r, device, server, _ := serverFixture(t)
	ctx := context.Background()
	oldKey := commandIdentity(t, f, device)
	cfg, err := service.NewFrpService(r, f.rdb).SaveConfig(ctx, f.aliceID, false, service.SaveConfigInput{DeviceID: device, ServerID: server.ID, ServerRevisionID: server.RevisionID})
	if err != nil {
		t.Fatal(err)
	}
	m := agentcommand.New(f.db, f.cfg.FRPKeys)
	row, err := m.Enqueue(ctx, f.aliceID, false, device, agentcommand.EnqueueInput{RequestID: strings.Repeat("a", 32), Action: "deploy_config", ConfigID: cfg.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	oldClaim, err := m.Claim(ctx, oldKey, strings.Repeat("b", 32))
	if err != nil || oldClaim == nil {
		t.Fatal(err)
	}
	newKey := commandIdentity(t, f, device)
	if _, err := agentauth.New(f.db).Beat(ctx, newKey, "cloudnest-agent-0.3.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Enqueue(ctx, f.aliceID, false, device, agentcommand.EnqueueInput{RequestID: strings.Repeat("c", 32), Action: "forget_config"}, ""); err != nil {
		t.Fatal(err)
	}
	finishClaim(t, m, newKey, 5)
	var count int64
	if err := f.db.Model(&model.FrpReservation{}).Where("reference=?", row.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("new enrollment erased old physical instance claim", count, err)
	}
	if err := m.Finish(ctx, oldKey, row.ID, oldClaim.LeaseID, true, "ok"); err == nil {
		t.Fatal("revoked generation could acknowledge")
	}
}

func TestFRPReservationConcurrentDeploymentConflict(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.cfg.FRPKeyFile, f.cfg.FRPKeys = frpTestKeys(t, "one", map[string][]byte{"one": []byte(strings.Repeat("x", 32))})
	r := repo.NewFrpRepo(f.db, f.cfg.FRPKeys)
	devices := []int64{agentDevice(t, f), agentDevice(t, f)}
	for _, device := range devices {
		commandIdentity(t, f, device)
	}
	mapping, err := r.CreateMapping(ctx, contract.FrpCreateMappingInput{FrpActor: contract.FrpActor{UserID: f.aliceID}, DeviceID: devices[0], LocalPort: 8080, Status: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.DeleteMapping(ctx, contract.FrpActor{UserID: f.aliceID}, mapping.ID); err != nil {
		t.Fatal(err)
	}
	configs := []model.FrpConfig{}
	for _, device := range devices {
		cfg, err := r.CreateConfig(ctx, f.aliceID, false, model.FrpConfig{DeviceID: &device, ConfigText: fmt.Sprintf("serverAddr: localhost\nserverPort: 7000\nproxies:\n - name: tcp\n   type: tcp\n   localPort: 8080\n   remotePort: %d\n", mapping.RemotePort)})
		if err != nil {
			t.Fatal(err)
		}
		configs = append(configs, cfg)
	}
	t.Cleanup(func() { f.db.Where("device_id IN ?", devices).Delete(&model.FrpConfig{}) })
	m := agentcommand.New(f.db, f.cfg.FRPKeys)
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := range devices {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := m.Enqueue(ctx, f.aliceID, false, devices[i], agentcommand.EnqueueInput{RequestID: fmt.Sprintf("%032x", i+1), Action: "deploy_config", ConfigID: configs[i].ID}, "")
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for err := range results {
		if err == nil {
			wins++
		} else if errors.Is(err, contract.ErrFrpMappingConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatal("concurrent runtime allocation", wins, conflicts)
	}
}

func TestFRPReservationExplicitMigrationKeepsOpaqueHistory(t *testing.T) {
	f := newFixture(t)
	device := agentDevice(t, f)
	cfg := model.FrpConfig{DeviceID: &device, ConfigText: "old unassociated plaintext preserved"}
	if err := f.db.Create(&cfg).Error; err != nil {
		t.Fatal(err)
	}
	var maxID int64
	if err := f.db.Model(&model.FrpReservation{}).Select("COALESCE(MAX(id),0)").Scan(&maxID).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		f.db.Where("id>? AND reference LIKE 'legacy-config:%'", maxID).Delete(&model.FrpReservation{})
		f.db.Delete(&cfg)
	})
	if err := f.db.Where("id=1").Delete(&model.FrpReservationSchema{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.CheckReservationSchema(f.db); err == nil {
		t.Fatal("missing history checkpoint accepted")
	}
	if err := store.Migrate(f.db); err != nil {
		t.Fatal(err)
	}
	var held model.FrpReservation
	if err := f.db.Where("reference=?", fmt.Sprintf("legacy-config:%d", cfg.ID)).Take(&held).Error; err != nil || held.Kind != "unknown" || held.CredentialID != "" || held.ServerID != 0 {
		t.Fatal("historical association guessed", err)
	}
	var unchanged model.FrpConfig
	if err := f.db.First(&unchanged, cfg.ID).Error; err != nil || unchanged.ConfigText != cfg.ConfigText {
		t.Fatal("migration modified history", err)
	}
	if err := store.Migrate(f.db); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := f.db.Model(&model.FrpReservation{}).Where("reference=?", held.Reference).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("migration duplicated opaque hold", count, err)
	}
	// Upgrade a v1 ledger without the reconciliation field. Existing claims
	// must remain active and keep their original IDs.
	if err := f.db.Exec("ALTER TABLE frp_reservation DROP COLUMN reconciled_at").Error; err != nil {
		t.Fatal(err)
	}
	if err := store.CheckReservationSchema(f.db); err == nil {
		t.Fatal("missing reconciliation column accepted")
	}
	if err := store.Migrate(f.db); err != nil {
		t.Fatal(err)
	}
	var upgraded model.FrpReservation
	if err := f.db.First(&upgraded, held.ID).Error; err != nil || upgraded.Reference != held.Reference || upgraded.ReconciledAt != nil {
		t.Fatal("ledger upgrade changed historical claim", err)
	}
}

func TestFRPReservationTLSOwnershipAndSchemaDrift(t *testing.T) {
	f, r, device, server, _ := serverFixture(t)
	ctx := context.Background()
	key := commandIdentity(t, f, device)
	cfg, err := service.NewFrpService(r, f.rdb).SaveConfig(ctx, f.aliceID, false, service.SaveConfigInput{DeviceID: device, ServerID: server.ID, ServerRevisionID: server.RevisionID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agentcommand.New(f.db, f.cfg.FRPKeys).Enqueue(ctx, f.aliceID, false, device, agentcommand.EnqueueInput{RequestID: strings.Repeat("a", 32), Action: "deploy_config", ConfigID: cfg.ID}, ""); err != nil {
		t.Fatal(err)
	}
	f.json(t, "GET", "/api/frp/reservations", f.alice, nil, 426)
	base, client := startAgentTLSServer(t, f)
	call := func(user, path string, status, count int) {
		t.Helper()
		req, err := http.NewRequest("GET", base+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+user)
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != status || res.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("reservation API status=%d want=%d", res.StatusCode, status)
		}
		if bytes.Contains(raw, []byte(key)) || bytes.Contains(raw, []byte("credential_id")) || bytes.Contains(raw, []byte("synthetic-server-token")) {
			t.Fatal("reservation list exposed secret")
		}
		if status == 200 {
			var body struct{ Items []model.FrpReservation }
			if err := json.Unmarshal(raw, &body); err != nil || len(body.Items) != count {
				t.Fatal("reservation scope", len(body.Items), count, err)
			}
		}
	}
	call(f.alice, "/api/frp/reservations", 200, 1)
	call(f.bob, "/api/frp/reservations", 200, 0)
	call(f.alice, "/api/frp/reservations?limit=101", 400, 0)
	call(f.alice, "/api/frp/reservations?after_id=-1", 400, 0)
	if err := f.db.Model(&model.Device{}).Where("id=?", device).Update("user_id", f.bobID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := r.DeleteDevice(ctx, contract.FrpActor{UserID: f.bobID}, device); err != nil {
		t.Fatal(err)
	}
	call(f.bob, fmt.Sprintf("/api/frp/reservations?device_id=%d", device), 200, 0)
	call(f.alice, fmt.Sprintf("/api/frp/reservations?device_id=%d", device), 200, 1)
	if err := f.db.Exec("ALTER TABLE frp_reservation DROP INDEX uq_frp_reservation_reference").Error; err != nil {
		t.Fatal(err)
	}
	if err := store.CheckReservationSchema(f.db); err == nil {
		t.Fatal("missing reservation identity accepted")
	}
	if err := f.db.Exec("ALTER TABLE frp_reservation ADD UNIQUE INDEX uq_frp_reservation_reference (`reference`,kind,resource)").Error; err != nil {
		t.Fatal(err)
	}
	if err := store.CheckReservationSchema(f.db); err != nil {
		t.Fatal(err)
	}
}

func TestFRPReservationEnqueueRollbackAndUncertainOutcomes(t *testing.T) {
	f, r, device, server, _ := serverFixture(t)
	ctx := context.Background()
	actor := contract.FrpActor{UserID: f.aliceID}
	key := commandIdentity(t, f, device)
	cfg, err := service.NewFrpService(r, f.rdb).SaveConfig(ctx, actor.UserID, false, service.SaveConfigInput{DeviceID: device, ServerID: server.ID, ServerRevisionID: server.RevisionID})
	if err != nil {
		t.Fatal(err)
	}
	m := agentcommand.New(f.db, f.cfg.FRPKeys)
	input := agentcommand.EnqueueInput{RequestID: strings.Repeat("d", 32), Action: "deploy_config", ConfigID: cfg.ID}
	count := func(want int64) {
		t.Helper()
		var actual int64
		if err := f.db.Model(&model.FrpReservation{}).Where("device_id=?", device).Count(&actual).Error; err != nil || actual != want {
			t.Fatal("runtime claim count", actual, want, err)
		}
	}
	const hook = "test:reservation_enqueue_audit"
	if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if log, ok := tx.Statement.Dest.(*model.OperationLog); ok && log.Action == "agent.command.queued" {
			tx.AddError(errors.New("injected queue audit failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	_, err = m.Enqueue(ctx, actor.UserID, false, device, input, "")
	f.db.Callback().Create().Remove(hook)
	if err == nil {
		t.Fatal("queue audit failure accepted")
	}
	count(0)
	var queued int64
	if err := f.db.Model(&model.AgentCommand{}).Where("device_id=?", device).Count(&queued).Error; err != nil || queued != 0 {
		t.Fatal("queue audit failure left a command", queued, err)
	}
	row, err := m.Enqueue(ctx, actor.UserID, false, device, input, "")
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := m.Enqueue(ctx, actor.UserID, false, device, input, "")
	if err != nil || duplicate.ID != row.ID {
		t.Fatal("deployment retry changed identity", err)
	}
	count(1)
	claim, err := m.Claim(ctx, key, strings.Repeat("a", 32))
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	if err := m.Finish(ctx, key, claim.Command.ID, claim.LeaseID, false, "config_invalid"); err != nil {
		t.Fatal(err)
	}
	count(1)
	input.RequestID = strings.Repeat("e", 32)
	row, err = m.Enqueue(ctx, actor.UserID, false, device, input, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&row).Update("expires_at", gorm.Expr("UTC_TIMESTAMP(6)-INTERVAL 1 SECOND")).Error; err != nil {
		t.Fatal(err)
	}
	if next, err := m.Claim(ctx, key, strings.Repeat("b", 32)); err != nil || next != nil {
		t.Fatal("expired deployment remained claimable", err)
	}
	count(2)
	if _, err := r.DeleteDevice(ctx, actor, device); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Delete(&cfg).Error; err != nil {
		t.Fatal(err)
	}
	count(2)
}

func TestFRPReservationDomainSurvivesMappingChange(t *testing.T) {
	f, r, device, server, input := serverFixture(t)
	ctx := context.Background()
	actor := contract.FrpActor{UserID: f.aliceID}
	input.ExpectedRevisionID = server.RevisionID
	input.Spec.HTTPPort, input.Spec.HTTPSPort = 8080, 8443
	server, err := r.WriteServer(ctx, actor, server.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	key := commandIdentity(t, f, device)
	domain := fmt.Sprintf("held-%d.example", device)
	mapping, err := r.CreateMapping(ctx, contract.FrpCreateMappingInput{FrpActor: actor, DeviceID: device, LocalPort: 8080, Domain: "HTTP://" + strings.ToUpper(domain) + ".", Status: 1})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := service.NewFrpService(r, f.rdb).SaveConfig(ctx, actor.UserID, false, service.SaveConfigInput{DeviceID: device, ServerID: server.ID, ServerRevisionID: server.RevisionID})
	if err != nil {
		t.Fatal(err)
	}
	m := agentcommand.New(f.db, f.cfg.FRPKeys)
	if _, err := m.Enqueue(ctx, actor.UserID, false, device, agentcommand.EnqueueInput{RequestID: strings.Repeat("a", 32), Action: "deploy_config", ConfigID: cfg.ID}, ""); err != nil {
		t.Fatal(err)
	}
	finishClaim(t, m, key, 2)
	changed := "new-" + domain
	if _, err := r.UpdateMapping(ctx, contract.FrpUpdateMappingInput{FrpActor: actor, MappingID: mapping.ID, Domain: &changed}); err != nil {
		t.Fatal(err)
	}
	other := mappingDevice(t, f, f.bobID)
	t.Cleanup(func() { f.db.Where("device_id=?", other).Delete(&model.FrpMapping{}) })
	request := contract.FrpCreateMappingInput{FrpActor: contract.FrpActor{UserID: f.bobID}, DeviceID: other, LocalPort: 8081, Domain: domain, Status: 1}
	if _, err := r.CreateMapping(ctx, request); !errors.Is(err, contract.ErrFrpMappingConflict) {
		t.Fatal("mapping edit freed deployed HTTP domain", err)
	}
	request.Domain = "https://" + domain
	if _, err := r.CreateMapping(ctx, request); err != nil {
		t.Fatal("HTTP claim incorrectly blocked distinct HTTPS resource", err)
	}
	if _, err := agentauth.New(f.db).Beat(ctx, key, "cloudnest-agent-0.3.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Enqueue(ctx, actor.UserID, false, device, agentcommand.EnqueueInput{RequestID: strings.Repeat("b", 32), Action: "forget_config"}, ""); err != nil {
		t.Fatal(err)
	}
	finishClaim(t, m, key, 3)
	request.Domain = domain
	if _, err := r.CreateMapping(ctx, request); err != nil {
		t.Fatal("confirmed forget did not free old HTTP domain", err)
	}
}

func TestFRPReservationHistoricalReconciliation(t *testing.T) {
	f := newFixture(t)
	device := mappingDevice(t, f, f.aliceID)
	ctx := context.Background()
	r := repo.NewFrpRepo(f.db)
	actor := contract.FrpActor{UserID: f.aliceID}
	cfg := model.FrpConfig{DeviceID: &device, ConfigText: "historical fixture content"}
	if err := f.db.Create(&cfg).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Delete(&cfg) })
	old := model.FrpReservation{OwnerID: f.aliceID, DeviceID: device, ConfigID: cfg.ID, Reference: fmt.Sprintf("legacy-config:%d", cfg.ID), Kind: "unknown"}
	if err := f.db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	inspect, err := r.ReconcileReservation(ctx, old.ID, "")
	if err != nil || inspect.Eligibility != "legacy_execution_enabled" {
		t.Fatal(inspect, err)
	}
	if _, err := r.ReconcileReservation(ctx, old.ID, "CHG-42"); !errors.Is(err, contract.ErrConflict) {
		t.Fatal("unfenced legacy claim released", err)
	}
	commandIdentity(t, f, device)
	var binding model.AgentBinding
	if err := f.db.First(&binding, "device_id=?", device).Error; err != nil {
		t.Fatal(err)
	}
	current := model.FrpReservation{OwnerID: f.aliceID, DeviceID: device, CredentialID: *binding.CredentialID, ConfigID: cfg.ID, Reference: fmt.Sprintf("fixture-current:%d", device), Kind: "tcp", Resource: "32401"}
	if err := f.db.Create(&current).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReconcileReservation(ctx, current.ID, "CHG-42"); !errors.Is(err, contract.ErrConflict) {
		t.Fatal("current claim released", err)
	}
	input := contract.FrpCreateMappingInput{FrpActor: actor, DeviceID: device, LocalPort: 8080, Status: 1}
	if _, err := r.CreateMapping(ctx, input); !errors.Is(err, contract.ErrFrpMappingConflict) {
		t.Fatal("historical unknown claim did not block allocation", err)
	}
	hook := "test:reconciliation_audit_failure"
	if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if row, ok := tx.Statement.Dest.(*model.OperationLog); ok && row.Action == "frp.reservation.reconcile" {
			tx.AddError(errors.New("fixture audit failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	_, fault := r.ReconcileReservation(ctx, old.ID, "CHG-42")
	if err := f.db.Callback().Create().Remove(hook); err != nil {
		t.Fatal(err)
	}
	if fault == nil {
		t.Fatal("reconciliation committed without audit")
	}
	inspect, err = r.ReconcileReservation(ctx, old.ID, "")
	if err != nil || inspect.Eligibility != "historical" || inspect.Claim.ReconciledAt != nil {
		t.Fatal(inspect, err)
	}
	for _, expected := range []string{"reconciled", "already_reconciled"} {
		got, err := r.ReconcileReservation(ctx, old.ID, "CHG-42")
		if err != nil || got.Eligibility != expected || got.Claim.ReconciledAt == nil {
			t.Fatal(got, err)
		}
	}
	if _, err := r.CreateMapping(ctx, input); err != nil {
		t.Fatal("reconciled historical claim still blocks allocation", err)
	}
	var preserved model.FrpConfig
	if err := f.db.First(&preserved, cfg.ID).Error; err != nil || preserved.ConfigText != cfg.ConfigText {
		t.Fatal("configuration history changed", err)
	}
	var audits int64
	if err := f.db.Model(&model.OperationLog{}).Where("action=? AND detail=?", "frp.reservation.reconcile", fmt.Sprintf("reservation_id=%d device_id=%d config_id=%d evidence=CHG-42", old.ID, device, cfg.ID)).Count(&audits).Error; err != nil || audits != 1 {
		t.Fatal("reconciliation audit count", audits, err)
	}
	if err := agentauth.New(f.db).Revoke(ctx, f.aliceID, device, "disposable-fixture-password"); err != nil {
		t.Fatal(err)
	}
	got, err := r.ReconcileReservation(ctx, current.ID, "CHG-43")
	if err != nil || got.Eligibility != "reconciled" {
		t.Fatal(got, err)
	}
}
