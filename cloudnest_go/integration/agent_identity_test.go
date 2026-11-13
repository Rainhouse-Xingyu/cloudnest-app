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
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"cloudenest_go/internal/agentauth"
	"cloudenest_go/internal/store"
	"cloudenest_go/model"
	mysqldsn "github.com/go-sql-driver/mysql"
)

func agentDevice(t *testing.T, f *fixture) int64 {
	t.Helper()
	var response struct {
		Item model.Device `json:"item"`
	}
	decode(t, f.json(t, "POST", "/api/frp/devices", f.alice, map[string]string{"device_name": "test device"}, 200), &response)
	return response.Item.ID
}

func TestAgentTLSRegistrationRotationAndIsolation(t *testing.T) {
	f := newFixture(t)
	id := agentDevice(t, f)
	base, client := startAgentTLSServer(t, f)
	call := func(method, path, token string, body any, want int, dst any) {
		t.Helper()
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest(method, base+path, bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("%s %s got %d want %d", method, path, res.StatusCode, want)
		}
		if dst != nil {
			if err := json.NewDecoder(res.Body).Decode(dst); err != nil {
				t.Fatal(err)
			}
		} else {
			io.Copy(io.Discard, res.Body)
		}
	}
	issue := fmt.Sprintf("/api/frp/devices/%d/enrollment", id)
	revoke := fmt.Sprintf("/api/frp/devices/%d/credential/revoke", id)
	password := map[string]string{"password": "disposable-fixture-password"}
	f.json(t, "POST", issue, f.alice, password, 426)
	call("POST", issue, f.bob, password, 404, nil)
	call("POST", issue, f.alice, map[string]string{"password": "wrong"}, 401, nil)
	register := func() (string, string) {
		t.Helper()
		var e agentauth.Issued
		call("POST", issue, f.alice, password, 201, &e)
		if e.DeviceID != id || len(e.Enrollment) < 64 || e.ExpiresAt.Before(time.Now()) {
			t.Fatal("invalid issued enrollment")
		}
		var k agentauth.Registered
		call("POST", "/api/agent/enroll", "", map[string]string{"enrollment": e.Enrollment}, 201, &k)
		if k.DeviceID != id || len(k.Credential) < 64 {
			t.Fatal("invalid issued key")
		}
		return e.Enrollment, k.Credential
	}
	e, k := register()
	call("POST", "/api/agent/enroll", "", map[string]string{"enrollment": e}, 401, nil)
	call("POST", "/api/agent/heartbeat", f.alice, map[string]string{}, 401, nil)
	call("POST", "/api/agent/heartbeat", e, map[string]string{}, 401, nil)
	call("GET", "/api/auth/me", k, nil, 401, nil)
	call("POST", "/api/agent/heartbeat", k, map[string]any{"device_id": id + 1}, 400, nil)
	call("POST", "/api/agent/heartbeat", k, nil, 400, nil)
	var beat agentauth.Heartbeat
	call("POST", "/api/agent/heartbeat", k, map[string]string{"client_version": "v1.0"}, 200, &beat)
	if beat.DeviceID != id || time.Since(beat.ReceivedAt) > 10*time.Second {
		t.Fatal("wrong device/time")
	}
	var status struct {
		Item struct {
			Online  bool
			Version string `json:"client_version"`
		}
	}
	statusPath := fmt.Sprintf("/api/frp/devices/%d/status", id)
	call("GET", statusPath, f.alice, nil, 200, &status)
	if !status.Item.Online || status.Item.Version != "v1.0" {
		t.Fatal("owner did not see managed heartbeat")
	}
	if err := f.db.Exec("UPDATE agent_binding SET last_seen_at=UTC_TIMESTAMP(6)-INTERVAL 100 SECOND WHERE device_id=?", id).Error; err != nil {
		t.Fatal(err)
	}
	call("GET", statusPath, f.alice, nil, 200, &status)
	if status.Item.Online {
		t.Fatal("inactive device stayed online")
	}
	call("POST", "/api/agent/heartbeat", k, map[string]string{"client_version": "v1.0"}, 200, &beat)
	call("GET", statusPath, f.bob, nil, 403, nil)
	call("POST", statusPath+"/report", f.alice, map[string]any{"online": true}, 403, nil)
	queue := fmt.Sprintf("frp:device:cmd:%d", id)
	if err := f.rdb.RPush(context.Background(), queue, "test-managed-command").Err(); err != nil {
		t.Fatal(err)
	}
	call("GET", fmt.Sprintf("/api/frp/devices/%d/commands/next", id), f.alice, nil, 403, nil)
	if count, err := f.rdb.LLen(context.Background(), queue).Result(); err != nil || count != 1 {
		t.Fatal("user consumed managed command")
	}
	if err := f.rdb.HSet(context.Background(), "frp:command:data:test-managed-command", "device_id", id, "status", "pending").Err(); err != nil {
		t.Fatal(err)
	}
	call("POST", "/api/frp/commands/test-managed-command/result", f.alice, map[string]any{"success": true}, 403, nil)
	var row model.AgentBinding
	if err := f.db.First(&row, "device_id=?", id).Error; err != nil {
		t.Fatal(err)
	}
	if row.EnrollmentID != nil || len(row.CredentialDigest) != 32 || row.LastSeenAt == nil || row.ClientVersion != "v1.0" {
		t.Fatal("binding not consumed/published")
	}
	var logs []model.OperationLog
	if err := f.db.Where("user_id=? AND action LIKE 'agent.%'", f.aliceID).Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 2 {
		t.Fatal("missing issue/exchange audit", len(logs))
	}
	for _, l := range logs {
		if strings.Contains(l.Detail, e) || strings.Contains(l.Detail, k) {
			t.Fatal("secret in audit")
		}
	}
	_, k2 := register()
	call("POST", "/api/agent/heartbeat", k, map[string]string{}, 401, nil)
	call("POST", "/api/agent/heartbeat", k2, map[string]string{}, 200, nil)
	call("POST", revoke, f.bob, password, 404, nil)
	call("POST", revoke, f.alice, password, 204, nil)
	call("POST", "/api/agent/heartbeat", k2, map[string]string{}, 401, nil)
	call("GET", statusPath, f.alice, nil, 200, &status)
	if status.Item.Online {
		t.Fatal("revoked device still online")
	}
	call("POST", statusPath+"/report", f.alice, map[string]any{"online": true}, 403, nil)

}

