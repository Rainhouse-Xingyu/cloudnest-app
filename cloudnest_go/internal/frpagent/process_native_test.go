//go:build frpnative

package frpagent

import (
	"context"
	"os"
	"path/filepath"
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
	b, e := Normalize([]byte(validConfig), testPolicy())
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Write("candidate.json", b, 0600); e != nil {
		t.Fatal(e)
	}
	p := NewProcess(s)
	defer p.Stop()
	if e = p.Verify(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e = s.Write("active.json", b, 0600); e != nil {
		t.Fatal(e)
	}
	if e = p.Start(context.Background()); e != nil {
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
