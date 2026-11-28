//go:build integration && aimodel

package integration

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cloudenest_go/internal/aiindex"
	"cloudenest_go/internal/app"
	"cloudenest_go/internal/config"
	"cloudenest_go/model"
)

func TestAITextLiveSummaryAndAssistantUseBoundedGeneration(t *testing.T) {
	if os.Getenv("CLOUDNEST_TEST_AI_MODEL") != "1" {
		t.Fatal("explicit local-model opt-in required")
	}
	f, _ := indexFixture(t)
	f.cfg.AIOllamaBaseURL = "http://127.0.0.1:11434"
	f.cfg.AITimeoutSec = 120
	f.cfg.AIOllamaModel = strings.TrimSpace(os.Getenv("CLOUDNEST_TEST_GENERATION_MODEL"))
	if f.cfg.AIOllamaModel == "" {
		f.cfg.AIOllamaModel = config.DefaultGenerationModel
	}
	f.cfg.AIGuideDocPath = filepath.Join(t.TempDir(), "synthetic-guide.txt")
	if err := os.WriteFile(f.cfg.AIGuideDocPath, []byte("CloudNest upload recovery: the documented recovery code is CN-RESUME-42. Open Transfers, select the paused upload and choose Resume. Never restart an unknown operation automatically."), 0600); err != nil {
		t.Fatal(err)
	}
	provider, err := aiindex.NewOllama(f.cfg.AIOllamaBaseURL, f.cfg.AIOllamaModel)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := provider.Digest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("actual generation model=%s digest=%s; summary then assistant, no concurrent inference", f.cfg.AIOllamaModel, digest)
	f.router = fixtureRouter(app.NewContainer(f.cfg, f.db, f.rdb).NewRouter())
	body := []byte("CloudNest migration plan: first create a verified backup. Restore the backup into an isolated database and check file hashes before any production migration. Preserve historical user files. If validation fails, stop and restore from the verified backup.")
	file := f.upload(t, f.alice, "generation-source.txt", body)
	f.json(t, "POST", "/api/ai/summarize", f.bob, map[string]any{"file_id": file.ID}, 403)
	var summary struct {
		Item struct {
			FileID         int64 `json:"file_id"`
			Model, Summary string
			Truncated      bool
		}
	}
	decode(t, f.json(t, "POST", "/api/ai/summarize", f.alice, map[string]any{"file_id": file.ID, "max_points": 3}, 200), &summary)
	text := strings.ToLower(summary.Item.Summary)
	if summary.Item.FileID != file.ID || summary.Item.Model != f.cfg.AIOllamaModel || summary.Item.Truncated || !(strings.Contains(text, "backup") || strings.Contains(text, "备份")) || !(strings.Contains(text, "restore") || strings.Contains(text, "恢复") || strings.Contains(text, "还原")) {
		t.Fatal("actual summary lost source facts or identity")
	}
	var metadata model.AiMetadata
	if err := f.db.Where("file_id=?", file.ID).First(&metadata).Error; err != nil || metadata.SourceHash != fmt.Sprintf("%x", sha256.Sum256(body)) || metadata.OcrText != string(body) {
		t.Fatal("actual summary source fence", err)
	}
	var count int64
	if err := f.db.Model(&model.AiTask{}).Where("file_id=? AND task_type='document_summary' AND status='success'", file.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("actual summary history", count, err)
	}
	var answer struct {
		Item struct {
			Model, Answer string
			GuideLoaded   bool `json:"guide_doc_loaded"`
		}
	}
	decode(t, f.json(t, "POST", "/api/ai/assistant/ask", f.alice, map[string]any{"question": "根据项目文档，如何继续已暂停的上传？请给出文档中的恢复代码。"}, 200), &answer)
	if answer.Item.Model != f.cfg.AIOllamaModel || !answer.Item.GuideLoaded || !strings.Contains(answer.Item.Answer, "CN-RESUME-42") {
		t.Fatal("actual assistant lost guide fact or model identity")
	}
	confirmed, err := provider.Digest(context.Background())
	if err != nil || confirmed != digest {
		t.Fatal("actual generation model changed", err)
	}
	waitNoReadLeases(t, f, *file.StorageID)
	t.Logf("actual summary bytes=%d sha256=%x; assistant bytes=%d sha256=%x; source hash/history/lease verified", len(summary.Item.Summary), sha256.Sum256([]byte(summary.Item.Summary)), len(answer.Item.Answer), sha256.Sum256([]byte(answer.Item.Answer)))
}
