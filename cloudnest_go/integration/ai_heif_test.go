//go:build integration && aisearch

package integration

import (
	"context"
	"fmt"
	"os"
	"testing"

	"cloudenest_go/model"
)

func TestAIImageHEIFRequiresDecoderWithoutPublishingPartialIndex(t *testing.T) {
	f, _ := indexFixture(t)
	b, err := os.ReadFile("../internal/imageprep/testdata/invoice-synthetic.heic")
	if err != nil {
		t.Fatal(err)
	}
	file := f.upload(t, f.alice, "snapshot.heic", b)
	if worked, err := textWorker(t, f, f.cfg.AIOllamaBaseURL).Step(context.Background()); !worked || err == nil {
		t.Fatal(worked, err)
	}
	job := readIndex(t, f, file.ID)
	if job.Status != "retry" || job.ErrorCode != "extraction_unavailable" {
		t.Fatal(job.Status, job.ErrorCode)
	}
	for _, table := range []any{&model.AIIndexDocument{}, &model.AIIndexChunk{}} {
		var count int64
		if err := f.db.Model(table).Where("file_id=?", file.ID).Count(&count).Error; err != nil || count != 0 {
			t.Fatal(count, err)
		}
	}
	f.json(t, "POST", "/api/ai/ocr", f.alice, map[string]any{"file_id": file.ID}, 503)
	path := fmt.Sprintf("/api/files/%d/thumbnail", file.ID)
	if w := identityRequest(f, f.alice, path, nil); w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := identityRequest(f, f.bob, path, nil); w.Code != 403 {
		t.Fatal(w.Code)
	}
	waitNoReadLeases(t, f, *file.StorageID)
}
