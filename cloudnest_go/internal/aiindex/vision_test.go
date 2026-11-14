package aiindex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// These synthetic HTTP responses exercise protocol rejection only; real OCR
// accuracy and ingestion are tested by TestAITextLivePDF with actual Ollama.
func TestVisionRejectsMalformedTruncatedAndChangedModel(t *testing.T) {
	var imageBytes bytes.Buffer
	if err := png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		payload string
		change  bool
		want    error
	}{
		{"truncated", `{"model":"vision:test","done":true,"done_reason":"length","message":{"role":"assistant","content":"partial"}}`, false, ErrVisionResponse},
		{"wrong model", `{"model":"another:test","done":true,"done_reason":"stop","message":{"role":"assistant","content":"text"}}`, false, ErrVisionResponse},
		{"unfinished", `{"model":"vision:test","done":false,"done_reason":"stop","message":{"role":"assistant","content":"text"}}`, false, ErrVisionResponse},
		{"empty", `{"model":"vision:test","done":true,"done_reason":"stop","message":{"role":"assistant","content":" "}}`, false, ErrVisionResponse},
		{"invalid JSON", `{`, false, ErrVisionResponse},
		{"retagged", `{"model":"vision:test","done":true,"done_reason":"stop","message":{"role":"assistant","content":"text"}}`, true, ErrModelChanged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/tags" {
					calls++
					digest := strings.Repeat("a", 64)
					if tc.change && calls > 1 {
						digest = strings.Repeat("b", 64)
					}
					json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]string{"name": "vision:test", "digest": digest}}})
					return
				}
				var req map[string]any
				if r.URL.Path != "/api/chat" || json.NewDecoder(r.Body).Decode(&req) != nil || req["stream"] != false || req["think"] != false {
					t.Error("wrong OCR request")
				}
				w.Write([]byte(tc.payload))
			}))
			defer srv.Close()
			o, err := NewOllama(srv.URL, "vision:test")
			if err != nil {
				t.Fatal(err)
			}
			got, err := o.Transcribe(context.Background(), imageBytes.Bytes())
			if got != "" || !errors.Is(err, tc.want) {
				t.Fatalf("got %q / %v", got, err)
			}
		})
	}
}
