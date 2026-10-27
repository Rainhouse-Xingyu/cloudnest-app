//go:build darwin

package pdfextract

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Darwin does not enforce a usable RLIMIT_AS for this process. Sample RSS and
// kill on excess instead. This is a watchdog threshold, NOT a hard RSS ceiling:
// allocation can overshoot between observations. Linux uses hard RLIMIT_AS.
func platformMemoryLimit() error { return nil }
func monitorMemory(ctx context.Context, pid int, cancel context.CancelFunc) error {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			probe, end := context.WithTimeout(ctx, time.Second)
			cmd := exec.CommandContext(probe, "/bin/ps", "-o", "rss=", "-p", strconv.Itoa(pid))
			cmd.Env = []string{"LC_ALL=C"}
			data, err := cmd.Output()
			end()
			if ctx.Err() != nil {
				return nil
			}
			// Exit between observation and ps is expected. A monitor failure is handled
			// conservatively; successful parse output is never published after it.
			if err != nil {
				var exited *exec.ExitError
				if errors.As(err, &exited) && exited.ExitCode() == 1 {
					return nil
				}
				cancel()
				return ErrLimit
			}
			rss, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
			if err != nil || rss > memoryLimit/1024 {
				cancel()
				return ErrLimit
			}
		}
	}
}
