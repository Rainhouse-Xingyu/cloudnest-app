package contract

import "cloudenest_go/model"

// Fixed protocol ceilings bound manifests and each request; logical quota and
// the future durable temporary-space budget are separate policies.
const (
	MaxUploadChunkBytes int64 = 64 << 20
	MaxUploadChunks           = 16384
	MaxUploadFileBytes  int64 = 1 << 40
)

type ChunkCommit struct{ CandidateDisposable bool }

func ExpectedChunkBytes(t model.UploadTask, index int) (int64, error) {
	if t.ChunkProtocol != 1 {
		return 0, ErrUploadContract
	}
	if t.TotalSize == nil || t.ChunkSize == nil || t.TotalChunks == nil || *t.TotalSize <= 0 || *t.TotalSize > MaxUploadFileBytes || *t.ChunkSize <= 0 || int64(*t.ChunkSize) > MaxUploadChunkBytes || *t.TotalChunks <= 0 || *t.TotalChunks > MaxUploadChunks || int64(*t.TotalChunks) != 1+(*t.TotalSize-1)/int64(*t.ChunkSize) || index < 0 || index >= *t.TotalChunks {
		return 0, ErrInvalidInput
	}
	remaining := *t.TotalSize - int64(index)*int64(*t.ChunkSize)
	if remaining > int64(*t.ChunkSize) {
		return int64(*t.ChunkSize), nil
	}
	return remaining, nil
}
