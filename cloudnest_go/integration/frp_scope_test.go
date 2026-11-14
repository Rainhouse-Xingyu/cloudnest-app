//go:build integration

package integration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"cloudenest_go/internal/agentauth"
	"cloudenest_go/internal/agentcommand"
	"cloudenest_go/internal/frpconfig"
	"cloudenest_go/internal/frpwire"
	"cloudenest_go/internal/service"
	"cloudenest_go/internal/service/contract"
	repo "cloudenest_go/internal/storage/mysql"
	"cloudenest_go/internal/store"
	"cloudenest_go/model"
)

func scopedFixture(t *testing.T) (*fixture, *repo.FrpRepo, []int64, []contract.FrpServerView) {
	t.Helper()
	f := newFixture(t)
	f.cfg.FRPKeyFile, f.cfg.FRPKeys = frpTestKeys(t, "one", map[string][]byte{"one": bytes.Repeat([]byte{1}, 32)})
	r := repo.NewFrpRepo(f.db, f.cfg.FRPKeys)
	devices := []int64{mappingDevice(t, f, f.aliceID), mappingDevice(t, f, f.aliceID), mappingDevice(t, f, f.bobID)}
	servers := []contract.FrpServerView{scopedServer(t, f, r, f.aliceID, 1), scopedServer(t, f, r, f.aliceID, 2)}
	t.Cleanup(func() {
		if err := f.db.Where("device_id IN ?", devices).Delete(&model.FrpConfig{}).Error; err != nil {
			t.Error(err)
		}
	})
	return f, r, devices, servers
}
func scopedServer(t *testing.T, f *fixture, r *repo.FrpRepo, owner int64, n int) contract.FrpServerView {
	t.Helper()
	secret := "dedicated-scope-fixture-token"
	s, err := r.WriteServer(context.Background(), contract.FrpActor{UserID: owner}, 0, contract.FrpServerInput{Spec: frpconfig.ServerSpec{Name: "scope fixture", Address: fmt.Sprintf("scope-%d-%d.example", f.aliceID, n), TCPStart: 32000, TCPEnd: 32002, HTTPPort: 18080, HTTPSPort: 18443}, Token: &secret})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.db.Where("server_id=?", s.ID).Delete(&model.FrpServerRevision{}).Error; err != nil {
			t.Error(err)
		}
		if err := f.db.Delete(&model.FrpServer{}, s.ID).Error; err != nil {
			t.Error(err)
		}
	})
	return s
}
func TestFRPScopedMappingsSeparateServersAndKeepLegacyOverlap(t *testing.T) {
	f, r, devices, servers := scopedFixture(t)
	ctx := context.Background()
	actor := contract.FrpActor{UserID: f.aliceID}
	create := func(server, device int64, port *int, domain string) (model.FrpMapping, error) {
		return r.CreateMapping(ctx, contract.FrpCreateMappingInput{FrpActor: actor, ServerID: server, DeviceID: device, LocalPort: 8080, RemotePort: port, Domain: domain, Status: 1})
	}
	first, err := create(servers[0].ID, devices[0], nil, "")
	if err != nil || first.RemotePort != 32000 {
		t.Fatal("server pool start", first.RemotePort, err)
	}
	second, err := create(servers[1].ID, devices[0], nil, "")
	if err != nil || second.RemotePort != first.RemotePort {
		t.Fatal("independent server could not reuse port", err)
	}
	if _, err := create(servers[0].ID, devices[1], &first.RemotePort, ""); !errors.Is(err, contract.ErrFrpMappingConflict) {
		t.Fatal("same-server duplicate accepted", err)
	}
	// Same-device exclusion is valid for runtime replacement, not new mappings.
	if _, err := create(0, devices[0], &first.RemotePort, ""); !errors.Is(err, contract.ErrFrpMappingConflict) {
		t.Fatal("legacy mapping overlapped own managed mapping", err)
	}
	domain := fmt.Sprintf("scope-%d.example", devices[0])
	for _, server := range servers {
		mapped, err := create(server.ID, devices[0], nil, "HTTP://"+strings.ToUpper(domain)+".")
		if err != nil || mapped.RemotePort != 0 {
			t.Fatal("managed HTTP consumed TCP resource", err)
		}
	}
	if _, err := create(servers[0].ID, devices[1], nil, domain); !errors.Is(err, contract.ErrFrpMappingConflict) {
		t.Fatal("canonical same-server domain duplicate", err)
	}
	if _, err := create(0, devices[1], nil, domain); !errors.Is(err, contract.ErrFrpMappingConflict) {
		t.Fatal("global domain missed managed reservation", err)
	}
	if mapped, err := create(servers[0].ID, devices[0], nil, "second-"+domain); err != nil || mapped.RemotePort != 0 {
		t.Fatal("two HTTP mappings cannot share the non-port zero", err)
	}
	legacyPort := 32001
	legacyDomain := "global-" + domain
	if _, err := create(0, devices[0], &legacyPort, legacyDomain); err != nil {
		t.Fatal(err)
	}
	for _, server := range servers {
		if _, err := create(server.ID, devices[1], &legacyPort, ""); !errors.Is(err, contract.ErrFrpMappingConflict) {
			t.Fatal("legacy HTTP's historical TCP reservation was lost", err)
		}
		if _, err := create(server.ID, devices[1], nil, legacyDomain); !errors.Is(err, contract.ErrFrpMappingConflict) {
			t.Fatal("managed domain bypassed legacy global mapping", err)
		}
		mapped, err := create(server.ID, devices[1], nil, "")
		if err != nil || mapped.RemotePort != 32002 {
			t.Fatal("allocator ignored shared legacy resource", mapped.RemotePort, err)
		}
		if _, err := create(server.ID, devices[1], nil, ""); !errors.Is(err, contract.ErrFrpPortExhausted) {
			t.Fatal("server-specific pool did not exhaust", err)
		}
	}
	// Config generation selects only legacy plus the explicitly selected server.
	cfg, err := service.NewFrpService(r, f.rdb).SaveConfig(ctx, f.aliceID, false, service.SaveConfigInput{DeviceID: devices[0], ServerID: servers[0].ID, ServerRevisionID: servers[0].RevisionID})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := f.cfg.FRPKeys.OpenConfig(devices[0], cfg.ID, cfg.ServerID, cfg.ServerRevisionID, cfg.ConfigText)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := frpwire.Decode([]byte(plain))
	if err != nil || len(parsed.Proxies) != 4 {
		t.Fatal("managed config included other server mappings", len(parsed.Proxies), err)
	}
	legacy, err := service.NewFrpService(r, f.rdb).SaveConfig(ctx, f.aliceID, false, service.SaveConfigInput{DeviceID: devices[0], ServerAddr: "legacy.example", ServerPort: 7000})
	if err != nil {
		t.Fatal(err)
	}
	plain, err = f.cfg.FRPKeys.OpenConfig(devices[0], legacy.ID, 0, 0, legacy.ConfigText)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err = frpwire.Decode([]byte(plain))
	if err != nil || len(parsed.Proxies) != 1 {
		t.Fatal("raw legacy generator included managed mappings", len(parsed.Proxies), err)
	}
}

