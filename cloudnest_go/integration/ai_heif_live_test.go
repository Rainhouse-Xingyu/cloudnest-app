//go:build integration && aimodel && heifmodel

package integration

import (
	"bytes"
	"context"
	"fmt"
	"image/jpeg"
	"os"
	"strings"
	"testing"

	"cloudenest_go/internal/app"
	"cloudenest_go/model"
)

func TestAITextLiveHEIFUsesPixelsAndRealSemanticSearch(t *testing.T) {
	if os.Getenv("CLOUDNEST_TEST_AI_MODEL") != "1" {
		t.Fatal("run real-model script")
	}
	helper := os.Getenv("CLOUDNEST_TEST_HEIF_HELPER")
	if helper == "" {
		t.Fatal("set native HEIF helper for the real HEIF test")
	}
	f, _ := indexFixture(t)
	f.cfg.ImageHEIFHelper = helper
	f.cfg.AIOllamaBaseURL = "http://127.0.0.1:11434"
	f.cfg.AIEmbeddingModel = "qwen3-embedding:0.6b"
	f.cfg.AIOCRModel = "qwen3-vl:4b"
	f.router = fixtureRouter(app.NewContainer(f.cfg, f.db, f.rdb).NewRouter())
	b, err := os.ReadFile("../internal/imageprep/testdata/invoice-synthetic.heic")
	if err != nil {
		t.Fatal(err)
	}
	file := f.upload(t, f.alice, "snapshot.heic", b)
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
		t.Fatal("actual HEIF retrieval", reply)
	}
	decode(t, f.json(t, "POST", "/api/ai/search/nl", f.bob, map[string]any{"question": question}, 200), &reply)
	if len(reply.Items) != 0 {
		t.Fatal("foreign image leaked", reply)
	}
	var manual struct {
		Item model.AiMetadata `json:"item"`
	}
	decode(t, f.json(t, "POST", "/api/ai/ocr", f.alice, map[string]any{"file_id": file.ID}, 200), &manual)
	if !strings.Contains(manual.Item.OcrText, "5999") {
		t.Fatal("native manual OCR lost content", manual)
	}
	thumb := identityRequest(f, f.alice, fmt.Sprintf("/api/files/%d/thumbnail?size=128", file.ID), nil)
	if thumb.Code != 200 {
		t.Fatal(thumb.Code, thumb.Body.String())
	}
	im, err := jpeg.Decode(bytes.NewReader(thumb.Body.Bytes()))
	if err != nil || im.Bounds().Dx() > 128 || im.Bounds().Dy() > 128 {
		t.Fatal("native thumbnail", err)
	}
	waitNoReadLeases(t, f, *file.StorageID)
	t.Logf("real HEIF indexed: chunks=%d vision=%s digest=%s", len(chunks), doc.VisionModel, doc.VisionDigest)
}
