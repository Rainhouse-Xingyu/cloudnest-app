// Package pdfextract runs Poppler outside the server process. It is resource
// containment, not a filesystem/network security sandbox; deploy under a
// restricted service identity and keep the system Poppler package updated.
package pdfextract

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const helperFlag = "--internal-pdf-tool"
const memoryLimit = 768 << 20

var (
	ErrUnavailable = errors.New("PDF tools unavailable")
	ErrLimit       = errors.New("PDF extraction resource limit")
	ErrInvalid     = errors.New("invalid or unsupported PDF")
	ErrBusy        = errors.New("PDF extraction capacity unavailable")
	ErrOCRRequired = errors.New("PDF page requires OCR")
)

// Helper must be called at main entry, before loading configuration or opening
// databases. No user-supplied PDF bytes are parsed in the Go helper.
func Helper(args []string) (bool, int) {
	if len(args) == 0 || args[0] != helperFlag {
		return false, 0
	}
	if len(args) != 4 || !filepath.IsAbs(args[1]) {
		return true, 126
	}
	argv, err := toolArgs(args[2], args[3])
	if err != nil || filepath.Base(args[1]) != argv[0] {
		return true, 126
	}
	var st unix.Stat_t
	if unix.Fstat(3, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
		return true, 126
	}
	env := []string{"LC_ALL=C", "HOME=/nonexistent", "TMPDIR=/nonexistent"}
	// Construct argv before reducing the address-space limit, then immediately
	// exec the native parser. Never apply limits to the serving process.
	if setLimits() != nil {
		return true, 126
	}
	if unix.Exec(args[1], argv, env) != nil {
		return true, 126
	}
	return true, 126
}

func toolArgs(mode, page string) ([]string, error) {
	switch mode {
	case "info":
		if page != "0" {
			return nil, ErrInvalid
		}
		return []string{"pdfinfo", "/dev/fd/3"}, nil
	case "text":
		if page != "0" {
			return nil, ErrInvalid
		}
		return []string{"pdftotext", "-layout", "-enc", "UTF-8", "-eol", "unix", "/dev/fd/3", "-"}, nil
	case "render":
		n, e := strconv.Atoi(page)
		if e != nil || n < 1 || n > MaxPages || strconv.Itoa(n) != page {
			return nil, ErrInvalid
		}
		return []string{"pdftoppm", "-f", page, "-l", page, "-singlefile", "-scale-to", "1600", "-png", "/dev/fd/3"}, nil
	}
	return nil, ErrInvalid
}

func setLimits() error {
	for _, x := range []struct {
		resource int
		value    uint64
	}{{unix.RLIMIT_CPU, 15}, {unix.RLIMIT_NOFILE, 64}, {unix.RLIMIT_CORE, 0}, {unix.RLIMIT_FSIZE, 0}} {
		var old unix.Rlimit
		if err := unix.Getrlimit(x.resource, &old); err != nil {
			return err
		}
		v := min(x.value, old.Max)
		if err := unix.Setrlimit(x.resource, &unix.Rlimit{Cur: v, Max: v}); err != nil {
			return err
		}
	}
	return platformMemoryLimit()
}

type boundedOutput struct {
	buffer   bytes.Buffer
	max      int
	exceeded bool
	cancel   context.CancelFunc
}

func (w *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if n > w.max-w.buffer.Len() {
		w.exceeded = true
		w.cancel()
		return 0, ErrLimit
	}
	_, err := w.buffer.Write(p)
	return n, err
}

func run(ctx context.Context, dir string, in *os.File, mode string, page, maxOutput int) ([]byte, error) {
	argv, err := toolArgs(mode, strconv.Itoa(page))
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(dir) {
		return nil, ErrUnavailable
	}
	path := filepath.Join(dir, argv[0])
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0111 == 0 {
		return nil, ErrUnavailable
	}
	self, err := os.Executable()
	if err != nil {
		return nil, ErrUnavailable
	}
	if _, err = in.Seek(0, io.SeekStart); err != nil {
		return nil, ErrInvalid
	}
	op, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(op, self, helperFlag, path, mode, strconv.Itoa(page))
	cmd.Env = []string{"LC_ALL=C"}
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
	out := &boundedOutput{max: maxOutput, cancel: cancel}
	diagnostic := &boundedOutput{max: 16 << 10, cancel: cancel}
	cmd.Stdout = out
	cmd.Stderr = diagnostic
	if err := cmd.Start(); err != nil {
		return nil, ErrUnavailable
	}
	// Stop and join the monitor before returning or releasing the caller's FD.
	done := make(chan error, 1)
	monitorCtx, stopMonitor := context.WithCancel(op)
	go func() { done <- monitorMemory(monitorCtx, cmd.Process.Pid, cancel) }()
	err = cmd.Wait()
	stopMonitor()
	monitorErr := <-done
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if out.exceeded || diagnostic.exceeded || monitorErr != nil || op.Err() != nil {
		return nil, ErrLimit
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 126 {
			return nil, ErrUnavailable
		}
		return nil, ErrInvalid
	}
	return out.buffer.Bytes(), nil
}
