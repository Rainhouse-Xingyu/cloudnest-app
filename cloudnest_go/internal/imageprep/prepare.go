package imageprep

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
)

const MaxOutputBytes = 8 << 20

type Prepared struct {
	PNG           []byte
	Width, Height int
	Format        string
	Metadata      Metadata
}

// Prepare retains only the bounded PNG after releasing the shared decode
// reservation. No goroutine outlives the caller; cancellation is checked by
// reads, every resampling row and the encoder writer. CPU inside a codec is
// cooperative, not forcibly preempted by this package.
func Prepare(ctx context.Context, in *os.File) (Prepared, error) {
	ctx, cancel := context.WithTimeout(ctx, BuildTimeout)
	defer cancel()
	cfg, format, err := Config(ctx, in)
	if err != nil {
		return Prepared{}, err
	}
	weight, err := Weight(cfg.Width, cfg.Height)
	if err != nil {
		return Prepared{}, err
	}
	release, err := ProcessBudget.Acquire(ctx, weight)
	if err != nil {
		return Prepared{}, err
	}
	defer release()
	meta, err := ReadMetadata(ctx, in, format)
	if err != nil {
		return Prepared{}, err
	}
	src, err := Decode(ctx, in, cfg, format)
	if err != nil {
		return Prepared{}, err
	}
	out, err := orientAndResize(ctx, src, meta.Orientation, 1600)
	if err != nil {
		return Prepared{}, err
	}
	dst := &output{ctx: ctx}
	if err := png.Encode(dst, out); err != nil {
		return Prepared{}, err
	}
	return Prepared{PNG: dst.buffer.Bytes(), Width: cfg.Width, Height: cfg.Height, Format: format, Metadata: meta}, nil
}

type output struct {
	ctx    context.Context
	buffer bytes.Buffer
}

func (w *output) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) > MaxOutputBytes-w.buffer.Len() {
		return 0, ErrLimit
	}
	return w.buffer.Write(p)
}

func orientAndResize(ctx context.Context, src image.Image, orientation, maxEdge int) (image.Image, error) {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if _, err := Weight(sw, sh); err != nil {
		return nil, err
	}
	if orientation < 1 || orientation > 8 || maxEdge < 1 || maxEdge > 1600 {
		return nil, ErrInvalid
	}
	ow, oh := sw, sh
	if orientation >= 5 {
		ow, oh = sh, sw
	}
	dw, dh := ow, oh
	if dw > maxEdge || dh > maxEdge {
		if dw >= dh {
			dh = max(1, dh*maxEdge/dw)
			dw = maxEdge
		} else {
			dw = max(1, dw*maxEdge/dh)
			dh = maxEdge
		}
	}
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		oy := y * oh / dh
		for x := 0; x < dw; x++ {
			ox := x * ow / dw
			sx, sy := ox, oy
			switch orientation {
			case 2:
				sx = sw - 1 - ox
			case 3:
				sx, sy = sw-1-ox, sh-1-oy
			case 4:
				sy = sh - 1 - oy
			case 5:
				sx, sy = oy, ox
			case 6:
				sx, sy = oy, sh-1-ox
			case 7:
				sx, sy = sw-1-oy, sh-1-ox
			case 8:
				sx, sy = sw-1-oy, ox
			}
			dst.Set(x, y, src.At(b.Min.X+sx, b.Min.Y+sy))
		}
	}
	return dst, nil
}
