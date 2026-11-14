//go:build integration

package integration

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image/jpeg"
	"image/png"
	"os"
	"testing"
)

func syntheticInvoiceBMP(t *testing.T) []byte {
	t.Helper()
	in, err := os.Open("testdata/images/invoice-synthetic.png")
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	img, err := png.Decode(in)
	if err != nil {
		t.Fatal(err)
	}
	bounds := img.Bounds()
	stride := (bounds.Dx()*3 + 3) &^ 3
	data := make([]byte, 54+stride*bounds.Dy())
	copy(data, "BM")
	binary.LittleEndian.PutUint32(data[2:], uint32(len(data)))
	binary.LittleEndian.PutUint32(data[10:], 54)
	binary.LittleEndian.PutUint32(data[14:], 40)
	binary.LittleEndian.PutUint32(data[18:], uint32(bounds.Dx()))
	binary.LittleEndian.PutUint32(data[22:], uint32(-int32(bounds.Dy())))
	binary.LittleEndian.PutUint16(data[26:], 1)
	binary.LittleEndian.PutUint16(data[28:], 24)
	for y := 0; y < bounds.Dy(); y++ {
		for x := 0; x < bounds.Dx(); x++ {
			r, g, b, _ := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			i := 54 + y*stride + x*3
			data[i], data[i+1], data[i+2] = byte(b>>8), byte(g>>8), byte(r>>8)
		}
	}
	return data
}

func TestBMPThumbnailAuthorizationAndLimits(t *testing.T) {
	f := newFixture(t)
	data := syntheticInvoiceBMP(t)
	file := f.upload(t, f.alice, "snapshot.bmp", data)
	path := fmt.Sprintf("/api/files/%d/thumbnail?size=128", file.ID)
	w := identityRequest(f, f.alice, path, nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(w.Body.Bytes()))
	if err != nil || cfg.Width != 128 || cfg.Height != 55 {
		t.Fatal("invalid thumbnail dimensions", cfg, err)
	}
	if w := identityRequest(f, f.bob, path, nil); w.Code != 403 && w.Code != 404 {
		t.Fatal("foreign thumbnail access", w.Code)
	}

	binary.LittleEndian.PutUint32(data[18:], 20_000)
	bad := f.upload(t, f.alice, "oversized.bmp", data[:54])
	w = identityRequest(f, f.alice, fmt.Sprintf("/api/files/%d/thumbnail?size=128", bad.ID), nil)
	if w.Code != 413 {
		t.Fatal("oversize BMP should fail before pixel decoding", w.Code, w.Body.String())
	}
}
