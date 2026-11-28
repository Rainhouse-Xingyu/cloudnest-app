package aiindex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGenerateChecksProtocolDigestAndRedirects(t *testing.T) {
	for _, mode := range []string{"valid", "digest-change", "redirect", "invalid-utf8"} {
		t.Run(mode, func(t *testing.T) {
			var tags, redirected atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
			defer target.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/tags" {
					digest := strings.Repeat("a", 64)
					if tags.Add(1) > 1 && mode == "digest-change" {
						digest = strings.Repeat("b", 64)
					}
					json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]string{"name": "fixture:latest", "digest": digest}}})
					return
				}
				var body struct {
					Model, Prompt string
					Stream, Think bool
					KeepAlive     string `json:"keep_alive"`
					Options       struct {
						Temperature float64
						Predict     int `json:"num_predict"`
						Context     int `json:"num_ctx"`
					}
				}
				if r.URL.Path != "/api/generate" || json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "fixture:latest" || body.Prompt != "synthetic prompt" || body.Stream || body.Think || body.KeepAlive != "1m" || body.Options.Temperature != 0 || body.Options.Predict != 4096 || body.Options.Context != 8192 {
					t.Error("generation protocol or budget changed")
				}
				if mode == "redirect" {
					http.Redirect(w, r, target.URL, 307)
					return
				}
				if mode == "invalid-utf8" {
					w.Write(append([]byte(`{"model":"fixture:latest","done":true,"done_reason":"stop","response":"`), append([]byte{255}, []byte(`"}`)...)...))
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"model": "fixture:latest", "done": true, "done_reason": "stop", "response": "摘要🚀"})
			}))
			defer server.Close()
			o, err := NewOllama(server.URL, "fixture:latest")
			if err != nil {
				t.Fatal(err)
			}
			got, digest, err := o.Generate(context.Background(), "synthetic prompt")
			if mode == "valid" {
				if err != nil || got != "摘要🚀" || digest != strings.Repeat("a", 64) || tags.Load() != 2 {
					t.Fatal(got, digest, err)
				}
			} else if err == nil || got != "" || digest != "" {
				t.Fatal("unsafe result accepted", mode)
			}
			if redirected.Load() != 0 {
				t.Fatal("redirect followed")
			}
		})
	}
}

func TestGenerateAdmissionAndCancellationReleaseSharedSlots(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]string{"name": "fixture:latest", "digest": strings.Repeat("a", 64)}}})
			return
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 512<<10))
		entered <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-ctx.Done():
		}
	}))
	defer server.Close()
	o, _ := NewOllama(server.URL, "fixture:latest")
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, _, err := o.Generate(ctx, "synthetic prompt"); results <- err }()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("generation did not enter")
		}
	}
	if _, _, err := o.Generate(ctx, "excess prompt"); !errors.Is(err, ErrExtractionBusy) {
		t.Fatal("shared admission exceeded", err)
	}
	cancel()
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			if !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("cancellation retained worker")
		}
	}
	release, err := admitGeneration(context.Background())
	if err != nil {
		t.Fatal("slot leaked", err)
	}
	release()
}
