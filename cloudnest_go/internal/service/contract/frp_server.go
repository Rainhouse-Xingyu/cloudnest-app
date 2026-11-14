package contract

import (
	"context"
	"time"

	"cloudenest_go/internal/frpconfig"
	"cloudenest_go/model"
)

type FrpServerInput struct {
	Spec               frpconfig.ServerSpec
	Token              *string
	ExpectedRevisionID int64
	Enabled            *bool
}

// Deliberately contains neither plaintext credentials nor their ciphertext.
type FrpServerView struct {
	ID              int64     `json:"id"`
	UserID          int64     `json:"user_id"`
	RevisionID      int64     `json:"revision_id"`
	Name            string    `json:"name"`
	Address         string    `json:"server_addr"`
	Port            int       `json:"server_port"`
	TCPStart        int       `json:"tcp_start"`
	TCPEnd          int       `json:"tcp_end"`
	HTTPPort        int       `json:"http_port"`
	HTTPSPort       int       `json:"https_port"`
	Enabled         bool      `json:"enabled"`
	TokenConfigured bool      `json:"token_configured"`
	CreatedAt       time.Time `json:"created_at"`
}

type FrpServerRepository interface {
	WriteServer(context.Context, FrpActor, int64, FrpServerInput) (FrpServerView, error)
	ListServers(context.Context, FrpActor, int64, int) ([]FrpServerView, error)
	GetServer(context.Context, FrpActor, int64) (FrpServerView, error)
	BuildServerConfig(context.Context, FrpActor, int64, int64, int64, bool, func(frpconfig.ServerSpec, []model.FrpMapping) (string, error)) (model.FrpConfig, string, error)
}
