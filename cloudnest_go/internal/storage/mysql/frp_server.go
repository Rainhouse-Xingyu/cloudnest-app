package mysql

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"cloudenest_go/internal/frpconfig"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	driver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (r *FrpRepo) openServer(tx *gorm.DB, server model.FrpServer, revision int64) (frpconfig.ServerSpec, error) {
	var row model.FrpServerRevision
	if err := tx.First(&row, "id=? AND server_id=?", revision, server.ID).Error; err != nil {
		return frpconfig.ServerSpec{}, err
	}
	raw, err := r.keys.OpenServer(server.UserID, server.ID, row.ID, row.Data)
	if err != nil {
		return frpconfig.ServerSpec{}, err
	}
	var spec frpconfig.ServerSpec
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&spec) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return spec, frpconfig.ErrInvalid
	}
	normalized, err := frpconfig.NormalizeServer(spec)
	if err != nil || spec != normalized || spec.Address != server.Address || spec.Port != server.Port {
		return spec, frpconfig.ErrInvalid
	}
	return spec, nil
}
func serverView(server model.FrpServer, spec frpconfig.ServerSpec) contract.FrpServerView {
	return contract.FrpServerView{ID: server.ID, UserID: server.UserID, RevisionID: server.RevisionID, Name: spec.Name, Address: spec.Address, Port: spec.Port, TCPStart: spec.TCPStart, TCPEnd: spec.TCPEnd, HTTPPort: spec.HTTPPort, HTTPSPort: spec.HTTPSPort, Enabled: server.Enabled, TokenConfigured: spec.Token != "", CreatedAt: server.CreatedAt}
}
func (r *FrpRepo) WriteServer(ctx context.Context, actor contract.FrpActor, id int64, input contract.FrpServerInput) (contract.FrpServerView, error) {
	var view contract.FrpServerView
	err := r.frpTransaction(ctx, func(tx *gorm.DB) error {
		var server model.FrpServer
		ownerID := actor.UserID
		if id > 0 {
			if err := tx.First(&server, id).Error; err != nil {
				return err
			}
			if !actor.Admin && server.UserID != actor.UserID {
				return contract.ErrForbidden
			}
			ownerID = server.UserID
		} else if id < 0 || input.ExpectedRevisionID != 0 || input.Token == nil {
			return contract.ErrInvalidInput
		}
		var owner model.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&owner, "id=? AND status=1", ownerID).Error; err != nil {
			return err
		}
		if id > 0 {
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&server, "id=? AND user_id=?", id, ownerID).Error; err != nil {
				return err
			}
			if input.ExpectedRevisionID <= 0 || server.RevisionID != input.ExpectedRevisionID {
				return contract.ErrConflict
			}
			old, err := r.openServer(tx, server, server.RevisionID)
			if err != nil {
				return err
			}
			if input.Token == nil {
				input.Spec.Token = old.Token
			}
		}
		if input.Token != nil {
			input.Spec.Token = *input.Token
		}
		spec, err := frpconfig.NormalizeServer(input.Spec)
		if err != nil {
			return contract.ErrInvalidInput
		}
		if id > 0 && (spec.Address != server.Address || spec.Port != server.Port) {
			return contract.ErrConflict
		}
		if id == 0 {
			server = model.FrpServer{UserID: ownerID, Address: spec.Address, Port: spec.Port, Enabled: true, CreatedAt: time.Now().UTC()}
			if err := tx.Create(&server).Error; err != nil {
				var dbErr *driver.MySQLError
				if errors.As(err, &dbErr) && dbErr.Number == 1062 {
					return contract.ErrConflict
				}
				return err
			}
		}
		revision := model.FrpServerRevision{ServerID: server.ID, CreatedAt: time.Now().UTC()}
		if err := tx.Create(&revision).Error; err != nil {
			return err
		}
		raw, err := json.Marshal(spec)
		if err != nil {
			return err
		}
		sealed, err := r.keys.SealServer(ownerID, server.ID, revision.ID, string(raw))
		if err != nil {
			return err
		}
		if err := tx.Model(&revision).Update("data", sealed).Error; err != nil {
			return err
		}
		server.RevisionID = revision.ID
		if input.Enabled != nil {
			server.Enabled = *input.Enabled
		}
		if err := tx.Model(&server).Updates(map[string]any{"revision_id": server.RevisionID, "enabled": server.Enabled}).Error; err != nil {
			return err
		}
		if err := tx.Create(&model.OperationLog{UserID: &actor.UserID, Action: "frp_server_revision", Detail: fmt.Sprintf("server_id=%d revision_id=%d", server.ID, revision.ID), ClientIP: actor.ClientIP}).Error; err != nil {
			return err
		}
		view = serverView(server, spec)
		return nil
	})
	if err != nil {
		return contract.FrpServerView{}, err
	}
	return view, err
}
func (r *FrpRepo) GetServer(ctx context.Context, actor contract.FrpActor, id int64) (contract.FrpServerView, error) {
	var server model.FrpServer
	if err := r.db.WithContext(ctx).First(&server, id).Error; err != nil {
		return contract.FrpServerView{}, err
	}
	if !actor.Admin && server.UserID != actor.UserID {
		return contract.FrpServerView{}, contract.ErrForbidden
	}
	spec, err := r.openServer(r.db.WithContext(ctx), server, server.RevisionID)
	if err != nil {
		return contract.FrpServerView{}, err
	}
	return serverView(server, spec), nil
}
func (r *FrpRepo) ListServers(ctx context.Context, actor contract.FrpActor, after int64, limit int) ([]contract.FrpServerView, error) {
	if after < 0 || limit < 1 || limit > 100 {
		return nil, contract.ErrInvalidInput
	}
	q := r.db.WithContext(ctx).Where("id>?", after)
	if !actor.Admin {
		q = q.Where("user_id=?", actor.UserID)
	}
	var servers []model.FrpServer
	if err := q.Order("id").Limit(limit).Find(&servers).Error; err != nil {
		return nil, err
	}
	result := make([]contract.FrpServerView, 0, len(servers))
	for _, server := range servers {
		spec, err := r.openServer(r.db.WithContext(ctx), server, server.RevisionID)
		if err != nil {
			return nil, err
		}
		result = append(result, serverView(server, spec))
	}
	return result, nil
}

