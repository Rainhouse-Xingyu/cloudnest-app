//go:build integration && aimodel

package integration

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"cloudenest_go/internal/aiindex"
	"cloudenest_go/internal/app"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
)

// This opt-in test uses the real local model and disposable application DB.
// It proves upload -> extraction -> embedding -> fenced SQL publication. The
// final cosine comparison is a smoke check, not a retrieval-quality evaluation.
func TestAITextLiveOllamaPublishesRecoverableChunkVectors(t *testing.T) {
	if os.Getenv("CLOUDNEST_TEST_AI_MODEL") != "1" {
		t.Fatal("run scripts/test-ai-index-model.sh to authorize local synthetic model fixtures")
	}
	f, _ := indexFixture(t)
	const base = "http://127.0.0.1:11434"
	provider, err := aiindex.NewOllama(base, "qwen3-embedding:0.6b")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	digest, err := provider.Digest(ctx)
	if err != nil {
		t.Fatal("real embedding model required", err)
	}
	texts := []string{"数据库迁移方案：MySQL 升级前先备份，并验证迁移后的数据一致性。", "海边日落的风景照片，沙滩与海浪。"}
	files := make([]model.File, len(texts))
	stored := make([][]float32, len(texts))
	for i, text := range texts {
		files[i] = f.upload(t, f.alice, fmt.Sprintf("actual-embedding-%d.md", i), []byte(text))
		if worked, err := textWorker(t, f, base).Step(ctx); !worked || err != nil {
			t.Fatal("real processor failed", worked, err)
		}
		j := readIndex(t, f, files[i].ID)
		var doc model.AIIndexDocument
		if err := f.db.First(&doc, "file_id=?", files[i].ID).Error; err != nil {
			t.Fatal(err)
		}
		if j.Status != "done" || doc.Generation != j.Generation || doc.SourceHash != j.SourceHash || doc.ChunkCount != 1 || doc.Dimensions != 1024 || doc.ModelDigest != digest {
			t.Fatalf("wrong actual index metadata: %+v / %+v", j, doc)
		}
		var chunk model.AIIndexChunk
		if err := f.db.First(&chunk, "file_id=? AND ordinal=0", files[i].ID).Error; err != nil {
			t.Fatal(err)
		}
		if chunk.Content != text || chunk.StartRune != 0 || chunk.EndRune != len([]rune(text)) || len(chunk.Vector) != doc.Dimensions*4 {
			t.Fatal("persisted chunk lost source or dimensions")
		}
		stored[i] = decodeVector(t, chunk.Vector)
		waitNoReadLeases(t, f, *files[i].StorageID)
	}
	query, err := provider.Embed(ctx, []string{"MySQL 升级与数据迁移的执行计划。"})
	if err != nil {
		t.Fatal(err)
	}
	var scores [2]float64
	for i := range stored {
		for k, n := range stored[i] {
			scores[i] += float64(n) * float64(query[0][k])
		}
	}
	if scores[0] <= scores[1] {
		t.Fatal("actual model smoke ranking reversed", scores)
	}
	t.Logf("real model=%s digest=%s dimensions=1024 migration_cosine=%.6f photo_cosine=%.6f", provider.Model(), digest, scores[0], scores[1])
	// Historical recovery uses the public CAS API and an actual restarted
	// production server, with no direct queue Enqueue or Step call.
	var status struct {
		Item contract.AIIndexStatus `json:"item"`
	}
	statusPath := fmt.Sprintf("/api/ai/index/%d", files[0].ID)
	decode(t, f.json(t, "GET", statusPath, f.alice, nil, 200), &status)
	if !status.Item.IndexedCurrent || status.Item.Generation != 1 {
		t.Fatal(status)
	}
	f.json(t, "POST", "/api/ai/index/rebuild", f.alice, map[string]any{"items": []contract.AIRebuildItem{{FileID: files[0].ID, Generation: status.Item.Generation}}}, 202)
	runLiveIndexServer(t, f, files[0].ID)
	decode(t, f.json(t, "GET", statusPath, f.alice, nil, 200), &status)
	if status.Item.Status != "done" || status.Item.Generation != 2 || !status.Item.IndexedCurrent || status.Item.ChunkCount != 1 {
		t.Fatal("API/server rebuild", status)
	}
	// A longer UTF-8 document verifies multiple persisted chunks and source spans.
	longText := strings.Repeat("Spring Boot 权限管理：角色授权、资源级检查与审计日志。\n", 55)
	longFile := f.upload(t, f.alice, "permissions.md", []byte(longText))
	if worked, err := textWorker(t, f, base).Step(ctx); !worked || err != nil {
		t.Fatal("long text processing", worked, err)
	}
	var chunks []model.AIIndexChunk
	if err := f.db.Where("file_id=?", longFile.ID).Order("ordinal").Find(&chunks).Error; err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 2 {
		t.Fatal("long document was not chunked")
	}
	runes := []rune(longText)
	for _, chunk := range chunks {
		if chunk.Content != string(runes[chunk.StartRune:chunk.EndRune]) {
			t.Fatal("long-text source offsets lost")
		}
		decodeVector(t, chunk.Vector)
	}
	// Actual permanent delete removes both job and vector rows.
	f.json(t, "DELETE", fmt.Sprintf("/api/files/%d", longFile.ID), f.alice, nil, 200)
	f.json(t, "DELETE", fmt.Sprintf("/api/files/%d/permanent", longFile.ID), f.alice, nil, 200)
	for _, table := range []any{&model.AIIndexJob{}, &model.AIIndexDocument{}, &model.AIIndexChunk{}} {
		var n int64
		if err := f.db.Model(table).Where("file_id=?", longFile.ID).Count(&n).Error; err != nil || n != 0 {
			t.Fatal("delete retained index", n, err)
		}
	}
	// Start the actual production binary, with no direct Step call. It must
	// discover the durable pending row and stop cleanly on SIGTERM.
	pending := f.upload(t, f.alice, "server-worker.txt", []byte("服务启动后自动建立文本索引。"))
	runLiveIndexServer(t, f, pending.ID)
	waitNoReadLeases(t, f, *pending.StorageID)
}

