package imageprep

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image/png"
	"os"
	"testing"
)

func webpFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name + ".webp")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func webpChunk(name string, data []byte) []byte {
	b := make([]byte, 8+len(data)+len(data)%2)
	copy(b, name)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(data)))
	copy(b[8:], data)
	return b
}

func webpContainer(chunks ...[]byte) []byte {
	b := []byte("RIFF\x00\x00\x00\x00WEBP")
	for _, c := range chunks {
		b = append(b, c...)
	}
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	return b
}

func TestWebPStaticPixelsAndMetadata(t *testing.T) {
	for _, name := range []string{"lossless", "lossy", "alpha"} {
		t.Run(name, func(t *testing.T) {
			out, err := Prepare(context.Background(), fileFor(t, webpFixture(t, name)))
			if err != nil || out.Format != "webp" || out.Width != 3 || out.Height != 2 {
				t.Fatal(out, err)
			}
			im, err := png.Decode(bytes.NewReader(out.PNG))
			if err != nil || im.Bounds().Dx() != 3 || im.Bounds().Dy() != 2 {
				t.Fatal(im, err)
			}
			if name == "lossless" {
				r, g, b, a := im.At(0, 0).RGBA()
				if r != 65535 || g != 0 || b != 0 || a != 65535 {
					t.Fatal("lossless pixels changed")
				}
			}
			if name != "lossy" {
				_, _, _, a := im.At(1, 1).RGBA()
				if a != 0 {
					t.Fatal("alpha lost")
				}
			}
		})
	}
	for _, prefix := range []string{"", "Exif\x00\x00"} {
		v := []byte{8, 0, 0, 0, 2, 0, 0, 1, 0, 0} // EXIF, 3x2 canvas.
		b := webpContainer(webpChunk("VP8X", v), webpFixture(t, "lossless")[12:], webpChunk("EXIF", append([]byte(prefix), exifFixture(binary.LittleEndian, 6)...)))
		out, err := Prepare(context.Background(), fileFor(t, b))
		if err != nil || out.Metadata.Orientation != 6 || out.Metadata.CapturedLocal == nil || out.Metadata.Offset != "+08:00" {
			t.Fatal(out.Metadata, err)
		}
		im, err := png.Decode(bytes.NewReader(out.PNG))
		if err != nil || im.Bounds().Dx() != 2 || im.Bounds().Dy() != 3 {
			t.Fatal("orientation lost", err)
		}
	}
}

func TestWebPRejectsUnsafeContainersBeforeDecoding(t *testing.T) {
	pixels := webpFixture(t, "lossless")[12:]
	valid := webpContainer(pixels)
	badPad := webpContainer(pixels, webpChunk("JUNK", []byte{1}))
	badPad[len(badPad)-1] = 1
	badSize := append([]byte{}, valid...)
	binary.LittleEndian.PutUint32(badSize[16:], 0xffffffff)
	extended := webpChunk("VP8X", []byte{8, 0, 0, 0, 2, 0, 0, 1, 0, 0})
	exif := webpChunk("EXIF", exifFixture(binary.LittleEndian, 1))
	for _, tc := range []struct {
		name string
		data []byte
		want error
	}{
		{"truncated", valid[:len(valid)-1], ErrInvalid},
		{"trailing", append(append([]byte{}, valid...), 0), ErrInvalid},
		{"invalid padding", badPad, ErrInvalid},
		{"chunk outside file", badSize, ErrInvalid},
		{"duplicate EXIF", webpContainer(extended, pixels, exif, exif), ErrInvalid},
		{"animation flag", webpContainer(webpChunk("VP8X", []byte{2, 0, 0, 0, 2, 0, 0, 1, 0, 0}), pixels), ErrFormat},
		{"animation chunk", webpContainer(pixels, webpChunk("ANMF", nil)), ErrFormat},
		{"missing pixels", webpContainer(webpChunk("JUNK", nil)), ErrInvalid},
		{"duplicate pixels", webpContainer(pixels, pixels), ErrInvalid},
		{"missing EXIF", webpContainer(webpChunk("VP8X", []byte{8, 0, 0, 0, 2, 0, 0, 1, 0, 0}), pixels), ErrInvalid},
		{"unflagged EXIF", webpContainer(pixels, webpChunk("EXIF", []byte{1})), ErrInvalid},
		{"oversize EXIF", webpContainer(webpChunk("VP8X", []byte{8, 0, 0, 0, 2, 0, 0, 1, 0, 0}), pixels, webpChunk("EXIF", make([]byte, (256<<10)+1))), ErrLimit},
		{"too many chunks", webpContainer(append(bytes.Repeat(webpChunk("JUNK", nil), 4096), pixels...)), ErrLimit},
		{"huge canvas", webpContainer(webpChunk("VP8X", []byte{0, 0, 0, 0, 255, 255, 255, 255, 255, 255}), pixels), ErrLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Prepare(context.Background(), fileFor(t, tc.data))
			if !errors.Is(err, tc.want) {
				t.Fatal(err, tc.want)
			}
		})
	}
	// A small extended canvas must not permit a larger bitstream allocation.
	bad := webpContainer(webpChunk("VP8X", []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0}), pixels)
	if _, err := Prepare(context.Background(), fileFor(t, bad)); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Prepare(ctx, fileFor(t, valid)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
