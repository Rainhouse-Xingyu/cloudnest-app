package blob

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var ErrManagedPath = errors.New("managed object path is not trusted")
var tokenPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func RandomToken() (string, error) {
	p := make([]byte, 32)
	if _, err := rand.Read(p); err != nil {
		return "", err
	}
	return hex.EncodeToString(p), nil
}
func ValidToken(s string) bool { return tokenPattern.MatchString(s) }
func CanonicalRoot(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode().Perm()&0022 != 0 {
		return "", ErrManagedPath
	}
	parent, err := os.Stat(filepath.Dir(canonical))
	if err != nil || parent.Mode().Perm()&0022 != 0 {
		return "", ErrManagedPath
	}
	return canonical, nil
}
func CleanRelative(path string) bool {
	return path != "" && path != "." && !filepath.IsAbs(path) && filepath.Clean(path) == path && path != ".." && !strings.HasPrefix(path, ".."+string(filepath.Separator)) && !strings.Contains(path, "\\")
}

// ManagedLocation never adopts old objects by age or filename. Only the upload
// publisher calls this for its exclusive, newly generated canonical candidate.
func ManagedLocation(root, path string) (string, string, error) {
	canonical, err := CanonicalRoot(root)
	if err != nil {
		return "", "", err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", "", err
	}
	relative, err := filepath.Rel(canonical, absolute)
	if err != nil || !CleanRelative(relative) {
		return "", "", ErrManagedPath
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) != 3 || parts[0] != ".managed-v1" || len(parts[1]) != 6 || !strings.HasPrefix(parts[2], "blob-") || !ValidToken(strings.TrimPrefix(parts[2], "blob-")) {
		return "", "", ErrManagedPath
	}
	anchored, err := os.OpenRoot(canonical)
	if err != nil {
		return "", "", err
	}
	defer anchored.Close()
	if err := RegularPath(anchored, relative); err != nil {
		return "", "", err
	}
	return canonical, relative, nil
}

// RegularPath rejects symlinks at every component, including the final file.
// Root operations additionally confine resolution beneath the opened root.
func RegularPath(root *os.Root, path string) error {
	if !CleanRelative(path) {
		return ErrManagedPath
	}
	parts := strings.Split(path, string(filepath.Separator))
	current := ""
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
			return ErrManagedPath
		}
		if i < len(parts)-1 {
			if !info.IsDir() {
				return ErrManagedPath
			}
		} else if !info.Mode().IsRegular() {
			return ErrManagedPath
		}
	}
	return nil
}

// EnsurePrivateDir creates components one by one only beneath checked private
// parents. It never chmods existing directories or follows symlink components.
func EnsurePrivateDir(root *os.Root, path string) error {
	if path == "." {
		return nil
	}
	if !CleanRelative(path) {
		return ErrManagedPath
	}
	current := ""
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := root.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := root.Mkdir(current, 0700); err != nil {
				return err
			}
			info, err = root.Lstat(current)
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
			return ErrManagedPath
		}
	}
	return nil
}
