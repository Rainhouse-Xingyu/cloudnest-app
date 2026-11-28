package aiindex

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"
)

var ErrGenerationResponse = errors.New("invalid generation response")
var ErrGenerationOutputLimit = fmt.Errorf("%w: output token limit", ErrGenerationResponse)

// Generate shares provider transport, digest fencing and process-wide admission
// with visual generation. No error contains provider text or request contents.
func (o *Ollama) Generate(ctx context.Context, prompt string) (string, string, error) {
	if len(prompt) > 256<<10 || !utf8.ValidString(prompt) || strings.IndexByte(prompt, 0) >= 0 || strings.TrimSpace(prompt) == "" {
		return "", "", ErrTextLimit
	}
	release, err := admitGeneration(ctx)
	if err != nil {
		return "", "", err
	}
	defer release()
	digest, err := o.Digest(ctx)
	if err != nil {
		return "", "", err
	}
	var out struct {
		Model    string `json:"model"`
		Done     bool   `json:"done"`
		Reason   string `json:"done_reason"`
		Response string `json:"response"`
		Error    string `json:"error"`
	}
	body := map[string]any{"model": o.model, "prompt": prompt, "stream": false, "think": false, "keep_alive": "1m", "options": map[string]any{"temperature": 0, "num_predict": 4096, "num_ctx": 8192}}
	if err := o.request(ctx, http.MethodPost, "/api/generate", body, &out); err != nil {
		if errors.Is(err, ErrEmbedding) {
			return "", "", ErrGenerationResponse
		}
		return "", "", err
	}
	if out.Model == o.model && out.Done && out.Reason == "length" {
		return "", "", ErrGenerationOutputLimit
	}
	text := strings.TrimSpace(out.Response)
	if out.Model != o.model || !out.Done || out.Reason != "stop" || out.Error != "" || text == "" || !utf8.ValidString(text) || strings.IndexByte(text, 0) >= 0 || len(text) > 32<<10 {
		return "", "", ErrGenerationResponse
	}
	confirmed, err := o.Digest(ctx)
	if err != nil {
		return "", "", err
	}
	if confirmed != digest {
		return "", "", ErrModelChanged
	}
	return text, digest, nil
}
