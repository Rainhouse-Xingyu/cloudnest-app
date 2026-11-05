package frpagent

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"golang.org/x/sys/unix"
)

var ErrVault = errors.New("agent encryption key unavailable or state authentication failed")
var ErrPlainState = errors.New("unencrypted agent state requires explicit protect-state migration")

const vaultPrefix = "CNAGENT1\x00"

type VaultOptions struct{ KeyFile, Keychain string }
type vaultMetadata struct {
	Version  int    `json:"version"`
	ID       string `json:"id"`
	Backend  string `json:"backend"`
	KeyFile  string `json:"key_file,omitempty"`
	Keychain string `json:"keychain,omitempty"`
}
type vault struct {
	metadata vaultMetadata
	aead     cipher.AEAD
}

// UnlockVault never substitutes a file key for a failed Keychain lookup. A new
// Linux installation requires an explicit operator-owned 32-byte key file;
// macOS defaults to Keychain when no file backend was explicitly selected.
func UnlockVault(s *Store, options VaultOptions, create bool) error {
	s.vault = nil
	raw, e := s.Read("vault.json", 8192)
	var meta vaultMetadata
	newVault := errors.Is(e, os.ErrNotExist)
	if newVault {
		if !create {
			return ErrPlainState
		}
		id, err := randomID()
		if err != nil {
			return ErrVault
		}
		meta = vaultMetadata{Version: 1, ID: id, Backend: "keychain", Keychain: options.Keychain}
		if options.KeyFile != "" {
			if options.Keychain != "" {
				return ErrVault
			}
			meta.Backend = "file"
			meta.KeyFile = options.KeyFile
		}
		if runtime.GOOS != "darwin" && meta.Backend != "file" {
			return fmt.Errorf("%w: this platform requires explicit --key-file", ErrVault)
		}
	} else {
		if e != nil {
			return ErrVault
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if d.Decode(&meta) != nil || d.Decode(&struct{}{}) != io.EOF {
			return ErrVault
		}
		if options.KeyFile != "" && (meta.Backend != "file" || meta.KeyFile != options.KeyFile) {
			return ErrVault
		}
		if options.Keychain != "" && (meta.Backend != "keychain" || meta.Keychain != options.Keychain) {
			return ErrVault
		}
	}
	if meta.Version != 1 || !validID(meta.ID) {
		return ErrVault
	}
	var key []byte
	switch meta.Backend {
	case "file":
		if meta.Keychain != "" {
			return ErrVault
		}
		key, e = readVaultKey(meta.KeyFile, s.Path)
	case "keychain":
		if meta.KeyFile != "" || (meta.Keychain != "" && !filepath.IsAbs(meta.Keychain)) {
			return ErrVault
		}
		if newVault && meta.Keychain == "" {
			meta.Keychain, e = defaultKeychainPath()
			if e != nil {
				return e
			}
		}
		if newVault {
			key = make([]byte, 32)
			if _, e = rand.Read(key); e != nil {
				return ErrVault
			}
			e = keychainKey(meta.Keychain, meta.ID, key, true)
		} else {
			key = make([]byte, 32)
			e = keychainKey(meta.Keychain, meta.ID, key, false)
		}
	default:
		return ErrVault
	}
	defer clear(key)
	if e != nil {
		return e
	}
	if len(key) != 32 {
		return ErrVault
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return ErrVault
	}
	aead, e := cipher.NewGCMWithRandomNonce(block)
	if e != nil {
		return ErrVault
	}
	if newVault {
		b, e := json.Marshal(meta)
		if e != nil {
			return ErrVault
		}
		if e = s.Write("vault.json", b, 0600); e != nil {
			// Metadata may have been renamed before a directory fsync failure.
			// Keep the key rather than making possibly committed data unreadable.
			return ErrVault
		}
	}
	s.vault = &vault{metadata: meta, aead: aead}
	return nil
}
func readVaultKey(path, statePath string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, ErrVault
	}
	parent, e := filepath.EvalSymlinks(filepath.Dir(path))
	if e != nil {
		return nil, ErrVault
	}
	state, e := filepath.EvalSymlinks(statePath)
	if e != nil {
		return nil, ErrVault
	}
	rel, e := filepath.Rel(state, filepath.Join(parent, filepath.Base(path)))
	if e != nil || rel == "." || (!filepath.IsAbs(rel) && rel != ".." && !bytes.HasPrefix([]byte(rel), []byte(".."+string(os.PathSeparator)))) {
		return nil, ErrVault
	}
	fd, e := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if e != nil {
		return nil, ErrVault
	}
	f := os.NewFile(uintptr(fd), "agent-vault-key")
	defer f.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0077 != 0 || st.Nlink != 1 || int(st.Uid) != os.Geteuid() {
		return nil, ErrVault
	}
	b, e := io.ReadAll(io.LimitReader(f, 33))
	if e != nil || len(b) != 32 {
		return nil, ErrVault
	}
	return b, nil
}
func (s *Store) secretAAD(name string) []byte {
	return []byte("cloudnest/agent/v1/" + s.vault.metadata.ID + "/" + name)
}
func (s *Store) WriteSecret(name string, raw []byte) error {
	if s.vault == nil || !localName(name) || len(raw) > 4<<20 {
		return ErrVault
	}
	sealed := s.vault.aead.Seal(nil, nil, raw, s.secretAAD(name))
	return s.Write(name, append([]byte(vaultPrefix), sealed...), 0600)
}
func (s *Store) ReadSecret(name string) ([]byte, error) {
	if s.vault == nil {
		return nil, ErrVault
	}
	raw, e := s.Read(name, (4<<20)+64)
	if e != nil {
		return nil, e
	}
	if !bytes.HasPrefix(raw, []byte(vaultPrefix)) {
		if bytes.HasPrefix(raw, []byte("CNAGENT")) {
			return nil, ErrVault
		}
		return nil, ErrPlainState
	}
	b, e := s.vault.aead.Open(nil, nil, raw[len(vaultPrefix):], s.secretAAD(name))
	if e != nil {
		return nil, ErrVault
	}
	return b, nil
}

