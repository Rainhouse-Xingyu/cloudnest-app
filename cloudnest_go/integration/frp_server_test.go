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

	"cloudenest_go/internal/agentcommand"
	"cloudenest_go/internal/frpconfig"
	"cloudenest_go/internal/service"
	"cloudenest_go/internal/service/contract"
	repo "cloudenest_go/internal/storage/mysql"
	"cloudenest_go/internal/store"
	"cloudenest_go/model"
	driver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

func serverFixture(t *testing.T) (*fixture, *repo.FrpRepo, int64, contract.FrpServerView, contract.FrpServerInput) {
	t.Helper()
	f := newFixture(t)
	f.cfg.FRPKeyFile, f.cfg.FRPKeys = frpTestKeys(t, "one", map[string][]byte{"one": bytes.Repeat([]byte{1}, 32)})
	r := repo.NewFrpRepo(f.db, f.cfg.FRPKeys)
	device := agentDevice(t, f)
	secret := "synthetic-server-token #: never public"
	input := contract.FrpServerInput{Spec: frpconfig.ServerSpec{Name: "private server", Address: fmt.Sprintf("fixture-%d.example", device)}, Token: &secret}
	server, err := r.WriteServer(context.Background(), contract.FrpActor{UserID: f.aliceID}, 0, input)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, q := range []*gorm.DB{f.db.Where("device_id=?", device).Delete(&model.FrpConfig{}), f.db.Where("device_id=?", device).Delete(&model.FrpMapping{}), f.db.Where("server_id=?", server.ID).Delete(&model.FrpServerRevision{}), f.db.Delete(&model.FrpServer{}, server.ID)} {
			if q.Error != nil {
				t.Error(q.Error)
			}
		}
	})
	if _, err = r.CreateMapping(context.Background(), contract.FrpCreateMappingInput{FrpActor: contract.FrpActor{UserID: f.aliceID}, DeviceID: device, LocalPort: 8080, Status: 1}); err != nil {
		t.Fatal(err)
	}
	return f, r, device, server, input
}

