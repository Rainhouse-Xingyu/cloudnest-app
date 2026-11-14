//go:build integration && frpnative && launchdnative

package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This helper is called only from the Darwin-specific test. It never installs
// into Library/LaunchAgents or acts on the production label.
func testLaunchAgentRecovery(t *testing.T, ctx context.Context, bin, state, work string, probe, diagnose func()) {
	t.Helper()
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	var nonce [16]byte
	rand.Read(nonce[:])
	label := fmt.Sprintf("com.cloudnest.fixture.%x", nonce)
	target := domain + "/" + label
	run := func(name string, args ...string) []byte {
		t.Helper()
		out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v: %s", name, err, out)
		}
		return out
	}
	plist := filepath.Join(work, label+".plist")
	raw := run(bin, "launch-agent", "--state", state)
	if err := os.WriteFile(plist, raw, 0600); err != nil {
		t.Fatal(err)
	}
	run("plutil", "-lint", plist)
	run("plutil", "-replace", "Label", "-string", label, plist)
	run("launchctl", "bootstrap", domain, plist)
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(c, "launchctl", "bootout", target).CombinedOutput(); err != nil {
			t.Errorf("unload fixture %s: %v %s", target, err, out)
		}
	}()
	awaitPID := func(old string) string {
		t.Helper()
		for ctx.Err() == nil {
			out := run("launchctl", "print", target)
			for _, line := range bytes.Split(out, []byte{'\n'}) {
				id, ok := strings.CutPrefix(strings.TrimSpace(string(line)), "pid = ")
				if n, err := strconv.Atoi(id); ok && err == nil && n > 1 && id != old {
					return id
				}
			}
			time.Sleep(150 * time.Millisecond)
		}
		t.Fatal("no new launchd-managed Agent process")
		return ""
	}
	first := awaitPID("")
	probe()
	diagnose()
	run("launchctl", "kill", "SIGKILL", target)
	second := awaitPID(first)
	probe()
	diagnose()
	t.Logf("launchd replaced Agent PID %s with %s; authenticated diagnose and three live tunnels recovered", first, second)
}

func TestLaunchAgentNativeCrashRecovery(t *testing.T) {
	testAgentNativeTunnels(t, testLaunchAgentRecovery)
}
