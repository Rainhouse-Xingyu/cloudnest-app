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
	"cloudenest_go/internal/service/contract"
	repo "cloudenest_go/internal/storage/mysql"
	"cloudenest_go/internal/store"
	"cloudenest_go/model"
	"gorm.io/gorm"
)

func TestFRPDeviceRetirementPreservesHistoryAndReservations(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id := agentDevice(t, f)
	auth := agentauth.New(f.db)
	key := commandIdentity(t, f, id)
	pending, err := auth.Issue(ctx, f.aliceID, id, "disposable-fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	f.cfg.FRPKeyFile, f.cfg.FRPKeys = frpTestKeys(t, "one", map[string][]byte{"one": bytes.Repeat([]byte{1}, 32)})
	r := repo.NewFrpRepo(f.db, f.cfg.FRPKeys)
	actor := contract.FrpActor{UserID: f.aliceID}
	mapping, err := r.CreateMapping(ctx, contract.FrpCreateMappingInput{FrpActor: actor, DeviceID: id, LocalPort: 8080, Status: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		f.db.Where("device_id=?", id).Delete(&model.FrpMapping{})
		f.db.Where("device_id=?", id).Delete(&model.FrpConfig{})
	})
	cfg, err := r.CreateConfig(ctx, f.aliceID, false, model.FrpConfig{DeviceID: &id, ConfigText: "retirement fixture config"})
	if err != nil {
		t.Fatal(err)
	}
	commands := agentcommand.New(f.db, f.cfg.FRPKeys)
	enqueue := func(n int) model.AgentCommand {
		t.Helper()
		c, err := commands.Enqueue(ctx, f.aliceID, false, id, agentcommand.EnqueueInput{RequestID: fmt.Sprintf("%032x", n), Action: "stop"}, "")
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	finished := enqueue(1)
	delivery, err := commands.Claim(ctx, key, strings.Repeat("a", 32))
	if err != nil || delivery == nil {
		t.Fatal(err)
	}
	if err := commands.Finish(ctx, key, finished.ID, delivery.LeaseID, true, "ok"); err != nil {
		t.Fatal(err)
	}
	leased := enqueue(2)
	delivery, err = commands.Claim(ctx, key, strings.Repeat("b", 32))
	if err != nil || delivery == nil {
		t.Fatal(err)
	}
	queued := enqueue(3)
	path := fmt.Sprintf("/api/frp/devices/%d", id)
	f.json(t, "DELETE", path, f.bob, nil, 403)
	var retiredResponse struct {
		Retired              bool
		ReservationsRetained bool `json:"reservations_retained"`
	}
	decode(t, f.json(t, "DELETE", path, f.alice, nil, 200), &retiredResponse)
	if !retiredResponse.Retired || !retiredResponse.ReservationsRetained {
		t.Fatal("retirement response hid retained reservations")
	}
	f.json(t, "DELETE", path, f.alice, nil, 404)
	f.json(t, "PUT", path, f.alice, map[string]any{"status": 1}, 404)
	for _, suffix := range []string{"", "/mappings", "/configs", "/status"} {
		f.json(t, "GET", path+suffix, f.alice, nil, 404)
	}
	f.json(t, "GET", path+"?retired=1", f.bob, nil, 403)
	var detail struct {
		Item struct {
			Device           model.Device
			MappingCount     int64      `json:"mapping_count"`
			LatestConfigTime *time.Time `json:"latest_config_time"`
		}
	}
	decode(t, f.json(t, "GET", path+"?retired=1", f.alice, nil, 200), &detail)
	if !detail.Item.Device.RetiredAt.Valid || detail.Item.Device.Status != 0 || detail.Item.MappingCount != 1 || detail.Item.LatestConfigTime == nil {
		t.Fatal("retirement metadata lost", detail)
	}
	for _, token := range []string{f.alice, f.bob} {
		var active struct{ Items []model.Device }
		decode(t, f.json(t, "GET", "/api/frp/devices", token, nil, 200), &active)
		for _, d := range active.Items {
			if d.ID == id {
				t.Fatal("retired device appeared active")
			}
		}
		var archived struct{ Items []model.Device }
		decode(t, f.json(t, "GET", "/api/frp/devices?retired=1", token, nil, 200), &archived)
		found := false
		for _, d := range archived.Items {
			if d.ID == id {
				found = true
			}
		}
		if found != (token == f.alice) {
			t.Fatal("archive isolation failed")
		}
	}
	if _, err := auth.Beat(ctx, key, "test"); !errors.Is(err, agentauth.ErrDenied) {
		t.Fatal("retired credential remained valid", err)
	}
	if _, err := auth.Exchange(ctx, pending.Enrollment); !errors.Is(err, agentauth.ErrDenied) {
		t.Fatal("retired enrollment remained valid", err)
	}
	if _, err := auth.Issue(ctx, f.aliceID, id, "disposable-fixture-password"); !errors.Is(err, agentauth.ErrOwner) {
		t.Fatal("retired identity reenrolled", err)
	}
	if _, err := commands.Claim(ctx, key, strings.Repeat("c", 32)); !errors.Is(err, agentauth.ErrDenied) {
		t.Fatal("retired key claimed work", err)
	}
	if err := commands.Finish(ctx, key, leased.ID, delivery.LeaseID, true, "ok"); !errors.Is(err, agentauth.ErrDenied) {
		t.Fatal("late retired ACK accepted", err)
	}
	var rows []model.AgentCommand
	if err := f.db.Where("device_id=?", id).Find(&rows).Error; err != nil || len(rows) != 3 {
		t.Fatal("command history deleted", err)
	}
	for _, c := range rows {
		if c.ID == finished.ID {
			if c.Status != "succeeded" || c.ResultCode != "ok" {
				t.Fatal("completed receipt changed", c.Status)
			}
		} else if (c.ID == leased.ID || c.ID == queued.ID) && (c.Status != "cancelled" || c.CompletedAt == nil) {
			t.Fatal("unfinished work not cancelled", c.Status)
		}
	}
	var retained model.FrpConfig
	if err := f.db.First(&retained, cfg.ID).Error; err != nil || retained.ConfigText != cfg.ConfigText {
		t.Fatal("encrypted config history changed", err)
	}
	other := mappingDevice(t, f, f.aliceID)
	if _, err := r.CreateMapping(ctx, contract.FrpCreateMappingInput{FrpActor: actor, DeviceID: other, LocalPort: 8080, RemotePort: &mapping.RemotePort, Status: 1}); !errors.Is(err, contract.ErrFrpMappingConflict) {
		t.Fatal("retirement prematurely released remote reservation", err)
	}
	if _, err := r.GetAgentHeartbeat(ctx, f.aliceID, false, id); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatal("raw heartbeat query exposed retired identity", err)
	}
	var audits int64
	if err := f.db.Model(&model.OperationLog{}).Where("user_id=? AND action='frp_device_delete'", f.aliceID).Count(&audits).Error; err != nil || audits != 1 {
		t.Fatal("retirement audit mismatch", audits, err)
	}
}

func TestFRPDeviceMutationsRollbackWithAuditFailure(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id := agentDevice(t, f)
	key := commandIdentity(t, f, id)
	m := agentcommand.New(f.db, nil)
	c, err := m.Enqueue(ctx, f.aliceID, false, id, agentcommand.EnqueueInput{RequestID: strings.Repeat("a", 32), Action: "stop"}, "")
	if err != nil {
		t.Fatal(err)
	}
	callback := "test:device_audit_failure"
	if err := f.db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "operation_log" {
			tx.AddError(errors.New("injected device audit failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer f.db.Callback().Create().Remove(callback)
	path := fmt.Sprintf("/api/frp/devices/%d", id)
	f.json(t, "POST", "/api/frp/devices", f.alice, map[string]any{"device_name": "must rollback"}, 500)
	f.json(t, "PUT", path, f.alice, map[string]any{"device_name": "must rollback", "status": 0}, 500)
	f.json(t, "DELETE", path, f.alice, nil, 500)
	var devices []model.Device
	if err := f.db.Unscoped().Where("user_id=?", f.aliceID).Find(&devices).Error; err != nil || len(devices) != 1 || devices[0].Status != 1 || devices[0].RetiredAt.Valid || devices[0].DeviceName != "test device" {
		t.Fatal("audit failure committed device mutation", devices, err)
	}
	var binding model.AgentBinding
	if err := f.db.First(&binding, "device_id=?", id).Error; err != nil || binding.CredentialID == nil {
		t.Fatal("audit failure committed revocation", err)
	}
	if err := f.db.First(&c, "id=?", c.ID).Error; err != nil || c.Status != "pending" {
		t.Fatal("audit failure committed cancellation", c.Status, err)
	}
	// Heartbeat itself writes no operation audit, so it can still authenticate.
	if _, err := agentauth.New(f.db).Beat(ctx, key, "test"); err != nil {
		t.Fatal("rolled-back credential unusable", err)
	}
}

func TestFRPDeviceRechecksOwnershipAfterLockWait(t *testing.T) {
	for _, action := range []string{"update", "retire"} {
		t.Run(action, func(t *testing.T) {
			f := newFixture(t)
			id := agentDevice(t, f)
			r := repo.NewFrpRepo(f.db)
			actor := contract.FrpActor{UserID: f.aliceID}
			holder := f.db.Begin()
			if holder.Error != nil {
				t.Fatal(holder.Error)
			}
			defer holder.Rollback()
			if err := holder.Exec("SELECT id FROM device WHERE id=? FOR UPDATE", id).Error; err != nil {
				t.Fatal(err)
			}
			hinted := make(chan struct{}, 1)
			callback := "test:device_ownership_hint"
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
			done := make(chan error, 1)
			go func() {
				var err error
				if action == "update" {
					_, err = r.UpdateDevice(context.Background(), actor, id, map[string]any{"device_name": "stale write"})
				} else {
					_, err = r.DeleteDevice(context.Background(), actor, id)
				}
				done <- err
			}()
			select {
			case <-hinted:
			case <-time.After(3 * time.Second):
				t.Fatal("no ownership hint")
			}
			if err := holder.Model(&model.Device{}).Where("id=?", id).Update("user_id", f.bobID).Error; err != nil {
				t.Fatal(err)
			}
			if err := holder.Commit().Error; err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if !errors.Is(err, contract.ErrForbidden) {
					t.Fatal("stale device mutation accepted", err)
				}
			case <-time.After(6 * time.Second):
				t.Fatal("device write stuck")
			}
			var d model.Device
			if err := f.db.First(&d, id).Error; err != nil || d.DeviceName != "test device" || d.UserID != f.bobID {
				t.Fatal("new owner device changed", d, err)
			}
		})
	}
}

func TestFRPDeviceFieldsAndRetirementFilter(t *testing.T) {
	f := newFixture(t)
	id := agentDevice(t, f)
	path := fmt.Sprintf("/api/frp/devices/%d", id)
	for _, payload := range []map[string]any{{"status": 2}, {"status": -1}, {"device_name": strings.Repeat("a", 101)}, {"device_ip": strings.Repeat("a", 51)}, {"device_name": "bad\nname"}, {}} {
		f.json(t, "PUT", path, f.alice, payload, 400)
	}
	for _, query := range []string{"retired=true", "retired=2"} {
		f.json(t, "GET", "/api/frp/devices?"+query, f.alice, nil, 400)
		f.json(t, "GET", path+"?"+query, f.alice, nil, 400)
	}
	f.json(t, "GET", path+"?retired=1", f.alice, nil, 404)
	f.json(t, "POST", "/api/frp/devices", f.alice, map[string]any{"device_name": strings.Repeat("a", 101)}, 400)
}

func TestFRPDeviceRetirementSchemaUpgradeAndTriggerDrift(t *testing.T) {
	f := newFixture(t)
	id := agentDevice(t, f)
	if err := f.db.Exec("ALTER TABLE device MODIFY COLUMN retired_at DATETIME(6) NULL DEFAULT CURRENT_TIMESTAMP(6)").Error; err != nil {
		t.Fatal(err)
	}
	if err := store.CheckAgentSchema(f.db); err == nil {
		t.Fatal("automatic retirement default accepted")
	}
	if err := f.db.Exec("ALTER TABLE device ALTER COLUMN retired_at DROP DEFAULT").Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec("DROP TRIGGER cn_agent_device_retire_v1").Error; err != nil {
		t.Fatal(err)
	}
	if err := store.CheckAgentSchema(f.db); err == nil {
		t.Fatal("missing retirement trigger accepted")
	}
	if err := f.db.Exec("DROP PROCEDURE cn_agent_trigger_check_v2").Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec("ALTER TABLE device DROP COLUMN retired_at").Error; err != nil {
		t.Fatal(err)
	}
	// Simulate the retained older routine: v2 installation must not overwrite it.
	if err := f.db.Exec("CREATE PROCEDURE cn_agent_trigger_check_v1(IN p_trigger_name VARCHAR(64)) READS SQL DATA SELECT p_trigger_name").Error; err != nil {
		t.Fatal(err)
	}
	defer f.db.Exec("DROP PROCEDURE cn_agent_trigger_check_v1")
	defer func() {
		if err := store.Migrate(f.db); err != nil {
			t.Error(err)
		}
	}()
	if err := store.CheckAgentSchema(f.db); err == nil {
		t.Fatal("legacy schema accepted at startup")
	}
	if err := store.Migrate(f.db); err != nil {
		t.Fatal(err)
	}
	var d model.Device
	if err := f.db.First(&d, id).Error; err != nil || d.RetiredAt.Valid {
		t.Fatal("upgrade retired historical active device", err)
	}
	var preserved string
	if err := f.db.Raw("SELECT routine_definition FROM information_schema.routines WHERE routine_schema=DATABASE() AND routine_name='cn_agent_trigger_check_v1'").Scan(&preserved).Error; err != nil || preserved != "SELECT p_trigger_name" {
		t.Fatal("old routine replaced", preserved, err)
	}
	if err := f.db.Exec("DROP TRIGGER cn_agent_device_retire_v1").Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec("CREATE TRIGGER cn_agent_device_retire_v1 AFTER UPDATE ON device FOR EACH ROW SET @retirement_probe=1").Error; err != nil {
		t.Fatal(err)
	}
	if err := store.CheckAgentSchema(f.db); err == nil {
		t.Fatal("same-name ineffective retirement trigger accepted")
	}
	if err := store.Migrate(f.db); err == nil {
		t.Fatal("altered trigger silently overwritten")
	}
	if err := f.db.Exec("DROP TRIGGER cn_agent_device_retire_v1").Error; err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(f.db); err != nil {
		t.Fatal(err)
	}
	// GORM soft deletion also hits the same trigger (not just HTTP retirement).
	key := commandIdentity(t, f, id)
	if err := f.db.Delete(&d).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := agentauth.New(f.db).Beat(context.Background(), key, ""); !errors.Is(err, agentauth.ErrDenied) {
		t.Fatal("direct soft deletion left credentials valid", err)
	}
	if err := store.Migrate(f.db); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Unscoped().First(&d, id).Error; err != nil || !d.RetiredAt.Valid {
		t.Fatal("repeat migration restored retired device", err)
	}
}

func TestFRPDeviceRetirementSerializesMappingCreation(t *testing.T) {
	f := newFixture(t)
	id := mappingDevice(t, f, f.aliceID)
	r := repo.NewFrpRepo(f.db)
	actor := contract.FrpActor{UserID: f.aliceID}
	start := make(chan struct{})
	retired := make(chan error, 1)
	mapped := make(chan error, 1)
	go func() { <-start; _, err := r.DeleteDevice(context.Background(), actor, id); retired <- err }()
	go func() {
		<-start
		_, err := r.CreateMapping(context.Background(), contract.FrpCreateMappingInput{FrpActor: actor, DeviceID: id, LocalPort: 8080, Status: 1})
		mapped <- err
	}()
	close(start)
	if err := <-retired; err != nil {
		t.Fatal(err)
	}
	createErr := <-mapped
	if createErr != nil && !errors.Is(createErr, gorm.ErrRecordNotFound) {
		t.Fatal(createErr)
	}
	var d model.Device
	if err := f.db.Unscoped().First(&d, id).Error; err != nil || !d.RetiredAt.Valid {
		t.Fatal("retired identity missing", err)
	}
	var count int64
	if err := f.db.Model(&model.FrpMapping{}).Where("device_id=?", id).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if (createErr == nil && count != 1) || (createErr != nil && count != 0) {
		t.Fatal("interleaved mapping was lost or inserted after retirement", createErr, count)
	}
}