// ProtectState is an explicit, resumable local migration. Existing ciphertext
// must authenticate, and each plaintext JSON file is validated before replace.
// Old backups/snapshots are not erased by this operation.
func ProtectState(s *Store, options VaultOptions) error {
	identity, e := s.Read("identity.json", (4<<20)+64)
	if e != nil {
		return e
	}
	// An encrypted identity with missing metadata must never cause a new key
	// to be generated. Recovery requires its original metadata and key.
	create := !bytes.HasPrefix(identity, []byte("CNAGENT"))
	if e := UnlockVault(s, options, create); e != nil {
		return e
	}
	for _, name := range []string{"identity.json", "state.json"} {
		raw, e := s.Read(name, (4<<20)+64)
		if errors.Is(e, os.ErrNotExist) && name == "state.json" {
			continue
		}
		if e != nil {
			return e
		}
		if bytes.HasPrefix(raw, []byte(vaultPrefix)) {
			if _, e = s.ReadSecret(name); e != nil {
				return e
			}
			continue
		}
		if len(raw) > 4<<20 {
			return ErrStorage
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if name == "identity.json" {
			var id Identity
			if d.Decode(&id) != nil || id.Version != 1 || id.Registration.DeviceID <= 0 || !tokenOK(id.Registration.Credential, "cna1.") || id.Policy.Validate() != nil {
				return ErrStorage
			}
		} else {
			var state State
			if d.Decode(&state) != nil || state.Version != 1 || state.Completed == nil {
				return ErrStorage
			}
		}
		if d.Decode(&struct{}{}) != io.EOF {
			return ErrStorage
		}
		if e = s.WriteSecret(name, raw); e != nil {
			return e
		}
	}
	for _, name := range []string{"active.json", "candidate.json"} {
		if e := s.RemoveRuntime(name); e != nil {
			return e
		}
	}
	return nil
}
