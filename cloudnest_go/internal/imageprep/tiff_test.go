package imageprep

import (
	"encoding/binary"
	"math"
	"testing"
)

func gpsFixture(order binary.ByteOrder) []byte {
	b := make([]byte, 296)
	copy(b, "II")
	if order == binary.BigEndian {
		copy(b, "MM")
	}
	order.PutUint16(b[2:], 42)
	order.PutUint32(b[4:], 8)
	order.PutUint16(b[8:], 3)
	entry := func(at int, tag, kind uint16, n, value uint32) {
		order.PutUint16(b[at:], tag)
		order.PutUint16(b[at+2:], kind)
		order.PutUint32(b[at+4:], n)
		order.PutUint32(b[at+8:], value)
	}
	entry(10, 0x10f, 2, 6, 200)
	copy(b[200:], "ACME!\x00")
	entry(22, 0x110, 2, 8, 210)
	copy(b[210:], "Camera1\x00")
	entry(34, 0x8825, 4, 1, 60)
	order.PutUint16(b[60:], 6)
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
		order.PutUint32(b[240+i*8:], v)
		order.PutUint32(b[244+i*8:], 1)
	}
	return b
}
func TestCameraGPSBothByteOrdersAndAltitudeSign(t *testing.T) {
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		m, err := parseEXIF(gpsFixture(order))
		if err != nil || m.CameraMake != "ACME!" || m.CameraModel != "Camera1" || m.Latitude == nil || m.Longitude == nil || m.Altitude == nil {
			t.Fatal(m, err)
		}
		if math.Abs(*m.Latitude+33.5) > 1e-10 || math.Abs(*m.Longitude-151.2) > 1e-10 || *m.Altitude != -10 {
			t.Fatal("wrong coordinate sign/value", m)
		}
		if m.CapturedLocal != nil {
			t.Fatal("GPS was used to invent capture time")
		}
	}
}
func TestGPSRejectsBadRationalsRangesOffsetsAndCycles(t *testing.T) {
	for _, mutate := range []func([]byte){
		func(b []byte) { binary.LittleEndian.PutUint32(b[244:], 0) },
		func(b []byte) { binary.LittleEndian.PutUint32(b[240:], 91) },
		func(b []byte) { binary.LittleEndian.PutUint32(b[272:], 60) },
		func(b []byte) { binary.LittleEndian.PutUint32(b[82:], 0xfffffff0) },
		func(b []byte) { binary.LittleEndian.PutUint32(b[42:], 8) },
		func(b []byte) { binary.LittleEndian.PutUint16(b[86:], 1) },
		func(b []byte) { b[118] = 2 },
		func(b []byte) { b[70] = 'X' },
		func(b []byte) { binary.LittleEndian.PutUint32(b[14:], 300) },
		func(b []byte) { b[200] = 0xff },
	} {
		b := gpsFixture(binary.LittleEndian)
		mutate(b)
		if _, err := parseEXIF(b); err == nil {
			t.Fatal("invalid GPS/camera data accepted")
		}
	}
}
func TestPartialGPSDoesNotInventCoordinates(t *testing.T) {
	b := gpsFixture(binary.LittleEndian)
	binary.LittleEndian.PutUint16(b[98:], 0x100)
	m, err := parseEXIF(b)
	if err != nil || m.Latitude != nil || m.Longitude != nil {
		t.Fatal(m, err)
	}
}
