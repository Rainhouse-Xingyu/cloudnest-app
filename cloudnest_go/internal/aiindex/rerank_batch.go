package aiindex

import (
	"context"
	"encoding/hex"
	"errors"
	"math"
	"net/http"
	"strings"
)

// BatchReranker uses the llama.cpp documents API of a dedicated reranking
// server. Its model alias must include the deployment's verified GGUF digest.
type BatchReranker struct {
	client *Ollama
	digest string
}

func NewBatchReranker(base, model string) (*BatchReranker, error) {
	name, digest, found := strings.Cut(model, "@sha256:")
	raw, err := hex.DecodeString(digest)
	if !found || name == "" || err != nil || len(raw) != 32 || hex.EncodeToString(raw) != digest {
		return nil, ErrRerankResponse
	}
	client, err := NewOllama(base, model)
	if err != nil {
		return nil, err
	}
	return &BatchReranker{client: client, digest: digest}, nil
}

// Digest checks the serving model alias. The server API exposes the alias,
// so the deployment must verify the model file before assigning that alias.
func (b *BatchReranker) Digest(ctx context.Context) (string, error) {
	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := b.client.request(ctx, http.MethodGet, "/v1/models", nil, &response); err != nil {
		return "", err
	}
	if len(response.Data) != 1 || response.Data[0].ID != b.client.model {
		return "", ErrModelChanged
	}
	return b.digest, nil
}

func (b *BatchReranker) Rerank(parent context.Context, question string, documents []string) ([]float64, string, error) {
	ctx, cancel := context.WithTimeout(parent, RerankTimeout)
	defer cancel()
	if err := validateRerankInput(question, documents); err != nil {
		return nil, "", err
	}
	release, err := admitGeneration(ctx)
	if err != nil {
		return nil, "", err
	}
	defer release()
	digest, err := b.Digest(ctx)
	if err != nil {
		return nil, "", err
	}
	// Avoid interpreting source text as model role or media control tokens.
	escape := strings.NewReplacer("<|", "< ", "<__media__>", "< __media__>")
	texts := make([]string, len(documents))
	for i, text := range documents {
		texts[i] = escape.Replace(text)
	}
	body := map[string]any{
		"model": b.client.model, "query": escape.Replace(question),
		"documents": texts, "top_n": len(texts),
	}
	var response struct {
		Model   string `json:"model"`
		Results []struct {
			Index *int     `json:"index"`
			Score *float64 `json:"relevance_score"`
		} `json:"results"`
	}
	if err := b.client.request(ctx, http.MethodPost, "/v1/rerank", body, &response); err != nil {
		if errors.Is(err, ErrEmbedding) {
			err = ErrRerankResponse
		}
		return nil, "", err
	}
	if response.Model != b.client.model || len(response.Results) != len(texts) {
		return nil, "", ErrRerankResponse
	}
	scores := make([]float64, len(texts))
	seen := make([]bool, len(texts))
	for _, row := range response.Results {
		if row.Index == nil || row.Score == nil || *row.Index < 0 || *row.Index >= len(texts) ||
			seen[*row.Index] || math.IsNaN(*row.Score) || math.IsInf(*row.Score, 0) {
			return nil, "", ErrRerankResponse
		}
		seen[*row.Index] = true
		scores[*row.Index] = *row.Score
	}
	if _, err := b.Digest(ctx); err != nil {
		return nil, "", err
	}
	return scores, digest, nil
}
