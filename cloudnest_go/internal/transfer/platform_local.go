//go:build linux || darwin

package transfer

import (
	"context"
	"errors"
	"math"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func fileIdentity(f *os.File) (uint64, uint64, error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return 0, 0, err
	}
	return uint64(st.Dev), uint64(st.Ino), nil
}
func lockFile(ctx context.Context, f *os.File, shared, wait bool) error {
	kind := unix.LOCK_EX
	if shared {
		kind = unix.LOCK_SH
	}
	for {
		err := unix.Flock(int(f.Fd()), kind|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return err
		}
		if !wait {
			return errLocked
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
func diskFree(path string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	if remoteFilesystem(st) {
		return 0, errUnsupported
	}
	block := uint64(st.Bsize)
	if block == 0 {
		return 0, errUnsupported
	}
	free := uint64(st.Bavail)
	if free > math.MaxInt64/block {
		return math.MaxInt64, nil
	}
	return int64(free * block), nil
}
