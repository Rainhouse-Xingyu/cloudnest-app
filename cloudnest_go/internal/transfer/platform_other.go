//go:build !linux && !darwin

package transfer

import (
	"context"
	"os"
)

func fileIdentity(*os.File) (uint64, uint64, error)        { return 0, 0, errUnsupported }
func lockFile(context.Context, *os.File, bool, bool) error { return errUnsupported }
func diskFree(string) (int64, error)                       { return 0, errUnsupported }
