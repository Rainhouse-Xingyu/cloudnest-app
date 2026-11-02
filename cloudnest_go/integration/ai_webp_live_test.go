//go:build integration && aimodel

package integration

import (
	"context"
	"os"
	"strings"
	"testing"

	"cloudenest_go/internal/app"
	"cloudenest_go/model"
)

func TestAITextLiveWebPUsesPixelsAndRealSemanticSearch(t *testing.T) {
	if os.Getenv("CLOUDNEST_TEST_AI_MODEL") != "1" {
		t.Fatal("run real-model script")
	}
	f, _ := indexFixture(t)
	f.cfg.AIOllamaBaseURL = "http://127.0.0.1:11434"
	f.cfg.AIEmbeddingModel = "qwen3-embedding:0.6b"
	f.router = fixtureRouter(app.NewContainer(f.cfg, f.db, f.rdb).NewRouter())
	b, err := os.ReadFile("testdata/images/invoice-synthetic.webp")
	if err != nil {
		t.Fatal(err)
	}
	file := f.upload(t, f.alice, "snapshot.webp", b)
	if worked, err := textWorker(t, f, f.cfg.AIOllamaBaseURL).Step(context.Background()); !worked || err != nil {
		t.Fatal(worked, err)
	}
	var doc model.AIIndexDocument
	if err := f.db.First(&doc, "file_id=?", file.ID).Error; err != nil {
		t.Fatal(err)
	}
	if doc.ExtractionKind != "image" || !doc.HasOCR || doc.VisionModel != "qwen3-vl:4b" || len(doc.VisionDigest) != 64 || doc.CapturedAtLocal != nil {
		t.Fatal("invalid provenance", doc)
	}
	var chunks []model.AIIndexChunk
	if err := f.db.Where("file_id=?", file.ID).Find(&chunks).Error; err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, c := range chunks {
		decodeVector(t, c.Vector)
		text.WriteString(c.Content)
	}
	for _, needle := range []string{"Laptop", "5999", "192.0.2.42"} {
		if !strings.Contains(text.String(), needle) {
			t.Fatal("actual OCR lost", needle, text.String())
		}
	}
	question := "购买电脑的发票 5999"
	var reply struct {
		Items []struct {
			ID      int64    `json:"id"`
			Sources []string `json:"match_sources"`
		} `json:"items"`
	}
	decode(t, f.json(t, "POST", "/api/ai/search/nl", f.alice, map[string]any{"question": question}, 200), &reply)
	if len(reply.Items) != 1 || reply.Items[0].ID != file.ID || !strings.Contains(strings.Join(reply.Items[0].Sources, ","), "semantic") {
		t.Fatal("actual WebP retrieval", reply)
	}
	decode(t, f.json(t, "POST", "/api/ai/search/nl", f.bob, map[string]any{"question": question}, 200), &reply)
	if len(reply.Items) != 0 {
		t.Fatal("foreign image leaked", reply)
	}
	waitNoReadLeases(t, f, *file.StorageID)
	t.Logf("real WebP indexed: chunks=%d vision=%s digest=%s", len(chunks), doc.VisionModel, doc.VisionDigest)
}
