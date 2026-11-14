package frpconfig

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testDocument(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"version": 1, "active": "one", "keys": []map[string]string{{"id": "one", "key": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))}}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestEnvelopeAuthenticatesIdentityAndEveryCiphertextByte(t *testing.T) {
	k, err := parse(testDocument(t))
	if err != nil {
		t.Fatal(err)
	}
	a, err := k.Seal(1, 2, "sensitive config")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := k.Seal(1, 2, "sensitive config")
	if a == b || strings.Contains(a, "sensitive") {
		t.Fatal("nonce reused or plaintext persisted")
	}
	if got, err := k.Open(1, 2, a); err != nil || got != "sensitive config" {
		t.Fatal("round trip failed", err)
	}
	for _, ids := range [][2]int64{{2, 2}, {1, 3}, {0, 2}} {
		if _, err := k.Open(ids[0], ids[1], a); err == nil {
			t.Fatal("identity substitution accepted")
		}
	}
	encoded := strings.TrimPrefix(a, prefix+"one:")
	raw, _ := base64.RawStdEncoding.DecodeString(encoded)
	for i := range raw {
		changed := bytes.Clone(raw)
		changed[i] ^= 1
		if _, err := k.Open(1, 2, prefix+"one:"+base64.RawStdEncoding.EncodeToString(changed)); !errors.Is(err, ErrInvalid) {
			t.Fatal("tampering accepted", i)
		}
	}
	for _, bad := range []string{strings.Replace(a, "cnfrp:1:", "cnfrp:2:", 1), a + "=", a[:len(a)-1], "cnfrp:1:one:"} {
		if _, err := k.Open(1, 2, bad); err == nil {
			t.Fatal("invalid envelope accepted")
		}
	}
	if _, err := k.Open(1, 2, "legacy config"); !errors.Is(err, ErrLegacy) {
		t.Fatal("plaintext fallback")
	}
	if _, err := k.Open(1, 2, strings.Replace(a, ":one:", ":missing:", 1)); !errors.Is(err, ErrUnavailable) {
		t.Fatal("missing key accepted")
	}
	wrong, err := parse(bytes.ReplaceAll(testDocument(t), []byte(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))), []byte(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)))))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrong.Open(1, 2, a); !errors.Is(err, ErrInvalid) {
		t.Fatal("wrong key material accepted")
	}
	var absent *Keyring
	if _, err := absent.Seal(1, 2, "x"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("write without key")
	}
	if _, err := absent.Open(1, 2, a); !errors.Is(err, ErrUnavailable) {
		t.Fatal("read without key")
	}
	if _, err := k.Seal(1, 2, strings.Repeat("x", MaxPlaintext+1)); err == nil {
		t.Fatal("unbounded config")
	}
}

func TestKeyFileRejectsUnsafeStorageAndInvalidDocuments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.json")
	if err := os.WriteFile(path, testDocument(t), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); err == nil {
		t.Fatal("public key file accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{link, dir, "relative.json"} {
		if _, err := LoadFile(bad); err == nil {
			t.Fatal("unsafe path accepted")
		}
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte{' '}, 16385), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); err == nil {
		t.Fatal("oversized file accepted")
	}
	for _, bad := range []string{`null`, `{}`, string(testDocument(t)) + ` {}`, strings.Replace(string(testDocument(t)), `"version":1`, `"version":2`, 1), strings.Replace(string(testDocument(t)), `"active":"one"`, `"active":"absent"`, 1), strings.Replace(string(testDocument(t)), `"version":1`, `"extra":true,"version":1`, 1)} {
		if _, err := parse([]byte(bad)); err == nil {
			t.Fatal("invalid keyring accepted")
		}
	}
}
