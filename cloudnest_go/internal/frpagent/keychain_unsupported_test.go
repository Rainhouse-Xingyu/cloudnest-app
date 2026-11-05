//go:build !darwin || !cgo

package frpagent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestUnavailableKeychainNeverCreatesFallbackKey(t *testing.T) {
	s, e := OpenStore(filepath.Join(t.TempDir(), "state"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = UnlockVault(s, VaultOptions{}, true); !errors.Is(e, ErrVault) {
		t.Fatal("missing Keychain accepted", e)
	}
	files, e := os.ReadDir(s.Path)
	if e != nil {
		t.Fatal(e)
	}
	if len(files) != 1 || files[0].Name() != "agent.lock" {
		t.Fatal("implicit key backend created")
	}
}
