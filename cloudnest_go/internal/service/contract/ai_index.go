package contract

import "time"

// AIIndexStatus exposes diagnostics without worker tokens, paths or content.
type AIIndexStatus struct {
	FileID         int64      `json:"file_id"`
	Filename       string     `json:"filename"`
	Generation     int64      `json:"generation"`
	Status         string     `json:"status"`
	Attempts       int        `json:"attempts"`
	ErrorCode      string     `json:"error_code"`
	AvailableAt    *time.Time `json:"available_at"`
	UpdatedAt      *time.Time `json:"updated_at"`
	SourceCurrent  bool       `json:"source_current"`
	IndexedCurrent bool       `json:"indexed_current"`
	ChunkCount     int        `json:"chunk_count"`
	IndexedAt      *time.Time `json:"indexed_at"`
}

type AIRebuildItem struct {
	FileID     int64 `json:"file_id"`
	Generation int64 `json:"generation"`
}
