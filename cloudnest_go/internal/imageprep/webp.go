package imageprep

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
)

// readWebP validates the static RIFF envelope before a codec can allocate pixels.
// Walk by offsets rather than loading compressed image/unknown chunk payloads.
// Animation is rejected explicitly: a still decoder must never silently index
// only part of an animated WebP. EXIF may occur after the compressed pixels.
func readWebP(ctx context.Context, in *os.File) ([]byte, error) {
	st, err := in.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() < 12 {
		return nil, ErrInvalid
	}
	if st.Size() > MaxEncodedBytes {
		return nil, ErrLimit
	}
	var head [12]byte
	if _, err := in.ReadAt(head[:], 0); err != nil || string(head[:4]) != "RIFF" || string(head[8:]) != "WEBP" || int64(binary.LittleEndian.Uint32(head[4:]))+8 != st.Size() {
		return nil, ErrInvalid
	}
	var exif []byte
	var extended, image, alpha bool
	var flags byte
	for offset, count := int64(12), 0; offset < st.Size(); count++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if count >= 4096 {
			return nil, ErrLimit
		}
		var h [8]byte
		if _, err := in.ReadAt(h[:], offset); err != nil {
			return nil, ErrInvalid
		}
		size := int64(binary.LittleEndian.Uint32(h[4:]))
		padded := size + size%2
		if padded > st.Size()-offset-8 {
			return nil, ErrInvalid
		}
		if size%2 != 0 {
			var pad [1]byte
			if _, err := in.ReadAt(pad[:], offset+8+size); err != nil || pad[0] != 0 {
				return nil, ErrInvalid
			}
		}
		switch string(h[:4]) {
		case "VP8X":
			if extended || offset != 12 || size != 10 {
				return nil, ErrInvalid
			}
			var v [10]byte
			if _, err := in.ReadAt(v[:], offset+8); err != nil {
				return nil, ErrInvalid
			}
			extended, flags = true, v[0]
			if flags&2 != 0 {
				return nil, ErrFormat
			}
			width := 1 + int(v[4]) + int(v[5])<<8 + int(v[6])<<16
			height := 1 + int(v[7]) + int(v[8])<<8 + int(v[9])<<16
			if _, err := Weight(width, height); err != nil {
				return nil, err
			}
		case "ANIM", "ANMF":
			return nil, ErrFormat
		case "ALPH":
			if !extended || flags&16 == 0 || alpha || image {
				return nil, ErrInvalid
			}
			alpha = true
		case "VP8 ", "VP8L":
			if image || (string(h[:4]) == "VP8L" && alpha) {
				return nil, ErrInvalid
			}
			image = true
		case "EXIF":
			if !extended || flags&8 == 0 || exif != nil || size == 0 {
				return nil, ErrInvalid
			}
			if size > 256<<10 {
				return nil, ErrLimit
			}
			exif = make([]byte, size)
			if _, err := in.ReadAt(exif, offset+8); err != nil {
				return nil, ErrInvalid
			}
		}
		offset += 8 + padded
	}
	if !image || (flags&8 != 0) != (exif != nil) {
		return nil, ErrInvalid
	}
	// Both bare TIFF and the common Exif\0\0-prefixed representation occur in
	// WebP files. The existing TIFF parser validates all offsets and field limits.
	exif = bytes.TrimPrefix(exif, []byte("Exif\x00\x00"))
	return exif, nil
}
