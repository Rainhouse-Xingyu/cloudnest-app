package model

import "time"

// Stable endpoint identity; revisions are immutable encrypted snapshots.
type FrpServer struct {
	ID         int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID     int64     `gorm:"not null;index" json:"user_id"`
	Address    string    `gorm:"type:varchar(253);not null;uniqueIndex:uq_frp_server_endpoint,priority:1" json:"server_addr"`
	Port       int       `gorm:"not null;uniqueIndex:uq_frp_server_endpoint,priority:2" json:"server_port"`
	RevisionID int64     `gorm:"not null;default:0" json:"revision_id"`
	Enabled    bool      `gorm:"not null" json:"enabled"`
	CreatedAt  time.Time `gorm:"type:datetime(6);not null" json:"created_at"`
}

func (FrpServer) TableName() string { return "frp_server" }

type FrpServerRevision struct {
	ID        int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	ServerID  int64     `gorm:"not null;index" json:"server_id"`
	Data      string    `gorm:"type:text;not null" json:"-"`
	CreatedAt time.Time `gorm:"type:datetime(6);not null" json:"created_at"`
}

func (FrpServerRevision) TableName() string { return "frp_server_revision" }
