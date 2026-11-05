package frpagent

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

var ErrStorage = errors.New("private agent storage unavailable")
var ErrLocked = errors.New("agent or managed frpc already holds this state directory")

// Store anchors all I/O to an open, private directory. Close must happen only
// after owned children have exited. Children inherit the flock descriptor, so
// an abrupt Agent death cannot allow a second Agent to launch duplicate frpc.
type Store struct {
	dir, lock *os.File
	Path      string
	vault     *vault
}

func OpenStore(path string) (*Store, error) {
	if !filepath.IsAbs(path) {
		return nil, ErrStorage
	}
	path = filepath.Clean(path)
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, ErrStorage
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrStorage
	}
	s := &Store{dir: os.NewFile(uintptr(fd), "agent-state"), Path: path}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || int(st.Uid) != os.Geteuid() || st.Mode&0077 != 0 {
		s.Close()
		return nil, ErrStorage
	}
	l, err := s.open("agent.lock", unix.O_RDWR|unix.O_CREAT, 0600)
	if err != nil {
		s.Close()
		return nil, err
	}
	s.lock = l
	if unix.Flock(int(l.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		s.Close()
		return nil, ErrLocked
	}
	return s, nil
}
func (s *Store) Close() error {
	s.vault = nil
	// Do not LOCK_UN: children share this open-file description and must retain
	// the lock if the parent dies. The last close releases it.
	if s.lock != nil {
		s.lock.Close()
		s.lock = nil
	}
	if s.dir != nil {
		err := s.dir.Close()
		s.dir = nil
		return err
	}
	return nil
}

// RemoveRuntime only removes Agent-generated plaintext runtime caches, never
// identity/history or operator key files. Active children retain the lock.
func (s *Store) RemoveRuntime(name string) error {
	if name != "active.json" && name != "candidate.json" && name != "diagnostics.pem" {
		return ErrStorage
	}
	f, e := s.open(name, unix.O_RDONLY, 0)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	f.Close()
	if unix.Unlinkat(int(s.dir.Fd()), name, 0) != nil || s.dir.Sync() != nil {
		return ErrStorage
	}
	return nil
}
func localName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\")
}
func (s *Store) open(name string, flags int, mode uint32) (*os.File, error) {
	if !localName(name) || s.dir == nil {
		return nil, ErrStorage
	}
	fd, err := unix.Openat(int(s.dir.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, mode)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, os.ErrNotExist
		}
		return nil, ErrStorage
	}
	f := os.NewFile(uintptr(fd), name)
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0077 != 0 || st.Nlink != 1 || int(st.Uid) != os.Geteuid() {
		f.Close()
		return nil, ErrStorage
	}
	return f, nil
}
func (s *Store) Read(name string, limit int64) ([]byte, error) {
	f, err := s.open(name, unix.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, ErrStorage
	}
	return b, nil
}
func (s *Store) Write(name string, data []byte, mode uint32) error {
	if !localName(name) || (mode != 0600 && mode != 0700) {
		return ErrStorage
	}
	id, err := randomID()
	if err != nil {
		return ErrStorage
	}
	temp := ".write-" + id
	f, err := s.open(temp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer unix.Unlinkat(int(s.dir.Fd()), temp, 0)
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return ErrStorage
	}
	if unix.Renameat(int(s.dir.Fd()), temp, int(s.dir.Fd()), name) != nil || s.dir.Sync() != nil {
		return ErrStorage
	}
	return nil
}
func randomID() (string, error) {
	var b [16]byte
	_, err := rand.Read(b[:])
	return hex.EncodeToString(b[:]), err
}
func validID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
