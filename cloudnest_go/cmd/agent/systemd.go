package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"
)

func systemdUnit(args []string, out io.Writer) error {
	if runtime.GOOS != "linux" || os.Geteuid() == 0 {
		return errors.New("systemd-unit requires an unprivileged Linux user")
	}
	flags := flag.NewFlagSet("systemd-unit", flag.ContinueOnError)
	state := flags.String("state", "", "absolute enrolled state directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("expected systemd-unit --state /absolute/enrolled/directory")
	}
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	return writeSystemdUnit(out, bin, *state)
}

func writeSystemdUnit(out io.Writer, executable, state string) error {
	for _, path := range []string{executable, state} {
		if !filepath.IsAbs(path) || path == "/" || filepath.Clean(path) != path || !utf8.ValidString(path) || strings.ContainsFunc(path, unicode.IsControl) {
			return errors.New("systemd unit paths must be clean absolute paths without control characters")
		}
	}
	// systemd requires the executable word to be a literal path. It expands
	// percent specifiers in both words and environment references in arguments.
	if strings.Contains(executable, "$") {
		return errors.New("systemd executable path cannot contain a dollar sign")
	}
	escape := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "%", "%%")
	executable = escape.Replace(executable)
	state = strings.ReplaceAll(escape.Replace(state), "$", "$$")
	_, err := fmt.Fprintf(out, `[Unit]
Description=CloudNest FRP Agent

[Service]
Type=exec
ExecStart="%s" run --state "%s"
Restart=on-failure
RestartSec=10s
KillMode=mixed
TimeoutStopSec=15s
UMask=0077
NoNewPrivileges=yes

[Install]
WantedBy=default.target
`, executable, state)
	return err
}
