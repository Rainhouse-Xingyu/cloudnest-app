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
