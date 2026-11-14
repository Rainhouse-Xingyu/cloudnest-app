package model

import "cloudenest_go/internal/agentstatus"

// AgentCommand stores metadata only. Configuration plaintext and device secrets
// are never copied into the queue. CredentialID fences enrollment generations.
type AgentCommand struct {
	ID                string                  `gorm:"type:char(32);primaryKey" json:"command_id"`
	DeviceID          int64                   `gorm:"not null;uniqueIndex:uq_agent_request,priority:1;index:ix_agent_pending,priority:1" json:"device_id"`
	CredentialID      string                  `gorm:"type:char(32);not null" json:"-"`
	RequestID         string                  `gorm:"type:char(32);not null;uniqueIndex:uq_agent_request,priority:2" json:"request_id"`
	Action            string                  `gorm:"type:varchar(24);not null" json:"action"`
	RequestedConfigID int64                   `gorm:"not null" json:"-"`
	ConfigID          int64                   `gorm:"not null" json:"config_id"`
	ConfigSHA256      string                  `gorm:"type:char(64);not null" json:"config_sha256"`
	Status            string                  `gorm:"type:varchar(16);not null;index:ix_agent_pending,priority:2" json:"status"`
	Attempts          int                     `gorm:"not null" json:"attempts"`
	ClaimID           string                  `gorm:"type:char(32);not null" json:"-"`
	LeaseID           string                  `gorm:"type:char(32);not null" json:"-"`
	LeaseExpires      *CommandTime            `gorm:"type:datetime(6)" json:"lease_expires_at,omitempty"`
	ExpiresAt         CommandTime             `gorm:"type:datetime(6);not null" json:"expires_at"`
	CreatedAt         CommandTime             `gorm:"type:datetime(6);not null;index:ix_agent_pending,priority:3;autoCreateTime:false" json:"created_at"`
	CompletedAt       *CommandTime            `gorm:"type:datetime(6)" json:"completed_at,omitempty"`
	DiagnosticData    string                  `gorm:"type:varchar(512);not null;default:''" json:"-"`
	DiagnosticProxies []agentstatus.ProxyView `gorm:"-" json:"diagnostic_proxies,omitempty"`
	Diagnostic        *agentstatus.Report     `gorm:"-" json:"diagnostic,omitempty"`
	ResultCode        string                  `gorm:"type:varchar(32);not null" json:"result_code"`
}

func (AgentCommand) TableName() string { return "agent_command" }