func TestFRPScopedMappingPolicyOwnershipAndTransitions(t *testing.T) {
	f, r, devices, servers := scopedFixture(t)
	ctx := context.Background()
	actor := contract.FrpActor{UserID: f.aliceID}
	server := servers[0]
	makeInput := func() contract.FrpCreateMappingInput {
		return contract.FrpCreateMappingInput{FrpActor: actor, ServerID: server.ID, DeviceID: devices[0], LocalPort: 8080, Status: 1}
	}
	bad := makeInput()
	bad.DeviceID = devices[2]
	bad.Admin = true
	if _, err := r.CreateMapping(ctx, bad); !errors.Is(err, contract.ErrForbidden) {
		t.Fatal("admin mixed device and server owners", err)
	}
	outside := 31999
	bad = makeInput()
	bad.RemotePort = &outside
	if _, err := r.CreateMapping(ctx, bad); !errors.Is(err, contract.ErrConflict) {
		t.Fatal("out-of-policy port accepted", err)
	}
	for _, sid := range []int64{-1, server.ID} {
		body := map[string]any{"device_id": devices[0], "server_id": sid, "local_port": 8080, "remote_port": 0}
		f.json(t, "POST", "/api/frp/mappings", f.alice, body, 400)
	}
	input := makeInput()
	input.Domain = fmt.Sprintf("transition-%d.example", devices[0])
	mapping, err := r.CreateMapping(ctx, input)
	if err != nil || mapping.RemotePort != 0 {
		t.Fatal(err)
	}
	f.json(t, "PUT", fmt.Sprintf("/api/frp/mappings/%d", mapping.ID), f.alice, map[string]any{"server_id": servers[1].ID, "local_port": 8081}, 400)
	empty := ""
	mapping, err = r.UpdateMapping(ctx, contract.FrpUpdateMappingInput{FrpActor: actor, MappingID: mapping.ID, Domain: &empty})
	if err != nil || mapping.RemotePort != 32000 {
		t.Fatal("HTTP to TCP did not allocate current policy", mapping.RemotePort, err)
	}
	domain := "https://" + input.Domain
	mapping, err = r.UpdateMapping(ctx, contract.FrpUpdateMappingInput{FrpActor: actor, MappingID: mapping.ID, Domain: &domain})
	if err != nil || mapping.RemotePort != 0 {
		t.Fatal("TCP to HTTPS retained unused port", err)
	}
	secret := "dedicated-scope-fixture-token"
	policy := contract.FrpServerInput{ExpectedRevisionID: server.RevisionID, Spec: frpconfig.ServerSpec{Name: server.Name, Address: server.Address, Port: server.Port, TCPStart: 32001, TCPEnd: 32002}, Token: &secret}
	server, err = r.WriteServer(ctx, actor, server.ID, policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateMapping(ctx, input); !errors.Is(err, contract.ErrConflict) {
		t.Fatal("HTTP without vhost listener accepted", err)
	}
	input.Domain = ""
	mapped, err := r.CreateMapping(ctx, input)
	if err != nil || mapped.RemotePort != 32001 {
		t.Fatal("allocation ignored latest policy revision", err)
	}
	off := false
	policy.ExpectedRevisionID = server.RevisionID
	policy.Enabled = &off
	if _, err := r.WriteServer(ctx, actor, server.ID, policy); err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", "/api/frp/mappings", f.alice, map[string]any{"device_id": devices[0], "server_id": server.ID, "local_port": 8080}, 409)
}

func TestFRPScopedRuntimeClaimsReleaseOnlySelectedAgent(t *testing.T) {
	f, r, devices, servers := scopedFixture(t)
	ctx := context.Background()
	actor := contract.FrpActor{UserID: f.aliceID}
	m := agentcommand.New(f.db, f.cfg.FRPKeys)
	keys := []string{commandIdentity(t, f, devices[0]), commandIdentity(t, f, devices[1])}
	var mappings []model.FrpMapping
	for i, server := range servers {
		mapping, err := r.CreateMapping(ctx, contract.FrpCreateMappingInput{FrpActor: actor, ServerID: server.ID, DeviceID: devices[i], LocalPort: 8080, Status: 1})
		if err != nil {
			t.Fatal(err)
		}
		mappings = append(mappings, mapping)
		cfg, err := service.NewFrpService(r, f.rdb).SaveConfig(ctx, f.aliceID, false, service.SaveConfigInput{DeviceID: devices[i], ServerID: server.ID, ServerRevisionID: server.RevisionID})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.Enqueue(ctx, f.aliceID, false, devices[i], agentcommand.EnqueueInput{RequestID: strings.Repeat("a", 32), Action: "deploy_config", ConfigID: cfg.ID}, ""); err != nil {
			t.Fatal("separate server runtime conflict", err)
		}
		finishClaim(t, m, keys[i], i+1)
		if _, err := r.DeleteMapping(ctx, actor, mapping.ID); err != nil {
			t.Fatal(err)
		}
	}
	if mappings[0].RemotePort != mappings[1].RemotePort {
		t.Fatal("fixture did not reuse same port")
	}
	port := mappings[0].RemotePort
	for i, server := range servers {
		_, err := r.CreateMapping(ctx, contract.FrpCreateMappingInput{FrpActor: actor, ServerID: server.ID, DeviceID: devices[1-i], LocalPort: 8080, RemotePort: &port, Status: 1})
		if !errors.Is(err, contract.ErrFrpMappingConflict) {
			t.Fatal("deleted scoped mapping freed other Agent runtime", err)
		}
	}
	if _, err := agentauth.New(f.db).Beat(ctx, keys[0], "cloudnest-agent-0.3.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Enqueue(ctx, f.aliceID, false, devices[0], agentcommand.EnqueueInput{RequestID: strings.Repeat("b", 32), Action: "forget_config"}, ""); err != nil {
		t.Fatal(err)
	}
	finishClaim(t, m, keys[0], 3)
	if _, err := r.CreateMapping(ctx, contract.FrpCreateMappingInput{FrpActor: actor, ServerID: servers[0].ID, DeviceID: devices[1], LocalPort: 8080, RemotePort: &port, Status: 1}); err != nil {
		t.Fatal("confirmed server resource not released", err)
	}
	if _, err := r.CreateMapping(ctx, contract.FrpCreateMappingInput{FrpActor: actor, ServerID: servers[1].ID, DeviceID: devices[0], LocalPort: 8080, RemotePort: &port, Status: 1}); !errors.Is(err, contract.ErrFrpMappingConflict) {
		t.Fatal("forget on another Agent/server released live resource", err)
	}
}

func TestFRPScopedConcurrentLegacyAndManagedAllocation(t *testing.T) {
	f, r, devices, servers := scopedFixture(t)
	ctx := context.Background()
	for remote := 32000; remote <= 32002; remote++ {
		requests := []contract.FrpCreateMappingInput{
			{FrpActor: contract.FrpActor{UserID: f.aliceID}, DeviceID: devices[0], ServerID: servers[0].ID, LocalPort: 8080, RemotePort: &remote, Status: 1},
			{FrpActor: contract.FrpActor{UserID: f.aliceID}, DeviceID: devices[1], ServerID: servers[1].ID, LocalPort: 8081, RemotePort: &remote, Status: 1},
			{FrpActor: contract.FrpActor{UserID: f.bobID}, DeviceID: devices[2], LocalPort: 8082, RemotePort: &remote, Status: 1},
		}
		type result struct {
			mapping model.FrpMapping
			actor   contract.FrpActor
			err     error
		}
		results := make(chan result, len(requests))
		start := make(chan struct{})
		var workers sync.WaitGroup
		for _, request := range requests {
			workers.Add(1)
			go func(request contract.FrpCreateMappingInput) {
				defer workers.Done()
				<-start
				mapping, err := r.CreateMapping(ctx, request)
				results <- result{mapping, request.FrpActor, err}
			}(request)
		}
		close(start)
		workers.Wait()
		close(results)
		global, scoped := 0, 0
		for result := range results {
			if result.err != nil {
				if !errors.Is(result.err, contract.ErrFrpMappingConflict) {
					t.Fatal(result.err)
				}
				continue
			}
			if result.mapping.ServerID == 0 {
				global++
			} else {
				scoped++
			}
			if _, err := r.DeleteMapping(ctx, result.actor, result.mapping.ID); err != nil {
				t.Fatal(err)
			}
		}
		if !((global == 1 && scoped == 0) || (global == 0 && scoped == 2)) {
			t.Fatal("legacy and managed namespaces overlapped during allocation", global, scoped)
		}
	}
}

// Exercise an actual pre-server schema, preserving row identity and legacy
// spelling, then reject drift of the new composite unique constraint.
func TestFRPScopedMigrationPreservesLegacyAndRejectsDrift(t *testing.T) {
	f := newFixture(t)
	device := mappingDevice(t, f, f.aliceID)
	original := model.FrpMapping{DeviceID: device, LocalPort: 8080, RemotePort: 32100, Domain: fmt.Sprintf("HTTP://HISTORY-%d.EXAMPLE.", device), Status: 0}
	if err := f.db.Create(&original).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.First(&original, original.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec("ALTER TABLE frp_mapping DROP INDEX uq_frp_port_v2, DROP INDEX uq_frp_domain_v2, DROP COLUMN cn_port_key_v2, DROP COLUMN server_id, ADD UNIQUE INDEX uni_frp_mapping_remote_port(remote_port), ADD UNIQUE INDEX uq_frp_domain_v1(cn_domain_key_v1)").Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Migrate(f.db); err != nil {
			t.Error(err)
		}
	}()
	if err := store.CheckMappingSchema(f.db); err == nil {
		t.Fatal("legacy structure accepted as scoped")
	}
	for i := 0; i < 2; i++ {
		if err := store.Migrate(f.db); err != nil {
			t.Fatal("explicit upgrade", err)
		}
		var got model.FrpMapping
		if err := f.db.First(&got, original.ID).Error; err != nil {
			t.Fatal(err)
		}
		if got.ID != original.ID || got.DeviceID != original.DeviceID || got.ServerID != 0 || got.LocalPort != original.LocalPort || got.RemotePort != original.RemotePort || got.Domain != original.Domain || got.Status != original.Status || !got.CreateTime.Equal(original.CreateTime) {
			t.Fatalf("legacy mapping changed: got %+v want %+v", got, original)
		}
	}
	if err := f.db.Exec("ALTER TABLE frp_mapping DROP INDEX uq_frp_port_v2, ADD INDEX uq_frp_port_v2(server_id,cn_port_key_v2)").Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := f.db.Exec("ALTER TABLE frp_mapping DROP INDEX uq_frp_port_v2, ADD UNIQUE INDEX uq_frp_port_v2(server_id,cn_port_key_v2)").Error; err != nil {
			t.Error(err)
		}
	}()
	if err := store.CheckMappingSchema(f.db); err == nil {
		t.Fatal("nonunique scoped port index accepted")
	}
	if err := store.Migrate(f.db); err == nil {
		t.Fatal("migration silently repaired unrecognized index drift")
	}
}
