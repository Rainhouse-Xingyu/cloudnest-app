//go:build integration && darwin

package integration

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"cloudenest_go/internal/app"
	"cloudenest_go/model"
)

// Opt-in real Ollama -> persisted index -> TLS Go API -> shipping Swift core.
// No model responses or vectors are substituted by the harness.
func TestMacOSClientLiveAISearch(t *testing.T) {
	if os.Getenv("CLOUDNEST_TEST_SWIFT") != "1" || os.Getenv("CLOUDNEST_TEST_AI_MODEL") != "1" {
		t.Skip("requires the disposable macOS runner and explicit local model acceptance")
	}
	f, _ := indexFixture(t)
	f.cfg.AIOllamaBaseURL = "http://127.0.0.1:11434"
	f.cfg.AIEmbeddingModel = "qwen3-embedding:0.6b"
	f.router = fixtureRouter(app.NewContainer(f.cfg, f.db, f.rdb).NewRouter())
	content := "数据库迁移方案：MySQL 升级前先备份，并验证迁移后的数据一致性。迁移失败时回滚到已验证的备份。"
	owner := f.upload(t, f.alice, "Project Atlas.md", []byte(content))
	f.upload(t, f.bob, "Private Atlas.md", []byte(content))
	f.upload(t, f.alice, "Seaside.md", []byte("海边日落的风景照片，沙滩与海浪。"))
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	worker := textWorker(t, f, f.cfg.AIOllamaBaseURL)
	for i := 0; i < 3; i++ {
		if worked, err := worker.Step(ctx); !worked || err != nil {
			t.Fatal("real indexing required", worked, err)
		}
	}
	var document model.AIIndexDocument
	if err := f.db.First(&document, "file_id=?", owner.ID).Error; err != nil {
		t.Fatal(err)
	}
	t.Logf("real embedding model=%s digest=%s dimensions=%d chunks=%d", document.EmbeddingModel, document.ModelDigest, document.Dimensions, document.ChunkCount)
	server := httptest.NewTLSServer(f.router)
	defer server.Close()
	certificate := filepath.Join(t.TempDir(), "server.der")
	if err := os.WriteFile(certificate, server.Certificate().Raw, 0600); err != nil {
		t.Fatal(err)
	}
	var user model.User
	if err := f.db.First(&user, f.aliceID).Error; err != nil {
		t.Fatal(err)
	}
	packagePath, err := filepath.Abs("../../cloudnest_macos")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, "swift", "test", "--package-path", packagePath, "--filter", "realBackendLiveAISemanticSearch")
	command.Env = append(os.Environ(), "CLOUDNEST_SWIFT_ORIGIN="+server.URL,
		"CLOUDNEST_SWIFT_USERNAME="+user.Username, "CLOUDNEST_SWIFT_PASSWORD=disposable-fixture-password",
		"CLOUDNEST_SWIFT_CERTIFICATE="+certificate, "CLOUDNEST_SWIFT_AI_LIVE=1",
		"CLOUDNEST_SWIFT_SEARCH_FILE="+strconv.FormatInt(owner.ID, 10))
	output, err := command.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatal(err)
	}
}
