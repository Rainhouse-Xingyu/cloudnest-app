package imageprep

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

var ErrDecoderUnavailable = errors.New("native image decoder unavailable")

const heifOutputLimit = 36 + (256 << 10) + 1600*1600*4
const heifMemoryLimit = 768 << 20

// IsHEIF recognizes still-image brands only. The native library validates the
// complete container; this bounded sniff is not a substitute for decoding.
func IsHEIF(in *os.File) bool {
	var b [12]byte
	if _, err := in.ReadAt(b[:], 0); err != nil || string(b[4:8]) != "ftyp" {
		return false
	}
	switch string(b[8:]) {
	case "heic", "heix", "heim", "heis", "mif1":
		return true
	}
	return false
}

func PrepareConfigured(ctx context.Context, in *os.File, helper string) (Prepared, error) {
	if err := ctx.Err(); err != nil {
		return Prepared{}, err
	}
	if IsHEIF(in) {
		return PrepareHEIF(ctx, in, helper)
	}
	return Prepare(ctx, in)
}

// PrepareHEIF uses an explicitly configured native helper, never a shell or a
// filename chosen by source content. The verified source descriptor is inherited
// as FD 3. Caller must hold its source lease until this function returns.
func PrepareHEIF(parent context.Context, in *os.File, helper string) (Prepared, error) {
	if !filepath.IsAbs(helper) {
		return Prepared{}, ErrDecoderUnavailable
	}
	st, err := os.Stat(helper)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0111 == 0 {
		return Prepared{}, ErrDecoderUnavailable
	}
	st, err = in.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() < 12 {
		return Prepared{}, ErrInvalid
	}
	if st.Size() > MaxEncodedBytes {
		return Prepared{}, ErrLimit
	}
	ctx, cancel := context.WithTimeout(parent, BuildTimeout)
	defer cancel()
	// Native decoding has its own 768 MiB process limit. Reserve the entire
	// shared image budget to exclude other image decodes during this operation;
	// 512 MiB is not a claim about total native + Go RSS.
	release, err := ProcessBudget.Acquire(ctx, MemoryBudget)
	if err != nil {
		return Prepared{}, err
	}
	defer release()
	cmd := exec.CommandContext(ctx, helper)
	cmd.Env = []string{"LC_ALL=C", "HOME=/nonexistent", "TMPDIR=/nonexistent", "LIBHEIF_PLUGIN_PATH=/nonexistent"}
	cmd.Dir = "/"
	cmd.ExtraFiles = []*os.File{in}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := unix.Kill(-cmd.Process.Pid, unix.SIGKILL)
		if errors.Is(err, unix.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = time.Second
	out := &heifOutput{max: heifOutputLimit, cancel: cancel}
	diagnostic := &heifOutput{max: 16 << 10, cancel: cancel}
	cmd.Stdout, cmd.Stderr = out, diagnostic
	if err := cmd.Start(); err != nil {
		if parent.Err() != nil {
			return Prepared{}, parent.Err()
		}
		if ctx.Err() != nil {
			return Prepared{}, ErrLimit
		}
		return Prepared{}, ErrDecoderUnavailable
	}
	monitorCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- monitorHEIFMemory(monitorCtx, cmd.Process.Pid, cancel) }()
	err = cmd.Wait()
	stop()
	monitorErr := <-done
	if parent.Err() != nil {
		return Prepared{}, parent.Err()
	}
	if out.exceeded || diagnostic.exceeded || monitorErr != nil || ctx.Err() != nil {
		return Prepared{}, ErrLimit
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			switch exit.ExitCode() {
			case 21:
				return Prepared{}, ErrLimit
			case 22:
				return Prepared{}, ErrDecoderUnavailable
			case 24:
				return Prepared{}, ErrFormat
			}
		}
		return Prepared{}, ErrInvalid
	}
	return decodeHEIFOutput(ctx, out.buffer.Bytes())
}

type heifOutput struct {
	buffer   bytes.Buffer
	max      int
	exceeded bool
	cancel   context.CancelFunc
}

func (w *heifOutput) Write(p []byte) (int, error) {
	if len(p) > w.max-w.buffer.Len() {
		w.exceeded = true
		w.cancel()
		return 0, ErrLimit
	}
	return w.buffer.Write(p)
}

func decodeHEIFOutput(ctx context.Context, b []byte) (Prepared, error) {
	if len(b) < 36 || string(b[:8]) != "CNHEIF01" {
		return Prepared{}, ErrInvalid
	}
	values := make([]int, 7)
	for i := range values {
		values[i] = int(binary.BigEndian.Uint32(b[8+4*i:]))
	}
	sw, sh, w, h, exifSize, count, flags := values[0], values[1], values[2], values[3], values[4], values[5], values[6]
	if _, err := Weight(sw, sh); err != nil {
		return Prepared{}, err
	}
	if w < 1 || h < 1 || w > 1600 || h > 1600 || w > sw || h > sh || exifSize < 0 || exifSize > 256<<10 || count < 1 || count > 4096 || flags < 0 || flags > 1 {
		return Prepared{}, ErrInvalid
	}
	ew, eh := sw, sh
	if ew > 1600 || eh > 1600 {
		if ew >= eh {
			eh = max(1, eh*1600/ew)
			ew = 1600
		} else {
			ew = max(1, ew*1600/eh)
			eh = 1600
		}
	}
	if w != ew || h != eh {
		return Prepared{}, ErrInvalid
	}
	// Until multi-image provenance is exposed by the application, never silently
	// publish a single primary image as a complete multi-image document.
	if count != 1 {
		return Prepared{}, ErrFormat
	}
	if len(b) != 36+exifSize+w*h*4 {
		return Prepared{}, ErrInvalid
	}
	meta := Metadata{Orientation: 1}
	if exifSize > 0 {
		var err error
		meta, err = parseEXIF(b[36 : 36+exifSize])
		if err != nil {
			return Prepared{}, err
		}
		// HEIF crop/mirror/rotation were already applied by libheif. EXIF's
		// orientation must not rotate these pixels a second time.
		meta.Orientation = 1
	}
	var pixels image.Image
	data := b[36+exifSize:]
	if flags == 1 {
		pixels = &image.RGBA{Pix: data, Stride: w * 4, Rect: image.Rect(0, 0, w, h)}
	} else {
		pixels = &image.NRGBA{Pix: data, Stride: w * 4, Rect: image.Rect(0, 0, w, h)}
	}
	out := &output{ctx: ctx}
	if err := png.Encode(out, pixels); err != nil {
		return Prepared{}, err
	}
	return Prepared{PNG: out.buffer.Bytes(), Width: sw, Height: sh, Format: "heif", Metadata: meta}, nil
}
