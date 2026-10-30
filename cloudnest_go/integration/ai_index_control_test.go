//go:build integration && aisearch

package integration

import (
	"bytes"
	"cloudenest_go/internal/indexqueue"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func indexStatusAPI(t *testing.T, f *fixture, token string, id int64) contract.AIIndexStatus {
	t.Helper()
	w := f.json(t, "GET", fmt.Sprintf("/api/ai/index/%d", id), token, nil, 200)
	for _, forbidden := range []string{"lease_token", "lease_until", "source_hash", "file_path", "vector"} {
		if strings.Contains(w.Body.String(), forbidden) {
			t.Fatal("private queue field exposed", forbidden)
		}
	}
	var reply struct {
		Item contract.AIIndexStatus `json:"item"`
	}
	decode(t, w, &reply)
	return reply.Item
}

func rebuildBody(items ...contract.AIRebuildItem) any { return map[string]any{"items": items} }

func TestAIIndexControlStatusACLAndRetryFencing(t *testing.T) {
	f, q := indexFixture(t)
	file := f.upload(t, f.alice, "control.md", []byte("state machine"))
	path := fmt.Sprintf("/api/ai/index/%d", file.ID)
	f.json(t, "GET", path, "", nil, 401)
	f.json(t, "GET", path, f.bob, nil, 404)
	j := claimIndex(t, q)
	s := indexStatusAPI(t, f, f.alice, file.ID)
	if s.Status != "running" || s.Attempts != 1 || !s.SourceCurrent || s.IndexedCurrent {
		t.Fatal(s)
	}
	if err := q.Fail(context.Background(), j, "model_unavailable", true); err != nil {
		t.Fatal(err)
	}
	s = indexStatusAPI(t, f, f.alice, file.ID)
	if s.Status != "retry" || s.ErrorCode != "model_unavailable" || s.AvailableAt == nil {
		t.Fatal(s)
	}
	if err := f.db.Create(&model.FilePermission{FileID: file.ID, UserID: f.bobID, Permission: "write"}).Error; err != nil {
		t.Fatal(err)
	}
	indexStatusAPI(t, f, f.bob, file.ID)
	body := rebuildBody(contract.AIRebuildItem{FileID: file.ID, Generation: j.Generation})
	f.json(t, "POST", "/api/ai/index/rebuild", f.bob, body, 404)
	f.json(t, "POST", "/api/ai/index/rebuild", f.alice, body, 202)
	f.json(t, "POST", "/api/ai/index/rebuild", f.alice, body, 409)
	s = indexStatusAPI(t, f, f.alice, file.ID)
	if s.Status != "queued" || s.Attempts != 0 || s.Generation != 2 || s.ErrorCode != "" {
		t.Fatal(s)
	}
	if err := q.Publish(context.Background(), j, func(*gorm.DB) error { return nil }); !errors.Is(err, indexqueue.ErrLostLease) {
		t.Fatal("stale publisher", err)
	}
	if err := f.db.Where("file_id=?", file.ID).Delete(&model.FilePermission{}).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "GET", path, f.bob, nil, 404)
	var count int64
	if err := f.db.Model(&model.OperationLog{}).Where("file_id=? AND action='ai_rebuild'", file.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("replayed audit", count, err)
	}
}

func TestAIIndexControlHistoricalCursorAndAtomicOwnership(t *testing.T) {
	f, _ := indexFixture(t)
	a := f.upload(t, f.alice, "historical.md", []byte("history"))
	b := f.upload(t, f.alice, "second.md", []byte("second"))
	foreign := f.upload(t, f.bob, "foreign.md", []byte("private"))
	if err := f.db.Where("file_id=?", a.ID).Delete(&model.AIIndexJob{}).Error; err != nil {
		t.Fatal(err)
	}
	var page struct {
		Items []contract.AIIndexStatus `json:"items"`
		Next  int64                    `json:"next_after_id"`
		More  bool                     `json:"has_more"`
	}
	decode(t, f.json(t, "GET", "/api/ai/index?limit=1", f.alice, nil, 200), &page)
	if len(page.Items) != 1 || page.Items[0].FileID != a.ID || page.Items[0].Status != "not_indexed" || page.Items[0].Generation != 0 || page.Next != a.ID || !page.More {
		t.Fatal(page)
	}
	decode(t, f.json(t, "GET", fmt.Sprintf("/api/ai/index?limit=1&after_id=%d", page.Next), f.alice, nil, 200), &page)
	if len(page.Items) != 1 || page.Items[0].FileID != b.ID || page.More {
		t.Fatal("foreign row/cursor", page)
	}
	f.json(t, "POST", "/api/ai/index/rebuild", f.alice, rebuildBody(contract.AIRebuildItem{FileID: a.ID}, contract.AIRebuildItem{FileID: foreign.ID, Generation: 1}), 404)
	if indexStatusAPI(t, f, f.alice, a.ID).Generation != 0 {
		t.Fatal("partial unauthorized batch")
	}
	f.json(t, "POST", "/api/ai/index/rebuild", f.alice, rebuildBody(contract.AIRebuildItem{FileID: a.ID}, contract.AIRebuildItem{FileID: b.ID, Generation: 0}), 409)
	if indexStatusAPI(t, f, f.alice, a.ID).Generation != 0 {
		t.Fatal("partial conflicting batch")
	}
	// Reverse order input is normalized before acquiring database locks.
	f.json(t, "POST", "/api/ai/index/rebuild", f.alice, rebuildBody(contract.AIRebuildItem{FileID: b.ID, Generation: 1}, contract.AIRebuildItem{FileID: a.ID}), 202)
	if readIndex(t, f, a.ID).Generation != 1 || readIndex(t, f, b.ID).Generation != 2 {
		t.Fatal("historical batch not queued")
	}
}

