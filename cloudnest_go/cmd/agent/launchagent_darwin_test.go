package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchAgentPlist(t *testing.T) {
	state := filepath.Join(t.TempDir(), "A&B <用户> \"quoted\"")
	var plist bytes.Buffer
	if err := launchAgent([]string{"--state", state}, &plist); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"Label": "com.cloudnest.agent", "ProgramArguments.0": exe,
		"ProgramArguments.1": "run", "ProgramArguments.2": "--state",
		"ProgramArguments.3": state, "KeepAlive": "true", "Umask": "63",
	} {
		cmd := exec.Command("/usr/bin/plutil", "-extract", key, "raw", "-o", "-", "-")
		cmd.Stdin = bytes.NewReader(plist.Bytes())
		out, err := cmd.CombinedOutput()
		if err != nil || strings.TrimSuffix(string(out), "\n") != want {
			t.Fatalf("plist %s: %q, %v", key, out, err)
		}
	}
	for _, invalid := range []string{"relative", "/path/../state", "/line\nbreak"} {
		var out bytes.Buffer
		if err := launchAgent([]string{"--state", invalid}, &out); err == nil || out.Len() != 0 {
			t.Fatal("invalid service path produced plist")
		}
	}
}
