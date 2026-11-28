//go:build integration && aisearch

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cloudenest_go/internal/app"
	"cloudenest_go/model"
	"gorm.io/gorm"
)

// Synthetic model output pauses publication; real recognition is tested in the
// opt-in model suite. Version/recycle use production APIs; private fixture
// grants are revoked directly in SQL, as in the existing OCR race test.
func TestAISummaryRechecksSourceAndRevokedGrant(t *testing.T) {
	for _, mode := range []string{"replace", "revoke", "recycle"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := indexFixture(t)
			f.cfg.AIOllamaModel = "fixture:latest"
			f.cfg.AITimeoutSec = 15
			entered, release := make(chan struct{}), make(chan struct{})
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/tags" {
					json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]string{"name": f.cfg.AIOllamaModel, "digest": strings.Repeat("a", 64)}}})
					return
				}
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"model": f.cfg.AIOllamaModel, "done": true, "done_reason": "stop", "response": "synthetic paused summary"})
			}))
			defer provider.Close()
			f.cfg.AIOllamaBaseURL = provider.URL
			f.router = fixtureRouter(app.NewContainer(f.cfg, f.db, f.rdb).NewRouter())
			file := f.upload(t, f.alice, "race-summary.txt", []byte("Original backup and migration source."))
			token, want := f.alice, 409
			if mode == "revoke" {
				token, want = f.bob, 403
				if err := f.db.Create(&model.FilePermission{FileID: file.ID, UserID: f.bobID, Permission: "read"}).Error; err != nil {
					t.Fatal(err)
				}
			}
			req := httptest.NewRequest("POST", "/api/ai/summarize", strings.NewReader(fmt.Sprintf(`{"file_id":%d}`, file.ID))).WithContext(ctx)
			req.RemoteAddr = f.remoteAddr
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			result := make(chan *httptest.ResponseRecorder, 1)
			go func() { w := httptest.NewRecorder(); f.router.ServeHTTP(w, req); result <- w }()
			select {
			case <-entered:
			case w := <-result:
				t.Fatalf("summary exited before provider: %d", w.Code)
			case <-ctx.Done():
				t.Fatal("provider not reached")
			}
			switch mode {
			case "replace":
				other := f.upload(t, f.alice, "new-summary.txt", []byte("Different source bytes."))
				f.json(t, "POST", fmt.Sprintf("/api/files/%d/versions", file.ID), f.alice, map[string]any{"storage_id": other.StorageID, "base_version": 1}, 200)
			case "revoke":
				if err := f.db.Where("file_id=? AND user_id=?", file.ID, f.bobID).Delete(&model.FilePermission{}).Error; err != nil {
					t.Fatal(err)
				}
			case "recycle":
				f.json(t, "DELETE", fmt.Sprintf("/api/files/%d", file.ID), f.alice, nil, 200)
			}
			close(release)
			select {
			case w := <-result:
				if w.Code != want {
					t.Error("stale summary accepted", w.Code)
				}
			case <-ctx.Done():
				t.Fatal("summary did not finish")
			}
			for _, table := range []any{&model.AiMetadata{}, &model.AiTask{}} {
				var count int64
				q := f.db.Model(table).Where("file_id=?", file.ID)
				if _, ok := table.(*model.AiTask); ok {
					q = q.Where("status='success'")
				}
				if err := q.Count(&count).Error; err != nil || count != 0 {
					t.Error("stale result published", count, err)
				}
			}
			waitNoReadLeases(t, f, *file.StorageID)
		})
	}
}

func TestAIGenerationHTTPBodiesAreBoundedBeforeProvider(t *testing.T) {
	f, _ := indexFixture(t)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("oversized request reached provider")
		w.WriteHeader(503)
	}))
	defer provider.Close()
	f.cfg.AIOllamaBaseURL = provider.URL
	f.router = fixtureRouter(app.NewContainer(f.cfg, f.db, f.rdb).NewRouter())
	f.json(t, "POST", "/api/ai/summarize", f.alice, map[string]any{"file_id": 1, "padding": strings.Repeat("x", 20<<10)}, 400)
	f.json(t, "POST", "/api/ai/assistant/ask", f.alice, map[string]any{"question": strings.Repeat("x", 20<<10)}, 400)
}

func TestAISummaryVerifiedSourceAtomicHistoryAndPrivateFailures(t *testing.T) {
	for _, mode := range []string{"success", "audit-failure", "corrupt-source", "provider-error"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := indexFixture(t)
			f.cfg.AIOllamaModel = "fixture:latest"
			var calls atomic.Int32
			const marker = "private-generated-error-text"
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/tags" {
					json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]string{"name": f.cfg.AIOllamaModel, "digest": strings.Repeat("a", 64)}}})
					return
				}
				calls.Add(1)
				if mode == "provider-error" {
					w.WriteHeader(503)
					w.Write([]byte(marker))
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"model": f.cfg.AIOllamaModel, "done": true, "done_reason": "stop", "response": "synthetic verified summary"})
			}))
			defer provider.Close()
			f.cfg.AIOllamaBaseURL = provider.URL
			f.router = fixtureRouter(app.NewContainer(f.cfg, f.db, f.rdb).NewRouter())
			file := f.upload(t, f.alice, "verified-summary.txt", []byte("Verified source body."))
			var storage model.Storage
			if err := f.db.First(&storage, *file.StorageID).Error; err != nil {
				t.Fatal(err)
			}
			want := 200
			if mode == "audit-failure" {
				want = 500
				hook := "test:summary_audit_failure"
				if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
					if tx.Statement.Table == "operation_log" {
						tx.AddError(errors.New("injected summary audit failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { f.db.Callback().Create().Remove(hook) })
			}
			if mode == "corrupt-source" {
				want = 409
				if err := os.WriteFile(storage.FilePath, []byte("Corrupt source body.."), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "provider-error" {
				want = 502
			}
			response := f.json(t, "POST", "/api/ai/summarize", f.alice, map[string]any{"file_id": file.ID}, want)
			if strings.Contains(response.Body.String(), marker) {
				t.Fatal("provider body leaked through API")
			}
			var metadata []model.AiMetadata
			if err := f.db.Where("file_id=?", file.ID).Find(&metadata).Error; err != nil {
				t.Fatal(err)
			}
			var success, audits int64
			if err := f.db.Model(&model.AiTask{}).Where("file_id=? AND task_type='document_summary' AND status='success'", file.ID).Count(&success).Error; err != nil {
				t.Fatal(err)
			}
			if err := f.db.Model(&model.OperationLog{}).Where("file_id=? AND action='ai_summary'", file.ID).Count(&audits).Error; err != nil {
				t.Fatal(err)
			}
			if mode == "success" {
				if len(metadata) != 1 || metadata[0].SourceHash != storage.FileHash || metadata[0].OcrText != "Verified source body." || success != 1 || audits != 1 {
					t.Fatal("publication lost verified source or atomic history", metadata, success, audits)
				}
			} else if len(metadata) != 0 || success != 0 || audits != 0 {
				t.Fatal("partial summary published", metadata, success, audits)
			}
			if mode == "provider-error" {
				var task model.AiTask
				if err := f.db.Where("file_id=? AND status='failed'", file.ID).First(&task).Error; err != nil || task.Result != "generation_failed" {
					t.Fatal("private provider error persisted", task.Result, err)
				}
			}
			if mode == "corrupt-source" && calls.Load() != 0 {
				t.Fatal("unverified bytes reached provider")
			}
			waitNoReadLeases(t, f, *file.StorageID)
		})
	}
}
