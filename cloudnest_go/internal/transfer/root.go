package transfer

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cloudenest_go/internal/blob"
	"cloudenest_go/internal/service/contract"
)

var errLocked = errors.New("transfer file is still open")
var errUnsupported = errors.New("transfer volume requires local filesystem locking")

type rootScope struct {
	root     *os.Root
	gate     *os.File
	path, id string
}

func (r *rootScope) Close() {
	if r.gate != nil {
		r.gate.Close()
	}
	if r.root != nil {
		r.root.Close()
	}
}
func (m *Manager) openRoot(ctx context.Context, create bool) (*rootScope, error) {
	if create {
		absolute, err := filepath.Abs(m.rootPath)
		if err != nil {
			return nil, err
		}
		var parents []string
		for path := absolute; ; path = filepath.Dir(path) {
			_, err := os.Stat(path)
			if err == nil {
				break
			}
			if !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			parent := filepath.Dir(path)
			parents = append(parents, parent)
			if parent == path {
				break
			}
		}
		if err := os.MkdirAll(absolute, 0700); err != nil {
			return nil, err
		}
		for _, parent := range parents {
			dir, err := os.Open(parent)
			if err != nil {
				return nil, err
			}
			if err := errors.Join(dir.Sync(), dir.Close()); err != nil {
				return nil, err
			}
		}
	}
	canonical, err := blob.CanonicalRoot(m.rootPath)
	if err != nil {
		return nil, err
	}
	if _, err := diskFree(canonical); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(canonical)
	if err != nil {
		return nil, err
	}
	scope := &rootScope{root: root, path: canonical}
	ok := false
	defer func() {
		if !ok {
			scope.Close()
		}
	}()
	const gatePath = ".transfer-admission-v1"
	if info, err := root.Lstat(gatePath); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
			return nil, blob.ErrManagedPath
		}
	} else if !errors.Is(err, os.ErrNotExist) || !create {
		return nil, err
	}
	flags := os.O_RDONLY
	if create {
		flags = os.O_RDWR | os.O_CREATE
	}
	gate, err := root.OpenFile(gatePath, flags, 0600)
	if err != nil {
		return nil, err
	}
	scope.gate = gate
	locking, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	if err := lockFile(locking, gate, false, true); err != nil {
		return nil, err
	}
	binding, err := DatabaseIdentity(ctx, m.db)
	if err != nil {
		return nil, err
	}
	const volumePath = ".transfer-volume-v1"
	if err := blob.RegularPath(root, volumePath); err == nil {
		file, err := root.Open(volumePath)
		if err != nil {
			return nil, err
		}
		raw, readErr := io.ReadAll(io.LimitReader(file, 131))
		err = errors.Join(readErr, file.Close())
		if err != nil {
			return nil, err
		}
		lines := strings.Split(string(raw), "\n")
		id := ""
		if len(lines) == 3 {
			id = lines[0]
		}
		if len(raw) != 130 || !blob.ValidToken(id) || len(lines) != 3 || lines[1] != binding || lines[2] != "" {
			return nil, blob.ErrManagedPath
		}
		scope.id = id
	} else if errors.Is(err, os.ErrNotExist) && create {
		id, err := blob.RandomToken()
		if err != nil {
			return nil, err
		}
		file, err := root.OpenFile(volumePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return nil, err
		}
		_, writeErr := io.WriteString(file, id+"\n"+binding+"\n")
		err = errors.Join(writeErr, file.Sync(), file.Close())
		if err != nil {
			return nil, err
		}
		if err := syncDirs(root, []string{"."}); err != nil {
			return nil, err
		}
		scope.id = id
	} else {
		return nil, err
	}
	ok = true
	return scope, nil
}
func syncDirs(root *os.Root, paths []string) error {
	for _, path := range paths {
		f, err := root.Open(path)
		if err != nil {
			return err
		}
		err = errors.Join(f.Sync(), f.Close())
		if err != nil {
			return err
		}
	}
	return nil
}
func artifactRelative(kind, token string) string {
	if kind == "archive" {
		return filepath.Join(".archives-v1", "archive-"+token+".zip")
	}
	if kind == "chunk" {
		return filepath.Join(".chunks-v1", "chunk-"+token+".part")
	}
	return filepath.Join(".managed-v1", time.Now().Format("200601"), "blob-"+token)
}
func validRelative(kind, token, relative string) bool {
	if !blob.ValidToken(token) || !blob.CleanRelative(relative) {
		return false
	}
	if kind == "archive" {
		return relative == filepath.Join(".archives-v1", "archive-"+token+".zip")
	}
	if kind == "chunk" {
		return relative == filepath.Join(".chunks-v1", "chunk-"+token+".part")
	}
	parts := strings.Split(relative, string(filepath.Separator))
	return kind == "merge" && len(parts) == 3 && parts[0] == ".managed-v1" && len(parts[1]) == 6 && parts[2] == "blob-"+token
}
func unavailable(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{contract.ErrTransferSpace, contract.ErrTransferBusy, contract.ErrTransferUnavailable, contract.ErrUploadContract, contract.ErrForbidden, contract.ErrInvalidInput, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, known) {
			return err
		}
	}
	return errors.Join(contract.ErrTransferUnavailable, err)
}

func syncArtifactDirs(root *os.Root, relative string) error {
	var paths []string
	for path := filepath.Dir(relative); ; path = filepath.Dir(path) {
		paths = append(paths, path)
		if path == "." {
			break
		}
	}
	return syncDirs(root, paths)
}