func TestFRPServerTLSIsolationAndRedaction(t *testing.T) {
	f, r, device, server, input := serverFixture(t)
	base, client := startAgentTLSServer(t, f)
	path := fmt.Sprintf("/api/frp/servers/%d", server.ID)
	call := func(method, path, user string, body any, want int) []byte {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest(method, base+path, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+user)
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d want %d (%s)", method, path, res.StatusCode, want, data)
		}
		if res.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("cacheable server credentials metadata")
		}
		if bytes.Contains(data, []byte(*input.Token)) || bytes.Contains(data, []byte("cnfrps:")) || bytes.Contains(data, []byte(`"token":`)) {
			t.Fatal("server secret disclosed")
		}
		return data
	}
	for _, method := range []string{"GET", "PUT"} {
		f.json(t, method, path, f.alice, map[string]any{}, 426)
	}
	call("GET", path, f.bob, nil, 403)
	call("GET", path, f.alice, nil, 200)
	var list struct{ Items []contract.FrpServerView }
	if err := json.Unmarshal(call("GET", "/api/frp/servers", f.bob, nil, 200), &list); err != nil || len(list.Items) != 0 {
		t.Fatal("cross-owner list", err)
	}
	call("GET", "/api/frp/servers?limit=101", f.alice, nil, 400)
	for _, body := range []any{nil, map[string]any{"unknown": true}, map[string]any{"name": "a", "server_addr": "test.example"}} {
		call("POST", "/api/frp/servers", f.alice, body, 400)
	}
	raw := call("GET", "/api/frp/servers?limit=1", f.alice, nil, 200)
	if err := json.Unmarshal(raw, &list); err != nil || len(list.Items) != 1 || !list.Items[0].TokenConfigured {
		t.Fatal("safe server metadata", err)
	}
	payload := map[string]any{"device_id": device, "server_id": server.ID, "server_revision_id": server.RevisionID}
	preview := call("POST", "/api/frp/configs/preview", f.alice, payload, 200)
	if !bytes.Contains(preview, []byte("[redacted]")) {
		t.Fatal("preview redaction")
	}
	call("POST", "/api/frp/configs/save", f.bob, payload, 403)
	saved := call("POST", "/api/frp/configs/save", f.alice, payload, 200)
	var response struct{ Item model.FrpConfig }
	if err := json.Unmarshal(saved, &response); err != nil {
		t.Fatal(err)
	}
	if response.Item.ServerID != server.ID || response.Item.ServerRevisionID != server.RevisionID {
		t.Fatal("lost config association")
	}
	var cfg model.FrpConfig
	if err := f.db.First(&cfg, response.Item.ID).Error; err != nil {
		t.Fatal(err)
	}
	plain, err := f.cfg.FRPKeys.OpenConfig(device, cfg.ID, server.ID, server.RevisionID, cfg.ConfigText)
	if err != nil || !strings.Contains(plain, *input.Token) || strings.Contains(cfg.ConfigText, *input.Token) {
		t.Fatal("managed encryption", err)
	}
	payload["token"] = "mixed"
	call("POST", "/api/frp/configs/save", f.alice, payload, 400)
	delete(payload, "token")
	payload["server_revision_id"] = server.RevisionID + 1000
	call("POST", "/api/frp/configs/save", f.alice, payload, 409)
	var revision model.FrpServerRevision
	if err := f.db.First(&revision, server.RevisionID).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(revision.Data, *input.Token) {
		t.Fatal("plaintext server row")
	}
	var logs []model.OperationLog
	if err := f.db.Where("user_id=?", f.aliceID).Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	for _, entry := range logs {
		if strings.Contains(entry.Detail, *input.Token) {
			t.Fatal("secret in audit")
		}
	}
	if _, err := r.GetServer(context.Background(), contract.FrpActor{UserID: f.bobID, Admin: true}, server.ID); err != nil {
		t.Fatal("admin metadata", err)
	}
}

