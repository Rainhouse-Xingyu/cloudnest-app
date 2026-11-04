package frpagent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, e := OpenStore(filepath.Join(t.TempDir(), "state"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func TestStorePrivateAtomicAndExclusive(t *testing.T) {
	s := testStore(t)
	if other, e := OpenStore(s.Path); e != ErrLocked {
		if other != nil {
			other.Close()
		}
		t.Fatal("duplicate supervisor", e)
	}
	if e := s.Write("credential", []byte("test-secret"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := s.Write("credential", []byte("replacement"), 0600); e != nil {
		t.Fatal(e)
	}
	b, e := s.Read("credential", 20)
	if e != nil || string(b) != "replacement" {
		t.Fatal("atomic replacement", e)
	}
	if _, e = s.Read("credential", 3); e != ErrStorage {
		t.Fatal("unbounded read")
	}
	if e = s.Write("../escape", nil, 0600); e != ErrStorage {
		t.Fatal("path escape")
	}
	if e = os.Chmod(filepath.Join(s.Path, "credential"), 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Read("credential", 20); e != ErrStorage {
		t.Fatal("public credential")
	}
	if e = os.Symlink("credential", filepath.Join(s.Path, "link")); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Read("link", 20); e != ErrStorage {
		t.Fatal("symlink read")
	}
	if e = os.Link(filepath.Join(s.Path, "credential"), filepath.Join(s.Path, "hard")); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Read("hard", 20); e != ErrStorage {
		t.Fatal("hard link read")
	}
	path := s.Path
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	next, e := OpenStore(path)
	if e != nil {
		t.Fatal(e)
	}
	next.Close()
}
func TestStoreRejectsDirectoryAndBinarySubstitution(t *testing.T) {
	s := testStore(t)
	p := filepath.Join(t.TempDir(), "public")
	if e := os.Mkdir(p, 0755); e != nil {
		t.Fatal(e)
	}
	if st, e := OpenStore(p); e != ErrStorage {
		if st != nil {
			st.Close()
		}
		t.Fatal("public state accepted")
	}
	p = filepath.Join(t.TempDir(), "symlink")
	if e := os.Symlink(s.Path, p); e != nil {
		t.Fatal(e)
	}
	if st, e := OpenStore(p); e != ErrStorage {
		if st != nil {
			st.Close()
		}
		t.Fatal("directory link accepted")
	}
	fake := filepath.Join(t.TempDir(), "frpc")
	if e := os.WriteFile(fake, []byte("#!/bin/sh\necho 0.71.1\n"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := InstallBinary(s, fake); !errors.Is(e, ErrBinary) {
		t.Fatal("version-spoofed binary accepted")
	}
}
