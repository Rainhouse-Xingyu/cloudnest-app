//go:build integration

package integration

import (
	"bytes"
	"cloudenest_go/model"
	"crypto/sha256"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"

	"encoding/binary"
	"fmt"
	"hash/crc32"
	"testing"
)

// A valid PNG configuration with no pixel data: proving preflight rejection
// does not require allocating a huge image in the test or in the server.
func thumbnailPNGHeader(width, height uint32) []byte {
	b := []byte("\x89PNG\r\n\x1a\n")
	chunk := make([]byte, 25)
	binary.BigEndian.PutUint32(chunk[:4], 13)
	copy(chunk[4:8], "IHDR")
	binary.BigEndian.PutUint32(chunk[8:12], width)
	binary.BigEndian.PutUint32(chunk[12:16], height)
	chunk[16], chunk[17] = 8, 2
	binary.BigEndian.PutUint32(chunk[21:], crc32.ChecksumIEEE(chunk[4:21]))
	return append(b, chunk...)
}

func TestThumbnailRejectsOversizedConfigurationBeforePixelDecode(t *testing.T) {
	f := newFixture(t)
	for i, dims := range [][2]uint32{{32768, 1}, {6000, 5000}} {
		file := f.upload(t, f.alice, fmt.Sprintf("oversized-%d.png", i), thumbnailPNGHeader(dims[0], dims[1]))
		w := identityRequest(f, f.alice, fmt.Sprintf("/api/files/%d/thumbnail", file.ID), nil)
		if w.Code != 413 || w.Header().Get("ETag") != "" {
			t.Fatalf("unsafe image configuration must fail before decoding: status=%d body=%s", w.Code, w.Body.String())
		}
	}
}

func TestThumbnailMalformedAndUnsupportedDataHaveStableErrors(t *testing.T) {
	f := newFixture(t)
	for i, tc := range []struct {
		data   []byte
		status int
	}{
		{thumbnailPNGHeader(64, 64), 422},
		{[]byte{0xff, 0xd8, 0xff}, 422},
		{[]byte("GIF89a"), 422},
		{[]byte("RIFFxxxxWEBPunsupported"), 422},
		{[]byte("not an image"), 415},
	} {
		file := f.upload(t, f.alice, fmt.Sprintf("invalid-%d.png", i), tc.data)
		w := identityRequest(f, f.alice, fmt.Sprintf("/api/files/%d/thumbnail", file.ID), nil)
		if w.Code != tc.status || w.Header().Get("ETag") != "" {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	entries, err := os.ReadDir(filepath.Join(f.cfg.DataDir, "thumbnails"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("invalid images left cache files", entries)
	}
}

func TestThumbnailSupportedFormatsStillProduceBoundedVerifiedJPEG(t *testing.T) {
	f := newFixture(t)
	resetRateSettings(t, f)
	img := image.NewRGBA(image.Rect(0, 0, 512, 256))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 200, G: 50, B: 30, A: 255}}, image.Point{}, draw.Src)
	for _, format := range []string{"png", "jpeg", "gif"} {
		var data bytes.Buffer
		var err error
		switch format {
		case "png":
			err = png.Encode(&data, img)
		case "jpeg":
			err = jpeg.Encode(&data, img, nil)
		case "gif":
			err = gif.Encode(&data, img, nil)
		}
		if err != nil {
			t.Fatal(err)
		}
		file := f.upload(t, f.alice, "supported-"+format+".png", data.Bytes()) // Dispatch uses content, not extension.
		path := fmt.Sprintf("/api/files/%d/thumbnail?size=64", file.ID)
		w := identityRequest(f, f.alice, path, nil)
		if w.Code != 200 {
			t.Fatal(format, w.Code, w.Body.String())
		}
		assertIdentity(t, w, w.Body.Bytes())
		decoded, err := jpeg.Decode(bytes.NewReader(w.Body.Bytes()))
		if err != nil || decoded.Bounds().Dx() != 64 || decoded.Bounds().Dy() != 32 {
			t.Fatal(format, decoded, err)
		}
		cached := identityRequest(f, f.alice, path, map[string]string{"If-None-Match": w.Header().Get("ETag")})
		if cached.Code != 304 || cached.Body.Len() != 0 {
			t.Fatal("cache conditional", cached.Code)
		}
	}
}

func TestThumbnailLimitCannotBypassAuthorizationOrUseLegacyCache(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "huge.png", thumbnailPNGHeader(6000, 5000))
	path := fmt.Sprintf("/api/files/%d/thumbnail?size=256", file.ID)
	if w := identityRequest(f, f.bob, path, nil); w.Code != 403 {
		t.Fatal("metadata leaked before authorization", w.Code)
	}
	var storage model.Storage
	if err := f.db.First(&storage, *file.StorageID).Error; err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(f.cfg.DataDir, "thumbnails")
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(base, fmt.Sprintf("thumb-v2_%d_%s_256.jpg", file.ID, storage.FileHash))
	if err := os.WriteFile(cache, []byte("old cache must remain untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if w := identityRequest(f, f.alice, path, nil); w.Code != 413 {
		t.Fatal("cache bypassed limits", w.Code)
	}
	if got, err := os.ReadFile(cache); err != nil || string(got) != "old cache must remain untouched" {
		t.Fatal("legacy cache changed", err)
	}
}

func TestThumbnailEncodedLimitPreservesOversizedSource(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "oversized-encoded.png", pngIdentity(t, false))
	var storage model.Storage
	if err := f.db.First(&storage, *file.StorageID).Error; err != nil {
		t.Fatal(err)
	}
	// A disposable sparse image with trailing bytes is an actual >64 MiB file;
	// its complete storage digest/size are updated, not a fake successful upload.
	size := int64(64<<20) + 1
	if err := os.Truncate(storage.FilePath, size); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(storage.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.New()
	_, err = io.Copy(digest, source)
	source.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.Storage{}).Where("id=?", storage.ID).Updates(map[string]any{"file_size": size, "file_hash": fmt.Sprintf("%x", digest.Sum(nil))}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.File{}).Where("id=?", file.ID).Update("size", size).Error; err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/files/%d/thumbnail", file.ID)
	w := identityRequest(f, f.alice, path, map[string]string{"If-None-Match": "*"})
	if w.Code != 413 || w.Header().Get("ETag") != "" {
		t.Fatal("encoded cap bypassed", w.Code, w.Body.String())
	}
	if info, err := os.Stat(storage.FilePath); err != nil || info.Size() != size {
		t.Fatal("source changed", err)
	}
}
