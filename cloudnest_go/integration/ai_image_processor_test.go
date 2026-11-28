//go:build integration && aisearch

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"cloudenest_go/internal/store"
	"cloudenest_go/model"
)

// A completed HTTP exchange is not evidence of a complete model result. In
// particular, a thinking-only length-limited response must neither be indexed
// nor retried as if the model server were temporarily unreachable.
func TestAIImageFailureDiagnosticsDistinguishUnavailableInvalidAndOutputLimit(t *testing.T) {
	body, err := os.ReadFile("testdata/images/beach-synthetic.png")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, reason, content, code, status string
		httpStatus                          int
	}{
		{"unavailable", "", "", "model_unavailable", "retry", 503},
		{"invalid", "stop", `{}`, "invalid_vision_response", "retry", 200},
		{"thinking output limit", "length", "", "vision_output_limit", "failed", 200},
		{"complete-looking but truncated", "length", `{"description":"visible beach","tags":["beach"],"text":"","has_text":false}`, "vision_output_limit", "failed", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/tags" {
					json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]string{"name": "qwen3-vl:4b", "digest": strings.Repeat("a", 64)}}})
					return
				}
				if r.URL.Path != "/api/chat" {
					t.Error("failed vision invoked another model stage", r.URL.Path)
					w.WriteHeader(500)
					return
				}
				w.WriteHeader(tc.httpStatus)
				if tc.httpStatus == 200 {
					json.NewEncoder(w).Encode(map[string]any{"model": "qwen3-vl:4b", "done": true, "done_reason": tc.reason, "message": map[string]string{"role": "assistant", "content": tc.content, "thinking": "PRIVATE_MODEL_TRACE_MUST_NOT_BE_STORED"}})
				}
			}))
			defer server.Close()
			f, _ := indexFixture(t)
			file := f.upload(t, f.alice, "entry.png", body)
			if worked, e := textWorker(t, f, server.URL).Step(context.Background()); !worked || e == nil {
				t.Fatal("vision failure not reported", worked, e)
			}
			job := readIndex(t, f, file.ID)
			if job.Status != tc.status || job.ErrorCode != tc.code || job.LeaseToken != "" || job.LeaseUntil != nil {
				t.Fatalf("wrong durable diagnostic: status=%s code=%s", job.Status, job.ErrorCode)
			}
			response := f.json(t, "GET", fmt.Sprintf("/api/ai/index/%d", file.ID), f.alice, nil, 200)
			if !strings.Contains(response.Body.String(), `"error_code":"`+tc.code+`"`) || strings.Contains(response.Body.String(), "PRIVATE_MODEL_TRACE") {
				t.Fatal("public diagnostic missing or leaked raw model output", response.Body.String())
			}
			for _, table := range []any{&model.AIIndexDocument{}, &model.AIIndexChunk{}} {
				var count int64
				if e := f.db.Model(table).Where("file_id=?", file.ID).Count(&count).Error; e != nil || count != 0 {
					t.Fatal("partial vision result published", count, e)
				}
			}
			waitNoReadLeases(t, f, *file.StorageID)
		})
	}
}

func TestAIImageFailuresNeverPublishPartialResults(t *testing.T) {
	for _, tc := range []struct {
		name, code, status string
		valid              bool
	}{
		{"valid.png", "model_unavailable", "retry", true},
		{"valid.webp", "model_unavailable", "retry", true},
		{"broken.png", "extraction_failed", "failed", false},
		{"broken.webp", "extraction_failed", "failed", false},
		{"broken.heic", "extraction_failed", "failed", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := indexFixture(t)
			body := []byte("invalid image source")
			if tc.valid {
				var err error
				source := "testdata/images/invoice-synthetic.png"
				if tc.name == "valid.webp" {
					source = "testdata/images/invoice-synthetic.webp"
				}
				body, err = os.ReadFile(source)
				if err != nil {
					t.Fatal(err)
				}
			}
			file := f.upload(t, f.alice, tc.name, body)
			if worked, err := textWorker(t, f, f.cfg.AIOllamaBaseURL).Step(context.Background()); !worked || err == nil {
				t.Fatal(worked, err)
			}
			job := readIndex(t, f, file.ID)
			if job.Status != tc.status || job.ErrorCode != tc.code {
				t.Fatal("incorrect image failure", job.Status, job.ErrorCode)
			}
			for _, table := range []any{&model.AIIndexDocument{}, &model.AIIndexChunk{}} {
				var count int64
				if err := f.db.Model(table).Where("file_id=?", file.ID).Count(&count).Error; err != nil || count != 0 {
					t.Fatal("partial image published", count, err)
				}
			}
			waitNoReadLeases(t, f, *file.StorageID)
		})
	}
}

func TestAIImageProvenanceRequiresExplicitMigration(t *testing.T) {
	f, _ := indexFixture(t)
	if err := store.CheckAIIndexSchema(f.db); err != nil {
		t.Fatal(err)
	}
	file := f.upload(t, f.alice, "old-index.md", []byte("old index"))
	lexicalIndexFixture(t, f, file, "old indexed content")
	// Simulate a pre-image checkpoint schema only in the guarded disposable DB.
	if err := f.db.Exec("ALTER TABLE ai_index_document DROP COLUMN captured_at_local, DROP COLUMN has_ocr").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Migrate(f.db); err != nil {
			t.Error(err)
		}
	})
	if err := store.CheckAIIndexSchema(f.db); err == nil {
		t.Fatal("old image schema accepted")
	}
	if f.db.Migrator().HasColumn(&model.AIIndexDocument{}, "captured_at_local") {
		t.Fatal("read-only check changed schema")
	}
	if err := store.Migrate(f.db); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckAIIndexSchema(f.db); err != nil {
		t.Fatal(err)
	}
	var doc model.AIIndexDocument
	if err := f.db.First(&doc, "file_id=?", file.ID).Error; err != nil {
		t.Fatal(err)
	}
	if doc.CapturedAtLocal != nil || doc.HasOCR || doc.VisionModel != "" {
		t.Fatal("migration invented provenance", doc)
	}
	if len(hybridQuery(t, f, f.alice, "old indexed content").Items) != 1 {
		t.Fatal("migration lost existing index")
	}
}
