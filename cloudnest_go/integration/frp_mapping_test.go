//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"cloudenest_go/internal/app"
	"cloudenest_go/internal/service/contract"
	repo "cloudenest_go/internal/storage/mysql"
	"cloudenest_go/internal/store"
	"cloudenest_go/model"
	driver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

func TestFRPMappingConditionalEditsKeepScopeAndAllocateTransitions(t *testing.T) {
	f, r, devices, servers := scopedFixture(t)
	f.router = fixtureRouter(app.NewContainer(f.cfg, f.db, f.rdb).NewRouter())
	var created struct{ Item model.FrpMapping }
	decode(t, f.json(t, "POST", "/api/frp/mappings", f.alice, map[string]any{"device_id": devices[0], "server_id": servers[0].ID, "local_port": 8080}, 200), &created)
	path := fmt.Sprintf("/api/frp/mappings/%d", created.Item.ID)
	baseline := func(m model.FrpMapping) contract.FrpMappingSnapshot {
		return contract.FrpMappingSnapshot{DeviceID: m.DeviceID, ServerID: m.ServerID, LocalPort: m.LocalPort, RemotePort: m.RemotePort, Domain: m.Domain, Status: m.Status}
	}
	f.json(t, "PUT", path, f.bob, map[string]any{"local_port": 8081, "expected": baseline(created.Item)}, 403)
	var current struct{ Item model.FrpMapping }
	decode(t, f.json(t, "PUT", path, f.alice, map[string]any{"local_port": 8081}, 200), &current)
	f.json(t, "PUT", path, f.alice, map[string]any{"local_port": 8090, "expected": baseline(created.Item)}, 409)
	f.json(t, "PUT", path, f.alice, map[string]any{"server_id": servers[1].ID, "local_port": 8090}, 400)
	wrong := baseline(current.Item)
	wrong.DeviceID = devices[1]
	f.json(t, "PUT", path, f.alice, map[string]any{"local_port": 8090, "expected": wrong}, 409)
	countAudits := func() int64 {
		t.Helper()
		var count int64
		if err := f.db.Model(&model.OperationLog{}).Where("action=? AND detail=?", "frp_mapping_update", fmt.Sprintf("mapping_id=%d device_id=%d", created.Item.ID, devices[0])).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		return count
	}
	before := countAudits()
	start := make(chan struct{})
	results := make(chan error, 2)
	var group sync.WaitGroup
	for _, port := range []int{8082, 8083} {
		group.Go(func() {
			<-start
			b := baseline(current.Item)
			_, err := r.UpdateMapping(context.Background(), contract.FrpUpdateMappingInput{FrpActor: contract.FrpActor{UserID: f.aliceID}, MappingID: created.Item.ID, LocalPort: &port, Expected: &b})
			results <- err
		})
	}
	close(start)
	group.Wait()
	close(results)
	ok, conflict := 0, 0
	for err := range results {
		if err == nil {
			ok++
		} else if errors.Is(err, contract.ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if ok != 1 || conflict != 1 || countAudits() != before+1 {
		t.Fatal("mapping conditional edit was not atomic", ok, conflict)
	}
	winning, err := r.GetMappingByID(context.Background(), created.Item.ID)
	if err != nil || winning.ServerID != servers[0].ID || winning.DeviceID != devices[0] || winning.LocalPort != 8082 && winning.LocalPort != 8083 {
		t.Fatal("mapping scope or winning content changed", err)
	}
	var domain struct{ Item model.FrpMapping }
	decode(t, f.json(t, "PUT", path, f.alice, map[string]any{"domain": "HTTPS://Mapping-Conditional.Example.", "remote_port": 0, "expected": baseline(winning)}, 200), &domain)
	if domain.Item.Domain != "https://mapping-conditional.example" || domain.Item.RemotePort != 0 {
		t.Fatal("modern domain normalization or non-TCP port changed")
	}
	var tcp struct{ Item model.FrpMapping }
	decode(t, f.json(t, "PUT", path, f.alice, map[string]any{"domain": "", "expected": baseline(domain.Item)}, 200), &tcp)
	if tcp.Item.Domain != "" || tcp.Item.RemotePort < servers[0].TCPStart || tcp.Item.RemotePort > servers[0].TCPEnd {
		t.Fatal("HTTP/S to TCP did not allocate in the current server policy")
	}
}

func mappingDevice(t *testing.T, f *fixture, uid int64) int64 {
	t.Helper()
	d := model.Device{UserID: uid, DeviceName: "mapping fixture", Status: 1}
	if err := f.db.Create(&d).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.db.Where("device_id=?", d.ID).Delete(&model.FrpMapping{}).Error; err != nil {
			t.Error(err)
		}
		if err := f.db.Delete(&d).Error; err != nil {
			t.Error(err)
		}
	})
	return d.ID
}

