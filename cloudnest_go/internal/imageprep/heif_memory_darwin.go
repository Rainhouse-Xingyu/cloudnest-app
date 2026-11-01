//go:build darwin

package imageprep

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// macOS does not provide a usable RLIMIT_AS here. This watchdog can overshoot
// between observations; it is not an OS-enforced resident-memory ceiling.
func monitorHEIFMemory(ctx context.Context, pid int, cancel context.CancelFunc) error {
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
			b, err := cmd.Output()
			end()
			if ctx.Err() != nil {
				return nil
			}
			if err != nil {
				var exited *exec.ExitError
				if errors.As(err, &exited) && exited.ExitCode() == 1 {
					return nil
				}
				cancel()
				return ErrLimit
			}
			rss, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
			if err != nil || rss > heifMemoryLimit/1024 {
				cancel()
				return ErrLimit
			}
		}
	}
}
