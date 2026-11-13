package model

import "time"

// FrpReservation is a durable potential runtime claim, not proof of reachability.
// A queued deployment can execute even when its eventual receipt is lost.
type FrpReservation struct {
	ID           int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	OwnerID      int64      `gorm:"not null;index" json:"owner_id"`
	DeviceID     int64      `gorm:"not null;index:ix_frp_reservation_generation,priority:1" json:"device_id"`
	CredentialID string     `gorm:"type:varchar(32);not null;index:ix_frp_reservation_generation,priority:2" json:"-"`
	Reference    string     `gorm:"type:varchar(64);not null;uniqueIndex:uq_frp_reservation_reference,priority:1" json:"reference"`
	ConfigID     int64      `gorm:"not null;index" json:"config_id"`
	ServerID     int64      `gorm:"not null;index:ix_frp_reservation_resource,priority:1" json:"server_id"`
	Kind         string     `gorm:"type:varchar(8);not null;uniqueIndex:uq_frp_reservation_reference,priority:2;index:ix_frp_reservation_resource,priority:2" json:"kind"`
	Resource     string     `gorm:"type:varchar(253);not null;uniqueIndex:uq_frp_reservation_reference,priority:3;index:ix_frp_reservation_resource,priority:3" json:"resource"`
	ReconciledAt *time.Time `gorm:"type:datetime(6)" json:"reconciled_at,omitempty"`
}

func (FrpReservation) TableName() string { return "frp_reservation" }

// Seeded only after explicit migration has retained unknown historical claims.
type FrpReservationSchema struct {
	ID      int64 `gorm:"primaryKey;autoIncrement:false"`
	Version int   `gorm:"not null"`
}

func (FrpReservationSchema) TableName() string { return "frp_reservation_schema" }
