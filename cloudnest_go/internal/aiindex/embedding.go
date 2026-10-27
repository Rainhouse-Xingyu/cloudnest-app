package aiindex

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const EmbedBatch = 8

var (
	ErrModelUnavailable = errors.New("embedding model unavailable")
	ErrEmbedding        = errors.New("invalid embedding response")
	ErrModelChanged     = errors.New("embedding model changed during indexing")
)

type Ollama struct {
	base   string
	model  string
	client *http.Client
}

func NewOllama(base, model string) (*Ollama, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimSpace(model) != model || model == "" || len(model) > 200 {
		return nil, errors.New("invalid embedding configuration")
	}
	return &Ollama{strings.TrimRight(base, "/"), model, &http.Client{
		Timeout:       120 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (o *Ollama) Model() string { return o.model }

func (o *Ollama) request(ctx context.Context, method, path string, body any, output any) error {
	var encoded []byte
	var err error
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			return ErrEmbedding
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, o.base+path, bytes.NewReader(encoded))
	if err != nil {
		return ErrModelUnavailable
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := o.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrModelUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ErrModelUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrModelUnavailable
	}
	// encoding/json replaces invalid UTF-8 with U+FFFD. Reject the original
	// bytes first so corrupted model text cannot become valid-looking content.
	if len(raw) > 2<<20 || !utf8.Valid(raw) || json.Unmarshal(raw, output) != nil {
		return ErrEmbedding
	}
	return nil
}

// Digest identifies the local installed model. Call before and after a batch of
// work; tag changes invalidate publication. Operators must not retag models
// during indexing (the remote API does not offer a transactional model snapshot).
func (o *Ollama) Digest(ctx context.Context) (string, error) {
	var response struct {
		Models []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"models"`
	}
	if err := o.request(ctx, http.MethodGet, "/api/tags", nil, &response); err != nil {
		return "", err
	}
	name := o.model
	if !strings.Contains(name, ":") {
		name += ":latest"
	}
	for _, m := range response.Models {
		if m.Name == name {
			b, err := hex.DecodeString(m.Digest)
			if err != nil || len(b) != 32 || hex.EncodeToString(b) != m.Digest {
				return "", ErrEmbedding
			}
			return m.Digest, nil
		}
	}
	return "", ErrModelUnavailable
}

// Embed calls the dedicated embedding endpoint, never the text/vision model.
// Counts, dimensions, finite values and nonzero norm are checked before storage.
func (o *Ollama) Embed(ctx context.Context, input []string) ([][]float32, error) {
	if len(input) == 0 || len(input) > EmbedBatch {
		return nil, ErrEmbedding
	}
	for _, text := range input {
		if !utf8.ValidString(text) || strings.TrimSpace(text) == "" || len(text) > 8192 {
			return nil, ErrEmbedding
		}
	}
	var response struct {
		Model      string      `json:"model"`
		Embeddings [][]float64 `json:"embeddings"`
	}
	body := map[string]any{"model": o.model, "input": input, "truncate": false, "keep_alive": "1m"}
	if err := o.request(ctx, http.MethodPost, "/api/embed", body, &response); err != nil {
		return nil, err
	}
	if response.Model != o.model || len(response.Embeddings) != len(input) {
		return nil, ErrEmbedding
	}
	dimensions := len(response.Embeddings[0])
	if dimensions < 1 || dimensions > 4096 {
		return nil, ErrEmbedding
	}
	out := make([][]float32, len(input))
	for i, vector := range response.Embeddings {
		if len(vector) != dimensions {
			return nil, ErrEmbedding
		}
		var norm float64
		for _, n := range vector {
			if math.IsNaN(n) || math.IsInf(n, 0) {
				return nil, ErrEmbedding
			}
			norm += n * n
		}
		if norm <= 0 || math.IsInf(norm, 0) {
			return nil, ErrEmbedding
		}
		norm = math.Sqrt(norm)
		out[i] = make([]float32, dimensions)
		for j, n := range vector {
			out[i][j] = float32(n / norm)
		}
	}
	return out, nil
}
