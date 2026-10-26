package model

import "time"

// AIIndexJob is the current desired index generation for a file. It deliberately
// has no storage_id: pending work must not retain old blobs indefinitely.
// AiTask remains the legacy API's history and is not used as a queue.
type AIIndexJob struct {
	FileID      int64      `gorm:"primaryKey;autoIncrement:false" json:"file_id"`
	Generation  int64      `gorm:"not null" json:"generation"`
	SourceHash  string     `gorm:"type:char(64);not null" json:"source_hash"`
	Pipeline    string     `gorm:"type:varchar(64);not null;index:idx_ai_index_ready,priority:1" json:"pipeline"`
	Status      string     `gorm:"type:varchar(20);not null;index:idx_ai_index_ready,priority:2" json:"status"`
	Attempts    int        `gorm:"not null" json:"attempts"`
	AvailableAt time.Time  `gorm:"type:datetime(6);not null;index:idx_ai_index_ready,priority:3" json:"available_at"`
	LeaseUntil  *time.Time `gorm:"type:datetime(6);index:idx_ai_index_expiry" json:"-"`
	LeaseToken  string     `gorm:"type:char(64);not null" json:"-"`
	ErrorCode   string     `gorm:"type:varchar(64);not null" json:"error_code"`
	UpdatedAt   time.Time  `gorm:"type:datetime(6);not null;autoUpdateTime:false" json:"updated_at"`
}

func (AIIndexJob) TableName() string { return "ai_index_job" }

// AIIndexDocument and its chunks are replaced atomically with a fenced job
// completion. Search must join the live file and matching done job generation.
type AIIndexDocument struct {
	FileID         int64     `gorm:"primaryKey;autoIncrement:false" json:"file_id"`
	Generation     int64     `gorm:"not null" json:"generation"`
	SourceHash     string    `gorm:"type:char(64);not null" json:"source_hash"`
	Pipeline       string    `gorm:"type:varchar(64);not null" json:"pipeline"`
	EmbeddingModel string    `gorm:"type:varchar(200);not null" json:"embedding_model"`
	ModelDigest    string    `gorm:"type:char(64);not null" json:"model_digest"`
	Dimensions     int       `gorm:"not null" json:"dimensions"`
	ChunkCount     int       `gorm:"not null" json:"chunk_count"`
	TextBytes      int       `gorm:"not null" json:"text_bytes"`
	IndexedAt      time.Time `gorm:"type:datetime(6);not null" json:"indexed_at"`
}

func (AIIndexDocument) TableName() string { return "ai_index_document" }

type AIIndexChunk struct {
	FileID    int64  `gorm:"primaryKey;autoIncrement:false" json:"file_id"`
	Ordinal   int    `gorm:"primaryKey;autoIncrement:false" json:"ordinal"`
	StartRune int    `gorm:"not null" json:"start_rune"`
	EndRune   int    `gorm:"not null" json:"end_rune"`
	Content   string `gorm:"type:text;not null" json:"content"`
	Vector    []byte `gorm:"type:varbinary(16384);not null" json:"-"`
}

func (AIIndexChunk) TableName() string { return "ai_index_chunk" }
