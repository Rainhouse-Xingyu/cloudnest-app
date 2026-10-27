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

func TestAIOfficeExtractionAndModelFailuresStayDistinct(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		valid      bool
	}{
		{"migration.docx", "model_unavailable", true},
		{"invoice.xlsx", "model_unavailable", true},
		{"permissions.pptx", "model_unavailable", true},
		{"broken.docx", "extraction_failed", false},
		{"legacy.doc", "unsupported_type", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := indexFixture(t)
			body := []byte("invalid office bytes")
			if tc.valid {
				var err error
				body, err = os.ReadFile(filepath.Join("testdata/office", tc.name))
				if err != nil {
					t.Fatal(err)
				}
			}
			file := f.upload(t, f.alice, tc.name, body)
			if worked, err := textWorker(t, f, f.cfg.AIOllamaBaseURL).Step(context.Background()); !worked || err == nil {
				t.Fatal(worked, err)
			}
			j := readIndex(t, f, file.ID)
			want := "failed"
			if tc.valid {
				want = "retry"
			}
			if j.Status != want || j.ErrorCode != tc.code {
				t.Fatal("wrong Office failure state", j.Status, j.ErrorCode)
			}
			var count int64
			for _, table := range []any{&model.AIIndexDocument{}, &model.AIIndexChunk{}} {
				if err := f.db.Model(table).Where("file_id=?", file.ID).Count(&count).Error; err != nil || count != 0 {
					t.Fatal("partial office result published", count, err)
				}
			}
			waitNoReadLeases(t, f, *file.StorageID)
		})
	}
}

func TestAIOfficeManualSummarySharesParserAndRejectsInvalidPackages(t *testing.T) {
	f, _ := indexFixture(t)
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
	body, err := os.ReadFile("testdata/office/migration.docx")
	if err != nil {
		t.Fatal(err)
	}
	file := f.upload(t, f.alice, "manual.docx", body)
	f.json(t, "POST", "/api/ai/summarize", f.bob, map[string]any{"file_id": file.ID}, 403)
	f.json(t, "POST", "/api/ai/summarize", f.alice, map[string]any{"file_id": file.ID}, 502)
	prompt := <-received
	if !strings.Contains(prompt, "备份数据库") || !strings.Contains(prompt, "192.0.2.42") || !strings.Contains(prompt, "CloudNest 合成测试文档") {
		t.Fatal("summary did not use structured parser", prompt)
	}
	bad := f.upload(t, f.alice, "invalid.docx", []byte("not a zip"))
	f.json(t, "POST", "/api/ai/summarize", f.alice, map[string]any{"file_id": bad.ID}, 422)
	select {
	case <-received:
		t.Fatal("invalid package reached model")
	default:
	}
	var n int64
	if err := f.db.Model(&model.AiTask{}).Where("file_id IN ? AND status='success'", []int64{file.ID, bad.ID}).Count(&n).Error; err != nil || n != 0 {
		t.Fatal("failure recorded as success", n, err)
	}
	waitNoReadLeases(t, f, *file.StorageID)
	waitNoReadLeases(t, f, *bad.StorageID)
}
