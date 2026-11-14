package pdfextract

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxSourceBytes = 32 << 20
	MaxTextBytes   = 256 << 10
	MaxPages       = 64
	MaxOCRPages    = 8
)

var slots = make(chan struct{}, 2)

type Reader struct{ BinDir string }
type OCR func(context.Context, []byte) (string, error)

// Read uses the caller's pinned, verified FD. No pathname is reopened. Page
// markers are part of the extracted text stream; offsets refer to that stream.
// A missing OCR provider fails an image-only page, never publishes partial text.
func (r Reader) Read(ctx context.Context, in *os.File, ocr OCR) (string, error) {
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	default:
		return "", ErrBusy
	}
	ctx, end := context.WithTimeout(ctx, 2*time.Minute)
	defer end()
	st, err := in.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return "", ErrInvalid
	}
	if st.Size() > MaxSourceBytes {
		return "", ErrLimit
	}
	var header [5]byte
	if _, err := in.ReadAt(header[:], 0); err != nil || string(header[:]) != "%PDF-" {
		return "", ErrInvalid
	}
	info, err := run(ctx, r.BinDir, in, "info", 0, 64<<10)
	if err != nil {
		return "", err
	}
	count, err := pageCount(string(info))
	if err != nil {
		return "", err
	}
	text, err := run(ctx, r.BinDir, in, "text", 0, MaxTextBytes)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(text) || bytes.IndexByte(text, 0) >= 0 {
		return "", ErrInvalid
	}
	pages := strings.Split(string(text), "\f")
	if len(pages) != count+1 || strings.TrimSpace(pages[count]) != "" {
		return "", ErrInvalid
	}
	var result strings.Builder
	scanned := 0
	for i, page := range pages[:count] {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		page = strings.TrimSpace(page)
		usedOCR := page == ""
		if page == "" {
			scanned++
			if scanned > MaxOCRPages {
				return "", ErrLimit
			}
			if ocr == nil {
				return "", ErrOCRRequired
			}
			data, err := run(ctx, r.BinDir, in, "render", i+1, 8<<20)
			if err != nil {
				return "", err
			}
			cfg, err := png.DecodeConfig(bytes.NewReader(data))
			if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 1600 || cfg.Height > 1600 {
				return "", ErrInvalid
			}
			page, err = ocr(ctx, data)
			if err != nil {
				return "", err
			}
			page = strings.TrimSpace(page)
			if !utf8.ValidString(page) || strings.IndexByte(page, 0) >= 0 || page == "" {
				return "", ErrInvalid
			}
		}
		marker := fmt.Sprintf("[Page %d]\n", i+1)
		if usedOCR {
			marker = fmt.Sprintf("[Page %d OCR]\n", i+1)
		}
		if len(marker)+len(page)+2 > MaxTextBytes-result.Len() {
			return "", ErrLimit
		}
		result.WriteString(marker)
		result.WriteString(page)
		result.WriteString("\n\n")
	}
	return result.String(), nil
}

func pageCount(info string) (int, error) {
	count := 0
	encrypted := false
	seenEncryption := false
	for _, line := range strings.Split(info, "\n") {
		if strings.HasPrefix(line, "Pages:") {
			if count != 0 {
				return 0, ErrInvalid
			}
			n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Pages:")))
			if err != nil || n < 1 {
				return 0, ErrInvalid
			}
			count = n
		}
		if strings.HasPrefix(line, "Encrypted:") {
			seenEncryption = true
			encrypted = strings.TrimSpace(strings.TrimPrefix(line, "Encrypted:")) != "no"
		}
	}
	if count > MaxPages {
		return 0, ErrLimit
	}
	if count == 0 || !seenEncryption || encrypted {
		return 0, ErrInvalid
	}
	return count, nil
}