func TestAIIndexControlConcurrentGenerationCAS(t *testing.T) {
	f, _ := indexFixture(t)
	file := f.upload(t, f.alice, "concurrent.md", []byte("single winning rebuild"))
	raw, err := json.Marshal(rebuildBody(contract.AIRebuildItem{FileID: file.ID, Generation: 1}))
	if err != nil {
		t.Fatal(err)
	}
	codes := make(chan int, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("POST", "/api/ai/index/rebuild", bytes.NewReader(raw))
			req.Header.Set("Authorization", "Bearer "+f.alice)
			req.Header.Set("Content-Type", "application/json")
			req.RemoteAddr = f.remoteAddr
			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)
			codes <- w.Code
		}()
	}
	wg.Wait()
	close(codes)
	winners := 0
	for code := range codes {
		if code == 202 {
			winners++
		} else if code != 409 {
			t.Fatal("unexpected concurrent response", code)
		}
	}
	if winners != 1 || readIndex(t, f, file.ID).Generation != 2 {
		t.Fatal("CAS restarted work repeatedly", winners)
	}
}

func TestAIIndexControlAuditFailureRollsBackWholeBatch(t *testing.T) {
	f, _ := indexFixture(t)
	a := f.upload(t, f.alice, "atomic-a.md", []byte("one"))
	b := f.upload(t, f.alice, "atomic-b.md", []byte("two"))
	const hook = "test:rebuild-audit-failure"
	if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "operation_log" {
			tx.AddError(errors.New("injected audit failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Callback().Create().Remove(hook) })
	f.json(t, "POST", "/api/ai/index/rebuild", f.alice, rebuildBody(contract.AIRebuildItem{FileID: a.ID, Generation: 1}, contract.AIRebuildItem{FileID: b.ID, Generation: 1}), 503)
	if readIndex(t, f, a.ID).Generation != 1 || readIndex(t, f, b.ID).Generation != 1 {
		t.Fatal("audit failure committed work")
	}
}

func TestAIIndexControlBoundsAndCurrentSourceDiagnostics(t *testing.T) {
	f, _ := indexFixture(t)
	file := f.upload(t, f.alice, "indexed.md", []byte("original source"))
	lexicalIndexFixture(t, f, file, "original source")
	s := indexStatusAPI(t, f, f.alice, file.ID)
	if !s.IndexedCurrent || s.ChunkCount != 1 || s.IndexedAt == nil {
		t.Fatal(s)
	}
	other := f.upload(t, f.alice, "replacement.md", []byte("replacement source"))
	if err := f.db.Model(&model.File{}).Where("id=?", file.ID).Update("storage_id", other.StorageID).Error; err != nil {
		t.Fatal(err)
	}
	s = indexStatusAPI(t, f, f.alice, file.ID)
	if s.SourceCurrent || s.IndexedCurrent || s.ChunkCount != 0 || s.IndexedAt != nil {
		t.Fatal("stale source diagnosed as current", s)
	}
	for _, path := range []string{"/api/ai/index?after_id=-1", "/api/ai/index?limit=101", "/api/ai/index?limit=0", "/api/ai/index?limit=bad", "/api/ai/index/0", "/api/ai/index/nope"} {
		f.json(t, "GET", path, f.alice, nil, 400)
	}
	for _, items := range [][]contract.AIRebuildItem{nil, {{FileID: file.ID, Generation: -1}}, {{FileID: file.ID}, {FileID: file.ID}}, make([]contract.AIRebuildItem, 101)} {
		f.json(t, "POST", "/api/ai/index/rebuild", f.alice, rebuildBody(items...), 400)
	}
	f.json(t, "POST", "/api/ai/index/rebuild", f.alice, map[string]any{"items": []any{}, "padding": strings.Repeat("x", 20<<10)}, 400)
	if err := f.db.Model(&model.File{}).Where("id=?", file.ID).Update("is_deleted", 1).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "GET", fmt.Sprintf("/api/ai/index/%d", file.ID), f.alice, nil, 404)
	f.json(t, "POST", "/api/ai/index/rebuild", f.alice, rebuildBody(contract.AIRebuildItem{FileID: file.ID, Generation: 1}), 404)
}
