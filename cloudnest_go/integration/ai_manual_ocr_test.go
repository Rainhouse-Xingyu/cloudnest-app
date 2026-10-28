//go:build integration && aisearch

package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cloudenest_go/internal/app"
	"cloudenest_go/internal/service/contract"
	storageMysql "cloudenest_go/internal/storage/mysql"
	"cloudenest_go/internal/store"
	"cloudenest_go/model"
	"gorm.io/gorm"
)

func TestAIImageManualOCRValidatesPixelsAuthorizationAndFailures(t *testing.T) {
	f, _ := indexFixture(t)
	f.cfg.AIOCRModel = "qwen3-vl:4b"
	var calls atomic.Int32
	var malformed atomic.Bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			if malformed.Load() {
				w.Write([]byte("{"))
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]string{"name": f.cfg.AIOCRModel, "digest": strings.Repeat("a", 64)}}})
			return
		}
		calls.Add(1)
		var request struct{ Messages []struct{ Images []string } }
		if r.URL.Path != "/api/chat" || json.NewDecoder(r.Body).Decode(&request) != nil || len(request.Messages) != 2 || len(request.Messages[1].Images) != 1 {
			t.Error("manual OCR did not use shared vision protocol")
		} else {
			pixels, err := base64.StdEncoding.DecodeString(request.Messages[1].Images[0])
			if err != nil {
				t.Error(err)
			}
			cfg, err := png.DecodeConfig(bytes.NewReader(pixels))
			if err != nil || cfg.Width > 1600 || cfg.Height > 1600 {
				t.Error("unprepared pixels", cfg, err)
			}
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer provider.Close()
	f.cfg.AIOllamaBaseURL = provider.URL
	f.router = fixtureRouter(app.NewContainer(f.cfg, f.db, f.rdb).NewRouter())
	body, err := os.ReadFile("testdata/images/invoice-synthetic.png")
	if err != nil {
		t.Fatal(err)
	}
	file := f.upload(t, f.alice, "manual.png", body)
	f.json(t, "POST", "/api/ai/ocr", f.bob, map[string]any{"file_id": file.ID}, 403)
	f.json(t, "POST", "/api/ai/ocr", f.alice, map[string]any{"file_id": file.ID, "tags": strings.Repeat("字", 256)}, 400)
	f.json(t, "POST", "/api/ai/ocr", f.alice, map[string]any{"file_id": file.ID, "description": strings.Repeat("x", 20<<10)}, 400)
	f.json(t, "POST", "/api/ai/ocr", f.alice, map[string]any{"file_id": file.ID}, 502)
	malformed.Store(true)
	f.json(t, "POST", "/api/ai/ocr", f.alice, map[string]any{"file_id": file.ID}, 502)
	bad := f.upload(t, f.alice, "broken.png", []byte("not an image"))
	f.json(t, "POST", "/api/ai/ocr", f.alice, map[string]any{"file_id": bad.ID}, 422)
	if calls.Load() != 1 {
		t.Fatal("invalid/unauthorized input reached model", calls.Load())
	}
	for _, table := range []any{&model.AiMetadata{}, &model.AiTask{}} {
		q := f.db.Model(table).Where("file_id IN ?", []int64{file.ID, bad.ID})
		if _, ok := table.(*model.AiTask); ok {
			q = q.Where("status='success'")
		}
		var count int64
		if err := q.Count(&count).Error; err != nil || count != 0 {
			t.Fatal("failure published result", count, err)
		}
	}
	waitNoReadLeases(t, f, *file.StorageID)
}

// The synthetic provider response is used only to pause publication and test
// races. Recognition quality is verified separately with actual Ollama.
func TestAIImageManualOCRRechecksSourceAndRevokedGrant(t *testing.T) {
	for _, mode := range []string{"replace", "revoke", "recycle"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := indexFixture(t)
			f.cfg.AIOCRModel = "qwen3-vl:4b"
			entered, release := make(chan struct{}), make(chan struct{})
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/tags" {
					json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]string{"name": f.cfg.AIOCRModel, "digest": strings.Repeat("a", 64)}}})
					return
				}
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"model": f.cfg.AIOCRModel, "done": true, "done_reason": "stop", "message": map[string]string{"role": "assistant", "content": `{"description":"protocol fixture","tags":[],"text":"","has_text":false}`}})
			}))
			defer provider.Close()
			f.cfg.AIOllamaBaseURL = provider.URL
			f.router = fixtureRouter(app.NewContainer(f.cfg, f.db, f.rdb).NewRouter())
			body, err := os.ReadFile("testdata/images/invoice-synthetic.png")
			if err != nil {
				t.Fatal(err)
			}
			file := f.upload(t, f.alice, "race.png", body)
			token := f.alice
			want := 409
			if mode == "revoke" {
				token = f.bob
				want = 403
				if err := f.db.Create(&model.FilePermission{FileID: file.ID, UserID: f.bobID, Permission: "read"}).Error; err != nil {
					t.Fatal(err)
				}
			}
			req := httptest.NewRequest("POST", "/api/ai/ocr", strings.NewReader(fmt.Sprintf(`{"file_id":%d}`, file.ID))).WithContext(ctx)
			req.RemoteAddr = f.remoteAddr
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			result := make(chan *httptest.ResponseRecorder, 1)
			go func() { w := httptest.NewRecorder(); f.router.ServeHTTP(w, req); result <- w }()
			select {
			case <-entered:
			case w := <-result:
				t.Fatalf("OCR exited before model: %d %s", w.Code, w.Body.String())
			case <-ctx.Done():
				t.Fatal("provider not reached")
			}
			switch mode {
			case "replace":
				other := f.upload(t, f.alice, "new.txt", []byte("different source"))
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
					t.Fatal("stale result accepted", w.Code, w.Body.String())
				}
			case <-ctx.Done():
				t.Fatal("OCR did not finish")
			}
			var count int64
			if err := f.db.Model(&model.AiMetadata{}).Where("file_id=?", file.ID).Count(&count).Error; err != nil || count != 0 {
				t.Fatal("stale metadata written", count, err)
			}
			if err := f.db.Model(&model.AiTask{}).Where("file_id=? AND status='success'", file.ID).Count(&count).Error; err != nil || count != 0 {
				t.Fatal("stale success written", count, err)
			}
			waitNoReadLeases(t, f, *file.StorageID)
		})
	}
}

