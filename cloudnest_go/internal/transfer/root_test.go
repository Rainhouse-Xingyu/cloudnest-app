//go:build darwin || linux

package transfer

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestAdmissionGateConcurrentCreationKeepsOneInode(t *testing.T) {
	for range 64 {
		dir := t.TempDir()
		var group sync.WaitGroup
		infos := make(chan os.FileInfo, 16)
		failures := make(chan error, 16)
		for range 16 {
			group.Go(func() {
				root, err := os.OpenRoot(dir)
				if err != nil {
					failures <- err
					return
				}
				defer root.Close()
				file, err := openAdmissionGate(root, true)
				if err != nil {
					failures <- err
					return
				}
				defer file.Close()
				info, err := file.Stat()
				if err != nil {
					failures <- err
					return
				}
				infos <- info
			})
		}
		group.Wait()
		close(failures)
		close(infos)
		for err := range failures {
			t.Error(err)
		}
		if t.Failed() {
			t.FailNow()
		}
		actual, err := os.Stat(filepath.Join(dir, admissionGatePath))
		if err != nil {
			t.Fatal(err)
		}
		for info := range infos {
			if !os.SameFile(info, actual) || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
				t.Fatal("gate identity or permissions changed")
			}
		}
	}
}

func TestAdmissionGateReadOnlyNeverCreatesAndExistingBytesRemain(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if file, err := openAdmissionGate(root, false); !errors.Is(err, os.ErrNotExist) {
		if file != nil {
			file.Close()
		}
		t.Fatal("read-only maintenance created a gate", err)
	}
	const marker = "retain gate inode and bytes"
	if err := os.WriteFile(filepath.Join(dir, admissionGatePath), []byte(marker), 0600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(filepath.Join(dir, admissionGatePath))
	file, err := openAdmissionGate(root, true)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("existing gate replaced", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, admissionGatePath))
	if err != nil || string(data) != marker {
		t.Fatal("existing gate truncated", err)
	}
}
