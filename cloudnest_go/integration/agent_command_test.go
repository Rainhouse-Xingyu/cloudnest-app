//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	mysqldsn "github.com/go-sql-driver/mysql"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"cloudenest_go/internal/agentauth"
	"cloudenest_go/internal/agentcommand"
	storageMysql "cloudenest_go/internal/storage/mysql"
	"cloudenest_go/internal/store"
	"cloudenest_go/model"
	"gorm.io/gorm"
)

func commandIdentity(t *testing.T, f *fixture, id int64) string {
	t.Helper()
	auth := agentauth.New(f.db)
	e, err := auth.Issue(context.Background(), f.aliceID, id, "disposable-fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	k, err := auth.Exchange(context.Background(), e.Enrollment)
	if err != nil {
		t.Fatal(err)
	}
	return k.Credential
}

func TestAgentCommandTLSConfigurationAndReceipt(t *testing.T) {
	f := newFixture(t)
	id := agentDevice(t, f)
	key := commandIdentity(t, f, id)
	f.cfg.FRPKeyFile, f.cfg.FRPKeys = frpTestKeys(t, "one", map[string][]byte{"one": bytes.Repeat([]byte{1}, 32)})
	secret := "command-fixture-frps-secret"
	cfg, err := storageMysql.NewFrpRepo(f.db, f.cfg.FRPKeys).CreateConfig(context.Background(), f.aliceID, false, model.FrpConfig{DeviceID: &id, ConfigText: "serverAddr: localhost\nauth:\n  token: " + secret + "\n"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Where("device_id=?", id).Delete(&model.FrpConfig{}) })
	otherID := agentDevice(t, f)
	otherKey := commandIdentity(t, f, otherID)
	base, client := startAgentTLSServer(t, f)
	call := func(path, token string, body any, want int, dst any, headers ...string) {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest("POST", base+path, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		if len(headers) > 0 {
			req.Header.Set("Idempotency-Key", headers[0])
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, err = io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != want {
			t.Fatalf("%s got %d want %d", path, resp.StatusCode, want)
		}
		if resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("missing no-store")
		}
		if (want != 200 || !strings.HasSuffix(path, "/config")) && bytes.Contains(raw, []byte(secret)) {
			t.Fatal("secret outside leased configuration")
		}
		if dst != nil {
			if err := json.Unmarshal(raw, dst); err != nil {
				t.Fatal(err)
			}
		}
	}
	enqueue := fmt.Sprintf("/api/frp/devices/%d/deploy-config", id)
	requestID := strings.Repeat("a", 32)
	f.json(t, "POST", "/api/agent/commands/claim", "", map[string]string{"claim_id": requestID}, 426)
	call(enqueue, f.alice, map[string]int64{"config_id": cfg.ID}, 400, nil)
	var queued struct {
		ID string `json:"command_id"`
	}
	call(enqueue, f.alice, map[string]int64{"config_id": cfg.ID}, 200, &queued, requestID)
	var duplicate struct {
		ID string `json:"command_id"`
	}
	call(enqueue, f.alice, map[string]int64{"config_id": cfg.ID}, 200, &duplicate, requestID)
	if duplicate.ID != queued.ID {
		t.Fatal("enqueue retry created another command")
	}
	call(enqueue, f.bob, map[string]int64{"config_id": cfg.ID}, 404, nil, requestID)
	managed := fmt.Sprintf("/api/frp/devices/%d/commands", id)
	call(managed, f.alice, agentcommand.EnqueueInput{RequestID: requestID, Action: "reload"}, 409, nil)
	call(managed, f.alice, agentcommand.EnqueueInput{RequestID: strings.Repeat("d", 32), Action: "shell"}, 400, nil)
	claim := map[string]string{"claim_id": strings.Repeat("b", 32)}
	call("/api/agent/commands/claim", f.alice, claim, 401, nil)
	call("/api/agent/commands/claim", key, map[string]any{"claim_id": strings.Repeat("b", 32), "device_id": otherID}, 400, nil)
	var delivery agentcommand.Delivery
	call("/api/agent/commands/claim", key, claim, 200, &delivery)
	if delivery.Command.ID != queued.ID || delivery.Command.Attempts != 1 {
		t.Fatal("wrong command claimed")
	}
	var retry agentcommand.Delivery
	call("/api/agent/commands/claim", key, claim, 200, &retry)
	if retry.LeaseID != delivery.LeaseID {
		t.Fatal("lost-response retry changed lease")
	}
	call("/api/agent/commands/claim", key, map[string]string{"claim_id": strings.Repeat("c", 32)}, 204, nil)
	prefix := "/api/agent/commands/" + queued.ID
	body := map[string]string{"lease_id": delivery.LeaseID}
	call(prefix+"/config", f.alice, body, 401, nil)
	call(prefix+"/config", otherKey, body, 409, nil)
	var config agentcommand.Configuration
	call(prefix+"/config", key, body, 200, &config)
	if !strings.Contains(config.Text, secret) || config.ConfigID != cfg.ID || config.SHA256 != delivery.Command.ConfigSHA256 {
		t.Fatal("leased config differs")
	}
	changed, err := f.cfg.FRPKeys.Seal(id, cfg.ID, "changed config after dispatch")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.FrpConfig{}).Where("id=?", cfg.ID).Update("config_text", changed).Error; err != nil {
		t.Fatal(err)
	}
	call(prefix+"/config", key, body, 409, nil)
	if err := f.db.Model(&model.FrpConfig{}).Where("id=?", cfg.ID).Update("config_text", cfg.ConfigText).Error; err != nil {
		t.Fatal(err)
	}
	call(prefix+"/renew", key, body, 200, nil)
	report := map[string]any{"state": "registered", "config_id": cfg.ID, "observed_at": time.Now().UTC(), "expected": 1, "registered": 1, "failed": 0, "pending": 0, "secret": "must-not-accept"}
	call(prefix+"/result", key, map[string]any{"lease_id": delivery.LeaseID, "success": true, "code": "ok", "diagnostic": report}, 400, nil)
	delete(report, "secret")
	// A configuration deployment cannot masquerade as a diagnostic receipt.
	call(prefix+"/result", key, map[string]any{"lease_id": delivery.LeaseID, "success": true, "code": "ok", "diagnostic": report}, 400, nil)
	ack := map[string]any{"lease_id": delivery.LeaseID, "success": true, "code": "ok"}
	call(prefix+"/result", key, ack, 204, nil)
	call(prefix+"/result", key, ack, 204, nil)
	call(prefix+"/result", key, map[string]any{"lease_id": delivery.LeaseID, "success": false, "code": "internal_error"}, 409, nil)
	call(prefix+"/config", key, body, 409, nil)
	var logs int64
	if err := f.db.Model(&model.OperationLog{}).Where("action=? AND detail=?", "agent.command.succeeded", "command_id="+queued.ID).Count(&logs).Error; err != nil || logs != 1 {
		t.Fatal("receipt retry duplicated audit", logs, err)
	}
	list, err := agentcommand.New(f.db, f.cfg.FRPKeys).List(context.Background(), f.aliceID, false, id, 20)
	if err != nil || len(list) != 1 || list[0].Status != "succeeded" {
		t.Fatal("durable history missing", err)
	}
}

func TestAgentCommandConcurrentClaimsExpiryAndGenerationFence(t *testing.T) {
	f := newFixture(t)
	id := agentDevice(t, f)
	key := commandIdentity(t, f, id)
	ctx := context.Background()
	m := agentcommand.New(f.db, nil)
	in := agentcommand.EnqueueInput{RequestID: strings.Repeat("a", 32), Action: "reload"}
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := agentcommand.New(f.db, nil).Enqueue(ctx, f.aliceID, false, id, in, "")
			if err != nil {
				errs <- err
			} else {
				ids <- out.ID
			}
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var commandID string
	for id := range ids {
		if commandID != "" && commandID != id {
			t.Fatal("duplicate enqueue")
		}
		commandID = id
	}
	claimed := make(chan *agentcommand.Delivery, 8)
	errs = make(chan error, 8)
	for n := range 8 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			out, err := agentcommand.New(f.db, nil).Claim(ctx, key, fmt.Sprintf("%032x", n+1))
			if err != nil {
				errs <- err
			} else if out != nil {
				claimed <- out
			}
		}(n)
	}
	wg.Wait()
	close(claimed)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if len(claimed) != 1 {
		t.Fatal("multiple claim winners", len(claimed))
	}
	first := <-claimed
	if err := f.db.Model(&model.AgentCommand{}).Where("id=?", commandID).Update("lease_expires", gorm.Expr("UTC_TIMESTAMP(6)-INTERVAL 1 SECOND")).Error; err != nil {
		t.Fatal(err)
	}
	second, err := agentcommand.New(f.db, nil).Claim(ctx, key, strings.Repeat("b", 32))
	if err != nil || second == nil || second.LeaseID == first.LeaseID || second.Command.Attempts != 2 {
		t.Fatal("lease not recovered", err)
	}
	if err := m.Finish(ctx, key, commandID, first.LeaseID, true, "ok"); !errors.Is(err, agentcommand.ErrConflict) {
		t.Fatal("stale ACK accepted")
	}
	if _, err := m.Renew(ctx, key, commandID, first.LeaseID); !errors.Is(err, agentcommand.ErrConflict) {
		t.Fatal("stale renewal accepted")
	}
	newKey := commandIdentity(t, f, id)
	if err := m.Finish(ctx, key, commandID, second.LeaseID, true, "ok"); !errors.Is(err, agentauth.ErrDenied) {
		t.Fatal("rotated credential accepted")
	}
	if out, err := m.Claim(ctx, newKey, strings.Repeat("c", 32)); err != nil || out != nil {
		t.Fatal("new generation inherited old command", err)
	}
	if _, err := m.Enqueue(ctx, f.aliceID, false, id, in, ""); !errors.Is(err, agentcommand.ErrConflict) {
		t.Fatal("stale request ID silently reissued")
	}
	rows, err := m.List(ctx, f.aliceID, false, id, 10)
	if err != nil || len(rows) != 1 || rows[0].Status != "cancelled" {
		t.Fatal("old command not cancelled", err)
	}
}

