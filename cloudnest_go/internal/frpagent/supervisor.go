package frpagent

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const (
	supervisorChildExited = 20
	supervisorSetupFailed = 21
	supervisorUnsafeExit  = 22
)

// SupervisorMain is the internal CLI entry point. All five descriptors must
// be inherited from an already locked Agent. No PID file or remote-supplied
// process identity is used. The liveness pipe has exactly one writer: Agent.
func SupervisorMain(ctx context.Context, path string) (exitCode int) {
	dir := os.NewFile(3, "agent-directory")
	lock := os.NewFile(4, "agent-lock")
	life := os.NewFile(5, "agent-liveness")
	ready := os.NewFile(6, "agent-readiness")
	tokenPipe := os.NewFile(7, "frpc-token-source")
	defer tokenPipe.Close()
	defer dir.Close()
	defer lock.Close()
	defer life.Close()
	defer ready.Close()
	s, err := inheritedStore(path, dir, lock)
	if err != nil {
		return supervisorSetupFailed
	}
	var lifeStat, readyStat, tokenStat unix.Stat_t
	mode, err := unix.FcntlInt(tokenPipe.Fd(), unix.F_GETFL, 0)
	if err != nil || mode&unix.O_ACCMODE != unix.O_RDONLY || unix.Fstat(int(tokenPipe.Fd()), &tokenStat) != nil || tokenStat.Mode&unix.S_IFMT != unix.S_IFIFO {
		return supervisorSetupFailed
	}
	if unix.Fstat(int(life.Fd()), &lifeStat) != nil || lifeStat.Mode&unix.S_IFMT != unix.S_IFIFO || unix.Fstat(int(ready.Fd()), &readyStat) != nil || readyStat.Mode&unix.S_IFMT != unix.S_IFIFO {
		return supervisorSetupFailed
	}
	// Inherited descriptors are open across this exec by design. Only raw frpc's
	// explicit lock and token read end should survive the next exec.
	for _, fd := range []int{3, 4, 5, 6, 7} {
		unix.CloseOnExec(fd)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { var b [1]byte; _, _ = io.ReadFull(life, b[:]); cancel() }() // EOF, error or data all stop the child.
	p := newRawProcess(s)
	defer func() {
		if err := p.Stop(); err != nil {
			exitCode = supervisorUnsafeExit
		} else if err := s.RemoveRuntime("active.json"); err != nil {
			exitCode = supervisorUnsafeExit
		}
	}()
	if err = p.Start(ctx, tokenPipe); err != nil {
		return supervisorSetupFailed
	}
	tokenPipe.Close()
	if _, err = ready.Write([]byte{1}); err != nil {
		return supervisorSetupFailed
	}
	ready.Close()
	select {
	case <-ctx.Done():
		return 0
	case <-p.done:
		// Wait has reaped the exact child. This exit code authorizes a later
		// restart; a signal-killed supervisor never gives that guarantee.
		p.cmd = nil
		p.done = nil
		return supervisorChildExited
	}
}

func inheritedStore(path string, dir, lock *os.File) (*Store, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrStorage
	}
	var ds, ls unix.Stat_t
	if unix.Fstat(int(dir.Fd()), &ds) != nil || ds.Mode&unix.S_IFMT != unix.S_IFDIR || ds.Mode&0077 != 0 || int(ds.Uid) != os.Geteuid() {
		return nil, ErrStorage
	}
	pathFD, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrStorage
	}
	defer unix.Close(pathFD)
	var ps unix.Stat_t
	if unix.Fstat(pathFD, &ps) != nil || ps.Dev != ds.Dev || ps.Ino != ds.Ino {
		return nil, ErrStorage
	}
	if unix.Fstat(int(lock.Fd()), &ls) != nil || ls.Mode&unix.S_IFMT != unix.S_IFREG || ls.Mode&0077 != 0 || ls.Nlink != 1 || int(ls.Uid) != os.Geteuid() {
		return nil, ErrStorage
	}
	s := &Store{dir: dir, lock: lock, Path: path}
	actual, err := s.open("agent.lock", unix.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer actual.Close()
	var as unix.Stat_t
	if unix.Fstat(int(actual.Fd()), &as) != nil || as.Dev != ls.Dev || as.Ino != ls.Ino {
		return nil, ErrStorage
	}
	// Same inherited open-file description succeeds; a competing supervisor
	// holding a different description cannot silently adopt this lock.
	if unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		return nil, ErrLocked
	}
	return s, nil
}
