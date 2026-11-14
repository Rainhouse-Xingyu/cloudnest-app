//go:build integration

package integration

import (
	"bytes"
	"fmt"
	"image/jpeg"
	"os"
	"testing"
)

func TestWebPThumbnailUsesActualSourceAndEnforcesOwner(t *testing.T) {
	f := newFixture(t)
	b, err := os.ReadFile("testdata/images/invoice-synthetic.webp")
	if err != nil {
		t.Fatal(err)
	}
	file := f.upload(t, f.alice, "snapshot.webp", b)
	path := fmt.Sprintf("/api/files/%d/thumbnail?size=128", file.ID)
	w := identityRequest(f, f.alice, path, nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	im, err := jpeg.Decode(bytes.NewReader(w.Body.Bytes()))
	if err != nil || im.Bounds().Dx() > 128 || im.Bounds().Dy() > 128 || im.Bounds().Dx() < 1 {
		t.Fatal("invalid thumbnail", err)
	}
	w = identityRequest(f, f.bob, path, nil)
	if w.Code != 403 && w.Code != 404 {
		t.Fatal("foreign thumbnail access", w.Code)
	}
}
