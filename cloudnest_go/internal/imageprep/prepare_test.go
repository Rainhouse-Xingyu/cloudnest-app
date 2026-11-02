package imageprep

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func exifFixture(order binary.ByteOrder, orientation uint16) []byte {
	raw := make([]byte, 100)
	copy(raw, "II")
	if order == binary.BigEndian {
		copy(raw, "MM")
	}
	order.PutUint16(raw[2:], 42)
	order.PutUint32(raw[4:], 8)
	order.PutUint16(raw[8:], 2)
	entry := func(at int, tag, kind uint16, count, value uint32) {
		order.PutUint16(raw[at:], tag)
		order.PutUint16(raw[at+2:], kind)
		order.PutUint32(raw[at+4:], count)
		order.PutUint32(raw[at+8:], value)
	}
	entry(10, 0x112, 3, 1, 0)
	order.PutUint16(raw[18:], orientation)
	entry(22, 0x8769, 4, 1, 38)
	order.PutUint16(raw[38:], 2)
	entry(40, 0x9003, 2, 20, 70)
	entry(52, 0x9011, 2, 7, 90)
	copy(raw[70:], "2025:06:15 10:20:30\x00")
	copy(raw[90:], "+08:00\x00")
	return raw[:97]
}
func fileFor(t *testing.T, data []byte) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "image")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}
func pngWithEXIF(t *testing.T, raw []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 12+len(raw))
	binary.BigEndian.PutUint32(chunk, uint32(len(raw)))
	copy(chunk[4:], "eXIf")
	copy(chunk[8:], raw)
	binary.BigEndian.PutUint32(chunk[8+len(raw):], crc32.ChecksumIEEE(chunk[4:8+len(raw)]))
	image := b.Bytes()
	out := append([]byte{}, image[:33]...)
	out = append(out, chunk...)
	return append(out, image[33:]...)
}
func TestEXIFCameraClockAndOrientationBothByteOrders(t *testing.T) {
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		meta, err := parseEXIF(exifFixture(order, 6))
		if err != nil || meta.Orientation != 6 || meta.CapturedLocal == nil || meta.CapturedLocal.Format("2006-01-02 15:04:05") != "2025-06-15 10:20:30" || meta.Offset != "+08:00" {
			t.Fatal(meta, err)
		}
		meta, err = ReadMetadata(context.Background(), fileFor(t, pngWithEXIF(t, exifFixture(order, 6))), "png")
		if err != nil || meta.Orientation != 6 || meta.CapturedLocal == nil {
			t.Fatal(meta, err)
		}
	}
}
func TestEXIFRejectsMalformedOffsetsCountsDatesAndPNGCRC(t *testing.T) {
	for _, mutate := range []func([]byte){
		func(b []byte) { binary.LittleEndian.PutUint32(b[4:], 0xffffffff) },
		func(b []byte) { binary.LittleEndian.PutUint16(b[8:], 257) },
		func(b []byte) { binary.LittleEndian.PutUint32(b[30:], 8) },
		func(b []byte) { binary.LittleEndian.PutUint32(b[48:], 0xfffffff0) },
		func(b []byte) { copy(b[70:], "2025:13:32 10:20:30") },
		func(b []byte) { copy(b[70:], "0001:01:01 10:20:30") },
		func(b []byte) { copy(b[90:], "+15:00") },
		func(b []byte) { binary.LittleEndian.PutUint16(b[18:], 9) },
	} {
		b := exifFixture(binary.LittleEndian, 1)
		mutate(b)
		if _, err := parseEXIF(b); err == nil {
			t.Fatal("unsafe metadata accepted")
		}
	}
	b := pngWithEXIF(t, exifFixture(binary.LittleEndian, 1))
	b[45] ^= 1
	if _, err := ReadMetadata(context.Background(), fileFor(t, b), "png"); !errors.Is(err, ErrInvalid) {
		t.Fatal("bad CRC", err)
	}
}

func TestMetadataSegmentBudgetAndEmptyEXIF(t *testing.T) {
	jpeg := append([]byte{0xff, 0xd8}, bytes.Repeat([]byte{0xff, 0x01}, 4096)...)
	jpeg = append(jpeg, 0xff, 0xd9)
	if _, err := ReadMetadata(context.Background(), fileFor(t, jpeg), "jpeg"); !errors.Is(err, ErrLimit) {
		t.Fatal("unbounded marker walk", err)
	}
	if _, err := ReadMetadata(context.Background(), fileFor(t, pngWithEXIF(t, nil)), "png"); !errors.Is(err, ErrInvalid) {
		t.Fatal("empty EXIF accepted", err)
	}
}
func TestJPEGMetadataAndNoTimestampSubstitution(t *testing.T) {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 3, 2)), nil); err != nil {
		t.Fatal(err)
	}
	meta, err := ReadMetadata(context.Background(), fileFor(t, b.Bytes()), "jpeg")
	if err != nil || meta.CapturedLocal != nil || meta.Offset != "" || meta.Orientation != 1 {
		t.Fatal(meta, err)
	}
	raw := append([]byte("Exif\x00\x00"), exifFixture(binary.LittleEndian, 3)...)
	segment := []byte{0xff, 0xe1, 0, 0}
	binary.BigEndian.PutUint16(segment[2:], uint16(len(raw)+2))
	segment = append(segment, raw...)
	data := append([]byte{0xff, 0xd8}, segment...)
	data = append(data, b.Bytes()[2:]...)
	meta, err = ReadMetadata(context.Background(), fileFor(t, data), "jpeg")
	if err != nil || meta.Orientation != 3 || meta.CapturedLocal == nil {
		t.Fatal(meta, err)
	}
}
func TestAllEXIFTransformsAndCancellation(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	for i := 0; i < 6; i++ {
		src.SetNRGBA(i%3, i/3, color.NRGBA{R: byte(i + 1), A: 255})
	}
	expected := [][]byte{{1, 2, 3, 4, 5, 6}, {3, 2, 1, 6, 5, 4}, {6, 5, 4, 3, 2, 1}, {4, 5, 6, 1, 2, 3}, {1, 4, 2, 5, 3, 6}, {4, 1, 5, 2, 6, 3}, {6, 3, 5, 2, 4, 1}, {3, 6, 2, 5, 1, 4}}
	for o := 1; o <= 8; o++ {
		out, err := orientAndResize(context.Background(), src, o, 1600)
		if err != nil {
			t.Fatal(err)
		}
		got := []byte{}
		for y := 0; y < out.Bounds().Dy(); y++ {
			for x := 0; x < out.Bounds().Dx(); x++ {
				r, _, _, _ := out.At(x, y).RGBA()
				got = append(got, byte(r>>8))
			}
		}
		if !bytes.Equal(got, expected[o-1]) {
			t.Fatal(o, got, expected[o-1])
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := orientAndResize(ctx, src, 1, 1600); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestPrepareRealPixelsAndSourceFD(t *testing.T) {
	data := pngWithEXIF(t, exifFixture(binary.LittleEndian, 6))
	in := fileFor(t, data)
	// Unlink/replacement must not affect this pinned descriptor.
	if err := os.Remove(in.Name()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(in.Name(), []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := Prepare(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(out.PNG))
	if err != nil || cfg.Width != 2 || cfg.Height != 3 || out.Metadata.CapturedLocal == nil {
		t.Fatal(out, err)
	}
}
