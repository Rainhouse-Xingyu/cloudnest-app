//go:build integration && aisearch

package integration

import (
	"cloudenest_go/internal/app"
	"cloudenest_go/model"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAIPDFExtractionFailureStatesNeverPublishPartialResults(t *testing.T) {
	for _, tc := range []struct {
		name, code, status string
		missing            bool
	}{
		{"migration.pdf", "extraction_unavailable", "retry", true},
		{"migration.pdf", "model_unavailable", "retry", false},
		{"scanned-invoice.pdf", "model_unavailable", "retry", false},
		{"encrypted.pdf", "extraction_failed", "failed", false},
		{"too-many-pages.pdf", "extraction_limit", "failed", false},
	} {
		t.Run(tc.name+tc.code, func(t *testing.T) {
			f, _ := indexFixture(t)
			f.cfg.AIPDFBinDir = os.Getenv("CLOUDNEST_TEST_POPPLER_BIN")
			if tc.missing {
				f.cfg.AIPDFBinDir = t.TempDir()
			} else if f.cfg.AIPDFBinDir == "" {
				t.Skip("CLOUDNEST_TEST_POPPLER_BIN required for native PDF failures")
			}
			body, err := os.ReadFile(filepath.Join("testdata/pdf", tc.name))
			if err != nil {
				t.Fatal(err)
			}
			file := f.upload(t, f.alice, tc.name, body)
			if worked, err := textWorker(t, f, f.cfg.AIOllamaBaseURL).Step(context.Background()); !worked || err == nil {
				t.Fatal(worked, err)
			}
			j := readIndex(t, f, file.ID)
			if j.Status != tc.status || j.ErrorCode != tc.code {
				t.Fatal(j.Status, j.ErrorCode)
			}
			for _, table := range []any{&model.AIIndexDocument{}, &model.AIIndexChunk{}} {
				var count int64
				if err := f.db.Model(table).Where("file_id=?", file.ID).Count(&count).Error; err != nil || count != 0 {
					t.Fatal("partial PDF published", count, err)
				}
			}
			waitNoReadLeases(t, f, *file.StorageID)
		})
	}
}

func TestAIPDFManualSummaryUsesVerifiedParserAndPreservesAuthorization(t *testing.T) {
	f, _ := indexFixture(t)
	f.cfg.AIPDFBinDir = os.Getenv("CLOUDNEST_TEST_POPPLER_BIN")
	if f.cfg.AIPDFBinDir == "" {
		t.Skip("CLOUDNEST_TEST_POPPLER_BIN required")
	}
	f.cfg.AIOCRModel = "qwen3-vl:4b"
	received := make(chan string, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Error("wrong summary endpoint", r.URL.Path)
		}
		var req struct {
			Prompt string `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		received <- req.Prompt
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer provider.Close()
	f.cfg.AIOllamaBaseURL = provider.URL
	f.router = fixtureRouter(app.NewContainer(f.cfg, f.db, f.rdb).NewRouter())
	body, err := os.ReadFile("testdata/pdf/migration.pdf")
	if err != nil {
		t.Fatal(err)
	}
	file := f.upload(t, f.alice, "manual.pdf", body)
	f.json(t, "POST", "/api/ai/summarize", f.bob, map[string]any{"file_id": file.ID}, 403)
	f.json(t, "POST", "/api/ai/summarize", f.alice, map[string]any{"file_id": file.ID}, 502)
	prompt := <-received
	if !strings.Contains(prompt, "verified backup") || !strings.Contains(prompt, "192.0.2.42") || !strings.Contains(prompt, "[Page 2]") {
		t.Fatal("summary did not use structured parser", prompt)
	}
	bad := f.upload(t, f.alice, "invalid.pdf", []byte("not a zip"))
	f.json(t, "POST", "/api/ai/summarize", f.alice, map[string]any{"file_id": bad.ID}, 422)
	select {
	case <-received:
		t.Fatal("invalid PDF reached model")
	default:
	}
	var n int64
	if err := f.db.Model(&model.AiTask{}).Where("file_id IN ? AND status='success'", []int64{file.ID, bad.ID}).Count(&n).Error; err != nil || n != 0 {
		t.Fatal("failure recorded as success", n, err)
	}
	waitNoReadLeases(t, f, *file.StorageID)
	waitNoReadLeases(t, f, *bad.StorageID)
}
