package frpconfig

import (
	"encoding/base64"
	"fmt"
	"strings"
)

const managedPrefix = "cnfrp:2:"

func managedAAD(device, config, server, revision int64, key string) []byte {
	return []byte(fmt.Sprintf("cloudnest/frp-config/v2/%d/%d/%d/%d/%s", device, config, server, revision, key))
}

// Managed configuration metadata is authenticated, including during rotation.
// Removing the association cannot downgrade a v2 ciphertext to the legacy path.
func (k *Keyring) SealConfig(device, config, server, revision int64, plain string) (string, error) {
	if server == 0 && revision == 0 {
		return k.Seal(device, config, plain)
	}
	if k == nil || k.keys[k.active] == nil {
		return "", ErrUnavailable
	}
	if device <= 0 || config <= 0 || server <= 0 || revision <= 0 || len(plain) == 0 || len(plain) > MaxPlaintext {
		return "", ErrInvalid
	}
	raw := k.keys[k.active].Seal(nil, nil, []byte(plain), managedAAD(device, config, server, revision, k.active))
	return managedPrefix + k.active + ":" + base64.RawStdEncoding.EncodeToString(raw), nil
}
func (k *Keyring) OpenConfig(device, config, server, revision int64, sealed string) (string, error) {
	if server == 0 && revision == 0 {
		return k.Open(device, config, sealed)
	}
	if k == nil {
		return "", ErrUnavailable
	}
	if device <= 0 || config <= 0 || server <= 0 || revision <= 0 || len(sealed) > 45000 || !strings.HasPrefix(sealed, managedPrefix) {
		return "", ErrInvalid
	}
	parts := strings.Split(strings.TrimPrefix(sealed, managedPrefix), ":")
	if len(parts) != 2 || !validID(parts[0]) {
		return "", ErrInvalid
	}
	cipher := k.keys[parts[0]]
	if cipher == nil {
		return "", ErrUnavailable
	}
	raw, err := base64.RawStdEncoding.Strict().DecodeString(parts[1])
	if err != nil || base64.RawStdEncoding.EncodeToString(raw) != parts[1] {
		return "", ErrInvalid
	}
	plain, err := cipher.Open(nil, nil, raw, managedAAD(device, config, server, revision, parts[0]))
	if err != nil || len(plain) == 0 || len(plain) > MaxPlaintext {
		return "", ErrInvalid
	}
	return string(plain), nil
}