func TestFRPServerCASPolicyAndAtomicAudit(t *testing.T) {
	f, r, device, server, input := serverFixture(t)
	ctx := context.Background()
	actor := contract.FrpActor{UserID: f.aliceID}
	duplicate := input
	duplicate.Spec.Address = strings.ToUpper(input.Spec.Address) + "."
	if _, err := r.WriteServer(ctx, actor, 0, duplicate); !errors.Is(err, contract.ErrConflict) {
		t.Fatal("canonical endpoint duplicate", err)
	}
	input.ExpectedRevisionID = server.RevisionID
	input.Token = nil
	const workers = 8
	results := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := r.WriteServer(ctx, actor, server.ID, input); results <- err }()
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else if !errors.Is(err, contract.ErrConflict) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatal("CAS wins", wins)
	}
	current, err := r.GetServer(ctx, actor, server.ID)
	if err != nil || !current.TokenConfigured {
		t.Fatal("nil token did not retain secret", err)
	}
	input.ExpectedRevisionID = current.RevisionID
	changed := input
	changed.Spec.Address = "other.example"
	if _, err := r.WriteServer(ctx, actor, server.ID, changed); !errors.Is(err, contract.ErrConflict) {
		t.Fatal("changed stable endpoint", err)
	}
	callback := "test:frp_server_audit_failure"
	if err := f.db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "operation_log" {
			tx.AddError(&driver.MySQLError{Number: 1062, Message: "injected audit duplicate"})
		}
	}); err != nil {
		t.Fatal(err)
	}
	var before int64
	f.db.Model(&model.FrpServerRevision{}).Where("server_id=?", server.ID).Count(&before)
	_, err = r.WriteServer(ctx, actor, server.ID, input)
	f.db.Callback().Create().Remove(callback)
	if err == nil || errors.Is(err, contract.ErrConflict) {
		t.Fatal("audit failure masked", err)
	}
	var after int64
	f.db.Model(&model.FrpServerRevision{}).Where("server_id=?", server.ID).Count(&after)
	current, _ = r.GetServer(ctx, actor, server.ID)
	if before != after || current.RevisionID != input.ExpectedRevisionID {
		t.Fatal("audit failure committed")
	}
	build := func(spec frpconfig.ServerSpec, _ []model.FrpMapping) (string, error) {
		return "test config with " + spec.Token, nil
	}
	if _, _, err = r.BuildServerConfig(ctx, actor, device, server.ID, server.RevisionID, false, build); !errors.Is(err, contract.ErrConflict) {
		t.Fatal("stale build", err)
	}
	input.Spec.TCPStart = 30000
	input.Spec.TCPEnd = 31000
	narrow, err := r.WriteServer(ctx, actor, server.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = r.BuildServerConfig(ctx, actor, device, server.ID, narrow.RevisionID, false, build); !errors.Is(err, contract.ErrConflict) {
		t.Fatal("port outside policy", err)
	}
	input.ExpectedRevisionID = narrow.RevisionID
	input.Spec.TCPStart = 0
	input.Spec.TCPEnd = 0
	off := false
	input.Enabled = &off
	disabled, err := r.WriteServer(ctx, actor, server.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = r.BuildServerConfig(ctx, actor, device, server.ID, disabled.RevisionID, false, build); !errors.Is(err, contract.ErrConflict) {
		t.Fatal("disabled server build", err)
	}
	other := mappingDevice(t, f, f.bobID)
	if _, _, err = r.BuildServerConfig(ctx, contract.FrpActor{UserID: f.aliceID, Admin: true}, other, server.ID, disabled.RevisionID, false, build); !errors.Is(err, contract.ErrForbidden) {
		t.Fatal("admin mixed owners", err)
	}
}

func TestFRPServerRollbackDeploymentOwnershipAndRotation(t *testing.T) {
	f, r, device, server, input := serverFixture(t)
	ctx := context.Background()
	actor := contract.FrpActor{UserID: f.aliceID}
	key := commandIdentity(t, f, device)
	cfg, err := service.NewFrpService(r, f.rdb).SaveConfig(ctx, actor.UserID, false, service.SaveConfigInput{DeviceID: device, ServerID: server.ID, ServerRevisionID: server.RevisionID})
	if err != nil {
		t.Fatal(err)
	}
	_, rolled, err := r.RollbackConfig(ctx, f.aliceID, false, cfg.ID)
	if err != nil || rolled.ServerID != server.ID || rolled.ServerRevisionID != server.RevisionID || rolled.ID == cfg.ID {
		t.Fatal("rollback association", err)
	}
	manager := agentcommand.New(f.db, f.cfg.FRPKeys)
	_, err = manager.Enqueue(ctx, f.aliceID, false, device, agentcommand.EnqueueInput{RequestID: strings.Repeat("a", 32), Action: "deploy_config", ConfigID: rolled.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := manager.Claim(ctx, key, strings.Repeat("b", 32))
	if err != nil || delivery == nil {
		t.Fatal("managed claim", err)
	}
	// Preserve authentic ciphertext but alter both association columns: opening and
	// enqueueing must fail, even if stripped metadata otherwise looks like legacy.
	if err := f.db.Model(&cfg).Updates(map[string]any{"server_id": 0, "server_revision_id": 0}).Error; err != nil {
		t.Fatal(err)
	}
	_, err = manager.Enqueue(ctx, f.aliceID, false, device, agentcommand.EnqueueInput{RequestID: strings.Repeat("c", 32), Action: "deploy_config", ConfigID: cfg.ID}, "")
	if !errors.Is(err, frpconfig.ErrInvalid) {
		t.Fatal("downgraded association accepted", err)
	}
	if err := f.db.Model(&cfg).Updates(map[string]any{"server_id": server.ID, "server_revision_id": server.RevisionID}).Error; err != nil {
		t.Fatal(err)
	}
	_, keys2 := frpTestKeys(t, "two", map[string][]byte{"one": bytes.Repeat([]byte{1}, 32), "two": bytes.Repeat([]byte{2}, 32)})
	rotate := repo.NewFrpRepo(f.db, keys2)
	dry, err := rotate.ReencryptConfigs(ctx, true)
	if err != nil || dry.ServerOldKey != 1 || dry.ServerRewritten != 0 {
		t.Fatal("server rotation audit", dry, err)
	}
	report, err := rotate.ReencryptConfigs(ctx, false)
	if err != nil || report.ServerRewritten != 1 {
		t.Fatal("server rotation", report, err)
	}
	_, onlyNew := frpTestKeys(t, "two", map[string][]byte{"two": bytes.Repeat([]byte{2}, 32)})
	r2 := repo.NewFrpRepo(f.db, onlyNew)
	if _, err := r2.GetServer(ctx, actor, server.ID); err != nil {
		t.Fatal("old key still required", err)
	}
	if _, _, err := r2.RollbackConfig(ctx, f.aliceID, false, cfg.ID); err != nil {
		t.Fatal("managed config rotation", err)
	}
	// A server-disable is a new-work fence, not a remote stop or token rotation.
	input.ExpectedRevisionID = server.RevisionID
	off := false
	input.Enabled = &off
	if _, err := r2.WriteServer(ctx, actor, server.ID, input); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r2.RollbackConfig(ctx, f.aliceID, false, cfg.ID); !errors.Is(err, contract.ErrConflict) {
		t.Fatal("disabled rollback", err)
	}
	m2 := agentcommand.New(f.db, onlyNew)
	if _, err := m2.Enqueue(ctx, f.aliceID, false, device, agentcommand.EnqueueInput{RequestID: strings.Repeat("d", 32), Action: "deploy_config", ConfigID: cfg.ID}, ""); !errors.Is(err, agentcommand.ErrConflict) {
		t.Fatal("disabled deploy", err)
	}
	if err := f.db.Model(&model.Device{}).Where("id=?", device).Update("user_id", f.bobID).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := r2.RollbackConfig(ctx, f.bobID, false, cfg.ID); !errors.Is(err, contract.ErrForbidden) {
		t.Fatal("new owner inherited server token", err)
	}
}

func TestFRPServerSchemaFailsClosedAndPreservesLegacy(t *testing.T) {
	f := newFixture(t)
	if err := store.CheckServerSchema(f.db); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec("ALTER TABLE frp_server DROP INDEX uq_frp_server_endpoint").Error; err != nil {
		t.Fatal(err)
	}
	if err := store.CheckServerSchema(f.db); err == nil {
		t.Fatal("missing unique index accepted")
	}
	if err := f.db.Exec("ALTER TABLE frp_server ADD INDEX uq_frp_server_endpoint (address,port)").Error; err != nil {
		t.Fatal(err)
	}
	if err := store.CheckServerSchema(f.db); err == nil {
		t.Fatal("nonunique replacement accepted")
	}
	if err := f.db.Exec("ALTER TABLE frp_server DROP INDEX uq_frp_server_endpoint, ADD UNIQUE INDEX uq_frp_server_endpoint (address,port)").Error; err != nil {
		t.Fatal(err)
	}
	if err := store.CheckServerSchema(f.db); err != nil {
		t.Fatal(err)
	}
	device := agentDevice(t, f)
	legacy := model.FrpConfig{DeviceID: &device, ConfigText: "historical plaintext unchanged"}
	if err := f.db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Delete(&legacy) })
	if err := store.Migrate(f.db); err != nil {
		t.Fatal(err)
	}
	var got model.FrpConfig
	if err := f.db.First(&got, legacy.ID).Error; err != nil || got.ServerID != 0 || got.ServerRevisionID != 0 || got.ConfigText != legacy.ConfigText {
		t.Fatal("legacy migration inferred or changed data", err)
	}
}

// The service must use the real YAML generator, with vhost policy enforced.
func TestFRPServerHTTPPolicyAndGeneratedConfig(t *testing.T) {
	f, r, device, server, input := serverFixture(t)
	ctx := context.Background()
	actor := contract.FrpActor{UserID: f.aliceID}
	_, err := r.CreateMapping(ctx, contract.FrpCreateMappingInput{FrpActor: actor, DeviceID: device, LocalPort: 8081, Domain: fmt.Sprintf("server-%d.example", device), Status: 1})
	if err != nil {
		t.Fatal(err)
	}
	svc := service.NewFrpService(r, f.rdb)
	req := service.SaveConfigInput{DeviceID: device, ServerID: server.ID, ServerRevisionID: server.RevisionID}
	if _, err := svc.SaveConfig(ctx, f.aliceID, false, req); !errors.Is(err, contract.ErrConflict) {
		t.Fatal("unconfigured HTTP listener", err)
	}
	input.ExpectedRevisionID = server.RevisionID
	input.Spec.HTTPPort = 80
	current, err := r.WriteServer(ctx, actor, server.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	req.ServerRevisionID = current.RevisionID
	cfg, err := svc.SaveConfig(ctx, f.aliceID, false, req)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := f.cfg.FRPKeys.OpenConfig(device, cfg.ID, cfg.ServerID, cfg.ServerRevisionID, cfg.ConfigText)
	if err != nil || !strings.Contains(plain, "type: http") || !strings.Contains(plain, "type: tcp") {
		t.Fatal("generator missed proxies", err)
	}
}

func TestFRPServerSnapshotTamperAndRotationAuditRollback(t *testing.T) {
	f, r, _, server, input := serverFixture(t)
	ctx := context.Background()
	actor := contract.FrpActor{UserID: f.aliceID}
	var original model.FrpServerRevision
	if err := f.db.First(&original, server.RevisionID).Error; err != nil {
		t.Fatal(err)
	}
	input.ExpectedRevisionID = server.RevisionID
	input.Token = nil
	input.Spec.Name = "revision two"
	next, err := r.WriteServer(ctx, actor, server.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	var second model.FrpServerRevision
	if err := f.db.First(&second, next.RevisionID).Error; err != nil {
		t.Fatal(err)
	}
	authentic := second.Data
	if err := f.db.Model(&second).Update("data", original.Data).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := r.GetServer(ctx, actor, server.ID); !errors.Is(err, frpconfig.ErrInvalid) {
		t.Fatal("swapped ciphertext accepted", err)
	}
	if _, err := r.ReencryptConfigs(ctx, true); !errors.Is(err, frpconfig.ErrInvalid) {
		t.Fatal("dry run skipped authentication", err)
	}
	if err := f.db.Model(&second).Update("data", authentic).Error; err != nil {
		t.Fatal(err)
	}
	_, keys := frpTestKeys(t, "two", map[string][]byte{"one": bytes.Repeat([]byte{1}, 32), "two": bytes.Repeat([]byte{2}, 32)})
	callback := "test:server_rotation_audit_failure"
	if err := f.db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if log, ok := tx.Statement.Dest.(*model.OperationLog); ok && log.Action == "frp_server_encrypt" {
			tx.AddError(errors.New("injected rotation audit failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	_, err = repo.NewFrpRepo(f.db, keys).ReencryptConfigs(ctx, false)
	f.db.Callback().Create().Remove(callback)
	if err == nil {
		t.Fatal("audit failure ignored")
	}
	var unchanged model.FrpServerRevision
	if err := f.db.First(&unchanged, original.ID).Error; err != nil || unchanged.Data != original.Data {
		t.Fatal("failed audit committed rotation", err)
	}
	input.ExpectedRevisionID = next.RevisionID
	empty := ""
	input.Token = &empty
	cleared, err := r.WriteServer(ctx, actor, server.ID, input)
	if err != nil || cleared.TokenConfigured {
		t.Fatal("explicit empty token not cleared", err)
	}
}
