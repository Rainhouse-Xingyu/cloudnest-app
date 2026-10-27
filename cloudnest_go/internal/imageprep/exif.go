package imageprep

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"os"
	"strconv"
	"strings"
	"time"
)

// CapturedLocal is a camera wall-clock value. Its UTC location is only a Go/SQL
// transport representation; it MUST NOT be exposed as a UTC capture instant.
// Offset is retained separately if EXIF explicitly supplies OffsetTimeOriginal.
type Metadata struct {
	Orientation   int
	CapturedLocal *time.Time
	Offset        string
}

func ReadMetadata(ctx context.Context, in *os.File, format string) (Metadata, error) {
	none := Metadata{Orientation: 1}
	st, err := in.Stat()
	if err != nil {
		return none, err
	}
	var exif []byte
	if format == "jpeg" {
		offset := int64(2)
		for n := 0; n < 4096; n++ {
			if err := ctx.Err(); err != nil {
				return none, err
			}
			var h [4]byte
			if offset > MaxHeaderBytes {
				return none, ErrLimit
			}
			if offset+2 > st.Size() {
				return none, ErrInvalid
			}
			if _, err := in.ReadAt(h[:2], offset); err != nil {
				return none, ErrInvalid
			}
			if h[0] != 0xff {
				return none, ErrInvalid
			}
			if h[1] == 0xff {
				offset++
				continue
			}
			if h[1] == 0xda || h[1] == 0xd9 {
				return none, nil
			}
			if h[1] >= 0xd0 && h[1] <= 0xd8 || h[1] == 0x01 {
				offset += 2
				continue
			}
			if _, err := in.ReadAt(h[2:], offset+2); err != nil {
				return none, ErrInvalid
			}
			size := int64(binary.BigEndian.Uint16(h[2:]))
			if size < 2 || offset+2+size > st.Size() {
				return none, ErrInvalid
			}
			if h[1] == 0xe1 && size >= 8 {
				var magic [6]byte
				if _, err := in.ReadAt(magic[:], offset+4); err != nil {
					return none, ErrInvalid
				}
				if string(magic[:]) == "Exif\x00\x00" {
					exif = make([]byte, size-8)
					if _, err := in.ReadAt(exif, offset+10); err != nil {
						return none, ErrInvalid
					}
					break
				}
			}
			offset += 2 + size
		}
		if exif == nil {
			return none, ErrLimit
		}
	} else if format == "png" {
		offset := int64(8)
		for n := 0; n < 4096 && offset < st.Size(); n++ {
			if err := ctx.Err(); err != nil {
				return none, err
			}
			var h [8]byte
			if _, err := in.ReadAt(h[:], offset); err != nil {
				return none, ErrInvalid
			}
			size := int64(binary.BigEndian.Uint32(h[:4]))
			if size > st.Size()-offset-12 {
				return none, ErrInvalid
			}
			if string(h[4:]) == "eXIf" {
				if size > 256<<10 {
					return none, ErrLimit
				}
				raw := make([]byte, size+4)
				if _, err := in.ReadAt(raw, offset+8); err != nil {
					return none, ErrInvalid
				}
				digest := crc32.NewIEEE()
				digest.Write(h[4:])
				digest.Write(raw[:size])
				if digest.Sum32() != binary.BigEndian.Uint32(raw[size:]) {
					return none, ErrInvalid
				}
				exif = raw[:size]
				break
			}
			if string(h[4:]) == "IEND" {
				return none, nil
			}
			offset += 12 + size
		}
		if exif == nil {
			if offset < st.Size() {
				return none, ErrLimit
			}
			return none, ErrInvalid
		}
	}
	if exif == nil {
		return none, nil
	}
	return parseEXIF(exif)
}

// Read only IFD0 and the EXIF sub-IFD. Never recursively walk thumbnails,
// MakerNotes or arbitrary next-IFD pointers. Each directory has <=256 entries.
func parseEXIF(raw []byte) (Metadata, error) {
	meta := Metadata{Orientation: 1}
	if len(raw) < 8 {
		return meta, ErrInvalid
	}
	var order binary.ByteOrder
	switch string(raw[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return meta, ErrInvalid
	}
	if order.Uint16(raw[2:4]) != 42 {
		return meta, ErrInvalid
	}
	var date, offset string
	var sub uint32
	seen := map[uint32]bool{}
	readIFD := func(at uint32, primary bool) error {
		if at < 8 || uint64(at)+2 > uint64(len(raw)) || seen[at] {
			return ErrInvalid
		}
		seen[at] = true
		count := int(order.Uint16(raw[at : at+2]))
		if count > 256 || uint64(at)+2+uint64(count)*12+4 > uint64(len(raw)) {
			return ErrInvalid
		}
		tags := map[uint16]bool{}
		for i := 0; i < count; i++ {
			row := raw[int(at)+2+i*12 : int(at)+14+i*12]
			tag := order.Uint16(row[:2])
			if tag != 0x112 && tag != 0x8769 && tag != 0x9003 && tag != 0x9011 {
				continue
			}
			if tags[tag] {
				return ErrInvalid
			}
			tags[tag] = true
			kind, n := order.Uint16(row[2:4]), order.Uint32(row[4:8])
			if primary && tag == 0x112 {
				if kind != 3 || n != 1 {
					return ErrInvalid
				}
				v := int(order.Uint16(row[8:10]))
				if v < 1 || v > 8 {
					return ErrInvalid
				}
				meta.Orientation = v
			} else if primary && tag == 0x8769 {
				if kind != 4 || n != 1 {
					return ErrInvalid
				}
				sub = order.Uint32(row[8:12])
			} else if !primary && (tag == 0x9003 || tag == 0x9011) {
				if kind != 2 || n == 0 || n > 32 {
					return ErrInvalid
				}
				var data []byte
				if n <= 4 {
					data = row[8 : 8+n]
				} else {
					p := order.Uint32(row[8:12])
					if uint64(p)+uint64(n) > uint64(len(raw)) {
						return ErrInvalid
					}
					data = raw[p : p+n]
				}
				if data[len(data)-1] != 0 || bytes.IndexByte(data[:len(data)-1], 0) >= 0 {
					return ErrInvalid
				}
				if tag == 0x9003 {
					date = string(data[:len(data)-1])
				} else {
					offset = string(data[:len(data)-1])
				}
			}
		}
		return nil
	}
	if err := readIFD(order.Uint32(raw[4:8]), true); err != nil {
		return meta, err
	}
	if sub != 0 {
		if err := readIFD(sub, false); err != nil {
			return meta, err
		}
	}
	if date != "" {
		value, err := time.Parse("2006:01:02 15:04:05", date)
		// MySQL DATETIME cannot represent years below 1000. Reject instead of
		// letting publication fail repeatedly after successful model inference.
		if err != nil || value.Year() < 1000 {
			return meta, ErrInvalid
		}
		meta.CapturedLocal = &value
	}
	if offset != "" {
		if len(offset) != 6 || (offset[0] != '+' && offset[0] != '-') || offset[3] != ':' {
			return meta, ErrInvalid
		}
		h, e1 := strconv.Atoi(offset[1:3])
		m, e2 := strconv.Atoi(offset[4:])
		if e1 != nil || e2 != nil || h > 14 || m > 59 || h == 14 && m != 0 || strings.IndexAny(offset[1:], "+- ") >= 0 {
			return meta, ErrInvalid
		}
		meta.Offset = offset
	}
	return meta, nil
}
