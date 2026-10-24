package blob

import (
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strings"
)

// VerifyStorageObject bounds memory and observes cancellation. Callers must pin
// published read objects or hold their metadata locks/references during writes.
func VerifyStorageObject(ctx context.Context, storage model.Storage) error {
	in, err := OpenVerifiedStorageObject(ctx, storage)
	if err != nil {
		return err
	}
	return in.Close()
}

// OpenVerifiedStorageObject verifies and rewinds the same descriptor the caller
// will consume. Its caller must pin published objects or hold write references.
func OpenVerifiedStorageObject(ctx context.Context, storage model.Storage) (*os.File, error) {
	if storage.State != "" && storage.State != Active {
		return nil, contract.ErrConflict
	}
	if storage.FileSize == nil || *storage.FileSize < 0 || len(storage.FileHash) != 64 {
		return nil, contract.ErrStorageIntegrity
	}
	if _, err := hex.DecodeString(storage.FileHash); err != nil {
		return nil, contract.ErrStorageIntegrity
	}
	in, err := os.Open(storage.FilePath)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = in.Close()
		}
	}()
	info, err := in.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != *storage.FileSize {
		return nil, contract.ErrStorageIntegrity
	}
	hash := sha256.New()
	buffer := make([]byte, 64*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := in.Read(buffer)
		total += int64(n)
		if total > *storage.FileSize {
			return nil, contract.ErrStorageIntegrity
		}
		if n > 0 {
			_, _ = hash.Write(buffer[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	if total != *storage.FileSize || !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), storage.FileHash) {
		return nil, contract.ErrStorageIntegrity
	}
	if _, err := in.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	ok = true
	return in, nil
}

func OpenPinnedStorage(ctx context.Context, object model.Storage) (*os.File, model.Storage, error) {
	pinned, err := Pin(ctx, object)
	if err != nil {
		return nil, model.Storage{}, err
	}
	file, err := OpenVerifiedStorageObject(ctx, pinned)
	return file, pinned, err
}
