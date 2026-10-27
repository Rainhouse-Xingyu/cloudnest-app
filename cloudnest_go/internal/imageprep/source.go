// Package imageprep owns bounded decoding and shared admission for thumbnail
// and AI image preparation. Memory is an admission estimate, not an OS limit.
package imageprep

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"sync"
	"time"
)

const (
	MaxEncodedBytes int64 = 64 << 20
	MaxHeaderBytes  int64 = 1 << 20
	MaxDimension          = 16384
	MaxPixels       int64 = 24_000_000
	MemoryBudget    int64 = 512 << 20
	BuildTimeout          = 15 * time.Second
	WaitTimeout           = time.Second
)

var (
	ErrLimit      = errors.New("image exceeds thumbnail processing limits")
	ErrFormat     = errors.New("thumbnail format is not supported")
	ErrInvalid    = errors.New("invalid image data")
	ErrBusy       = errors.New("thumbnail processing unavailable; retry later")
	ProcessBudget = NewBudget()
)

// This is an admission estimate, not an OS memory limit. It includes a
// conservative pixel working set and overhead; see THUMBNAIL-RESOURCES.md.
func Weight(width, height int) (int64, error) {
	if width <= 0 || height <= 0 {
		return 0, ErrInvalid
	}
	if width > MaxDimension || height > MaxDimension || int64(width) > MaxPixels/int64(height) {
		return 0, ErrLimit
	}
	return int64(width)*int64(height)*16 + (16 << 20), nil
}

type Budget struct {
	mu              sync.Mutex
	used            int64
	active, waiting int
	changed         chan struct{}
}

func NewBudget() *Budget { return &Budget{changed: make(chan struct{})} }

// At most two builds and sixteen waiters per process, shared by every router.
// Requests wait at most one second; no goroutine outlives its request.
func (b *Budget) Acquire(ctx context.Context, weight int64) (func(), error) {
	if weight <= 0 || weight > MemoryBudget {
		return nil, ErrLimit
	}
	wait, cancel := context.WithTimeout(ctx, WaitTimeout)
	defer cancel()
	b.mu.Lock()
	defer b.mu.Unlock()
	queued := false
	defer func() {
		if queued {
			b.waiting--
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if wait.Err() != nil {
			return nil, ErrBusy
		}
		if b.active < 2 && weight <= MemoryBudget-b.used {
			b.active++
			b.used += weight
			var once sync.Once
			return func() {
				once.Do(func() {
					b.mu.Lock()
					defer b.mu.Unlock()
					b.active--
					b.used -= weight
					close(b.changed)
					b.changed = make(chan struct{})
				})
			}, nil
		}
		if !queued {
			if b.waiting >= 16 {
				return nil, ErrBusy
			}
			b.waiting++
			queued = true
		}
		changed := b.changed
		b.mu.Unlock()
		select {
		case <-changed:
		case <-wait.Done():
		}
		b.mu.Lock()
	}
}

func Config(ctx context.Context, source *os.File) (image.Config, string, error) {
	var cfg image.Config
	info, err := source.Stat()
	if err != nil {
		return cfg, "", err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 {
		return cfg, "", ErrInvalid
	}
	if info.Size() > MaxEncodedBytes {
		return cfg, "", ErrLimit
	}
	if _, err = source.Seek(0, io.SeekStart); err != nil {
		return cfg, "", err
	}
	var magic [8]byte
	n, err := io.ReadFull(&contextReader{ctx: ctx, in: source}, magic[:])
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return cfg, "", err
	}
	var format string
	var decodeConfig func(io.Reader) (image.Config, error)
	switch {
	case n >= 2 && magic[0] == 0xff && magic[1] == 0xd8:
		format, decodeConfig = "jpeg", jpeg.DecodeConfig
	case n == 8 && bytes.Equal(magic[:], []byte("\x89PNG\r\n\x1a\n")):
		format, decodeConfig = "png", png.DecodeConfig
	case n >= 6 && (string(magic[:6]) == "GIF87a" || string(magic[:6]) == "GIF89a"):
		format, decodeConfig = "gif", gif.DecodeConfig
	default:
		return cfg, "", ErrFormat
	}
	if _, err = source.Seek(0, io.SeekStart); err != nil {
		return cfg, "", err
	}
	limited := &io.LimitedReader{R: &contextReader{ctx: ctx, in: source}, N: MaxHeaderBytes}
	cfg, err = decodeConfig(limited)
	if ctx.Err() != nil {
		return cfg, "", ctx.Err()
	}
	if limited.N == 0 {
		return cfg, "", ErrLimit
	}
	if err != nil {
		return cfg, "", ErrInvalid
	}
	if _, err = Weight(cfg.Width, cfg.Height); err != nil {
		return cfg, "", err
	}
	return cfg, format, nil
}

func Decode(ctx context.Context, source *os.File, cfg image.Config, format string) (image.Image, error) {
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	limited := &io.LimitedReader{R: &contextReader{ctx: ctx, in: source}, N: MaxEncodedBytes + 1}
	var img image.Image
	var err error
	switch format {
	case "jpeg":
		img, err = jpeg.Decode(limited)
	case "png":
		img, err = png.Decode(limited)
	case "gif":
		img, err = gif.Decode(limited) // First frame only, never DecodeAll.
	default:
		return nil, ErrFormat
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if limited.N == 0 {
		return nil, ErrLimit
	}
	if err != nil {
		return nil, ErrInvalid
	}
	b := img.Bounds()
	// GIF's first frame can occupy a subrectangle of its logical screen.
	if b.Dx() > cfg.Width || b.Dy() > cfg.Height {
		return nil, ErrInvalid
	}
	if _, err := Weight(b.Dx(), b.Dy()); err != nil {
		return nil, err
	}
	return img, nil
}

type contextReader struct {
	ctx context.Context
	in  io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.in.Read(p)
}