func TestAgentCommandLimitsAuditRollbackAndSchema(t *testing.T) {
	f := newFixture(t)
	id := agentDevice(t, f)
	key := commandIdentity(t, f, id)
	ctx := context.Background()
	m := agentcommand.New(f.db, nil)
	const hook = "test:agent_command_audit"
	if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if log, ok := tx.Statement.Dest.(*model.OperationLog); ok && strings.HasPrefix(log.Action, "agent.command.") {
			tx.AddError(errors.New("injected command audit error"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Callback().Create().Remove(hook) })
	input := agentcommand.EnqueueInput{RequestID: strings.Repeat("a", 32), Action: "stop"}
	if _, err := m.Enqueue(ctx, f.aliceID, false, id, input, ""); err == nil {
		t.Fatal("enqueue ignored audit failure")
	}
	var count int64
	f.db.Model(&model.AgentCommand{}).Where("device_id=?", id).Count(&count)
	if count != 0 {
		t.Fatal("failed enqueue persisted")
	}
	if err := f.db.Callback().Create().Remove(hook); err != nil {
		t.Fatal(err)
	}
	for n := range agentcommand.MaxPending - 2 {
		input.RequestID = fmt.Sprintf("%032x", n+1)
		if _, err := m.Enqueue(ctx, f.aliceID, false, id, input, ""); err != nil {
			t.Fatal(err)
		}
	}
	var capWG sync.WaitGroup
	capResults := make(chan error, 8)
	for n := range 8 {
		capWG.Add(1)
		go func(n int) {
			defer capWG.Done()
			_, err := agentcommand.New(f.db, nil).Enqueue(ctx, f.aliceID, false, id, agentcommand.EnqueueInput{RequestID: fmt.Sprintf("%032x", n+100), Action: "stop"}, "")
			capResults <- err
		}(n)
	}
	capWG.Wait()
	close(capResults)
	accepted := 0
	for err := range capResults {
		if err == nil {
			accepted++
		} else if !errors.Is(err, agentcommand.ErrFull) {
			t.Fatal(err)
		}
	}
	if accepted != 2 {
		t.Fatal("concurrent admission exceeded or lost capacity", accepted)
	}
	input.RequestID = strings.Repeat("f", 32)
	if _, err := m.Enqueue(ctx, f.aliceID, false, id, input, ""); !errors.Is(err, agentcommand.ErrFull) {
		t.Fatal("unbounded queue")
	}
	first, err := m.Claim(ctx, key, strings.Repeat("a", 32))
	if err != nil || first == nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.AgentCommand{}).Where("id=?", first.Command.ID).Updates(map[string]any{"attempts": agentcommand.MaxAttempts, "lease_expires": gorm.Expr("UTC_TIMESTAMP(6)-INTERVAL 1 SECOND")}).Error; err != nil {
		t.Fatal(err)
	}
	next, err := m.Claim(ctx, key, strings.Repeat("b", 32))
	if err != nil || next == nil || next.Command.ID == first.Command.ID {
		t.Fatal("retry exhaustion blocked queue", err)
	}
	var expired model.AgentCommand
	if err := f.db.First(&expired, "id=?", first.Command.ID).Error; err != nil || expired.Status != "timed_out" {
		t.Fatal("retry limit not persisted", err)
	}
	if err := f.db.Exec("ALTER TABLE agent_command DROP INDEX uq_agent_request").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Migrate(f.db); err != nil {
			t.Error(err)
		}
	})
	if err := store.CheckAgentSchema(f.db); err == nil {
		t.Fatal("startup accepted missing idempotency index")
	}
	if err := store.Migrate(f.db); err != nil {
		t.Fatal(err)
	}
}

