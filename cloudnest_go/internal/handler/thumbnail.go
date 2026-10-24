package handler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"cloudenest_go/internal/service/contract"
	"github.com/gin-gonic/gin"
)

const (
	thumbnailMaxEncodedBytes int64 = 64 << 20
	thumbnailMaxHeaderBytes  int64 = 1 << 20
	thumbnailMaxDimension          = 16384
	thumbnailMaxPixels       int64 = 24_000_000
	thumbnailMemoryBudget    int64 = 512 << 20
	thumbnailBuildTimeout          = 15 * time.Second
	thumbnailWaitTimeout           = time.Second
)

var (
	errThumbnailLimit      = errors.New("image exceeds thumbnail processing limits")
	errThumbnailFormat     = errors.New("thumbnail format is not supported")
	errThumbnailInvalid    = errors.New("invalid image data")
	errThumbnailBusy       = errors.New("thumbnail processing unavailable; retry later")
	processThumbnailBudget = newThumbnailBudget()
)

// This is an admission estimate, not an OS memory limit. It includes a
// conservative pixel working set and overhead; see THUMBNAIL-RESOURCES.md.
func thumbnailWeight(width, height int) (int64, error) {
	if width <= 0 || height <= 0 {
		return 0, errThumbnailInvalid
	}
	if width > thumbnailMaxDimension || height > thumbnailMaxDimension || int64(width) > thumbnailMaxPixels/int64(height) {
		return 0, errThumbnailLimit
	}
	return int64(width)*int64(height)*16 + (16 << 20), nil
}

type thumbnailBudget struct {
	mu              sync.Mutex
	used            int64
	active, waiting int
	changed         chan struct{}
}

func newThumbnailBudget() *thumbnailBudget { return &thumbnailBudget{changed: make(chan struct{})} }

// At most two builds and sixteen waiters per process, shared by every router.
// Requests wait at most one second; no goroutine outlives its request.
func (b *thumbnailBudget) acquire(ctx context.Context, weight int64) (func(), error) {
	if weight <= 0 || weight > thumbnailMemoryBudget {
		return nil, errThumbnailLimit
	}
	wait, cancel := context.WithTimeout(ctx, thumbnailWaitTimeout)
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
			return nil, errThumbnailBusy
		}
		if b.active < 2 && weight <= thumbnailMemoryBudget-b.used {
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
				return nil, errThumbnailBusy
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

func thumbnailConfig(ctx context.Context, source *os.File) (image.Config, string, error) {
	var cfg image.Config
	info, err := source.Stat()
	if err != nil {
		return cfg, "", err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 {
		return cfg, "", contract.ErrStorageIntegrity
	}
	if info.Size() > thumbnailMaxEncodedBytes {
		return cfg, "", errThumbnailLimit
	}
	if _, err = source.Seek(0, io.SeekStart); err != nil {
		return cfg, "", err
	}
	var magic [8]byte
	n, err := io.ReadFull(&uploadContextReader{ctx: ctx, in: source}, magic[:])
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
		return cfg, "", errThumbnailFormat
	}
	if _, err = source.Seek(0, io.SeekStart); err != nil {
		return cfg, "", err
	}
	limited := &io.LimitedReader{R: &uploadContextReader{ctx: ctx, in: source}, N: thumbnailMaxHeaderBytes}
	cfg, err = decodeConfig(limited)
	if ctx.Err() != nil {
		return cfg, "", ctx.Err()
	}
	if limited.N == 0 {
		return cfg, "", errThumbnailLimit
	}
	if err != nil {
		return cfg, "", errThumbnailInvalid
	}
	if _, err = thumbnailWeight(cfg.Width, cfg.Height); err != nil {
		return cfg, "", err
	}
	return cfg, format, nil
}

func decodeThumbnail(ctx context.Context, source *os.File, cfg image.Config, format string) (image.Image, error) {
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	limited := &io.LimitedReader{R: &uploadContextReader{ctx: ctx, in: source}, N: thumbnailMaxEncodedBytes + 1}
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
		return nil, errThumbnailFormat
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if limited.N == 0 {
		return nil, errThumbnailLimit
	}
	if err != nil {
		return nil, errThumbnailInvalid
	}
	b := img.Bounds()
	// GIF's first frame can occupy a subrectangle of its logical screen.
	if b.Dx() > cfg.Width || b.Dy() > cfg.Height {
		return nil, errThumbnailInvalid
	}
	if _, err := thumbnailWeight(b.Dx(), b.Dy()); err != nil {
		return nil, err
	}
	return img, nil
}

func writeThumbnailError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, errThumbnailLimit):
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": errThumbnailLimit.Error()})
	case errors.Is(err, errThumbnailFormat):
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": errThumbnailFormat.Error()})
	case errors.Is(err, errThumbnailInvalid):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": errThumbnailInvalid.Error()})
	case errors.Is(err, errThumbnailBusy), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		c.Header("Retry-After", "1")
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": errThumbnailBusy.Error()})
	default:
		writeFileMutationError(c, err, "generate thumbnail failed")
	}
}

// Cache keys include the source digest. Persistent cache capacity and eviction
// remain separate work; decode admission is not a disk budget.
func (h *FileHandler) buildOrGetThumbnail(ctx context.Context, source *os.File, fileID int64, digest string, size int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, thumbnailBuildTimeout)
	defer cancel()
	cfg, format, err := thumbnailConfig(ctx, source)
	if err != nil {
		return "", err
	}
	base := h.thumbBaseDir()
	path := filepath.Join(base, fmt.Sprintf("thumb-v2_%d_%s_%d.jpg", fileID, digest, size))
	cached := func() (bool, error) {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !info.Mode().IsRegular() {
			return false, contract.ErrStorageIntegrity
		}
		return true, nil
	}
	if hit, err := cached(); hit || err != nil {
		return path, err
	}
	weight, err := thumbnailWeight(cfg.Width, cfg.Height)
	if err != nil {
		return "", err
	}
	release, err := processThumbnailBudget.acquire(ctx, weight)
	if err != nil {
		return "", err
	}
	defer release()
	// A build that released the budget may already have published our key.
	if hit, err := cached(); hit || err != nil {
		return path, err
	}
	img, err := decodeThumbnail(ctx, source, cfg, format)
	if err != nil {
		return "", err
	}
	resized, err := resizeThumbnail(ctx, img, size)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(base, 0700); err != nil {
		return "", err
	}
	out, err := os.CreateTemp(base, ".thumb-v2-*.tmp")
	if err != nil {
		return "", err
	}
	defer os.Remove(out.Name())
	defer out.Close()
	if err := jpeg.Encode(archiveOutput{ctx: ctx, out: out}, resized, &jpeg.Options{Quality: 82}); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := out.Sync(); err != nil {
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.Link(out.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	return path, nil
}

func resizeThumbnail(ctx context.Context, src image.Image, maxEdge int) (image.Image, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 || maxEdge <= 0 {
		return nil, errThumbnailInvalid
	}
	if sw <= maxEdge && sh <= maxEdge {
		return src, nil
	}
	var dw, dh int
	if sw >= sh {
		dw, dh = maxEdge, sh*maxEdge/sw
	} else {
		dw, dh = sw*maxEdge/sh, maxEdge
	}
	dw, dh = max(dw, 1), max(dh, 1)
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	draw.Draw(dst, dst.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	for y := 0; y < dh; y++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sy := b.Min.Y + y*sh/dh
		for x := 0; x < dw; x++ {
			dst.Set(x, y, src.At(b.Min.X+x*sw/dw, sy))
		}
	}
	return dst, nil
}
