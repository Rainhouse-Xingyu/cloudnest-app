//go:build linux

package imageprep

import "context"

// The native helper installs RLIMIT_AS before parsing any source bytes.
func monitorHEIFMemory(ctx context.Context, _ int, _ context.CancelFunc) error {
	<-ctx.Done()
	return nil
}
