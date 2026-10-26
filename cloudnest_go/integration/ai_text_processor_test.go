//go:build integration

package integration

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"cloudenest_go/internal/aiindex"
	"cloudenest_go/internal/blob"
	"cloudenest_go/internal/indexqueue"
	"cloudenest_go/model"
)

func textWorker(t *testing.T, f *fixture, base string) *indexqueue.Worker {
	t.Helper()
	provider, err := aiindex.NewOllama(base, "qwen3-embedding:0.6b")
	if err != nil {
		t.Fatal(err)
	}
	return indexqueue.NewWorker(f.db, blob.NewManager(f.db), aiindex.NewProcessor(f.db, provider).Process)
}

func TestAITextUnavailableModelRetriesWithoutPublishingResults(t *testing.T) {
	f, _ := indexFixture(t)
	file := f.upload(t, f.alice, "provider-down.md", []byte("数据库迁移需要备份和一致性验证。"))
	worker := textWorker(t, f, f.cfg.AIOllamaBaseURL)
	worked, err := worker.Step(context.Background())
	var failure *indexqueue.Failure
	if !worked || !errors.As(err, &failure) || failure.Code != "model_unavailable" {
		t.Fatal("model failure not reported", worked, err)
	}
	j := readIndex(t, f, file.ID)
	if j.Status != "retry" || j.ErrorCode != "model_unavailable" {
		t.Fatal("model outage reported success", j.Status, j.ErrorCode)
	}
	for _, table := range []any{&model.AIIndexDocument{}, &model.AIIndexChunk{}} {
		var n int64
		if err := f.db.Model(table).Where("file_id=?", file.ID).Count(&n).Error; err != nil || n != 0 {
			t.Fatal("outage published result", n, err)
		}
	}
	waitNoReadLeases(t, f, *file.StorageID)
}

func TestAITextUnsupportedLimitAndCorruptionStayDistinct(t *testing.T) {
	for _, tc := range []struct {
		name, body, code string
		corrupt          bool
	}{
		{"unsupported.bin", "binary payload", "unsupported_type", false},
		{"too-large.txt", strings.Repeat("x", aiindex.MaxTextBytes+1), "extraction_limit", false},
		{"bad-source.txt", "source will fail integrity check", "source_integrity", true},
		{"bad-encoding.txt", "\xff\x00", "extraction_failed", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := indexFixture(t)
			file := f.upload(t, f.alice, tc.name, []byte(tc.body))
			if tc.corrupt {
				var s model.Storage
				if err := f.db.First(&s, *file.StorageID).Error; err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(s.FilePath, []byte("altered disposable fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if worked, err := textWorker(t, f, f.cfg.AIOllamaBaseURL).Step(context.Background()); !worked || err == nil {
				t.Fatal("failed source succeeded")
			}
			j := readIndex(t, f, file.ID)
			if j.Status != "failed" || j.ErrorCode != tc.code {
				t.Fatal("wrong diagnostic", j.Status, j.ErrorCode, tc.code)
			}
			waitNoReadLeases(t, f, *file.StorageID)
		})
	}
}
