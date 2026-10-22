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
	if storage.State != "" && storage.State != Active {
		return contract.ErrConflict
	}
	if storage.FileSize == nil || *storage.FileSize < 0 || len(storage.FileHash) != 64 {
		return contract.ErrStorageIntegrity
	}
	if _, err := hex.DecodeString(storage.FileHash); err != nil {
		return contract.ErrStorageIntegrity
	}
	in, err := os.Open(storage.FilePath)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != *storage.FileSize {
		return contract.ErrStorageIntegrity
	}
	hash := sha256.New()
	buffer := make([]byte, 64*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := in.Read(buffer)
		total += int64(n)
		if total > *storage.FileSize {
			return contract.ErrStorageIntegrity
		}
		if n > 0 {
			_, _ = hash.Write(buffer[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if total != *storage.FileSize || !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), storage.FileHash) {
		return contract.ErrStorageIntegrity
	}
	return nil
}
