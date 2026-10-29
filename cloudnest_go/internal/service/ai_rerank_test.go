package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cloudenest_go/internal/config"
	"cloudenest_go/internal/service/contract"
)

type rerankFenceRepo struct {
	hybridFixtureRepo
	checks  int
	changed *atomic.Bool
}

func (r *rerankFenceRepo) ValidateHybrid(ctx context.Context, uid int64, rows []contract.AIHybridRow) (map[int64]bool, error) {
	r.checks++
	valid := map[int64]bool{}
	for _, row := range rows {
		valid[row.ID] = r.allowed[row.ID] && !(row.ID == 1 && r.changed.Load())
	}
	return valid, ctx.Err()
}

// Protocol fixtures verify fallback/order/fencing only, never real AI quality.
func TestHybridRerankPreservesTailAndRevalidatesAfterEveryAttempt(t *testing.T) {
	for _, mode := range []string{"success", "partial-failure", "revoke-success", "revoke-failure", "cancel", "disabled", "embedding-outage"} {
		t.Run(mode, func(t *testing.T) {
			var changed atomic.Bool
			var calls atomic.Int32
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/tags":
					json.NewEncoder(w).Encode(map[string]any{"models": []any{
						map[string]any{"name": "embed:test", "digest": strings.Repeat("a", 64)},
						map[string]any{"name": "rank:test", "digest": strings.Repeat("b", 64)},
					}})
				case "/api/embed":
					if mode == "embedding-outage" {
						w.WriteHeader(503)
						return
					}
					json.NewEncoder(w).Encode(map[string]any{"model": "embed:test", "embeddings": [][]float64{{1, 0}}})
				case "/api/generate":
					io.Copy(io.Discard, r.Body)
					call := calls.Add(1)
					if strings.HasPrefix(mode, "revoke-") {
						changed.Store(true)
					}
					if mode == "cancel" {
						cancel()
						<-r.Context().Done()
						return
					}
					if (mode == "partial-failure" || mode == "revoke-failure") && call == 2 {
						w.WriteHeader(503)
						return
					}
					p := .1
					if call == 20 {
						p = .9
					}
					yes := map[string]any{"token": "yes", "bytes": []int{121, 101, 115}, "logprob": math.Log(p)}
					no := map[string]any{"token": "no", "bytes": []int{110, 111}, "logprob": math.Log(1 - p)}
					selected := map[string]any{"token": "yes", "bytes": []int{121, 101, 115}, "logprob": math.Log(p), "top_logprobs": []any{yes, no}}
					json.NewEncoder(w).Encode(map[string]any{"model": "rank:test", "done": true, "done_reason": "length", "eval_count": 1, "response": "yes", "logprobs": []any{selected}})
				default:
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			repo := &rerankFenceRepo{hybridFixtureRepo: hybridFixtureRepo{allowed: map[int64]bool{}}, changed: &changed}
			for id := int64(1); id <= 25; id++ {
				repo.rows = append(repo.rows, contract.AIHybridRow{ID: id, Filename: "neutral.txt", Content: "invoice", Generation: 1})
				repo.allowed[id] = true
			}
			cfg := config.Config{AIOllamaBaseURL: server.URL, AIEmbeddingModel: "embed:test", AIRerankModel: "rank:test"}
			if mode == "disabled" {
				cfg.AIRerankModel = ""
			}
			s := NewAIService(repo, cfg, nil)
			result, err := s.NLSearch(ctx, 1, "invoice", 1, 100)
			if mode == "cancel" {
				if !errors.Is(err, context.Canceled) || len(result.Search.Items) != 0 {
					t.Fatal(result, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantCalls, checks := int32(20), 2
			status := "applied"
			switch mode {
			case "partial-failure", "revoke-failure":
				wantCalls, status = 2, "fallback"
			case "disabled":
				wantCalls, checks, status = 0, 1, "disabled"
			case "embedding-outage":
				wantCalls, checks, status = 0, 1, "skipped"
			}
			if calls.Load() != wantCalls || repo.checks != checks || result.Rerank.Status != status {
				t.Fatal(calls.Load(), repo.checks, result.Rerank)
			}
			want := []int64{}
			if status == "applied" {
				want = append(want, 20)
			}
			for id := int64(1); id <= 25; id++ {
				if (status == "applied" && id == 20) || (changed.Load() && id == 1) {
					continue
				}
				want = append(want, id)
			}
			if result.Search.Total != int64(len(want)) || len(result.Search.Items) != len(want) {
				t.Fatal("lost tail or stale total", result.Search)
			}
			for i, item := range result.Search.Items {
				if item["id"] != want[i] {
					t.Fatal("unexpected order", i, item["id"], want[i])
				}
				_, scored := item["rerank_score"]
				if scored != (status == "applied" && want[i] <= 20) {
					t.Fatal("partial or invented scores", item)
				}
			}
			if status == "applied" && (result.Rerank.EvaluatedCount != 20 || !result.Rerank.WindowLimited || result.Rerank.ModelDigest != strings.Repeat("b", 64)) {
				t.Fatal(result.Rerank)
			}
			if status == "fallback" && (result.Rerank.EvaluatedCount != 0 || result.Rerank.ModelDigest != "" || len(result.Warnings) != 1 || result.Warnings[0] != "rerank_unavailable") {
				t.Fatal(result)
			}
		})
	}
}

func TestHybridRerankParentDeadlineDoesNotReturnPartialResults(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	s := NewAIService(hybridFixtureRepo{}, config.Config{AIRerankModel: "rank:test"}, nil)
	result := AINLSearchResult{}
	err := s.rerankHybrid(ctx, "invoice", []hybridHit{{row: contract.AIHybridRow{Content: "invoice"}}}, &result)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
