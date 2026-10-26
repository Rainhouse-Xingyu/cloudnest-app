//go:build integration && aisearch

package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"cloudenest_go/internal/indexqueue"
	"cloudenest_go/internal/service/contract"
	storageMysql "cloudenest_go/internal/storage/mysql"
	"cloudenest_go/model"
	"gorm.io/gorm"
)

type hybridReply struct {
	Items []struct {
		ID         int64    `json:"id"`
		Snippet    string   `json:"snippet"`
		Sources    []string `json:"match_sources"`
		Generation int64    `json:"index_generation"`
	} `json:"items"`
	Pagination struct {
		Total int64 `json:"total"`
	} `json:"pagination"`
	Retrieval struct {
		Mode          string   `json:"mode"`
		Warnings      []string `json:"warnings"`
		WindowLimited bool     `json:"window_limited"`
	} `json:"retrieval"`
}

func hybridQuery(t *testing.T, f *fixture, token, question string) hybridReply {
	t.Helper()
	var reply hybridReply
	decode(t, f.json(t, "POST", "/api/ai/search/nl", token, map[string]any{"question": question}, 200), &reply)
	return reply
}

// A lexical-only fixture, not simulated model acceptance: no vector is supplied.
// Real embedding retrieval is covered separately by integration,aimodel.
func lexicalIndexFixture(t *testing.T, f *fixture, file model.File, content string) {
	t.Helper()
	q := indexqueue.New(f.db)
	j := claimIndex(t, q)
	if j.FileID != file.ID {
		t.Fatal("fixture claimed unrelated job")
	}
	err := q.Publish(context.Background(), j, func(tx *gorm.DB) error {
		if err := tx.Where("file_id=?", file.ID).Delete(&model.AIIndexChunk{}).Error; err != nil {
			return err
		}
		if err := tx.Where("file_id=?", file.ID).Delete(&model.AIIndexDocument{}).Error; err != nil {
			return err
		}
		if err := tx.Create(&model.AIIndexDocument{FileID: file.ID, Generation: j.Generation, SourceHash: j.SourceHash, Pipeline: j.Pipeline, EmbeddingModel: "lexical-fixture", ModelDigest: strings.Repeat("0", 64), ChunkCount: 1, TextBytes: len(content), IndexedAt: time.Now().UTC()}).Error; err != nil {
			return err
		}
		return tx.Create(&model.AIIndexChunk{FileID: file.ID, Content: content, EndRune: len([]rune(content)), Vector: []byte{}}).Error
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAIHybridKeywordFallbackAndCrossUserIsolation(t *testing.T) {
	f, _ := indexFixture(t)
	own := f.upload(t, f.alice, "ordinary.md", []byte("数据库迁移"))
	lexicalIndexFixture(t, f, own, "数据库迁移方案和一致性验证")
	private := f.upload(t, f.bob, "private.md", []byte("数据库迁移 private"))
	lexicalIndexFixture(t, f, private, "数据库迁移 PRIVATE NEVER LEAK")
	r := hybridQuery(t, f, f.alice, "数据库迁移")
	if r.Retrieval.Mode != "keyword_fallback" || len(r.Retrieval.Warnings) == 0 || len(r.Items) != 1 || r.Items[0].ID != own.ID || !strings.Contains(r.Items[0].Snippet, "迁移") {
		t.Fatalf("fallback/ACL failed: %+v", r)
	}
	if err := f.db.Create(&model.FilePermission{FileID: own.ID, UserID: f.bobID, Permission: "read"}).Error; err != nil {
		t.Fatal(err)
	}
	r = hybridQuery(t, f, f.bob, "数据库迁移")
	if len(r.Items) != 2 {
		t.Fatal("explicit shared file not searchable", r)
	}
	if err := f.db.Where("file_id=? AND user_id=?", own.ID, f.bobID).Delete(&model.FilePermission{}).Error; err != nil {
		t.Fatal(err)
	}
	r = hybridQuery(t, f, f.bob, "数据库迁移")
	if len(r.Items) != 1 || r.Items[0].ID != private.ID {
		t.Fatal("revoked search grant remained", r)
	}
}

func TestAIHybridStaleGenerationAndChangedSourceNeverExposeOldText(t *testing.T) {
	f, _ := indexFixture(t)
	file := f.upload(t, f.alice, "neutral.md", []byte("old source"))
	lexicalIndexFixture(t, f, file, "old-secret-needle")
	if len(hybridQuery(t, f, f.alice, "old-secret-needle").Items) != 1 {
		t.Fatal("fixture not indexed")
	}
	if err := f.db.Transaction(func(tx *gorm.DB) error { return indexqueue.Enqueue(tx, file.ID, true) }); err != nil {
		t.Fatal(err)
	}
	if r := hybridQuery(t, f, f.alice, "old-secret-needle"); len(r.Items) != 0 {
		t.Fatal("old generation leaked", r)
	}
	lexicalIndexFixture(t, f, file, "old-secret-needle")
	other := f.upload(t, f.alice, "replacement.md", []byte("different current bytes"))
	if err := f.db.Model(&model.File{}).Where("id=?", file.ID).Update("storage_id", other.StorageID).Error; err != nil {
		t.Fatal(err)
	}
	if r := hybridQuery(t, f, f.alice, "old-secret-needle"); len(r.Items) != 0 {
		t.Fatal("old source leaked", r)
	}
}

func TestAIHybridRevalidatesRevocationAndRenameAfterScanning(t *testing.T) {
	f, _ := indexFixture(t)
	file := f.upload(t, f.alice, "shared.md", []byte("database migration"))
	lexicalIndexFixture(t, f, file, "database migration")
	if err := f.db.Create(&model.FilePermission{FileID: file.ID, UserID: f.bobID, Permission: "read"}).Error; err != nil {
		t.Fatal(err)
	}
	repo := storageMysql.NewAIRepo(f.db)
	var rows []contract.AIHybridRow
	if err := repo.ScanHybrid(context.Background(), f.bobID, contract.AIHybridFilter{}, func(r contract.AIHybridRow) error { rows = append(rows, r); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatal("wrong scan fixture", len(rows))
	}
	if err := f.db.Where("file_id=?", file.ID).Delete(&model.FilePermission{}).Error; err != nil {
		t.Fatal(err)
	}
	valid, err := repo.ValidateHybrid(context.Background(), f.bobID, rows)
	if err != nil || valid[file.ID] {
		t.Fatal("revoked result still valid", valid, err)
	}
	if err := f.db.Model(&model.File{}).Where("id=?", file.ID).Update("filename", "renamed.md").Error; err != nil {
		t.Fatal(err)
	}
	valid, err = repo.ValidateHybrid(context.Background(), f.aliceID, rows)
	if err != nil || valid[file.ID] {
		t.Fatal("renamed snapshot still valid", valid, err)
	}
}

func TestAIHybridRanksBeyondWindowAndReportsWindowLimit(t *testing.T) {
	f, _ := indexFixture(t)
	seed := f.upload(t, f.alice, "seed.bin", []byte("shared synthetic fixture bytes"))
	files := make([]model.File, 215)
	for i := range files {
		files[i] = model.File{UserID: f.aliceID, Filename: fmt.Sprintf("needle-%03d.txt", i), FileType: "txt", Size: seed.Size, StorageID: seed.StorageID}
	}
	files[len(files)-1].Filename = "needle-target.txt"
	if err := f.db.CreateInBatches(files, 50).Error; err != nil {
		t.Fatal(err)
	}
	r := hybridQuery(t, f, f.alice, "needle target")
	if len(r.Items) == 0 || r.Items[0].ID != files[len(files)-1].ID || r.Pagination.Total != 200 || !r.Retrieval.WindowLimited {
		t.Fatal("prefix truncation or hidden result window", r)
	}
	var legacy struct {
		Items []map[string]any `json:"items"`
	}
	decode(t, f.json(t, "GET", "/api/ai/search?keyword=needle-target", f.alice, nil, 200), &legacy)
	if len(legacy.Items) != 1 {
		t.Fatal("legacy keyword endpoint regressed")
	}
}

func TestAIHybridUploadTimeFilterAndUnsupportedCaptureTime(t *testing.T) {
	f, _ := indexFixture(t)
	old := f.upload(t, f.alice, "合同-old.md", []byte("contract old"))
	current := f.upload(t, f.alice, "合同-new.md", []byte("contract new"))
	zone := time.FixedZone("Asia/Shanghai", 8*3600)
	now := time.Now().In(zone)
	previous := time.Date(now.Year(), now.Month(), 1, 12, 0, 0, 0, zone).AddDate(0, -1, 0)
	if err := f.db.Model(&model.File{}).Where("id=?", old.ID).Update("create_time", previous.UTC()).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.File{}).Where("id=?", current.ID).Update("create_time", now.UTC()).Error; err != nil {
		t.Fatal(err)
	}
	r := hybridQuery(t, f, f.alice, "上个月上传的合同有哪些")
	if len(r.Items) != 1 || r.Items[0].ID != old.ID {
		t.Fatal("upload calendar wrong", r)
	}
	r = hybridQuery(t, f, f.alice, "去年拍摄的海边照片")
	if len(r.Items) != 0 || len(r.Retrieval.Warnings) != 1 || r.Retrieval.Warnings[0] != "capture_time_not_indexed" {
		t.Fatal("capture time invented", r)
	}
}

func TestAIHybridBoundsRequestInput(t *testing.T) {
	f, _ := indexFixture(t)
	f.json(t, "POST", "/api/ai/search/nl", f.alice, map[string]any{"question": strings.Repeat("x", 4097)}, 400)
	f.json(t, "POST", "/api/ai/search/nl", f.alice, map[string]any{"question": strings.Repeat("x", 20<<10)}, 400)
	f.json(t, "POST", "/api/ai/search/nl", f.alice, map[string]any{"question": "hello", "page": 10001}, 400)
}
