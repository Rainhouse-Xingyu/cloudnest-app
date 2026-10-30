package imageprep

import (
	"context"
	"encoding/binary"
	"hash/crc32"
	"os"
	"time"
)

// CapturedLocal is a camera wall-clock value. Its UTC location is only a Go/SQL
// transport representation; it MUST NOT be exposed as a UTC capture instant.
// Offset is retained separately if EXIF explicitly supplies OffsetTimeOriginal.
type Metadata struct {
	Orientation                   int
	CapturedLocal                 *time.Time
	Offset                        string
	CameraMake, CameraModel       string
	Latitude, Longitude, Altitude *float64
}

func ReadMetadata(ctx context.Context, in *os.File, format string) (Metadata, error) {
	none := Metadata{Orientation: 1}
	st, err := in.Stat()
	if err != nil {
		return none, err
	}
	var exif []byte
	if format == "webp" {
		exif, err = readWebP(ctx, in)
		if err != nil {
			return none, err
		}
	} else if format == "jpeg" {
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