func TestFRPMappingConcurrentAllocationAndDomains(t *testing.T) {
	f := newFixture(t)
	ids := []int64{mappingDevice(t, f, f.aliceID), mappingDevice(t, f, f.bobID)}
	users := []int64{f.aliceID, f.bobID}
	r := repo.NewFrpRepo(f.db)
	const workers = 24
	start := make(chan struct{})
	type result struct {
		m   model.FrpMapping
		err error
	}
	results := make(chan result, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			m, err := r.CreateMapping(context.Background(), contract.FrpCreateMappingInput{FrpActor: contract.FrpActor{UserID: users[i%2]}, DeviceID: ids[i%2], LocalPort: 8080, Status: 1})
			results <- result{m, err}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	ports := map[int]bool{}
	for got := range results {
		if got.err != nil {
			t.Fatal("concurrent allocation failed", got.err)
		}
		if ports[got.m.RemotePort] || got.m.RemotePort < 20000 || got.m.RemotePort > 20999 {
			t.Fatal("invalid allocation", got.m.RemotePort)
		}
		ports[got.m.RemotePort] = true
	}
	if len(ports) != workers {
		t.Fatal("lost allocation")
	}
	// Same canonical HTTP domain with different explicit ports: exactly one wins.
	start = make(chan struct{})
	results = make(chan result, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			domain := fmt.Sprintf("alias-%d.example", ids[0])
			if i%2 == 0 {
				domain = " HTTP://ALIAS-" + fmt.Sprint(ids[0]) + ".EXAMPLE. "
			}
			port := 30000 + i
			m, err := r.CreateMapping(context.Background(), contract.FrpCreateMappingInput{FrpActor: contract.FrpActor{UserID: users[i%2]}, DeviceID: ids[i%2], LocalPort: 8080, RemotePort: &port, Domain: domain, Status: 1})
			results <- result{m, err}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for got := range results {
		if got.err == nil {
			wins++
		} else if errors.Is(got.err, contract.ErrFrpMappingConflict) {
			conflicts++
		} else {
			t.Fatal(got.err)
		}
	}
	if wins != 1 || conflicts != workers-1 {
		t.Fatal("domain arbitration", wins, conflicts)
	}
	port := 31000
	if _, err := r.CreateMapping(context.Background(), contract.FrpCreateMappingInput{FrpActor: contract.FrpActor{UserID: f.aliceID}, DeviceID: ids[0], LocalPort: 443, RemotePort: &port, Domain: fmt.Sprintf("https://alias-%d.example", ids[0]), Status: 1}); err != nil {
		t.Fatal("independent HTTPS listener rejected", err)
	}
	var count int64
	if err := f.db.Model(&model.OperationLog{}).Where("user_id IN ? AND action='frp_mapping_create'", users).Count(&count).Error; err != nil || count != workers+2 {
		t.Fatal("audit mismatch", count, err)
	}
}

func TestFRPMappingExhaustionHoleReuseAndDatabaseUniqueness(t *testing.T) {
	f := newFixture(t)
	id := mappingDevice(t, f, f.aliceID)
	var used []int
	if err := f.db.Model(&model.FrpMapping{}).Where("remote_port BETWEEN 20000 AND 20999").Pluck("remote_port", &used).Error; err != nil {
		t.Fatal(err)
	}
	occupied := map[int]bool{}
	for _, p := range used {
		occupied[p] = true
	}
	var fill []model.FrpMapping
	for p := 20000; p <= 20999; p++ {
		if !occupied[p] {
			fill = append(fill, model.FrpMapping{DeviceID: id, LocalPort: 8080, RemotePort: p, Status: 1})
		}
	}
	if len(fill) < 2 {
		t.Fatal("fixture pool unexpectedly full")
	}
	if err := f.db.CreateInBatches(fill, 100).Error; err != nil {
		t.Fatal(err)
	}
	// Exhaustion is a bounded 409, rather than an internal allocator error.
	body := map[string]any{"device_id": id, "local_port": 8080}
	f.json(t, "POST", "/api/frp/mappings", f.alice, body, 409)
	hole := fill[len(fill)/2]
	if err := f.db.Delete(&hole).Error; err != nil {
		t.Fatal(err)
	}
	var got struct{ Item model.FrpMapping }
	decode(t, f.json(t, "POST", "/api/frp/mappings", f.alice, body, 200), &got)
	if got.Item.RemotePort != hole.RemotePort {
		t.Fatal("did not reuse only free port", got.Item.RemotePort, hole.RemotePort)
	}
	// DB constraints also protect direct SQL and old application writers.
	a := model.FrpMapping{DeviceID: id, LocalPort: 8080, RemotePort: 32000, Domain: fmt.Sprintf("HTTP://DB-%d.EXAMPLE.", id), Status: 1}
	if err := f.db.Create(&a).Error; err != nil {
		t.Fatal(err)
	}
	b := model.FrpMapping{DeviceID: id, LocalPort: 8080, RemotePort: 32001, Domain: fmt.Sprintf("db-%d.example", id), Status: 1}
	if err := f.db.Create(&b).Error; err == nil {
		t.Fatal("database accepted canonical duplicate domain")
	}
	b.Domain = ""
	b.RemotePort = a.RemotePort
	if err := f.db.Create(&b).Error; err == nil {
		t.Fatal("database accepted duplicate remote port")
	}
}

func TestFRPMappingAPIBoundsAndAtomicAudit(t *testing.T) {
	f := newFixture(t)
	id := mappingDevice(t, f, f.aliceID)
	for _, fields := range []map[string]any{{"local_port": 0}, {"local_port": 65536}, {"remote_port": -1}, {"status": 2}, {"domain": "http://x.example/path"}, {"domain": "https://user@x.example"}, {"domain": "{{ .Envs.HOME }}"}, {"domain": "x..example"}} {
		payload := map[string]any{"device_id": id, "local_port": 8080}
		for k, v := range fields {
			payload[k] = v
		}
		f.json(t, "POST", "/api/frp/mappings", f.alice, payload, 400)
	}
	var response struct{ Item model.FrpMapping }
	decode(t, f.json(t, "POST", "/api/frp/mappings", f.alice, map[string]any{"device_id": id, "local_port": 8080, "status": 0}, 200), &response)
	m := response.Item
	if m.Status != 0 {
		t.Fatal("disabled mapping became active")
	}
	path := fmt.Sprintf("/api/frp/mappings/%d", m.ID)
	for _, payload := range []map[string]any{{}, {"local_port": 0}, {"remote_port": 65536}, {"status": -1}, {"domain": "x.example:80"}} {
		f.json(t, "PUT", path, f.alice, payload, 400)
	}
	f.json(t, "PUT", path, f.bob, map[string]any{"local_port": 8081}, 403)
	f.json(t, "DELETE", path, f.bob, nil, 403)
	// Fail the transactional audit for all mutation types. No data may commit.
	callback := "test:frp_mapping_audit_failure"
	var auditErr error = errors.New("injected audit failure")
	if err := f.db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "operation_log" {
			tx.AddError(auditErr)
		}
	}); err != nil {
		t.Fatal(err)
	}
	activeCallback := true
	t.Cleanup(func() {
		if activeCallback {
			f.db.Callback().Create().Remove(callback)
		}
	})
	f.json(t, "POST", "/api/frp/mappings", f.alice, map[string]any{"device_id": id, "local_port": 8090}, 500)
	f.json(t, "PUT", path, f.alice, map[string]any{"local_port": 8091}, 500)
	f.json(t, "DELETE", path, f.alice, nil, 500)
	auditErr = &driver.MySQLError{Number: 1062, Message: "injected audit duplicate"}
	f.json(t, "POST", "/api/frp/mappings", f.alice, map[string]any{"device_id": id, "local_port": 8090}, 500)
	var rows []model.FrpMapping
	if err := f.db.Where("device_id=?", id).Find(&rows).Error; err != nil || len(rows) != 1 || rows[0].LocalPort != 8080 || rows[0].Status != 0 {
		t.Fatal("audit failure committed mutation", rows, err)
	}
	if err := f.db.Callback().Create().Remove(callback); err != nil {
		t.Fatal(err)
	}
	activeCallback = false
	if err := f.db.Model(&model.Device{}).Where("id=?", id).Update("status", 0).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", "/api/frp/mappings", f.alice, map[string]any{"device_id": id, "local_port": 8080}, 409)
	f.json(t, "PUT", path, f.alice, map[string]any{"local_port": 8091}, 409)
	f.json(t, "DELETE", path, f.alice, nil, 200)
}

