package aiindex

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxRerankDocuments = 20
const RerankTimeout = 6 * time.Second

var ErrRerankResponse = errors.New("invalid reranker response")

type tokenProbability struct {
	Token   string  `json:"token"`
	Bytes   []int   `json:"bytes"`
	Logprob float64 `json:"logprob"`
}
type rerankResponse struct {
	Model     string `json:"model"`
	Done      bool   `json:"done"`
	Reason    string `json:"done_reason"`
	Response  string `json:"response"`
	EvalCount int    `json:"eval_count"`
	Logprobs  []struct {
		tokenProbability
		Top []tokenProbability `json:"top_logprobs"`
	} `json:"logprobs"`
}

// Rerank evaluates query/document pairs with a Qwen-compatible binary prompt.
// It requires the actual yes AND no token logprobs; an omitted alternative is
// an invalid response, never an invented zero. Scores are normalized binary
// model scores, not calibrated probabilities of real-world relevance.
// The batch is all-or-nothing, shares inference admission with OCR, and does not
// change indexed document vectors. Callers must revalidate file ACL/source after
// inference and must not use model output to authorize access.
func (o *Ollama) Rerank(parent context.Context, question string, documents []string) ([]float64, string, error) {
	ctx, cancel := context.WithTimeout(parent, RerankTimeout)
	defer cancel()
	valid := func(text string, limit int) bool {
		return utf8.ValidString(text) && strings.TrimSpace(text) != "" && len(text) <= limit && !strings.ContainsRune(text, 0)
	}
	if !valid(question, 4096) || len(documents) == 0 || len(documents) > MaxRerankDocuments {
		return nil, "", ErrTextLimit
	}
	for _, doc := range documents {
		if !valid(doc, 8192) {
			return nil, "", ErrTextLimit
		}
	}
	release, err := admitGeneration(ctx)
	if err != nil {
		return nil, "", err
	}
	defer release()
	digest, err := o.Digest(ctx)
	if err != nil {
		return nil, "", err
	}
	scores := make([]float64, len(documents))
	for i, doc := range documents {
		body := map[string]any{
			"model": o.model, "prompt": rerankPrompt(question, doc), "raw": true, "stream": false,
			"think": false, "truncate": false, "logprobs": true, "top_logprobs": 20, "keep_alive": "1m",
			"options": map[string]any{"num_predict": 1, "temperature": 0, "num_ctx": 16384, "top_k": 0, "top_p": 1, "min_p": 0},
		}
		var response rerankResponse
		if err := o.request(ctx, http.MethodPost, "/api/generate", body, &response); err != nil {
			if errors.Is(err, ErrEmbedding) {
				err = ErrRerankResponse
			}
			return nil, "", err
		}
		score, err := binaryRerankScore(response, o.model)
		if err != nil {
			return nil, "", err
		}
		scores[i] = score
	}
	confirmed, err := o.Digest(ctx)
	if err != nil {
		return nil, "", err
	}
	if confirmed != digest {
		return nil, "", ErrModelChanged
	}
	return scores, digest, nil
}

func rerankPrompt(question, document string) string {
	// File text must not inject tokenizer role delimiters. This reduces control
	// token injection; model ranking is still untrusted and never an ACL decision.
	escape := func(text string) string { return strings.ReplaceAll(text, "<|", "< ") }
	return "<|im_start|>system\nDecide whether the Document satisfies the Query under the Instruct. Reply only \"yes\" or \"no\".<|im_end|>\n<|im_start|>user\n" +
		"<Instruct>: Given a request to find files, retrieve file contents that satisfy the request.\n<Query>: " + escape(question) + "\n<Document>: " + escape(document) +
		"<|im_end|>\n<|im_start|>assistant\n<think>\n\n</think>\n\n"
}

func binaryRerankScore(r rerankResponse, model string) (float64, error) {
	if r.Model != model || !r.Done || (r.Reason != "length" && r.Reason != "stop") || r.EvalCount != 1 || len(r.Logprobs) != 1 || (r.Response != "yes" && r.Response != "no") {
		return 0, ErrRerankResponse
	}
	chosen := r.Logprobs[0]
	validProbability := func(p tokenProbability) bool {
		if p.Token == "" || !utf8.ValidString(p.Token) || math.IsNaN(p.Logprob) || math.IsInf(p.Logprob, 0) || p.Logprob > 0 {
			return false
		}
		raw := []byte(p.Token)
		if len(raw) != len(p.Bytes) {
			return false
		}
		for i, b := range raw {
			if p.Bytes[i] != int(b) {
				return false
			}
		}
		return true
	}
	if chosen.Token != r.Response || !validProbability(chosen.tokenProbability) || len(chosen.Top) < 2 || len(chosen.Top) > 20 {
		return 0, ErrRerankResponse
	}
	probabilities := map[string]float64{}
	for _, p := range chosen.Top {
		if !validProbability(p) {
			return 0, ErrRerankResponse
		}
		if _, duplicate := probabilities[p.Token]; duplicate {
			return 0, ErrRerankResponse
		}
		probabilities[p.Token] = p.Logprob
	}
	yes, hasYes := probabilities["yes"]
	no, hasNo := probabilities["no"]
	if !hasYes || !hasNo || math.Abs(probabilities[chosen.Token]-chosen.Logprob) > 1e-6 {
		return 0, ErrRerankResponse
	}
	// Stable even for highly confident finite logits: never exp(large positive).
	delta := no - yes
	if delta >= 0 {
		e := math.Exp(-delta)
		return e / (1 + e), nil
	}
	return 1 / (1 + math.Exp(delta)), nil
}
