package model

import "time"

// AgentBinding holds only the current device registration state. Public lookup
// IDs and SHA-256 digests are stored separately; no secret is serialized.
type AgentBinding struct {
	DeviceID          int64      `gorm:"primaryKey;autoIncrement:false" json:"-"`
	OwnerID           int64      `gorm:"not null;index" json:"-"`
	EnrollmentID      *string    `gorm:"type:char(32);uniqueIndex:uq_agent_enrollment_id" json:"-"`
	EnrollmentDigest  []byte     `gorm:"type:binary(32)" json:"-"`
	EnrollmentExpires *time.Time `gorm:"type:datetime(6)" json:"-"`
	CredentialID      *string    `gorm:"type:char(32);uniqueIndex:uq_agent_credential_id" json:"-"`
	CredentialDigest  []byte     `gorm:"type:binary(32)" json:"-"`
	LastSeenAt        *time.Time `gorm:"type:datetime(6)" json:"-"`
	ClientVersion     string     `gorm:"type:varchar(64);not null;default:''" json:"-"`
	RuntimeReport     string     `gorm:"type:varchar(512);not null;default:''" json:"-"`
}

func (AgentBinding) TableName() string { return "agent_binding" }
