package aiindex

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBatchRerankerRequiresCompleteIndexedScoresAndStableAlias(t *testing.T) {
	wantDigest := strings.Repeat("a", 64)
	alias := "qwen@" + "sha256:" + wantDigest
	for _, mode := range []string{"success", "duplicate", "partial", "missing-index", "missing-score", "alias-changed"} {
		t.Run(mode, func(t *testing.T) {
			listings, requests := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/models":
					listings++
					id := alias
					if listings == 2 && mode == "alias-changed" {
						id = "different"
					}
					json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"id": id}}})
				case "/v1/rerank":
					requests++
					var request struct {
						Model     string
						Query     string
						Documents []string
						TopN      int `json:"top_n"`
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					if request.Model != alias || request.TopN != 2 || len(request.Documents) != 2 ||
						strings.Contains(request.Query, "<|") || strings.Contains(request.Documents[1], "<__media__>") {
						t.Error("unexpected batch request", request)
					}
					results := []map[string]any{{"index": 1, "relevance_score": 3.2}, {"index": 0, "relevance_score": -2.1}}
					switch mode {
					case "duplicate":
						results[1]["index"] = 1
					case "partial":
						results = results[:1]
					case "missing-index":
						delete(results[1], "index")
					case "missing-score":
						results[1]["relevance_score"] = nil
					}
					json.NewEncoder(w).Encode(map[string]any{"model": alias, "results": results})
				default:
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			provider, err := NewBatchReranker(server.URL, alias)
			if err != nil {
				t.Fatal(err)
			}
			scores, digest, err := provider.Rerank(context.Background(), "<|query", []string{"first", "<__media__>second"})
			switch mode {
			case "success":
				if err != nil || digest != wantDigest || len(scores) != 2 || scores[0] != -2.1 || scores[1] != 3.2 || requests != 1 || listings != 2 {
					t.Fatal(scores, digest, err, requests, listings)
				}
			case "alias-changed":
				if !errors.Is(err, ErrModelChanged) || scores != nil || digest != "" {
					t.Fatal(scores, digest, err)
				}
			default:
				if !errors.Is(err, ErrRerankResponse) || scores != nil || digest != "" {
					t.Fatal(scores, digest, err)
				}
			}
		})
	}
}