func TestFRPMappingRechecksOwnershipAfterLockWait(t *testing.T) {
	for _, action := range []string{"create", "update", "delete"} {
		t.Run(action, func(t *testing.T) {
			f := newFixture(t)
			id := mappingDevice(t, f, f.aliceID)
			r := repo.NewFrpRepo(f.db)
			actor := contract.FrpActor{UserID: f.aliceID}
			original, err := r.CreateMapping(context.Background(), contract.FrpCreateMappingInput{FrpActor: actor, DeviceID: id, LocalPort: 8080, Status: 1})
			if err != nil {
				t.Fatal(err)
			}
			holder := f.db.Begin()
			if holder.Error != nil {
				t.Fatal(holder.Error)
			}
			defer holder.Rollback()
			if err := holder.Exec("SELECT id FROM device WHERE id=? FOR UPDATE", id).Error; err != nil {
				t.Fatal(err)
			}
			hinted := make(chan struct{}, 1)
			callback := "test:mapping_hint"
			if err := f.db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table == "device" && tx.Statement.Clauses["FOR"].Name == "" {
					select {
					case hinted <- struct{}{}:
					default:
					}
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer f.db.Callback().Query().Remove(callback)
			result := make(chan error, 1)
			go func() {
				var err error
				switch action {
				case "create":
					_, err = r.CreateMapping(context.Background(), contract.FrpCreateMappingInput{FrpActor: actor, DeviceID: id, LocalPort: 8090, Status: 1})
				case "update":
					port := 8090
					_, err = r.UpdateMapping(context.Background(), contract.FrpUpdateMappingInput{FrpActor: actor, MappingID: original.ID, LocalPort: &port})
				case "delete":
					_, err = r.DeleteMapping(context.Background(), actor, original.ID)
				}
				result <- err
			}()
			select {
			case <-hinted:
			case <-time.After(3 * time.Second):
				t.Fatal("mutation did not read ownership hint")
			}
			if err := holder.Model(&model.Device{}).Where("id=?", id).Update("user_id", f.bobID).Error; err != nil {
				t.Fatal(err)
			}
			if err := holder.Commit().Error; err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-result:
				if !errors.Is(err, contract.ErrForbidden) {
					t.Fatal("stale owner mutation allowed", err)
				}
			case <-time.After(6 * time.Second):
				t.Fatal("mutation stuck")
			}
			var rows []model.FrpMapping
			if err := f.db.Where("device_id=?", id).Find(&rows).Error; err != nil || len(rows) != 1 || rows[0].LocalPort != 8080 {
				t.Fatal("stale mutation changed mapping", rows, err)
			}
		})
	}
}