func TestAIImageManualOCRAuditFailureRollsBackMetadataAndTask(t *testing.T) {
	f, _ := indexFixture(t)
	f.cfg.AIOCRModel = "qwen3-vl:4b"
	file := f.upload(t, f.alice, "transaction.png", []byte("repository transaction fixture"))
	var storage model.Storage
	if err := f.db.First(&storage, *file.StorageID).Error; err != nil {
		t.Fatal(err)
	}
	hook := "test:manual_ocr_audit_failure"
	if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "operation_log" {
			tx.AddError(errors.New("injected audit failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Callback().Create().Remove(hook) })
	if _, err := storageMysql.NewAIRepo(f.db).PublishOCR(context.Background(), f.aliceID, file, storage.FileHash, "fixture", "fixture", "transaction test text"); err == nil {
		t.Fatal("audit failure ignored")
	}
	for _, table := range []any{&model.AiMetadata{}, &model.AiTask{}} {
		var count int64
		if err := f.db.Model(table).Where("file_id=?", file.ID).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("partial publication", count, err)
		}
	}
}

// Repository fixtures test source identity/migration, not OCR accuracy.
func TestAIImageManualOCRPreservesHistoryAndFiltersChangedSources(t *testing.T) {
	f, _ := indexFixture(t)
	file := f.upload(t, f.alice, "source.png", []byte("transaction source fixture"))
	old := model.AiMetadata{FileID: file.ID, OcrText: "old-unknown-content"}
	if err := f.db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec("ALTER TABLE ai_metadata DROP COLUMN source_hash").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Migrate(f.db); err != nil {
			t.Error(err)
		}
	})
	if err := store.CheckAIIndexSchema(f.db); err == nil {
		t.Fatal("missing OCR source column accepted")
	}
	if f.db.Migrator().HasColumn(&model.AiMetadata{}, "source_hash") {
		t.Fatal("read-only check repaired schema")
	}
	if err := store.Migrate(f.db); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckAIIndexSchema(f.db); err != nil {
		t.Fatal(err)
	}
	repo := storageMysql.NewAIRepo(f.db)
	rows, _, err := repo.Search(context.Background(), f.aliceID, contract.AISearchQuery{Keyword: "old-unknown-content", Page: 1, Limit: 20})
	if err != nil || len(rows) != 0 {
		t.Fatal("unknown historical source guessed", rows, err)
	}
	var storage model.Storage
	if err := f.db.First(&storage, *file.StorageID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PublishOCR(context.Background(), f.aliceID, file, storage.FileHash, "fixture", "", "current-ocr-needle"); err != nil {
		t.Fatal(err)
	}
	rows, _, err = repo.Search(context.Background(), f.aliceID, contract.AISearchQuery{Keyword: "current-ocr-needle", Page: 1, Limit: 20})
	if err != nil || len(rows) != 1 {
		t.Fatal("current OCR unavailable", rows, err)
	}
	other := f.upload(t, f.alice, "new.txt", []byte("changed source bytes"))
	f.json(t, "POST", fmt.Sprintf("/api/files/%d/versions", file.ID), f.alice, map[string]any{"storage_id": other.StorageID, "base_version": 1}, 200)
	rows, _, err = repo.Search(context.Background(), f.aliceID, contract.AISearchQuery{Keyword: "current-ocr-needle", Page: 1, Limit: 20})
	if err != nil || len(rows) != 0 {
		t.Fatal("old OCR survived source replacement", rows, err)
	}
	var retained model.AiMetadata
	if err := f.db.First(&retained, old.ID).Error; err != nil || retained.OcrText != "current-ocr-needle" {
		t.Fatal("historical row removed", retained, err)
	}
}

func TestAIImageManualOCRConcurrentPublicationAndEmptyText(t *testing.T) {
	f, _ := indexFixture(t)
	file := f.upload(t, f.alice, "concurrent.png", []byte("repository publication fixture"))
	var storage model.Storage
	if err := f.db.First(&storage, *file.StorageID).Error; err != nil {
		t.Fatal(err)
	}
	repo := storageMysql.NewAIRepo(f.db)
	var wg sync.WaitGroup
	errors := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := repo.PublishOCR(context.Background(), f.aliceID, file, storage.FileHash, "fixture", "", "nonempty")
			errors <- err
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	if err := f.db.Model(&model.AiMetadata{}).Where("file_id=?", file.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("duplicate metadata", count, err)
	}
	row, err := repo.PublishOCR(context.Background(), f.aliceID, file, storage.FileHash, "", "", "")
	if err != nil || row.OcrText != "" || row.Tags != "" || row.Description != "" {
		t.Fatal("empty OCR did not clear old content", row, err)
	}
	if err := f.db.First(&row, row.ID).Error; err != nil || row.OcrText != "" {
		t.Fatal("empty OCR was not persisted", row, err)
	}
}