func TestAgentCommandLeaseAndReceiptAuditRollback(t *testing.T) {
	f := newFixture(t)
	id := agentDevice(t, f)
	key := commandIdentity(t, f, id)
	ctx := context.Background()
	m := agentcommand.New(f.db, nil)
	cmd, err := m.Enqueue(ctx, f.aliceID, false, id, agentcommand.EnqueueInput{RequestID: strings.Repeat("a", 32), Action: "diagnose"}, "")
	if err != nil {
		t.Fatal(err)
	}
	const hook = "test:command_transition_audit"
	install := func(action string) {
		t.Helper()
		if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
			if log, ok := tx.Statement.Dest.(*model.OperationLog); ok && log.Action == action {
				tx.AddError(errors.New("injected transition audit error"))
			}
		}); err != nil {
			t.Fatal(err)
		}
	}
	remove := func() {
		t.Helper()
		if err := f.db.Callback().Create().Remove(hook); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { f.db.Callback().Create().Remove(hook) })
	read := func() model.AgentCommand {
		t.Helper()
		var row model.AgentCommand
		if err := f.db.First(&row, "id=?", cmd.ID).Error; err != nil {
			t.Fatal(err)
		}
		return row
	}
	install("agent.command.leased")
	if out, err := m.Claim(ctx, key, strings.Repeat("b", 32)); err == nil || out != nil {
		t.Fatal("lease survived audit failure")
	}
	if row := read(); row.Status != "pending" || row.Attempts != 0 || row.LeaseID != "" {
		t.Fatal("failed claim changed command")
	}
	remove()
	delivery, err := m.Claim(ctx, key, strings.Repeat("b", 32))
	if err != nil || delivery == nil {
		t.Fatal(err)
	}
	install("agent.command.succeeded")
	if err := m.Finish(ctx, key, cmd.ID, delivery.LeaseID, true, "ok"); err == nil {
		t.Fatal("receipt survived audit failure")
	}
	if row := read(); row.Status != "leased" || row.CompletedAt != nil {
		t.Fatal("failed receipt changed command")
	}
	remove()
	if err := m.Finish(ctx, key, cmd.ID, delivery.LeaseID, true, "ok"); err != nil {
		t.Fatal(err)
	}
	expiring, err := m.Enqueue(ctx, f.aliceID, false, id, agentcommand.EnqueueInput{RequestID: strings.Repeat("c", 32), Action: "stop"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.AgentCommand{}).Where("id=?", expiring.ID).Update("expires_at", gorm.Expr("UTC_TIMESTAMP(6)-INTERVAL 1 SECOND")).Error; err != nil {
		t.Fatal(err)
	}
	if out, err := m.Claim(ctx, key, strings.Repeat("d", 32)); err != nil || out != nil {
		t.Fatal("expired command delivered", err)
	}
	if err := f.db.First(&expiring, "id=?", expiring.ID).Error; err != nil || expiring.Status != "timed_out" {
		t.Fatal("deadline not persisted", err)
	}
}

