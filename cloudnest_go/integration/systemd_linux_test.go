//go:build integration && frpnative && systemdnative

package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSystemdNativeCrashRecovery(t *testing.T) {
	testAgentNativeTunnels(t, testSystemdRecovery)
}

func testSystemdRecovery(t *testing.T, ctx context.Context, bin, state, work string, probe, diagnose func()) {
	t.Helper()
	run := func(name string, args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v: %s", name, args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	// work is unique to this fixture. Linking avoids editing any existing unit.
	label := fmt.Sprintf("cloudnest-fixture-%d-%d.service", os.Getpid(), time.Now().UnixNano())
	unit := filepath.Join(work, label)
	raw := run(bin, "systemd-unit", "--state", state)
	if err := os.WriteFile(unit, []byte(raw+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run("systemd-analyze", "--user", "verify", unit)
	run("systemctl", "--user", "link", unit)
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(c, "systemctl", "--user", "disable", "--now", label).CombinedOutput(); err != nil {
			t.Errorf("cleanup fixture unit: %v: %s", err, out)
		}
	}()
	run("systemctl", "--user", "start", label)
	awaitPID := func(old int) int {
		t.Helper()
		for ctx.Err() == nil {
			value := run("systemctl", "--user", "show", "--property=MainPID", "--value", label)
			if pid, err := strconv.Atoi(value); err == nil && pid > 1 && pid != old {
				return pid
			}
			time.Sleep(150 * time.Millisecond)
		}
		t.Fatal("systemd did not start a new Agent")
		return 0
	}
	first := awaitPID(0)
	probe()
	diagnose()
	run("systemctl", "--user", "kill", "--kill-whom=main", "--signal=SIGKILL", label)
	second := awaitPID(first)
	probe()
	diagnose()
	run("systemctl", "--user", "stop", label)
	if pid := run("systemctl", "--user", "show", "--property=MainPID", "--value", label); pid != "0" {
		t.Fatal("systemd retained main process after stop")
	}
	t.Logf("systemd replaced Agent PID %d with %d; three tunnels and authenticated diagnostics recovered", first, second)
}
