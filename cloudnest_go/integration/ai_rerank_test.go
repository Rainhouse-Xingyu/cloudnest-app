//go:build integration && aisearch

package integration

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"cloudenest_go/internal/app"
	"cloudenest_go/internal/indexqueue"
	"cloudenest_go/internal/service"
	"cloudenest_go/model"
	"gorm.io/gorm"
)

// Deliberately scripted scores test the real API/SQL race boundary, not quality.
func TestAIHybridRerankRechecksSQLAfterInference(t *testing.T) {
	for _, mode := range []string{"revoke", "revoke-failure", "rename", "source", "generation"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := indexFixture(t)
			shared := f.upload(t, f.bob, "shared.md", []byte("invoice shared"))
			lexicalIndexFixture(t, f, shared, "invoice old confidential")
			own := f.upload(t, f.alice, "own.md", []byte("invoice own"))
			lexicalIndexFixture(t, f, own, "invoice safe current")
			if err := f.db.Create(&model.FilePermission{FileID: shared.ID, UserID: f.aliceID, Permission: "read"}).Error; err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/tags":
					json.NewEncoder(w).Encode(map[string]any{"models": []any{
						map[string]any{"name": "embed:test", "digest": strings.Repeat("a", 64)},
						map[string]any{"name": "rank:test", "digest": strings.Repeat("b", 64)},
					}})
				case "/api/embed":
					json.NewEncoder(w).Encode(map[string]any{"model": "embed:test", "embeddings": [][]float64{{1, 0}}})
				case "/api/generate":
					io.Copy(io.Discard, r.Body)
					if calls.Add(1) == 1 {
						var err error
						switch mode {
						case "revoke", "revoke-failure":
							err = f.db.Where("file_id=? AND user_id=?", shared.ID, f.aliceID).Delete(&model.FilePermission{}).Error
						case "rename":
							err = f.db.Model(&model.File{}).Where("id=?", shared.ID).Update("filename", "new.md").Error
						case "source":
							err = f.db.Model(&model.File{}).Where("id=?", shared.ID).Update("storage_id", own.StorageID).Error
						case "generation":
							err = f.db.Transaction(func(tx *gorm.DB) error { return indexqueue.Enqueue(tx, shared.ID, true) })
						}
						if err != nil {
							t.Error(err)
							w.WriteHeader(500)
							return
						}
					}
					if mode == "revoke-failure" {
						w.WriteHeader(503)
						return
					}
					yes := map[string]any{"token": "yes", "bytes": []int{121, 101, 115}, "logprob": math.Log(.8)}
					no := map[string]any{"token": "no", "bytes": []int{110, 111}, "logprob": math.Log(.2)}
					chosen := map[string]any{"token": "yes", "bytes": []int{121, 101, 115}, "logprob": math.Log(.8), "top_logprobs": []any{yes, no}}
					json.NewEncoder(w).Encode(map[string]any{"model": "rank:test", "done": true, "done_reason": "length", "eval_count": 1, "response": "yes", "logprobs": []any{chosen}})
				default:
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			f.cfg.AIOllamaBaseURL, f.cfg.AIEmbeddingModel, f.cfg.AIRerankModel = server.URL, "embed:test", "rank:test"
			f.router = fixtureRouter(app.NewContainer(f.cfg, f.db, f.rdb).NewRouter())
			var reply struct {
				Items []struct {
					ID      int64    `json:"id"`
					Snippet string   `json:"snippet"`
					Score   *float64 `json:"rerank_score"`
				} `json:"items"`
				Pagination struct {
					Total int64 `json:"total"`
				} `json:"pagination"`
				Retrieval struct {
					Rerank service.AIRerankResult `json:"rerank"`
				} `json:"retrieval"`
			}
			decode(t, f.json(t, "POST", "/api/ai/search/nl", f.alice, map[string]any{"question": "invoice"}, 200), &reply)
			if len(reply.Items) != 1 || reply.Items[0].ID != own.ID || reply.Pagination.Total != 1 || strings.Contains(reply.Items[0].Snippet, "confidential") {
				t.Fatalf("stale SQL snapshot escaped: %+v", reply)
			}
			status := "applied"
			if mode == "revoke-failure" {
				status = "fallback"
			}
			if reply.Retrieval.Rerank.Status != status || (reply.Items[0].Score != nil) != (status == "applied") || calls.Load() == 0 {
				t.Fatal(reply.Retrieval, calls.Load())
			}
		})
	}
}