func TestAgentCommandUTCLeasesAcrossConnectionTimezones(t *testing.T) {
	f := newFixture(t)
	id := agentDevice(t, f)
	key := commandIdentity(t, f, id)
	ctx := context.Background()
	parsed, err := mysqldsn.ParseDSN(f.cfg.MySQLDSN)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Loc, err = time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	cfg := f.cfg
	cfg.MySQLDSN = parsed.FormatDSN()
	localDB, err := store.NewMySQL(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := localDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	local, utc := agentcommand.New(localDB, nil), agentcommand.New(f.db, nil)
	for i, pair := range [][2]*agentcommand.Manager{{local, utc}, {utc, local}} {
		cmd, err := pair[0].Enqueue(ctx, f.aliceID, false, id, agentcommand.EnqueueInput{RequestID: fmt.Sprintf("%032x", i+1), Action: "diagnose"}, "")
		if err != nil {
			t.Fatal(err)
		}
		if age := time.Since(cmd.CreatedAt.Time); age < -time.Second || age > 5*time.Second {
			t.Fatal("creation timestamp shifted with DSN timezone", age)
		}
		delivery, err := pair[1].Claim(ctx, key, fmt.Sprintf("%032x", i+10))
		if err != nil || delivery == nil || delivery.Command.ID != cmd.ID {
			t.Fatal("different timezone could not claim", err)
		}
		remaining := time.Until(delivery.Command.LeaseExpires.Time)
		if remaining < 50*time.Second || remaining > 61*time.Second {
			t.Fatal("lease duration shifted with DSN timezone", remaining)
		}
		if _, err := pair[0].Renew(ctx, key, cmd.ID, delivery.LeaseID); err != nil {
			t.Fatal(err)
		}
		if err := pair[1].Finish(ctx, key, cmd.ID, delivery.LeaseID, true, "ok"); err != nil {
			t.Fatal(err)
		}
	}
}
