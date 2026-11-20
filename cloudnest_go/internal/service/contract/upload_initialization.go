package contract

import (
	"crypto/sha256"
	"fmt"

	"cloudenest_go/model"
)

// UploadInitialization freezes the caller's contract. Zero chunk parameters ask
// for server defaults; a replay uses the original resolved values.
type UploadInitialization struct {
	UserID, ParentID, TotalSize int64
	Filename                    string
	ChunkSize, TotalChunks      int
}

// UploadInitializationID scopes an opaque client key to the authenticated owner.
// The reserved prefix cannot collide with legacy randomly generated IDs.
func UploadInitializationID(uid int64, key string) (string, error) {
	if uid <= 0 || len(key) == 0 || len(key) > 128 {
		return "", ErrInvalidInput
	}
	for _, c := range []byte(key) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return "", ErrInvalidInput
		}
	}
	return fmt.Sprintf("init1_%d_%x", uid, sha256.Sum256([]byte(key))), nil
}

func (in UploadInitialization) ValidateReplay(task model.UploadTask) error {
	if task.UserID == nil || *task.UserID != in.UserID || task.ContractVersion != 1 || task.TransferProtocol != 1 || task.ChunkProtocol != 1 || task.Status < 0 || task.Status > 4 || task.ParentID != in.ParentID || task.Filename != in.Filename || task.TotalSize == nil || *task.TotalSize != in.TotalSize || task.ChunkSize == nil || task.TotalChunks == nil || (in.ChunkSize != 0 && *task.ChunkSize != in.ChunkSize) || (in.TotalChunks != 0 && *task.TotalChunks != in.TotalChunks) {
		return ErrUploadContract
	}
	return nil
}
