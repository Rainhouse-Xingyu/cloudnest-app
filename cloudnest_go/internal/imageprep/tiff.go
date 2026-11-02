package imageprep

import (
	"bytes"
	"encoding/binary"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Read only IFD0, EXIF and GPS. No thumbnail, MakerNote or next-IFD recursion.
type tiffReader struct {
	raw   []byte
	order binary.ByteOrder
	seen  map[uint32]bool
}
type tiffField struct {
	row    []byte
	reader *tiffReader
}

func (r *tiffReader) directory(at uint32, allowed ...uint16) (map[uint16]tiffField, error) {
	if at < 8 || uint64(at)+2 > uint64(len(r.raw)) || r.seen[at] {
		return nil, ErrInvalid
	}
	r.seen[at] = true
	n := int(r.order.Uint16(r.raw[at : at+2]))
	if n > 256 || uint64(at)+2+uint64(n)*12+4 > uint64(len(r.raw)) {
		return nil, ErrInvalid
	}
	fields := map[uint16]tiffField{}
	for i := 0; i < n; i++ {
		row := r.raw[int(at)+2+i*12 : int(at)+14+i*12]
		tag := r.order.Uint16(row[:2])
		for _, wanted := range allowed {
			if tag != wanted {
				continue
			}
			if _, exists := fields[tag]; exists {
				return nil, ErrInvalid
			}
			fields[tag] = tiffField{row, r}
			break
		}
	}
	return fields, nil
}

func (f tiffField) data(kind uint16, count uint32, width uint64) ([]byte, error) {
	if f.reader.order.Uint16(f.row[2:4]) != kind || f.reader.order.Uint32(f.row[4:8]) != count {
		return nil, ErrInvalid
	}
	n := uint64(count) * width
	if n <= 4 {
		return f.row[8 : 8+int(n)], nil
	}
	at := uint64(f.reader.order.Uint32(f.row[8:12]))
	if at+n > uint64(len(f.reader.raw)) {
		return nil, ErrInvalid
	}
	return f.reader.raw[at : at+n], nil
}

func (f tiffField) ascii(limit uint32) (string, error) {
	n := f.reader.order.Uint32(f.row[4:8])
	if n == 0 || n > limit {
		return "", ErrInvalid
	}
	b, err := f.data(2, n, 1)
	if err != nil || b[len(b)-1] != 0 || bytes.IndexByte(b[:len(b)-1], 0) >= 0 || !utf8.Valid(b) {
		return "", ErrInvalid
	}
	for _, v := range b[:len(b)-1] {
		if v < 32 || v == 127 {
			return "", ErrInvalid
		}
	}
	return string(b[:len(b)-1]), nil
}

func (f tiffField) pointer() (uint32, error) {
	b, err := f.data(4, 1, 4)
	if err != nil {
		return 0, err
	}
	return f.reader.order.Uint32(b), nil
}

func (f tiffField) rationals(count uint32) ([]float64, error) {
	b, err := f.data(5, count, 8)
	if err != nil {
		return nil, err
	}
	out := make([]float64, count)
	for i := range out {
		n, d := f.reader.order.Uint32(b[i*8:]), f.reader.order.Uint32(b[i*8+4:])
		if d == 0 {
			return nil, ErrInvalid
		}
		out[i] = float64(n) / float64(d)
	}
	return out, nil
}

func parseEXIF(raw []byte) (Metadata, error) {
	m := Metadata{Orientation: 1}
	if len(raw) < 8 {
		return m, ErrInvalid
	}
	r := &tiffReader{raw: raw, seen: map[uint32]bool{}}
	switch string(raw[:2]) {
	case "II":
		r.order = binary.LittleEndian
	case "MM":
		r.order = binary.BigEndian
	default:
		return m, ErrInvalid
	}
	if r.order.Uint16(raw[2:4]) != 42 {
		return m, ErrInvalid
	}
	primary, err := r.directory(r.order.Uint32(raw[4:8]), 0x112, 0x8769, 0x8825, 0x10f, 0x110)
	if err != nil {
		return m, err
	}
	if f, ok := primary[0x112]; ok {
		b, e := f.data(3, 1, 2)
		if e != nil {
			return m, e
		}
		m.Orientation = int(r.order.Uint16(b))
		if m.Orientation < 1 || m.Orientation > 8 {
			return m, ErrInvalid
		}
	}
	for tag, target := range map[uint16]*string{0x10f: &m.CameraMake, 0x110: &m.CameraModel} {
		if f, ok := primary[tag]; ok {
			value, e := f.ascii(256)
			if e != nil {
				return m, e
			}
			*target = strings.TrimSpace(value)
		}
	}
	if f, ok := primary[0x8769]; ok {
		at, e := f.pointer()
		if e != nil {
			return m, e
		}
		if at != 0 {
			fields, e := r.directory(at, 0x9003, 0x9011)
			if e != nil {
				return m, e
			}
			if f, ok := fields[0x9003]; ok {
				s, e := f.ascii(32)
				if e != nil {
					return m, e
				}
				value, e := time.Parse("2006:01:02 15:04:05", s)
				if e != nil || value.Year() < 1000 {
					return m, ErrInvalid
				}
				m.CapturedLocal = &value
			}
			if f, ok := fields[0x9011]; ok {
				s, e := f.ascii(7)
				if e != nil {
					return m, e
				}
				if len(s) != 6 || (s[0] != '+' && s[0] != '-') || s[3] != ':' {
					return m, ErrInvalid
				}
				h, e1 := strconv.Atoi(s[1:3])
				min, e2 := strconv.Atoi(s[4:])
				if e1 != nil || e2 != nil || h > 14 || min > 59 || h == 14 && min != 0 || strings.IndexAny(s[1:], "+- ") >= 0 {
					return m, ErrInvalid
				}
				m.Offset = s
			}
		}
	}
	if f, ok := primary[0x8825]; ok {
		at, e := f.pointer()
		if e != nil {
			return m, e
		}
		if at != 0 {
			if e := r.gps(at, &m); e != nil {
				return m, e
			}
		}
	}
	return m, nil
}

func (r *tiffReader) gps(at uint32, m *Metadata) error {
	fields, err := r.directory(at, 1, 2, 3, 4, 5, 6)
	if err != nil {
		return err
	}
	var refs [2]string
	var coords [2]*float64
	for i := 0; i < 2; i++ {
		if f, ok := fields[uint16(1+2*i)]; ok {
			s, e := f.ascii(2)
			if e != nil {
				return e
			}
			if i == 0 && s != "N" && s != "S" || i == 1 && s != "E" && s != "W" {
				return ErrInvalid
			}
			refs[i] = s
		}
		if f, ok := fields[uint16(2+2*i)]; ok {
			values, e := f.rationals(3)
			if e != nil {
				return e
			}
			value := values[0] + values[1]/60 + values[2]/3600
			if values[1] >= 60 || values[2] >= 60 || value > float64(90*(i+1)) {
				return ErrInvalid
			}
			coords[i] = &value
		}
	}
	if refs[0] != "" && refs[1] != "" && coords[0] != nil && coords[1] != nil {
		if refs[0] == "S" {
			*coords[0] = -*coords[0]
		}
		if refs[1] == "W" {
			*coords[1] = -*coords[1]
		}
		m.Latitude, m.Longitude = coords[0], coords[1]
	}
	negative := false
	if f, ok := fields[5]; ok {
		b, e := f.data(1, 1, 1)
		if e != nil {
			return e
		}
		if b[0] > 1 {
			return ErrInvalid
		}
		negative = b[0] == 1
	}
	if f, ok := fields[6]; ok {
		values, e := f.rationals(1)
		if e != nil {
			return e
		}
		if negative {
			values[0] = -values[0]
		}
		m.Altitude = &values[0]
	}
	return nil
}
