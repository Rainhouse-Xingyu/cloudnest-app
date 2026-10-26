//go:build integration && aimodel

package integration

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	"cloudenest_go/internal/aiindex"
	"cloudenest_go/internal/app"
	"cloudenest_go/internal/indexqueue"
	"cloudenest_go/model"
)

// Two actual source blobs/vectors are repeated into logical indexed copies.
// This measures exact retrieval at 10k vectors, not 10k uploads or corpus quality.
func TestAITextLiveExactIndexScaleScansLateRelevantVector(t *testing.T) {
	if os.Getenv("CLOUDNEST_TEST_AI_MODEL") != "1" {
		t.Fatal("run scripts/test-ai-index-model.sh")
	}
	f, _ := indexFixture(t)
	f.cfg.AIOllamaBaseURL = "http://127.0.0.1:11434"
	f.cfg.AIEmbeddingModel = "qwen3-embedding:0.6b"
	f.router = fixtureRouter(app.NewContainer(f.cfg, f.db, f.rdb).NewRouter())
	provider, err := aiindex.NewOllama(f.cfg.AIOllamaBaseURL, f.cfg.AIEmbeddingModel)
	if err != nil {
		t.Fatal(err)
	}
	texts := []string{"海边日落的风景照片，沙滩与海浪。", "数据库迁移方案：MySQL 升级前先备份，并验证迁移后的数据一致性。"}
	digest, err := provider.Digest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	vectors, err := provider.Embed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	var seeds [2]model.File
	var objects [2]model.Storage
	var encoded [2][]byte
	for i, text := range texts {
		seeds[i] = f.upload(t, f.alice, fmt.Sprintf("scale-seed-%d.bin", i), []byte(text))
		if err := f.db.First(&objects[i], *seeds[i].StorageID).Error; err != nil {
			t.Fatal(err)
		}
		encoded[i] = make([]byte, len(vectors[i])*4)
		for k, v := range vectors[i] {
			binary.LittleEndian.PutUint32(encoded[i][k*4:], math.Float32bits(v))
		}
	}
	const count = 10000
	files := make([]model.File, count)
	for i := range files {
		source := 0
		if i == count-1 {
			source = 1
		}
		files[i] = model.File{UserID: f.aliceID, Filename: fmt.Sprintf("scale-%05d.md", i), FileType: "md", Size: seeds[source].Size, StorageID: seeds[source].StorageID}
	}
	if err := f.db.CreateInBatches(files, 100).Error; err != nil {
		t.Fatal(err)
	}
	jobs := make([]model.AIIndexJob, count)
	docs := make([]model.AIIndexDocument, count)
	chunks := make([]model.AIIndexChunk, count)
	now := time.Now().UTC()
	for i, file := range files {
		source := 0
		if i == count-1 {
			source = 1
		}
		jobs[i] = model.AIIndexJob{FileID: file.ID, Generation: 1, SourceHash: objects[source].FileHash, Pipeline: indexqueue.Pipeline, Status: "done", AvailableAt: now, UpdatedAt: now}
		docs[i] = model.AIIndexDocument{FileID: file.ID, Generation: 1, SourceHash: objects[source].FileHash, Pipeline: indexqueue.Pipeline, EmbeddingModel: f.cfg.AIEmbeddingModel, ModelDigest: digest, Dimensions: len(vectors[source]), ChunkCount: 1, TextBytes: len(texts[source]), IndexedAt: now}
		chunks[i] = model.AIIndexChunk{FileID: file.ID, Content: texts[source], EndRune: len([]rune(texts[source])), Vector: encoded[source]}
	}
	if err := f.db.CreateInBatches(jobs, 100).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.CreateInBatches(docs, 100).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.CreateInBatches(chunks, 100).Error; err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	var reply struct {
		Items []struct {
			ID       int64    `json:"id"`
			Semantic *float64 `json:"semantic_score"`
		} `json:"items"`
		Retrieval struct {
			Mode string `json:"mode"`
		} `json:"retrieval"`
	}
	decode(t, f.json(t, "POST", "/api/ai/search/nl", f.alice, map[string]any{"question": "寻找数据搬迁与 MySQL 升级的执行步骤"}, 200), &reply)
	elapsed := time.Since(started)
	if reply.Retrieval.Mode != "hybrid" || len(reply.Items) == 0 || reply.Items[0].ID != files[count-1].ID || reply.Items[0].Semantic == nil {
		t.Fatal("late actual vector was omitted or retrieval degraded", reply)
	}
	t.Logf("exact scan files=%d dimensions=%d vector_bytes=%d query_elapsed=%s first_score=%.6f; synthetic logical copies of two actual vectors, single local request", count, len(vectors[0]), count*len(encoded[0]), elapsed, *reply.Items[0].Semantic)
}
