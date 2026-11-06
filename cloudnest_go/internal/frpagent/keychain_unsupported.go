//go:build !darwin || !cgo

package frpagent

import "fmt"

func defaultKeychainPath() (string, error) {
	return "", fmt.Errorf("%w: Keychain requires a native macOS cgo build", ErrVault)
}

func keychainKey(path, id string, key []byte, create bool) error {
	_, err := defaultKeychainPath()
	return err
}
