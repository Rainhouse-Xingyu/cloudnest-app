package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strings"

	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
)

type verifiedCopyReader struct {
	ctx context.Context
	in  io.Reader
}

func (r verifiedCopyReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.in.Read(p)
}

// CopyPinnedStorage verifies the exact bytes copied into a private candidate.
// The caller must discard that entire candidate on ANY error before publication.
func CopyPinnedStorage(ctx context.Context, out io.Writer, object model.Storage) (int64, error) {
	pinned, err := Pin(ctx, object)
	if err != nil {
		return 0, err
	}
	if pinned.FileSize == nil || *pinned.FileSize < 0 || *pinned.FileSize > contract.MaxUploadFileBytes || len(pinned.FileHash) != 64 {
		return 0, contract.ErrStorageIntegrity
	}
	if _, err := hex.DecodeString(pinned.FileHash); err != nil {
		return 0, contract.ErrStorageIntegrity
	}
	in, err := os.Open(pinned.FilePath)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	stat, err := in.Stat()
	if err != nil {
		return 0, err
	}
	if !stat.Mode().IsRegular() || stat.Size() != *pinned.FileSize {
		return 0, contract.ErrStorageIntegrity
	}
	hash := sha256.New()
	n, err := io.CopyBuffer(io.MultiWriter(out, hash), verifiedCopyReader{ctx: ctx, in: io.LimitReader(in, *pinned.FileSize+1)}, make([]byte, 64<<10))
	if err != nil {
		return n, err
	}
	if n != *pinned.FileSize || !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), pinned.FileHash) {
		return n, contract.ErrStorageIntegrity
	}
	return n, nil
}
