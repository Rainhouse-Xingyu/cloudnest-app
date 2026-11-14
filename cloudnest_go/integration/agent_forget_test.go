//go:build integration

package integration

import (
	"cloudenest_go/internal/agentauth"
	"cloudenest_go/internal/agentcommand"
	"cloudenest_go/model"
	"context"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAgentForgetRequiresFreshCapabilityAndExclusiveQueue(t *testing.T) {
	f := newFixture(t)
	device := agentDevice(t, f)
	key := commandIdentity(t, f, device)
	ctx := context.Background()
	m := agentcommand.New(f.db, nil)
	auth := agentauth.New(f.db)
	input := agentcommand.EnqueueInput{RequestID: strings.Repeat("a", 32), Action: "forget_config"}
	if _, err := m.Enqueue(ctx, f.aliceID, false, device, input, ""); !errors.Is(err, agentcommand.ErrConflict) {
		t.Fatal("unknown capability accepted", err)
	}
	for _, version := range []string{"cloudnest-agent-0.2.0", "cloudnest-agent-0.30.0", "cloudnest-agent-0.3.0-extra"} {
		if _, err := auth.Beat(ctx, key, version); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Enqueue(ctx, f.aliceID, false, device, input, ""); !errors.Is(err, agentcommand.ErrConflict) {
			t.Fatal("unsupported capability accepted", err)
		}
	}
	if _, err := auth.Beat(ctx, key, "cloudnest-agent-0.3.0"); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.AgentBinding{}).Where("device_id=?", device).Update("last_seen_at", time.Now().UTC().Add(-2*time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := m.Enqueue(ctx, f.aliceID, false, device, input, ""); !errors.Is(err, agentcommand.ErrConflict) {
		t.Fatal("stale capability accepted", err)
	}
	if _, err := auth.Beat(ctx, key, "cloudnest-agent-0.3.0"); err != nil {
		t.Fatal(err)
	}
	prior, err := m.Enqueue(ctx, f.aliceID, false, device, agentcommand.EnqueueInput{RequestID: strings.Repeat("b", 32), Action: "stop"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Enqueue(ctx, f.aliceID, false, device, input, ""); !errors.Is(err, agentcommand.ErrConflict) {
		t.Fatal("forgot over pending command", err)
	}
	delivery, err := m.Claim(ctx, key, strings.Repeat("c", 32))
	if err != nil || delivery == nil || delivery.Command.ID != prior.ID {
		t.Fatal(err)
	}
	if err := m.Finish(ctx, key, prior.ID, delivery.LeaseID, true, "ok"); err != nil {
		t.Fatal(err)
	}
	forgotten, err := m.Enqueue(ctx, f.aliceID, false, device, input, "")
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := m.Enqueue(ctx, f.aliceID, false, device, input, "")
	if err != nil || duplicate.ID != forgotten.ID {
		t.Fatal("non-idempotent forget", err)
	}
	for _, action := range []string{"start", "reload", "stop", "diagnose", "forget_config"} {
		if _, err := m.Enqueue(ctx, f.aliceID, false, device, agentcommand.EnqueueInput{RequestID: strings.Repeat("d", 32), Action: action}, ""); !errors.Is(err, agentcommand.ErrConflict) {
			t.Fatal("crossed pending forget", action, err)
		}
	}
	delivery, err = m.Claim(ctx, key, strings.Repeat("e", 32))
	if err != nil || delivery == nil || delivery.Command.ID != forgotten.ID {
		t.Fatal(err)
	}
	if _, err := m.Enqueue(ctx, f.aliceID, false, device, agentcommand.EnqueueInput{RequestID: strings.Repeat("f", 32), Action: "start"}, ""); !errors.Is(err, agentcommand.ErrConflict) {
		t.Fatal("crossed leased forget", err)
	}
	if err := m.Finish(ctx, key, forgotten.ID, delivery.LeaseID, true, "ok"); err != nil {
		t.Fatal(err)
	}
	if err := m.Finish(ctx, key, forgotten.ID, delivery.LeaseID, true, "ok"); err != nil {
		t.Fatal("duplicate receipt", err)
	}
	var audits int64
	if err := f.db.Model(&model.OperationLog{}).Where("action='agent.command.succeeded' AND detail=?", "command_id="+forgotten.ID).Count(&audits).Error; err != nil || audits != 1 {
		t.Fatal("receipt audit duplicated", audits, err)
	}
	if _, err := m.Enqueue(ctx, f.aliceID, false, device, agentcommand.EnqueueInput{RequestID: strings.Repeat("f", 32), Action: "diagnose"}, ""); err != nil {
		t.Fatal("barrier not cleared", err)
	}
}

func TestAgentForgetConcurrentEnqueueAndAuditFailure(t *testing.T) {
	f := newFixture(t)
	device := agentDevice(t, f)
	key := commandIdentity(t, f, device)
	ctx := context.Background()
	if _, err := agentauth.New(f.db).Beat(ctx, key, "cloudnest-agent-0.3.0"); err != nil {
		t.Fatal(err)
	}
	m := agentcommand.New(f.db, nil)
	hook := "test:forget_audit_failure"
	if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if log, ok := tx.Statement.Dest.(*model.OperationLog); ok && log.Action == "agent.command.queued" {
			tx.AddError(errors.New("injected failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	_, err := m.Enqueue(ctx, f.aliceID, false, device, agentcommand.EnqueueInput{RequestID: strings.Repeat("a", 32), Action: "forget_config"}, "")
	f.db.Callback().Create().Remove(hook)
	if err == nil {
		t.Fatal("audit failure committed barrier")
	}
	const workers = 12
	type result struct {
		row model.AgentCommand
		err error
	}
	results := make(chan result, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			row, err := m.Enqueue(ctx, f.aliceID, false, device, agentcommand.EnqueueInput{RequestID: fmt.Sprintf("%032x", i+1), Action: "forget_config"}, "")
			results <- result{row, err}
		}(i)
	}
	wg.Wait()
	close(results)
	wins := 0
	for got := range results {
		if got.err == nil {
			wins++
		} else if !errors.Is(got.err, agentcommand.ErrConflict) {
			t.Fatal(got.err)
		}
	}
	if wins != 1 {
		t.Fatal("exclusive barrier winners", wins)
	}
	var count int64
	if err := f.db.Model(&model.AgentCommand{}).Where("device_id=?", device).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("failed audit retained row", count, err)
	}
}
