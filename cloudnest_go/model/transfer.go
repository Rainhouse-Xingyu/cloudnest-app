package model

import "time"

// TransferVolume is an explicit private local volume registration. Capacity is
// durable policy; environment values seed new volumes, never silently override.
type TransferVolume struct {
	ID           string `gorm:"type:char(64);primaryKey" json:"-"`
	RootPath     string `gorm:"type:varchar(500);not null" json:"-"`
	LimitBytes   int64  `gorm:"not null" json:"limit_bytes"`
	MinFreeBytes int64  `gorm:"not null" json:"min_free_bytes"`
}

func (TransferVolume) TableName() string { return "transfer_volume" }

// Records survive unlink/ownership handoff. Published/done are uncharged; all
// other states, including blocked/uncertain records, retain their reservation.
type TransferArtifact struct {
	CacheKey      *string   `gorm:"type:char(64);uniqueIndex:idx_transfer_cache,priority:2" json:"-"`
	TransferEpoch int64     `gorm:"type:bigint;not null;default:0" json:"-"`
	Token         string    `gorm:"type:char(64);primaryKey" json:"token"`
	VolumeID      string    `gorm:"type:char(64);not null;uniqueIndex:idx_transfer_path,priority:1;uniqueIndex:idx_transfer_cache,priority:1;index:idx_transfer_reconcile,priority:1" json:"-"`
	RelativePath  string    `gorm:"type:varchar(500);not null;uniqueIndex:idx_transfer_path,priority:2" json:"-"`
	UserID        int64     `gorm:"not null;index" json:"-"`
	TaskID        int64     `gorm:"not null;index" json:"-"`
	Kind          string    `gorm:"type:varchar(16);not null" json:"kind"`
	State         string    `gorm:"type:varchar(16);not null;index:idx_transfer_reconcile,priority:2" json:"state"`
	Bytes         int64     `gorm:"not null" json:"bytes"`
	Size          int64     `gorm:"not null;default:0" json:"-"`
	SHA256        string    `gorm:"type:char(64);not null;default:''" json:"-"`
	Device        uint64    `gorm:"type:bigint unsigned;not null;default:0" json:"-"`
	Inode         uint64    `gorm:"type:bigint unsigned;not null;default:0" json:"-"`
	ExpiresAt     time.Time `gorm:"type:datetime(6);not null;index:idx_transfer_reconcile,priority:3" json:"-"`
	CreatedAt     time.Time `gorm:"type:datetime(6);not null;autoCreateTime" json:"-"`
}

func (TransferArtifact) TableName() string { return "transfer_artifact" }