func TestAgentConcurrentExchangeLifecycleAndAuditRollback(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id := agentDevice(t, f)
	m := agentauth.New(f.db)
	issue := func() agentauth.Issued {
		t.Helper()
		e, err := m.Issue(ctx, f.aliceID, id, "disposable-fixture-password")
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	e := issue()
	if err := f.db.Exec("CREATE TRIGGER agent_test_audit BEFORE INSERT ON operation_log FOR EACH ROW BEGIN IF NEW.action='agent.enrollment.exchange' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test failure'; END IF; END").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Exec("DROP TRIGGER IF EXISTS agent_test_audit") })
	if k, err := m.Exchange(ctx, e.Enrollment); err == nil || k.Credential != "" {
		t.Fatal("issued credentials despite audit failure")
	}
	if err := f.db.Exec("DROP TRIGGER agent_test_audit").Error; err != nil {
		t.Fatal(err)
	}
	results := make(chan agentauth.Registered, 6)
	errs := make(chan error, 6)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k, err := m.Exchange(ctx, e.Enrollment)
			if err != nil {
				errs <- err
			} else {
				results <- k
			}
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	if len(results) != 1 || len(errs) != 5 {
		t.Fatalf("exchange outcomes: %d accepted, %d rejected", len(results), len(errs))
	}
	for err := range errs {
		if !errors.Is(err, agentauth.ErrDenied) {
			t.Fatal(err)
		}
	}
	key := (<-results).Credential
	pending := issue()
	if _, err := m.Beat(ctx, key, ""); err != nil {
		t.Fatal("issuing pending rotation revoked active key", err)
	}
	if err := f.db.Exec("UPDATE device SET status=0 WHERE id=?", id).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec("UPDATE device SET status=1 WHERE id=?", id).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := m.Beat(ctx, key, ""); !errors.Is(err, agentauth.ErrDenied) {
		t.Fatal("disabled device key revived")
	}
	if _, err := m.Exchange(ctx, pending.Enrollment); !errors.Is(err, agentauth.ErrDenied) {
		t.Fatal("disabled device enrollment revived")
	}
	newKey, err := m.Exchange(ctx, issue().Enrollment)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec("UPDATE user SET status=0 WHERE id=?", f.aliceID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec("UPDATE user SET status=1 WHERE id=?", f.aliceID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = m.Beat(ctx, newKey.Credential, ""); !errors.Is(err, agentauth.ErrDenied) {
		t.Fatal("disabled owner key revived")
	}
	expired := issue()
	if err := f.db.Exec("UPDATE agent_binding SET enrollment_expires=UTC_TIMESTAMP(6)-INTERVAL 1 SECOND WHERE device_id=?", id).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = m.Exchange(ctx, expired.Enrollment); !errors.Is(err, agentauth.ErrDenied) {
		t.Fatal("expired enrollment accepted")
	}
}

func TestAgentRuntimeChecksActualTriggersWithDMLPrivileges(t *testing.T) {
	f := newFixture(t)
	dsn, err := mysqldsn.ParseDSN(f.cfg.MySQLDSN)
	if err != nil {
		t.Fatal(err)
	}
	dsn.User = "cloudnest_agent_runtime"
	cfg := f.cfg
	cfg.MySQLDSN = dsn.FormatDSN()
	runtime, err := store.NewMySQL(cfg)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := runtime.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var visible int
	if err = runtime.Raw("SELECT COUNT(*) FROM information_schema.triggers WHERE trigger_schema=DATABASE()").Scan(&visible).Error; err != nil || visible != 0 {
		t.Fatal("runtime unexpectedly has direct trigger access", visible, err)
	}
	if err = store.CheckAgentSchema(runtime); err != nil {
		t.Fatal(err)
	}
	if err = f.db.Exec("DROP TRIGGER cn_agent_device_change_v1").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Migrate(f.db); err != nil {
			t.Error(err)
		}
	})
	if err = store.CheckAgentSchema(runtime); err == nil {
		t.Fatal("missing active revocation fence accepted")
	}
	if err = f.db.Exec("CREATE TRIGGER cn_agent_device_change_v1 AFTER UPDATE ON device FOR EACH ROW SET @agent_trigger_probe=1").Error; err != nil {
		t.Fatal(err)
	}
	if err = store.CheckAgentSchema(runtime); err == nil {
		t.Fatal("same trigger name with incorrect behavior accepted")
	}
	if err = store.CheckTransferCleanupContract(f.db); err == nil {
		t.Fatal("transfer cleanup accepted altered agent trigger")
	}
	if err = store.Migrate(f.db); err == nil {
		t.Fatal("unexpected trigger overwritten")
	}
	if err = f.db.Exec("DROP TRIGGER cn_agent_device_change_v1").Error; err != nil {
		t.Fatal(err)
	}
}