// Stable mapping snapshot and server revision selection share the write gate.
// A selected server is private to the device owner, even when an admin acts.
func (r *FrpRepo) BuildServerConfig(ctx context.Context, actor contract.FrpActor, deviceID, serverID, revisionID int64, preview bool, build func(frpconfig.ServerSpec, []model.FrpMapping) (string, error)) (model.FrpConfig, string, error) {
	var cfg model.FrpConfig
	var text string
	if serverID <= 0 || revisionID <= 0 {
		return cfg, text, contract.ErrInvalidInput
	}
	err := r.frpTransaction(ctx, func(tx *gorm.DB) error {
		if err := lockMappingDevice(tx, actor, deviceID, true); err != nil {
			return err
		}
		var device model.Device
		if err := tx.First(&device, deviceID).Error; err != nil {
			return err
		}
		var server model.FrpServer
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&server, serverID).Error; err != nil {
			return err
		}
		if server.UserID != device.UserID {
			return contract.ErrForbidden
		}
		if !server.Enabled || server.RevisionID != revisionID {
			return contract.ErrConflict
		}
		spec, err := r.openServer(tx, server, revisionID)
		if err != nil {
			return err
		}
		var mappings []model.FrpMapping
		if err := tx.Where("device_id=? AND status=1 AND server_id IN ?", deviceID, []int64{0, serverID}).Order("id").Limit(513).Find(&mappings).Error; err != nil {
			return err
		}
		if len(mappings) == 0 || len(mappings) > 512 {
			return contract.ErrConflict
		}
		for _, mapping := range mappings {
			if mapping.Domain == "" {
				if mapping.RemotePort < spec.TCPStart || mapping.RemotePort > spec.TCPEnd {
					return contract.ErrConflict
				}
			} else if len(mapping.Domain) >= 8 && mapping.Domain[:8] == "https://" {
				if spec.HTTPSPort == 0 {
					return contract.ErrConflict
				}
			} else if spec.HTTPPort == 0 {
				return contract.ErrConflict
			}
		}
		if preview && spec.Token != "" {
			spec.Token = "[redacted]"
		}
		text, err = build(spec, mappings)
		if err != nil {
			return err
		}
		if preview {
			return nil
		}
		cfg, err = r.insertServerConfig(tx, deviceID, serverID, revisionID, text)
		if err != nil {
			return err
		}
		text = ""
		return tx.Create(&model.OperationLog{UserID: &actor.UserID, Action: "frp_config_save", Detail: fmt.Sprintf("config_id=%d device_id=%d server_id=%d revision_id=%d", cfg.ID, deviceID, serverID, revisionID), ClientIP: actor.ClientIP}).Error
	})
	if err != nil {
		return model.FrpConfig{}, "", err
	}
	return cfg, text, nil
}
