package frpagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestVaultAuthenticationAndNoPlaintextFallback(t *testing.T) {
	s := testStore(t)
	plain := []byte("synthetic-journal-secret")
	if e := s.WriteSecret("state.json", plain); e != nil {
		t.Fatal(e)
	}
	raw, e := s.Read("state.json", 4096)
	if e != nil || bytes.Contains(raw, plain) {
		t.Fatal("plaintext journal", e)
	}
	if e = s.WriteSecret("state.json", plain); e != nil {
		t.Fatal(e)
	}
	second, e := s.Read("state.json", 4096)
	if e != nil || bytes.Equal(raw, second) {
		t.Fatal("nonce reused", e)
	}
	for i := len(vaultPrefix); i < len(raw); i++ {
		bad := bytes.Clone(raw)
		bad[i] ^= 1
		if e = s.Write("state.json", bad, 0600); e != nil {
			t.Fatal(e)
		}
		if _, e = s.ReadSecret("state.json"); !errors.Is(e, ErrVault) {
			t.Fatalf("tampering accepted at %d", i)
		}
	}
	if e = s.Write("identity.json", raw, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ReadSecret("identity.json"); !errors.Is(e, ErrVault) {
		t.Fatal("cross-file ciphertext accepted")
	}
	other, e := OpenStore(filepath.Join(t.TempDir(), "state"))
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	if e = UnlockVault(other, VaultOptions{KeyFile: s.vault.metadata.KeyFile}, true); e != nil {
		t.Fatal(e)
	}
	if e = other.Write("state.json", raw, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = other.ReadSecret("state.json"); !errors.Is(e, ErrVault) {
		t.Fatal("ciphertext moved between vault IDs", e)
	}
	if e = s.Write("state.json", plain, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ReadSecret("state.json"); !errors.Is(e, ErrPlainState) {
		t.Fatal("legacy plaintext accepted")
	}
	if e = os.Chmod(s.vault.metadata.KeyFile, 0644); e != nil {
		t.Fatal(e)
	}
	s.vault = nil
	if e = UnlockVault(s, VaultOptions{}, false); !errors.Is(e, ErrVault) {
		t.Fatal("insecure key file accepted", e)
	}
}

func TestVaultExplicitResumableMigration(t *testing.T) {
	s := testStore(t)
	id := Identity{Version: 1, Endpoint: "https://example.com", Registration: Registration{DeviceID: 1, Credential: fixtureToken()}, Policy: testPolicy()}
	plain, e := json.Marshal(id)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Write("identity.json", plain, 0600); e != nil {
		t.Fatal(e)
	}
	if e = s.Write("state.json", []byte("invalid"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = LoadIdentity(s); !errors.Is(e, ErrPlainState) {
		t.Fatal("implicit migration", e)
	}
	if e = ProtectState(s, VaultOptions{}); e == nil {
		t.Fatal("invalid legacy journal accepted")
	}
	sealed, e := s.Read("identity.json", 4096)
	if e != nil || !bytes.HasPrefix(sealed, []byte(vaultPrefix)) {
		t.Fatal("first migration step not committed", e)
	}
	state := State{Version: 1, Completed: map[string]Outcome{}}
	legacy, e := json.Marshal(state)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Write("state.json", legacy, 0600); e != nil {
		t.Fatal(e)
	}
	if e = ProtectState(s, VaultOptions{}); e != nil {
		t.Fatal("migration resume", e)
	}
	after, e := s.Read("identity.json", 4096)
	if e != nil || !bytes.Equal(after, sealed) {
		t.Fatal("already sealed file rewritten", e)
	}
	loaded, e := LoadIdentity(s)
	if e != nil || loaded.Registration.Credential != fixtureToken() {
		t.Fatal("migrated credential differs", e)
	}
	keyPath := s.vault.metadata.KeyFile
	if e = os.Remove(keyPath); e != nil {
		t.Fatal(e)
	}
	s.vault = nil
	if e = UnlockVault(s, VaultOptions{}, true); !errors.Is(e, ErrVault) {
		t.Fatal("missing key silently regenerated", e)
	}
}

func TestVaultRejectsKeyInsideState(t *testing.T) {
	s, e := OpenStore(filepath.Join(t.TempDir(), "state"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	path := filepath.Join(s.Path, "key")
	if e = os.WriteFile(path, bytes.Repeat([]byte{1}, 32), 0600); e != nil {
		t.Fatal(e)
	}
	if e = UnlockVault(s, VaultOptions{KeyFile: path}, true); !errors.Is(e, ErrVault) {
		t.Fatal("key stored beside ciphertext", e)
	}
	if e = UnlockVault(s, VaultOptions{}, false); !errors.Is(e, ErrPlainState) {
		t.Fatal("unencrypted state ran implicitly", e)
	}
}

func TestRuntimeCleanupCannotRemovePersistentState(t *testing.T) {
	s := testStore(t)
	if e := s.WriteSecret("identity.json", []byte("synthetic identity")); e != nil {
		t.Fatal(e)
	}
	if e := s.RemoveRuntime("identity.json"); e != ErrStorage {
		t.Fatal("identity removed by runtime cleanup", e)
	}
	if e := s.Write("candidate.json", []byte("synthetic runtime"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := s.RemoveRuntime("candidate.json"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ReadSecret("identity.json"); e != nil {
		t.Fatal("identity changed", e)
	}
	if e := s.RemoveRuntime("candidate.json"); e != nil {
		t.Fatal("cleanup not idempotent", e)
	}
}
