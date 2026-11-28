package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cloudenest_go/internal/aiindex"
	"cloudenest_go/internal/config"
)

// The provider is synthetic. These cases verify protocol and disclosure
// boundaries, not actual summary quality.
func TestAIGenerationRejectsIncompleteAndUnboundedProviderResults(t *testing.T) {
	for _, kind := range []string{"missing-done", "wrong-model", "length", "empty", "large-content", "large-json", "provider-error"} {
		t.Run(kind, func(t *testing.T) {
			const marker = "private-provider-marker"
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/tags" {
					json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]string{"name": "fixture:latest", "digest": strings.Repeat("a", 64)}}})
					return
				}
				out := map[string]any{"model": "fixture:latest", "done": true, "done_reason": "stop", "response": marker}
				switch kind {
				case "missing-done":
					delete(out, "done")
				case "wrong-model":
					out["model"] = "other:latest"
				case "length":
					out["done_reason"] = "length"
				case "empty":
					out["response"] = " "
				case "large-content":
					out["response"] = strings.Repeat("x", (32<<10)+1)
				case "large-json":
					out["padding"] = strings.Repeat("x", (2<<20)+1)
				case "provider-error":
					w.WriteHeader(503)
					out = map[string]any{"error": marker}
				}
				json.NewEncoder(w).Encode(out)
			}))
			defer provider.Close()
			s := NewAIService(nil, config.Config{AIOllamaBaseURL: provider.URL, AIOllamaModel: "fixture:latest"})
			got, err := s.callOllamaGenerate(context.Background(), "synthetic test prompt", nil)
			if err == nil || got != "" {
				t.Fatal("unsafe provider result accepted", kind, len(got))
			}
			if strings.Contains(err.Error(), marker) {
				t.Fatal("provider content disclosed in error")
			}
		})
	}
}

func TestSummaryTextKeepsUTF8BoundariesAndRejectsCorruption(t *testing.T) {
	text, truncated, err := readTextWithLimit(strings.NewReader("資料🚀末尾"), 5)
	if err != nil || !truncated || text != "資" {
		t.Fatal("UTF-8 prefix lost", text, truncated, err)
	}
	if _, _, err := readTextWithLimit(strings.NewReader(string([]byte{'a', 255, 'b'})), 10); !errors.Is(err, aiindex.ErrDocument) {
		t.Fatal("invalid source accepted", err)
	}
}
