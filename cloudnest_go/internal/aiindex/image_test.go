package aiindex

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Synthetic HTTP fixtures only validate the protocol and rejection behavior.
// Actual visual recognition is tested separately with a real Ollama model.
func TestImageAnalysisRejectsInvalidResponsesAndRetagging(t *testing.T) {
	var pixels bytes.Buffer
	if err := png.Encode(&pixels, image.NewNRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	valid := `{"description":"测试场景","tags":["场景"],"text":"","has_text":false}`
	for _, tc := range []struct {
		name, content, reason string
		retag                 bool
		want                  error
	}{
		{"valid no text", valid, "stop", false, nil},
		{"truncated", valid, "length", false, ErrVisionResponse},
		{"retagged", valid, "stop", true, ErrModelChanged},
		{"missing field", `{"description":"scene","tags":[],"text":""}`, "stop", false, ErrVisionResponse},
		{"null tags", `{"description":"scene","tags":null,"text":"","has_text":false}`, "stop", false, ErrVisionResponse},
		{"unknown field", `{"description":"scene","tags":[],"text":"","has_text":false,"extra":1}`, "stop", false, ErrVisionResponse},
		{"inconsistent OCR", `{"description":"scene","tags":[],"text":"invented","has_text":false}`, "stop", false, ErrVisionResponse},
		{"missing OCR", `{"description":"scene","tags":[],"text":"","has_text":true}`, "stop", false, ErrVisionResponse},
		{"wrong type", `{"description":"scene","tags":[],"text":"","has_text":"false"}`, "stop", false, ErrVisionResponse},
		{"tag separator", `{"description":"scene","tags":["one,two"],"text":"","has_text":false}`, "stop", false, ErrVisionResponse},
		{"empty description", `{"description":"  ","tags":[],"text":"","has_text":false}`, "stop", false, ErrVisionResponse},
		{"invalid JSON", `{`, "stop", false, ErrVisionResponse},
		{"oversize description", `{"description":"` + strings.Repeat("x", 4097) + `","tags":[],"text":"","has_text":false}`, "stop", false, ErrVisionResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tags := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/tags" {
					tags++
					digest := strings.Repeat("a", 64)
					if tc.retag && tags > 1 {
						digest = strings.Repeat("b", 64)
					}
					json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]string{"name": "vision:test", "digest": digest}}})
					return
				}
				var req struct {
					Stream, Think bool
					Format        map[string]any
					Messages      []struct{ Images []string }
				}
				if r.URL.Path != "/api/chat" || json.NewDecoder(r.Body).Decode(&req) != nil || req.Stream || req.Think || req.Format["type"] != "object" || len(req.Messages) != 2 || len(req.Messages[1].Images) != 1 {
					t.Error("wrong structured vision request")
				} else if req.Messages[1].Images[0] != base64.StdEncoding.EncodeToString(pixels.Bytes()) {
					t.Error("actual pixels were not sent")
				}
				json.NewEncoder(w).Encode(map[string]any{"model": "vision:test", "done": true, "done_reason": tc.reason, "message": map[string]string{"role": "assistant", "content": tc.content}})
			}))
			defer srv.Close()
			o, err := NewOllama(srv.URL, "vision:test")
			if err != nil {
				t.Fatal(err)
			}
			got, digest, err := o.AnalyzeImage(context.Background(), pixels.Bytes())
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if tc.want != nil && (digest != "" || got.Description != "" || got.Text != "") {
				t.Fatal("partial result escaped validation")
			}
			if tc.want == nil && (got.Description != "测试场景" || digest != strings.Repeat("a", 64) || tags != 2) {
				t.Fatal("validated result missing")
			}
		})
	}
}

func TestImageAnalysisRejectsInvalidUTF8BeforeJSONReplacement(t *testing.T) {
	var pixels bytes.Buffer
	if err := png.Encode(&pixels, image.NewNRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]string{"name": "vision:test", "digest": strings.Repeat("a", 64)}}})
			return
		}
		content := `{"description":"CORRUPT","tags":[],"text":"","has_text":false}`
		raw, err := json.Marshal(map[string]any{"model": "vision:test", "done": true, "done_reason": "stop", "message": map[string]string{"role": "assistant", "content": content}})
		if err != nil {
			t.Error(err)
		}
		w.Write(bytes.Replace(raw, []byte("CORRUPT"), []byte{0xff}, 1))
	}))
	defer srv.Close()
	o, err := NewOllama(srv.URL, "vision:test")
	if err != nil {
		t.Fatal(err)
	}
	got, digest, err := o.AnalyzeImage(context.Background(), pixels.Bytes())
	if !errors.Is(err, ErrVisionResponse) || got.Description != "" || digest != "" {
		t.Fatal("corrupted text published", got, digest, err)
	}
}
