package frpagent

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"testing"
)

func TestSupervisorRejectsMismatchedInheritedDescriptors(t *testing.T) {
	s, other := testStore(t), testStore(t)
	if _, e := inheritedStore(s.Path, s.dir, s.lock); e != nil {
		t.Fatal("own inherited lock rejected", e)
	}
	if _, e := inheritedStore(s.Path, s.dir, other.lock); e != ErrStorage {
		t.Fatal("foreign lock accepted", e)
	}
	if _, e := inheritedStore(other.Path, s.dir, s.lock); e != ErrStorage {
		t.Fatal("path and anchored directory differ", e)
	}
	if _, e := inheritedStore("relative", s.dir, s.lock); e != ErrStorage {
		t.Fatal("relative executable directory accepted", e)
	}
}

func TestSupervisorUnknownExitPreventsDuplicateProcess(t *testing.T) {
	s := testStore(t)
	r, e := NewRunner(s, nil, Identity{Registration: Registration{DeviceID: 1}, Policy: testPolicy()})
	if e != nil {
		t.Fatal(e)
	}
	reader, writer, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	defer reader.Close()
	r.process.life = writer
	r.process.exited(errors.New("unexpected supervisor loss"))
	var b [1]byte
	if _, e = reader.Read(b[:]); e != io.EOF {
		t.Fatal("liveness writer leaked", e)
	}
	if e = r.process.Start(context.Background()); e != ErrSupervisor {
		t.Fatal("unsafe duplicate restart", e)
	}
	r.process.cmd = &exec.Cmd{}
	r.process.done = make(chan error)
	if e = r.process.Start(context.Background()); e != ErrSupervisor {
		t.Fatal("uncertain running child treated as healthy", e)
	}
	r.process.cmd = nil
	r.process.done = nil
	if e = r.Run(context.Background()); e != ErrSupervisor {
		t.Fatal("Agent continued after supervision loss", e)
	}
}