func TestFRPMappingSchemaRefusesDriftAndPreservesDuplicates(t *testing.T) {
	f := newFixture(t)
	id := mappingDevice(t, f, f.aliceID)
	if err := store.CheckMappingSchema(f.db); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec("DELETE FROM frp_mapping_pool WHERE id=1").Error; err != nil {
		t.Fatal(err)
	}
	if err := store.CheckMappingSchema(f.db); err == nil {
		t.Fatal("missing gate accepted")
	}
	if err := store.Migrate(f.db); err != nil {
		t.Fatal("explicit migration did not restore gate", err)
	}
	if err := f.db.Exec("ALTER TABLE frp_mapping DROP INDEX uq_frp_port_v2, DROP INDEX uq_frp_domain_v2, DROP COLUMN cn_port_key_v2, DROP COLUMN cn_domain_key_v1, ADD UNIQUE INDEX uni_frp_mapping_remote_port(remote_port)").Error; err != nil {
		t.Fatal(err)
	}
	// Restore schema before the fixture closes, even if assertions fail.
	defer func() {
		f.db.Where("device_id=?", id).Delete(&model.FrpMapping{})
		if err := store.Migrate(f.db); err != nil {
			t.Error(err)
		}
	}()
	if err := store.CheckMappingSchema(f.db); err == nil {
		t.Fatal("missing unique domain accepted")
	}
	rows := []model.FrpMapping{{DeviceID: id, LocalPort: 8080, RemotePort: 33000, Domain: fmt.Sprintf("upgrade-%d.example", id), Status: 1}, {DeviceID: id, LocalPort: 8080, RemotePort: 33001, Domain: fmt.Sprintf("http://UPGRADE-%d.EXAMPLE.", id), Status: 1}}
	if err := f.db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(f.db); err == nil {
		t.Fatal("duplicate historical domains silently accepted")
	}
	var count int64
	if err := f.db.Model(&model.FrpMapping{}).Where("device_id=?", id).Count(&count).Error; err != nil || count != 2 {
		t.Fatal("migration modified history", count, err)
	}
	// Test-fixture owner explicitly removes one conflicting row; migration resumes.
	if err := f.db.Delete(&rows[1]).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(f.db); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(f.db); err != nil {
		t.Fatal("migration not repeatable", err)
	}
	if err := store.CheckAgentSchema(f.db); err != nil {
		t.Fatal(err)
	}
}