func TestAgentEnrollmentRateAndTransport(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 31; i++ {
		req := httptest.NewRequest("POST", "https://server.test/api/agent/enroll", strings.NewReader(`{"enrollment":"invalid"}`))
		req.RemoteAddr = f.remoteAddr
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("192.0.2.%d", i))
		w := httptest.NewRecorder()
		f.router.ServeHTTP(w, req)
		want := 401
		if i == 30 {
			want = 429
		}
		if w.Code != want || w.Header().Get("Cache-Control") != "no-store" || i == 30 && w.Header().Get("Retry-After") == "" {
			t.Fatal(i, w.Code)
		}
	}
	req := httptest.NewRequest("POST", "/api/agent/enroll", strings.NewReader(`{"enrollment":"invalid"}`))
	req.Header.Set("X-Forwarded-Proto", "https")
	req.RemoteAddr = f.remoteAddr
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	if w.Code != 426 {
		t.Fatal("untrusted TLS forwarding header accepted", w.Code)
	}
}

func TestAgentOwnershipChangeAndDeletionInvalidateBothSecrets(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	manager := agentauth.New(f.db)
	id := agentDevice(t, f)
	register := func(owner int64) (agentauth.Registered, agentauth.Issued) {
		t.Helper()
		first, err := manager.Issue(ctx, owner, id, "disposable-fixture-password")
		if err != nil {
			t.Fatal(err)
		}
		key, err := manager.Exchange(ctx, first.Enrollment)
		if err != nil {
			t.Fatal(err)
		}
		next, err := manager.Issue(ctx, owner, id, "disposable-fixture-password")
		if err != nil {
			t.Fatal(err)
		}
		return key, next
	}
	rejected := func(key agentauth.Registered, pending agentauth.Issued) {
		t.Helper()
		if _, err := manager.Beat(ctx, key.Credential, "v1"); !errors.Is(err, agentauth.ErrDenied) {
			t.Fatal("prior credential remained usable")
		}
		if _, err := manager.Exchange(ctx, pending.Enrollment); !errors.Is(err, agentauth.ErrDenied) {
			t.Fatal("prior enrollment remained usable")
		}
	}
	key, pending := register(f.aliceID)
	if err := f.db.Exec("UPDATE device SET user_id=? WHERE id=?", f.bobID, id).Error; err != nil {
		t.Fatal(err)
	}
	rejected(key, pending)
	key, pending = register(f.bobID)
	if err := f.db.Delete(&model.Device{}, id).Error; err != nil {
		t.Fatal(err)
	}
	rejected(key, pending)
	var binding model.AgentBinding
	if err := f.db.First(&binding, "device_id=?", id).Error; err != nil || binding.CredentialID != nil || binding.EnrollmentID != nil {
		t.Fatal("deleted device still has lookup keys", err)
	}
	id = agentDevice(t, f)
	key, pending = register(f.aliceID)
	if err := f.db.Delete(&model.User{}, f.aliceID).Error; err != nil {
		t.Fatal(err)
	}
	rejected(key, pending)
	if err := f.db.First(&model.AgentBinding{}, "device_id=? AND credential_id IS NULL AND enrollment_id IS NULL", id).Error; err != nil {
		t.Fatal("deleted owner still has lookup keys", err)
	}
}

