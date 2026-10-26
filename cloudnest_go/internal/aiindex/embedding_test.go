package aiindex

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Synthetic vectors here exercise protocol validation only. Actual embeddings
// and database publication are tested separately with the aimodel tag.
func TestEmbedUsesDedicatedEndpointAndNormalizesVectors(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/embed" {
			t.Error("wrong embedding endpoint")
		}
		var req struct {
			Model     string
			Input     []string
			Truncate  *bool
			KeepAlive string `json:"keep_alive"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req.Model != "embed:test" || len(req.Input) != 2 || req.Truncate == nil || *req.Truncate || req.KeepAlive != "1m" {
			t.Error("unbounded or wrong model request", req)
		}
		w.Write([]byte(`{"model":"embed:test","embeddings":[[3,4],[0,5]]}`))
	}))
	defer s.Close()
	o, err := NewOllama(s.URL, "embed:test")
	if err != nil {
		t.Fatal(err)
	}
	vectors, err := o.Embed(context.Background(), []string{"first", "second"})
	if err != nil || len(vectors) != 2 {
		t.Fatal(vectors, err)
	}
	if math.Abs(float64(vectors[0][0])-.6) > 1e-6 || vectors[1][1] != 1 {
		t.Fatal("invalid normalized output", vectors)
	}
}

func TestEmbedRejectsMalformedOrOversizedResults(t *testing.T) {
	for _, body := range []string{
		`{"model":"other","embeddings":[[1,2]]}`,
		`{"model":"embed:test","embeddings":[]}`,
		`{"model":"embed:test","embeddings":[[]]}`,
		`{"model":"embed:test","embeddings":[[0,0]]}`,
		`{"model":"embed:test","embeddings":[[1e309]]}`,
		`{"model":"embed:test","embeddings":[[1,2],[3]]}`,
		`{"model":"embed:test","embeddings":[[1,2]]} {}`,
		strings.Repeat(" ", (2<<20)+1),
	} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
		o, _ := NewOllama(s.URL, "embed:test")
		_, err := o.Embed(context.Background(), []string{"input"})
		s.Close()
		if !errors.Is(err, ErrEmbedding) {
			t.Fatal("invalid response accepted", len(body), err)
		}
	}
}

func TestEmbedRefusesRedirectsAndPropagatesCancellation(t *testing.T) {
	arrived := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { arrived = true }))
	defer target.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer s.Close()
	o, _ := NewOllama(s.URL, "embed:test")
	if _, err := o.Embed(context.Background(), []string{"private document"}); !errors.Is(err, ErrModelUnavailable) || arrived {
		t.Fatal("redirect sent document elsewhere", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := o.Embed(ctx, []string{"text"}); !errors.Is(err, context.Canceled) {
		t.Fatal("lost cancellation", err)
	}
}

func TestEmbeddingDigestAndConfiguration(t *testing.T) {
	digest := strings.Repeat("a", 64)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Error("wrong digest endpoint")
		}
		json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": "embed:latest", "digest": digest}}})
	}))
	defer s.Close()
	o, _ := NewOllama(s.URL, "embed")
	if got, err := o.Digest(context.Background()); err != nil || got != digest {
		t.Fatal(got, err)
	}
	for _, base := range []string{"file:///tmp", "http://u:p@localhost", "http://localhost?token=secret", "http://localhost/#fragment", ""} {
		if _, err := NewOllama(base, "embed"); err == nil {
			t.Fatal("invalid base accepted")
		}
	}
}
