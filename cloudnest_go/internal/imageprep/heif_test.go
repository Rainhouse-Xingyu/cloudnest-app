package imageprep

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func heifWire(exif []byte) []byte {
	b := make([]byte, 36+len(exif)+8)
	copy(b, "CNHEIF01")
	for i, v := range []uint32{2, 1, 2, 1, uint32(len(exif)), 1, 0} {
		binary.BigEndian.PutUint32(b[8+4*i:], v)
	}
	copy(b[36:], exif)
	copy(b[36+len(exif):], []byte{255, 0, 0, 255, 0, 255, 0, 128})
	return b
}

func TestHEIFOutputContractPixelsMetadataAndBounds(t *testing.T) {
	for _, exif := range [][]byte{nil, exifFixture(binary.LittleEndian, 6)} {
		out, err := decodeHEIFOutput(context.Background(), heifWire(exif))
		if err != nil || out.Format != "heif" || out.Width != 2 || out.Height != 1 || out.Metadata.Orientation != 1 {
			t.Fatal(out, err)
		}
		if exif != nil && (out.Metadata.CapturedLocal == nil || out.Metadata.Offset != "+08:00") {
			t.Fatal("EXIF lost", out.Metadata)
		}
		im, err := png.Decode(bytes.NewReader(out.PNG))
		if err != nil || im.Bounds().Dx() != 2 || im.Bounds().Dy() != 1 {
			t.Fatal("pixels rotated twice", err)
		}
		r, g, b, a := im.At(0, 0).RGBA()
		if r != 65535 || g != 0 || b != 0 || a != 65535 {
			t.Fatal("wrong pixels")
		}
	}
	for name, mutate := range map[string]func([]byte) []byte{
		"short":        func(b []byte) []byte { return b[:35] },
		"trailing":     func(b []byte) []byte { return append(b, 0) },
		"version":      func(b []byte) []byte { b[7] = '2'; return b },
		"large output": func(b []byte) []byte { binary.BigEndian.PutUint32(b[16:], 1601); return b },
		"source area": func(b []byte) []byte {
			binary.BigEndian.PutUint32(b[8:], 16000)
			binary.BigEndian.PutUint32(b[12:], 16000)
			return b
		},
		"large metadata":  func(b []byte) []byte { binary.BigEndian.PutUint32(b[24:], (256<<10)+1); return b },
		"multiple images": func(b []byte) []byte { binary.BigEndian.PutUint32(b[28:], 2); return b },
		"flags":           func(b []byte) []byte { binary.BigEndian.PutUint32(b[32:], 2); return b },
	} {
		t.Run(name, func(t *testing.T) {
			out, err := decodeHEIFOutput(context.Background(), mutate(heifWire(nil)))
			if err == nil || len(out.PNG) != 0 {
				t.Fatal("partial or invalid output accepted", err)
			}
		})
	}
}

func TestHEIFProcessCancellationLimitsAndExitStatus(t *testing.T) {
	// Explicit shell fixtures test process/protocol failure handling only.
	in := fileFor(t, bytes.Repeat([]byte{1}, 12))
	for _, tc := range []struct {
		name, script string
		want         error
	}{
		{"unavailable", "exit 22", ErrDecoderUnavailable},
		{"resource", "exit 21", ErrLimit},
		{"multiple images", "exit 24", ErrFormat},
		{"malformed", "printf bad", ErrInvalid},
		{"stderr bound", "/usr/bin/head -c 20000 /dev/zero >&2", ErrLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "decoder")
			if err := os.WriteFile(path, []byte("#!/bin/sh\n"+tc.script+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			out, err := PrepareHEIF(context.Background(), in, path)
			if !errors.Is(err, tc.want) || len(out.PNG) != 0 {
				t.Fatal(err, tc.want)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "decoder")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n/bin/sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := PrepareHEIF(ctx, in, path); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("canceled child retained output pipes")
	}
	release, err := ProcessBudget.Acquire(context.Background(), MemoryBudget)
	if err != nil {
		t.Fatal("native decode leaked shared reservation", err)
	}
	release()
}

func TestHEIFNativeOptIn(t *testing.T) {
	helper := os.Getenv("CLOUDNEST_TEST_HEIF_HELPER")
	if helper == "" {
		t.Skip("set CLOUDNEST_TEST_HEIF_HELPER to a built native decoder")
	}
	f, err := os.Open("testdata/invoice-synthetic.heic")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out, err := PrepareHEIF(context.Background(), f, helper)
	if err != nil || out.Width != 1100 || out.Height != 480 || out.Format != "heif" || out.Metadata.CapturedLocal != nil {
		t.Fatal(out.Width, out.Height, err)
	}
	im, err := png.Decode(bytes.NewReader(out.PNG))
	if err != nil || im.Bounds().Dx() != 1100 || im.Bounds().Dy() != 480 {
		t.Fatal(err)
	}
	t.Log("actual libheif primary decode and bounded PNG conversion passed")
	for _, tc := range []struct {
		name         string
		sw, sh, w, h int
		exif         bool
	}{
		{"invoice-exif.heic", 1100, 480, 1100, 480, true},
		{"large-synthetic.heic", 2000, 1000, 1600, 800, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := os.Open("testdata/" + tc.name)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			out, err := PrepareHEIF(context.Background(), f, helper)
			if err != nil || out.Width != tc.sw || out.Height != tc.sh {
				t.Fatal(out.Width, out.Height, err)
			}
			im, err := png.Decode(bytes.NewReader(out.PNG))
			if err != nil || im.Bounds().Dx() != tc.w || im.Bounds().Dy() != tc.h {
				t.Fatal("wrong scaled pixels", err)
			}
			if tc.exif && (out.Metadata.CapturedLocal == nil || out.Metadata.CapturedLocal.Format("2006-01-02 15:04:05") != "2025-06-15 10:20:30" || out.Metadata.Offset != "+08:00") {
				t.Fatal("native EXIF lost", out.Metadata)
			}
		})
	}
}
