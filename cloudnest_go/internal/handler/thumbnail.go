package handler

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"net/http"
	"os"
	"time"

	"cloudenest_go/internal/imageprep"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/internal/transfer"
	"crypto/sha256"
	"github.com/gin-gonic/gin"
	"log"
)

const (
	thumbnailMaxEncodedBytes = imageprep.MaxEncodedBytes
	thumbnailMaxHeaderBytes  = imageprep.MaxHeaderBytes
	thumbnailMemoryBudget    = imageprep.MemoryBudget
	thumbnailBuildTimeout    = imageprep.BuildTimeout
	thumbnailWaitTimeout     = imageprep.WaitTimeout
)

var (
	errThumbnailLimit   = imageprep.ErrLimit
	errThumbnailFormat  = imageprep.ErrFormat
	errThumbnailInvalid = imageprep.ErrInvalid
	errThumbnailBusy    = imageprep.ErrBusy
)

func thumbnailWeight(width, height int) (int64, error) { return imageprep.Weight(width, height) }
func thumbnailConfig(ctx context.Context, in *os.File) (image.Config, string, error) {
	return imageprep.Config(ctx, in)
}
func decodeThumbnail(ctx context.Context, in *os.File, cfg image.Config, format string) (image.Image, error) {
	return imageprep.Decode(ctx, in, cfg, format)
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

// Old unregistered cache files are never adopted or deleted.
func (h *FileHandler) buildOrGetThumbnail(parent context.Context, source *os.File, uid, fileID int64, digest string, size int) (*transfer.ThumbnailReader, error) {
	ctx, cancel := context.WithTimeout(parent, thumbnailBuildTimeout)
	defer cancel()
	cfg, format, err := thumbnailConfig(ctx, source)
	if err != nil {
		return nil, err
	}
	if h.Transfers == nil {
		return nil, contract.ErrTransferUnavailable
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("thumbnail-v3:%d:%s:%d", fileID, digest, size))))
	waitDeadline := time.Now().Add(thumbnailWaitTimeout)
	var candidate *transfer.Handle
	for {
		cached, err := h.Transfers.OpenThumbnail(ctx, key)
		if err == nil && cached != nil {
			return cached, nil
		}
		if err == nil {
			candidate, err = h.Transfers.CreateThumbnail(ctx, uid, key)
		}
		// A competing immutable-key build is retried within a bounded wait.
		if err == nil {
			break
		}
		if !errors.Is(err, contract.ErrTransferBusy) {
			return nil, err
		}
		if time.Now().After(waitDeadline) {
			return nil, errThumbnailBusy
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	keep := false
	defer func() {
		if err := candidate.Close(!keep); err != nil {
			log.Print("thumbnail cleanup retained for transfer reconciliation")
		}
	}()
	weight, err := thumbnailWeight(cfg.Width, cfg.Height)
	if err != nil {
		return nil, err
	}
	release, err := imageprep.ProcessBudget.Acquire(ctx, weight)
	if err != nil {
		return nil, err
	}
	defer release()
	img, err := decodeThumbnail(ctx, source, cfg, format)
	if err != nil {
		return nil, err
	}
	resized, err := resizeThumbnail(ctx, img, size)
	if err != nil {
		return nil, err
	}
	if err := jpeg.Encode(archiveOutput{ctx: ctx, out: candidate}, resized, &jpeg.Options{Quality: 82}); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := candidate.SealThumbnail(); err != nil {
		return nil, err
	}
	keep = true
	cached, err := h.Transfers.OpenThumbnail(ctx, key)
	if err == nil && cached == nil {
		err = contract.ErrTransferUnavailable
	}
	return cached, err
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
