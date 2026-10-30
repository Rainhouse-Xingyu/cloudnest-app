//go:build integration && aisearch

package integration

import (
	"context"
	"os"
	"testing"

	"cloudenest_go/internal/store"
	"cloudenest_go/model"
)

func TestAIImageFailuresNeverPublishPartialResults(t *testing.T) {
	for _, tc := range []struct {
		name, code, status string
		valid              bool
	}{
		{"valid.png", "model_unavailable", "retry", true},
		{"valid.webp", "model_unavailable", "retry", true},
		{"broken.png", "extraction_failed", "failed", false},
		{"broken.webp", "extraction_failed", "failed", false},
		{"legacy.heic", "unsupported_type", "failed", false},
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
