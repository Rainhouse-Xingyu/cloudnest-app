// Package frpconfig seals configuration at rest. User and Agent authorization
// must be checked separately before any plaintext is used.
package frpconfig

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const MaxPlaintext = 32 << 10
const prefix = "cnfrp:1:"

var ErrUnavailable = errors.New("FRP configuration key unavailable")
var ErrInvalid = errors.New("FRP configuration authentication failed")
var ErrLegacy = errors.New("FRP configuration requires explicit encryption migration")

type Keyring struct {
	active string
	keys   map[string]cipher.AEAD
}

func (k *Keyring) String() string { return "[FRP keyring]" }

// LoadFile reads a bounded, private regular file without following the final
// path component if it is a symlink. The trusted parent directory must also be
// protected by the operator. No error includes path contents or key material.
func LoadFile(path string) (*Keyring, error) {
	if !filepath.IsAbs(path) {
		return nil, ErrUnavailable
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrUnavailable
	}
	f := os.NewFile(uintptr(fd), "frp-keyring")
	defer f.Close()
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0077 != 0 || stat.Nlink != 1 || int(stat.Uid) != os.Geteuid() {
		return nil, ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(f, 16385))
	if err != nil || len(raw) > 16384 {
		return nil, ErrUnavailable
	}
	return parse(raw)
}

func parse(raw []byte) (*Keyring, error) {
	var doc struct {
		Version int    `json:"version"`
		Active  string `json:"active"`
		Keys    []struct {
			ID  string `json:"id"`
			Key string `json:"key"`
		} `json:"keys"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&doc) != nil || d.Decode(&struct{}{}) != io.EOF || doc.Version != 1 || len(doc.Keys) == 0 || len(doc.Keys) > 16 {
		return nil, ErrUnavailable
	}
	k := &Keyring{active: doc.Active, keys: make(map[string]cipher.AEAD)}
	seen := make(map[string]bool)
	for _, entry := range doc.Keys {
		if !validID(entry.ID) || k.keys[entry.ID] != nil || seen[entry.Key] {
			return nil, ErrUnavailable
		}
		key, err := base64.StdEncoding.Strict().DecodeString(entry.Key)
		if err != nil || len(key) != 32 || base64.StdEncoding.EncodeToString(key) != entry.Key {
			return nil, ErrUnavailable
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, ErrUnavailable
		}
		aead, err := cipher.NewGCMWithRandomNonce(block)
		if err != nil {
			return nil, ErrUnavailable
		}
		k.keys[entry.ID] = aead
		seen[entry.Key] = true
	}
	if k.keys[k.active] == nil {
		return nil, ErrUnavailable
	}
	return k, nil
}

func validID(id string) bool {
	if len(id) < 1 || len(id) > 32 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func aad(deviceID, configID int64, keyID string) []byte {
	return []byte(fmt.Sprintf("cloudnest/frp-config/v1/%d/%d/%s", deviceID, configID, keyID))
}

func (k *Keyring) Seal(deviceID, configID int64, plaintext string) (string, error) {
	if k == nil || k.keys[k.active] == nil {
		return "", ErrUnavailable
	}
	if deviceID <= 0 || configID <= 0 || len(plaintext) == 0 || len(plaintext) > MaxPlaintext {
		return "", ErrInvalid
	}
	sealed := k.keys[k.active].Seal(nil, nil, []byte(plaintext), aad(deviceID, configID, k.active))
	return prefix + k.active + ":" + base64.RawStdEncoding.EncodeToString(sealed), nil
}

// Open never accepts legacy plaintext or falls back after a decrypt failure.
func (k *Keyring) Open(deviceID, configID int64, sealed string) (string, error) {
	if k == nil {
		return "", ErrUnavailable
	}
	if !strings.HasPrefix(sealed, "cnfrp:") {
		return "", ErrLegacy
	}
	if !strings.HasPrefix(sealed, prefix) || len(sealed) > 45000 || deviceID <= 0 || configID <= 0 {
		return "", ErrInvalid
	}
	parts := strings.Split(strings.TrimPrefix(sealed, prefix), ":")
	if len(parts) != 2 || !validID(parts[0]) {
		return "", ErrInvalid
	}
	aead := k.keys[parts[0]]
	if aead == nil {
		return "", ErrUnavailable
	}
	raw, err := base64.RawStdEncoding.Strict().DecodeString(parts[1])
	if err != nil || base64.RawStdEncoding.EncodeToString(raw) != parts[1] {
		return "", ErrInvalid
	}
	plain, err := aead.Open(nil, nil, raw, aad(deviceID, configID, parts[0]))
	if err != nil || len(plain) == 0 || len(plain) > MaxPlaintext {
		return "", ErrInvalid
	}
	return string(plain), nil
}

func IsEnvelope(text string) bool { return strings.HasPrefix(text, "cnfrp:") }
func (k *Keyring) IsCurrent(text string) bool {
	return k != nil && strings.HasPrefix(text, prefix+k.active+":")
}
