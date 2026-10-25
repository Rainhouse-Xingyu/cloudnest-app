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
