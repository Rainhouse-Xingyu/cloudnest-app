//go:build integration

package integration

import (
	"encoding/binary"
	"hash/crc32"
	"testing"
	"time"
)

// Adds synthetic EXIF metadata without changing image pixels. Camera date is
// explicit test input, not generated/inferred by the vision model.
func syntheticCapturePNG(t *testing.T, png []byte, date time.Time) []byte {
	t.Helper()
	if len(png) < 33 || string(png[:8]) != "\x89PNG\r\n\x1a\n" {
		t.Fatal("PNG fixture required")
	}
	raw := make([]byte, 85)
	copy(raw, "II")
	order := binary.LittleEndian
	order.PutUint16(raw[2:], 42)
	order.PutUint32(raw[4:], 8)
	order.PutUint16(raw[8:], 1)
	entry := func(at int, tag, kind uint16, count, value uint32) {
		order.PutUint16(raw[at:], tag)
		order.PutUint16(raw[at+2:], kind)
		order.PutUint32(raw[at+4:], count)
		order.PutUint32(raw[at+8:], value)
	}
	entry(10, 0x8769, 4, 1, 26)
	order.PutUint16(raw[26:], 2)
	entry(28, 0x9003, 2, 20, 58)
	entry(40, 0x9011, 2, 7, 78)
	copy(raw[58:], date.Format("2006:01:02 15:04:05")+"\x00")
	copy(raw[78:], "+08:00\x00")
	chunk := make([]byte, 12+len(raw))
	binary.BigEndian.PutUint32(chunk, uint32(len(raw)))
	copy(chunk[4:], "eXIf")
	copy(chunk[8:], raw)
	binary.BigEndian.PutUint32(chunk[8+len(raw):], crc32.ChecksumIEEE(chunk[4:8+len(raw)]))
	result := append([]byte{}, png[:33]...)
	result = append(result, chunk...)
	return append(result, png[33:]...)
}

// Explicit synthetic camera/GPS values, never inferred from generated pixels.
func syntheticCameraGPSPNG(t *testing.T, png []byte) []byte {
	t.Helper()
	if len(png) < 33 || string(png[:8]) != "\x89PNG\r\n\x1a\n" {
		t.Fatal("PNG fixture required")
	}
	b := make([]byte, 296)
	copy(b, "II")
	o := binary.LittleEndian
	o.PutUint16(b[2:], 42)
	o.PutUint32(b[4:], 8)
	o.PutUint16(b[8:], 3)
	entry := func(at int, tag, kind uint16, n, value uint32) {
		o.PutUint16(b[at:], tag)
		o.PutUint16(b[at+2:], kind)
		o.PutUint32(b[at+4:], n)
		o.PutUint32(b[at+8:], value)
	}
	entry(10, 0x10f, 2, 6, 200)
	copy(b[200:], "ACME!\x00")
	entry(22, 0x110, 2, 8, 210)
	copy(b[210:], "Camera1\x00")
	entry(34, 0x8825, 4, 1, 60)
	o.PutUint16(b[60:], 6)
	entry(62, 1, 2, 2, 0)
	copy(b[70:], "S\x00")
	entry(74, 2, 5, 3, 240)
	entry(86, 3, 2, 2, 0)
	copy(b[94:], "E\x00")
	entry(98, 4, 5, 3, 264)
	entry(110, 5, 1, 1, 0)
	b[118] = 1
	entry(122, 6, 5, 1, 288)
	for i, v := range []uint32{33, 30, 0, 151, 12, 0, 10} {
		o.PutUint32(b[240+i*8:], v)
		o.PutUint32(b[244+i*8:], 1)
	}
	chunk := make([]byte, 12+len(b))
	binary.BigEndian.PutUint32(chunk, uint32(len(b)))
	copy(chunk[4:], "eXIf")
	copy(chunk[8:], b)
	binary.BigEndian.PutUint32(chunk[8+len(b):], crc32.ChecksumIEEE(chunk[4:8+len(b)]))
	out := append([]byte{}, png[:33]...)
	out = append(out, chunk...)
	return append(out, png[33:]...)
}