func runLiveIndexServer(t *testing.T, f *fixture, fileID int64) {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "cloudnest")
	if output, err := exec.Command("go", "build", "-o", binary, "../cmd/server").CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	command := exec.Command(binary)
	command.Dir = t.TempDir()
	command.Env = []string{"MYSQL_DSN=" + f.cfg.MySQLDSN, "REDIS_ADDR=" + f.cfg.RedisAddr, "JWT_SECRET=" + f.cfg.JWTSecret, "JWT_EXPIRE_HOURS=1", "DATA_DIR=" + f.cfg.DataDir, "APP_BIND_ADDR=127.0.0.1", fmt.Sprintf("APP_PORT=%d", port), "AI_OLLAMA_BASE_URL=http://127.0.0.1:11434", "AI_EMBEDDING_MODEL=qwen3-embedding:0.6b"}
	logFile, err := os.Create(filepath.Join(command.Dir, "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	stopped := false
	defer func() {
		if !stopped {
			command.Process.Kill()
			<-done
		}
	}()
	deadline := time.Now().Add(30 * time.Second)
	for {
		j := readIndex(t, f, fileID)
		if j.Status == "done" {
			break
		}
		if j.Status == "failed" || j.Status == "retry" || time.Now().After(deadline) {
			t.Fatalf("server worker did not publish: %s/%s", j.Status, j.ErrorCode)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		stopped = true
		if err != nil {
			t.Fatal("server shutdown", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("index worker prevented server shutdown")
	}
}

func decodeVector(t *testing.T, raw []byte) []float32 {
	t.Helper()
	if len(raw) != 4096 {
		t.Fatal("unexpected model dimensions", len(raw))
	}
	v := make([]float32, len(raw)/4)
	var norm float64
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
		if math.IsInf(float64(v[i]), 0) || math.IsNaN(float64(v[i])) {
			t.Fatal("non-finite stored vector")
		}
		norm += float64(v[i]) * float64(v[i])
	}
	if math.Abs(norm-1) > 1e-4 {
		t.Fatal("vector not normalized", norm)
	}
	return v
}

func TestAITextLiveHybridSearchReturnsRealSemanticMatchesAndFallback(t *testing.T) {
	if os.Getenv("CLOUDNEST_TEST_AI_MODEL") != "1" {
		t.Fatal("run scripts/test-ai-index-model.sh")
	}
	f, _ := indexFixture(t)
	f.cfg.AIOllamaBaseURL = "http://127.0.0.1:11434"
	f.cfg.AIEmbeddingModel = "qwen3-embedding:0.6b"
	f.router = fixtureRouter(app.NewContainer(f.cfg, f.db, f.rdb).NewRouter())
	texts := []string{"数据库迁移方案：MySQL 升级前先备份，并验证迁移后的数据一致性。", "海边日落的风景照片，沙滩与海浪。", "数据库迁移方案：MySQL 升级前先备份，并验证迁移后的数据一致性。PRIVATE BOB"}
	files := make([]model.File, len(texts))
	for i, text := range texts {
		token := f.alice
		if i == 2 {
			token = f.bob
		}
		files[i] = f.upload(t, token, fmt.Sprintf("neutral-%d.md", i), []byte(text))
		if worked, err := textWorker(t, f, f.cfg.AIOllamaBaseURL).Step(context.Background()); !worked || err != nil {
			t.Fatal("actual text indexing", worked, err)
		}
	}
	type response struct {
		Items []struct {
			ID       int64    `json:"id"`
			Snippet  string   `json:"snippet"`
			Sources  []string `json:"match_sources"`
			Semantic *float64 `json:"semantic_score"`
		} `json:"items"`
		Retrieval struct {
			Mode     string   `json:"mode"`
			Warnings []string `json:"warnings"`
		} `json:"retrieval"`
	}
	var r response
	decode(t, f.json(t, "POST", "/api/ai/search/nl", f.alice, map[string]any{"question": "寻找数据搬迁与 MySQL 升级的执行步骤"}, 200), &r)
	if r.Retrieval.Mode != "hybrid" || len(r.Items) == 0 || r.Items[0].ID != files[0].ID || r.Items[0].Semantic == nil {
		t.Fatalf("actual semantic retrieval failed: %+v", r)
	}
	for _, item := range r.Items {
		if item.ID == files[2].ID || strings.Contains(item.Snippet, "PRIVATE BOB") {
			t.Fatal("cross-user semantic leak")
		}
	}
	if !strings.Contains(r.Items[0].Snippet, "迁移") {
		t.Fatal("semantic hit missing original excerpt")
	}
	t.Logf("actual hybrid first_file=%d semantic_score=%.6f sources=%v", r.Items[0].ID, *r.Items[0].Semantic, r.Items[0].Sources)
	// Changing model identity must exclude vectors, while current text remains
	// usable by lexical ranking. No cross-model cosine is permitted.
	if err := f.db.Model(&model.AIIndexDocument{}).Where("file_id=?", files[0].ID).Update("model_digest", strings.Repeat("0", 64)).Error; err != nil {
		t.Fatal(err)
	}
	r = response{}
	decode(t, f.json(t, "POST", "/api/ai/search/nl", f.alice, map[string]any{"question": "数据库迁移"}, 200), &r)
	found := false
	for _, item := range r.Items {
		if item.ID == files[0].ID {
			found = true
			if item.Semantic != nil {
				t.Fatal("mixed model identities")
			}
		}
	}
	if !found {
		t.Fatal("model identity change removed valid keyword content")
	}
	f.cfg.AIOllamaBaseURL = "http://127.0.0.1:1"
	f.router = fixtureRouter(app.NewContainer(f.cfg, f.db, f.rdb).NewRouter())
	r = response{}
	decode(t, f.json(t, "POST", "/api/ai/search/nl", f.alice, map[string]any{"question": "数据库迁移"}, 200), &r)
	if r.Retrieval.Mode != "keyword_fallback" || len(r.Retrieval.Warnings) == 0 || len(r.Items) != 1 || r.Items[0].ID != files[0].ID {
		t.Fatalf("model outage lost actual indexed text: %+v", r)
	}
}
