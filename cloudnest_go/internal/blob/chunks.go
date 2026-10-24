package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var ErrChunkSize = errors.New("chunk length differs from immutable manifest")
var ErrChunkIntegrity = errors.New("chunk digest differs from immutable manifest")

func ChunkRelative(root, path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", ErrManagedPath
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || !CleanRelative(rel) {
		return "", ErrManagedPath
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 2 || parts[0] != ".chunks-v1" || !strings.HasPrefix(parts[1], "chunk-") || !strings.HasSuffix(parts[1], ".part") || !ValidToken(strings.TrimSuffix(strings.TrimPrefix(parts[1], "chunk-"), ".part")) {
		return "", ErrManagedPath
	}
	return rel, nil
}
func OpenChunk(root *os.Root, canonical, path string) (*os.File, error) {
	rel, err := ChunkRelative(canonical, path)
	if err != nil {
		return nil, err
	}
	if err := RegularPath(root, rel); err != nil {
		return nil, err
	}
	return root.Open(rel)
}
func VerifyChunk(ctx context.Context, storageRoot, path string, size int64, digest string) error {
	canonical, err := CanonicalRoot(storageRoot)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(canonical)
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := OpenChunk(root, canonical, path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() != size {
		return ErrChunkSize
	}
	hash := sha256.New()
	buf := make([]byte, 32*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, e := f.Read(buf)
		if n > 0 {
			total += int64(n)
			if total > size {
				return ErrChunkSize
			}
			_, _ = hash.Write(buf[:n])
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
	}
	if total != size {
		return ErrChunkSize
	}
	if hex.EncodeToString(hash.Sum(nil)) != digest {
		return ErrChunkIntegrity
	}
	return nil
}
