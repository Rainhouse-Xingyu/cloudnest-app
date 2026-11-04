package frpagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"golang.org/x/sys/unix"
)

const FRPVersion = "0.71.1"

var ErrBinary = errors.New("frpc does not match the pinned official release")
var ErrProcess = errors.New("managed frpc operation failed")

// Hashes are for extracted frpc bytes, obtained only after verifying the
// official release archive SHA-256 (documented in FRP-AGENT.md).
var binaryHashes = map[string]string{
	"darwin/amd64": "01cb3d59aba860a7097e1343ce04f9b5d8164b7488e21f5c551a34e62406c4c6",
	"darwin/arm64": "7aaaba058dbeeedc86be876c44912c4f33756ab3e6e7a085f8d7af7814a073c1",
	"linux/amd64":  "35c6054d4549bbcf2f02875543895fd9e44c2aae2925f61cd8ff5a693703c28b",
	"linux/arm64":  "651de3428bdc7f37b4bad76ed3236b221939a989c433cd7daee79d2817519f8b",
}

func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func verifyBinary(b []byte) bool {
	want := binaryHashes[runtime.GOOS+"/"+runtime.GOARCH]
	return want != "" && digest(b) == want
}

// InstallBinary never executes the supplied path. It copies bounded, verified
// bytes into private storage before any invocation of official frpc.
func InstallBinary(s *Store, path string) error {
	if !filepath.IsAbs(path) {
		return ErrBinary
	}
	fd, e := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if e != nil {
		return ErrBinary
	}
	f := os.NewFile(uintptr(fd), "frpc-source")
	defer f.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
		return ErrBinary
	}
	b, e := io.ReadAll(io.LimitReader(f, 32<<20))
	if e != nil || !verifyBinary(b) {
		return ErrBinary
	}
	return s.Write("frpc", b, 0700)
}

type Process struct {
	store *Store
	cmd   *exec.Cmd
	done  chan error
}

func NewProcess(s *Store) *Process { return &Process{store: s} }
func (p *Process) command(ctx context.Context, args ...string) (*exec.Cmd, error) {
	b, e := p.store.Read("frpc", 32<<20)
	if e != nil || !verifyBinary(b) {
		return nil, ErrBinary
	}
	c := exec.CommandContext(ctx, filepath.Join(p.store.Path, "frpc"), args...)
	c.Dir = p.store.Path
	c.Env = []string{"LANG=C", "HOME=" + p.store.Path} // no proxy, template or runtime env inheritance
	c.ExtraFiles = []*os.File{p.store.lock}
	// stdout/stderr are discarded: frpc diagnostics may contain auth secrets.
	return c, nil
}
func (p *Process) Verify(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, e := p.command(ctx, "verify", "-c", filepath.Join(p.store.Path, "candidate.json"))
	if e != nil {
		return e
	}
	if c.Run() != nil {
		return ErrConfig
	}
	return nil
}
func (p *Process) Running() bool {
	if p.cmd == nil {
		return false
	}
	select {
	case <-p.done:
		p.cmd = nil
		p.done = nil
		return false
	default:
		return true
	}
}
func (p *Process) Start(ctx context.Context) error {
	if p.Running() {
		return nil
	}
	// Child lifetime is explicit, not tied to an individual command lease.
	c, e := p.command(context.Background(), "-c", filepath.Join(p.store.Path, "active.json"))
	if e != nil {
		return e
	}
	if e = c.Start(); e != nil {
		return ErrProcess
	}
	p.cmd = c
	p.done = make(chan error, 1)
	done := p.done
	go func() { done <- c.Wait() }()
	select {
	case <-done:
		p.cmd = nil
		p.done = nil
		return ErrProcess
	case <-ctx.Done():
		_ = p.Stop()
		return ErrProcess
	case <-time.After(500 * time.Millisecond):
		return nil
	}
}
func (p *Process) Stop() error {
	if !p.Running() {
		return nil
	}
	if e := p.cmd.Process.Signal(os.Interrupt); e != nil && !errors.Is(e, os.ErrProcessDone) {
		return ErrProcess
	}
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		if e := p.cmd.Process.Kill(); e != nil && !errors.Is(e, os.ErrProcessDone) {
			return ErrProcess
		}
		<-p.done
	}
	p.cmd = nil
	p.done = nil
	return nil
}
