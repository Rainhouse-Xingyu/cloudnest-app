//go:build darwin && cgo && keychainnative

package frpagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDarwinKeychainVault(t *testing.T) {
	id, e := randomID()
	if e != nil {
		t.Fatal(e)
	}
	password := []byte(id)
	chain := filepath.Join(t.TempDir(), "agent-test.keychain")
	if e = testKeychainOperation(chain, password, 0); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := testKeychainOperation(chain, password, 3); e != nil {
			t.Error(e)
		}
	})
	s, e := OpenStore(filepath.Join(t.TempDir(), "state"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = UnlockVault(s, VaultOptions{Keychain: chain}, true); e != nil {
		t.Fatal("Keychain create", e)
	}
	plain := []byte("synthetic-agent-keychain-secret")
	if e = s.WriteSecret("identity.json", plain); e != nil {
		t.Fatal(e)
	}
	raw, e := s.Read("identity.json", 4096)
	if e != nil || bytes.Contains(raw, plain) {
		t.Fatal("plaintext on disk", e)
	}
	firstKey := make([]byte, 32)
	if e = keychainKey(chain, s.vault.metadata.ID, firstKey, false); e != nil {
		t.Fatal(e)
	}
	if e = keychainKey(chain, s.vault.metadata.ID, bytes.Repeat([]byte{0}, 32), true); e == nil {
		t.Fatal("duplicate key overwritten")
	}
	loaded := make([]byte, 32)
	if e = keychainKey(chain, s.vault.metadata.ID, loaded, false); e != nil || !bytes.Equal(firstKey, loaded) {
		t.Fatal("original Keychain key changed", e)
	}
	s.vault = nil
	if e = UnlockVault(s, VaultOptions{}, false); e != nil {
		t.Fatal("Keychain reopen", e)
	}
	b, e := s.ReadSecret("identity.json")
	if e != nil || !bytes.Equal(b, plain) {
		t.Fatal("Keychain round trip", e)
	}
	if e = testKeychainOperation(chain, password, 1); e != nil {
		t.Fatal(e)
	}
	s.vault = nil
	if e = UnlockVault(s, VaultOptions{}, false); !errors.Is(e, ErrVault) {
		t.Fatal("locked keychain silently bypassed", e)
	}
	if e = testKeychainOperation(chain, password, 2); e != nil {
		t.Fatal(e)
	}
	if e = UnlockVault(s, VaultOptions{}, false); e != nil {
		t.Fatal("unlock recovery", e)
	}
	// Metadata points only to a Keychain account, never to raw key material.
	meta, e := os.ReadFile(filepath.Join(s.Path, "vault.json"))
	if e != nil || bytes.Contains(meta, firstKey) {
		t.Fatal("key copied into metadata", e)
	}
}

func TestDarwinKeychainCLIProtectState(t *testing.T) {
	id, e := randomID()
	if e != nil {
		t.Fatal(e)
	}
	password := []byte(id)
	chain := filepath.Join(t.TempDir(), "agent-cli-test.keychain")
	if e = testKeychainOperation(chain, password, 0); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := testKeychainOperation(chain, password, 3); e != nil {
			t.Error(e)
		}
	})
	statePath := filepath.Join(t.TempDir(), "state")
	s, e := OpenStore(statePath)
	if e != nil {
		t.Fatal(e)
	}
	identity := Identity{Version: 1, Endpoint: "https://example.com", Registration: Registration{DeviceID: 1, Credential: fixtureToken()}, Policy: testPolicy()}
	b, e := json.Marshal(identity)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Write("identity.json", b, 0600); e != nil {
		t.Fatal(e)
	}
	s.Close()
	bin := filepath.Join(t.TempDir(), "agent")
	if out, e := exec.Command("go", "build", "-o", bin, "../../cmd/agent").CombinedOutput(); e != nil {
		t.Fatalf("build CLI %v %s", e, out)
	}
	call := func(wantSuccess bool) {
		t.Helper()
		out, e := exec.Command(bin, "protect-state", "--state", statePath, "--keychain", chain).CombinedOutput()
		if (e == nil) != wantSuccess {
			t.Fatalf("protect-state success=%v want=%v: %s", e == nil, wantSuccess, out)
		}
		if bytes.Contains(out, []byte(fixtureToken())) {
			t.Fatal("CLI disclosed credential")
		}
	}
	call(true)
	sealed, e := os.ReadFile(filepath.Join(statePath, "identity.json"))
	if e != nil || !bytes.HasPrefix(sealed, []byte(vaultPrefix)) || bytes.Contains(sealed, []byte(fixtureToken())) {
		t.Fatal("CLI did not seal credential", e)
	}
	call(true)
	after, e := os.ReadFile(filepath.Join(statePath, "identity.json"))
	if e != nil || !bytes.Equal(after, sealed) {
		t.Fatal("migration retry rewrote sealed identity", e)
	}
	if e = testKeychainOperation(chain, password, 1); e != nil {
		t.Fatal(e)
	}
	call(false)
	if e = testKeychainOperation(chain, password, 2); e != nil {
		t.Fatal(e)
	}
	call(true)
}
