// The standalone CLI prints a user LaunchAgent definition. Loading a job is
// an explicit local operator action, and never requires an enrollment token.
package main

import (
	"bytes"
	"encoding/xml"
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

func launchAgent(args []string, out io.Writer) error {
	if runtime.GOOS != "darwin" {
		return errors.New("launch-agent is only supported on macOS")
	}
	f := flag.NewFlagSet("launch-agent", flag.ContinueOnError)
	state := f.String("state", "", "absolute enrolled state directory")
	if e := f.Parse(args); e != nil {
		return e
	}
	if f.NArg() != 0 || !filepath.IsAbs(*state) || filepath.Clean(*state) != *state {
		return errors.New("expected launch-agent --state /absolute/enrolled/directory")
	}
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	escape := func(path string) (string, error) {
		if !utf8.ValidString(path) || strings.ContainsFunc(path, unicode.IsControl) || strings.ContainsAny(path, "\uFFFE\uFFFF") {
			return "", errors.New("invalid path for a LaunchAgent")
		}
		var b bytes.Buffer
		err := xml.EscapeText(&b, []byte(path))
		return b.String(), err
	}
	exe, err := escape(bin)
	if err != nil {
		return err
	}
	dir, err := escape(*state)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.cloudnest.agent</string>
  <key>ProgramArguments</key><array>
    <string>%s</string><string>run</string><string>--state</string><string>%s</string>
  </array>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>ExitTimeOut</key><integer>15</integer>
  <key>Umask</key><integer>63</integer>
</dict></plist>
`, exe, dir)
	return err
}
