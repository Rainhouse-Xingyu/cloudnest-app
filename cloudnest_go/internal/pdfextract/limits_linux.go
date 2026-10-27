//go:build linux

package pdfextract

import (
	"context"
	"golang.org/x/sys/unix"
)

func platformMemoryLimit() error {
	var old unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_AS, &old); err != nil {
		return err
	}
	v := min(uint64(memoryLimit), old.Max)
	return unix.Setrlimit(unix.RLIMIT_AS, &unix.Rlimit{Cur: v, Max: v})
}
func monitorMemory(ctx context.Context, pid int, cancel context.CancelFunc) error {
	<-ctx.Done()
	return nil
}
