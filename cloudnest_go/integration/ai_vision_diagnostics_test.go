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

	"cloudenest_go/model"
)

func TestAIPDFVisionOutputLimitNeverPublishesAndReleasesLeases(t *testing.T) {
	bin := os.Getenv("CLOUDNEST_TEST_POPPLER_BIN")
	if bin == "" {
		t.Skip("CLOUDNEST_TEST_POPPLER_BIN required for actual scanned-PDF decoding")
	}
	body, err := os.ReadFile("testdata/pdf/scanned-invoice.pdf")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]string{"name": "qwen3-vl:4b", "digest": strings.Repeat("a", 64)}}})
			return
		}
		if r.URL.Path != "/api/chat" {
			t.Error("failed PDF vision reached another model stage", r.URL.Path)
			w.WriteHeader(500)
			return
		}
		// Deliberately plausible partial text must still be rejected on length.
		json.NewEncoder(w).Encode(map[string]any{"model": "qwen3-vl:4b", "done": true, "done_reason": "length", "message": map[string]string{"role": "assistant", "content": "Laptop 5999", "thinking": "PRIVATE_PDF_MODEL_TRACE"}})
	}))
	defer server.Close()
	f, _ := indexFixture(t)
	f.cfg.AIPDFBinDir = bin
	file := f.upload(t, f.alice, "neutral.pdf", body)
	if worked, e := textWorker(t, f, server.URL).Step(context.Background()); !worked || e == nil {
		t.Fatal("PDF output limit not reported", worked, e)
	}
	job := readIndex(t, f, file.ID)
	if job.Status != "failed" || job.ErrorCode != "vision_output_limit" || job.LeaseToken != "" || job.LeaseUntil != nil {
		t.Fatalf("wrong PDF limit diagnostic: status=%s code=%s", job.Status, job.ErrorCode)
	}
	response := f.json(t, "GET", fmt.Sprintf("/api/ai/index/%d", file.ID), f.alice, nil, 200)
	if !strings.Contains(response.Body.String(), `"error_code":"vision_output_limit"`) || strings.Contains(response.Body.String(), "PRIVATE_PDF_MODEL_TRACE") || strings.Contains(response.Body.String(), "Laptop 5999") {
		t.Fatal("PDF diagnostic missing or partial output leaked", response.Body.String())
	}
	for _, table := range []any{&model.AIIndexDocument{}, &model.AIIndexChunk{}} {
		var count int64
		if e := f.db.Model(table).Where("file_id=?", file.ID).Count(&count).Error; e != nil || count != 0 {
			t.Fatal("partial PDF result published", count, e)
		}
	}
	waitNoReadLeases(t, f, *file.StorageID)
}
