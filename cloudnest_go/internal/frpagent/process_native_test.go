//go:build frpnative

package frpagent

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPinnedFRPCInheritsSingletonLock(t *testing.T) {
	dir := os.Getenv("CLOUDNEST_TEST_FRP_BIN_DIR")
	if !filepath.IsAbs(dir) {
		t.Fatal("set official FRP binary directory")
	}
	s := testStore(t)
	if e := InstallBinary(s, filepath.Join(dir, "frpc")); e != nil {
		t.Fatal(e)
	}
	// Own the control port even though this test intentionally provides no FRP
	// handshake; never contact a user's existing listener on a fixed port.
	listener, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	policy := testPolicy()
	policy.ServerPort = listener.Addr().(*net.TCPAddr).Port
	raw := strings.Replace(validConfig, "serverPort: 7000", "serverPort: "+strconv.Itoa(policy.ServerPort), 1)
	b, e := Normalize([]byte(raw), policy)
	if e != nil {
		t.Fatal(e)
	}
	verifyToken, e := s.stageRuntime("candidate.json", b)
	if e != nil {
		t.Fatal(e)
	}
	defer verifyToken.Close()
	p := newRawProcess(s)
	defer p.Stop()
	if e = p.Verify(context.Background(), verifyToken); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(s.Path, "candidate.json")); !os.IsNotExist(e) {
		t.Fatal("verification cache retained", e)
	}
	startToken, e := s.stageRuntime("active.json", b)
	if e != nil {
		t.Fatal(e)
	}
	defer startToken.Close()
	if e = p.Start(context.Background(), startToken); e != nil {
		t.Fatal(e)
	}
	// Closing all parent's descriptors models the OS cleanup after SIGKILL;
	// actual official frpc must retain the inherited lock, not just a test child.
	path := s.Path
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	if other, e := OpenStore(path); e != ErrLocked {
		if other != nil {
			other.Close()
		}
		t.Fatal("orphan frpc did not fence next supervisor", e)
	}
	if e = p.Stop(); e != nil {
		t.Fatal(e)
	}
	next, e := OpenStore(path)
	if e != nil {
		t.Fatal("lock not released after frpc exit", e)
	}
	next.Close()
}