func TestFRPMappingConcurrentExplicitPortHTTP(t *testing.T) {
	f := newFixture(t)
	ids := []int64{mappingDevice(t, f, f.aliceID), mappingDevice(t, f, f.bobID)}
	tokens := []string{f.alice, f.bob}
	start := make(chan struct{})
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			<-start
			b, _ := json.Marshal(map[string]any{"device_id": ids[i], "local_port": 8080, "remote_port": 34000})
			req := httptest.NewRequest("POST", "/api/frp/mappings", bytes.NewReader(b))
			req.Header.Set("Authorization", "Bearer "+tokens[i])
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)
			codes <- w.Code
		}(i)
	}
	close(start)
	a, b := <-codes, <-codes
	if !((a == 200 && b == 409) || (a == 409 && b == 200)) {
		t.Fatal("explicit port arbitration", a, b)
	}
}

func TestFRPMappingLockDeadlineAndReservationRelease(t *testing.T) {
	f := newFixture(t)
	id := mappingDevice(t, f, f.aliceID)
	r := repo.NewFrpRepo(f.db)
	actor := contract.FrpActor{UserID: f.aliceID}
	holder := f.db.Begin()
	if holder.Error != nil {
		t.Fatal(holder.Error)
	}
	defer holder.Rollback()
	if err := holder.Exec("SELECT id FROM frp_mapping_pool WHERE id=1 FOR UPDATE").Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := r.CreateMapping(ctx, contract.FrpCreateMappingInput{FrpActor: actor, DeviceID: id, LocalPort: 8080, Status: 1}); !errors.Is(err, contract.ErrFrpMappingBusy) {
		t.Fatal("lock cancellation lost bounded error", err)
	}
	if err := holder.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := f.db.Model(&model.FrpMapping{}).Where("device_id=?", id).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("canceled allocation wrote a row", count, err)
	}
	port := 35000
	domain := fmt.Sprintf("release-%d.example", id)
	req := contract.FrpCreateMappingInput{FrpActor: actor, DeviceID: id, LocalPort: 8080, RemotePort: &port, Domain: domain, Status: 1}
	m, err := r.CreateMapping(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	secondPort := 35001
	req.RemotePort = &secondPort
	req.Domain = ""
	second, err := r.CreateMapping(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.UpdateMapping(context.Background(), contract.FrpUpdateMappingInput{FrpActor: actor, MappingID: second.ID, RemotePort: &port}); !errors.Is(err, contract.ErrFrpMappingConflict) {
		t.Fatal("update duplicated port", err)
	}
	if _, err := r.UpdateMapping(context.Background(), contract.FrpUpdateMappingInput{FrpActor: actor, MappingID: second.ID, Domain: &domain}); !errors.Is(err, contract.ErrFrpMappingConflict) {
		t.Fatal("update duplicated domain", err)
	}
	if _, err := r.DeleteMapping(context.Background(), actor, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.UpdateMapping(context.Background(), contract.FrpUpdateMappingInput{FrpActor: actor, MappingID: second.ID, RemotePort: &port, Domain: &domain}); err != nil {
		t.Fatal("committed delete did not release reservations", err)
	}
	var audits []model.OperationLog
	if err := f.db.Where("user_id=? AND action LIKE 'frp_mapping_%'", f.aliceID).Find(&audits).Error; err != nil || len(audits) != 4 {
		t.Fatal("failed writes emitted audit", len(audits), err)
	}
}

func TestFRPMappingSchemaRejectsAlteredExpression(t *testing.T) {
	f := newFixture(t)
	defer func() {
		if err := f.db.Exec("ALTER TABLE frp_mapping DROP INDEX uq_frp_port_v2, DROP INDEX uq_frp_domain_v2, DROP COLUMN cn_port_key_v2, DROP COLUMN cn_domain_key_v1, ADD UNIQUE INDEX uni_frp_mapping_remote_port(remote_port)").Error; err != nil {
			t.Error(err)
			return
		}
		if err := store.Migrate(f.db); err != nil {
			t.Error(err)
		}
	}()
	// Same column/index names and type, but deliberately ineffective uniqueness.
	if err := f.db.Exec("ALTER TABLE frp_mapping MODIFY COLUMN cn_domain_key_v1 VARCHAR(108) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin GENERATED ALWAYS AS (NULL) STORED").Error; err != nil {
		t.Fatal(err)
	}
	if err := store.CheckMappingSchema(f.db); err == nil {
		t.Fatal("altered generated definition accepted")
	}
	if err := store.Migrate(f.db); err == nil {
		t.Fatal("migration silently overwrote changed definition")
	}
}