func TestLegacyFRPStatusExpiresByServerReceipt(t *testing.T) {
	f := newFixture(t)
	id := agentDevice(t, f)
	path := fmt.Sprintf("/api/frp/devices/%d/status", id)
	cache := fmt.Sprintf("frp:device:status:%d", id)
	online := func(want bool) {
		t.Helper()
		var result struct{ Item struct{ Online bool } }
		decode(t, f.json(t, "GET", path, f.alice, nil, 200), &result)
		if result.Item.Online != want {
			t.Fatalf("online=%t, want %t", result.Item.Online, want)
		}
	}
	online(false)
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	f.json(t, "POST", path+"/report", f.alice, map[string]any{"online": true, "last_heartbeat": future}, 200)
	online(true)
	for _, stale := range []string{time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339Nano), future, "invalid"} {
		if err := f.rdb.HSet(t.Context(), cache, "updated_at", stale).Err(); err != nil {
			t.Fatal(err)
		}
		online(false)
	}
	f.json(t, "POST", path+"/report", f.alice, map[string]any{"online": true}, 200)
	online(true)
	f.json(t, "POST", path+"/report", f.alice, map[string]any{"online": false}, 200)
	online(false)
	f.json(t, "POST", path+"/report", f.alice, map[string]any{"online": true}, 200)
	f.json(t, "PUT", fmt.Sprintf("/api/frp/devices/%d", id), f.alice, map[string]any{"status": 0}, 200)
	online(false)
	f.json(t, "POST", path+"/report", f.alice, map[string]any{"online": true}, 403)
	f.json(t, "GET", fmt.Sprintf("/api/frp/devices/%d/commands/next", id), f.alice, nil, 403)
}
