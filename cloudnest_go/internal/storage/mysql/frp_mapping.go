package mysql

import (
	"cloudenest_go/internal/frpconfig"
	"cloudenest_go/internal/frpreservation"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	driver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Legacy mappings overlap every server namespace. Serialize short write
// transactions across backend instances; a process-local mutex is insufficient.
// The lock order is pool -> owner -> device -> mapping. READ COMMITTED avoids
// freezing an ownership hint or allocation scan before a competing commit.
func (r *FrpRepo) frpTransaction(ctx context.Context, fn func(*gorm.DB) error) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := frpreservation.Lock(tx); err != nil {
			return err
		}
		return fn(tx)
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	var dbError *driver.MySQLError
	if errors.As(err, &dbError) {
		switch dbError.Number {
		case 1205, 1213:
			return contract.ErrFrpMappingBusy
		}
	}
	if err != nil && ctx.Err() != nil {
		return contract.ErrFrpMappingBusy
	}
	return err
}

func lockMappingDevice(tx *gorm.DB, actor contract.FrpActor, deviceID int64, requireEnabled bool) error {
	var hint model.Device
	if err := tx.First(&hint, "id=?", deviceID).Error; err != nil {
		return err
	}
	if actor.UserID <= 0 || (!actor.Admin && hint.UserID != actor.UserID) {
		return contract.ErrForbidden
	}
	var owner model.User
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&owner, "id=?", hint.UserID).Error; err != nil {
		return err
	}
	if owner.Status != 1 {
		return contract.ErrForbidden
	}
	var device model.Device
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&device, "id=?", deviceID).Error; err != nil {
		return err
	}
	if device.UserID != hint.UserID {
		return contract.ErrForbidden
	}
	if requireEnabled && device.Status != 1 {
		return contract.ErrFrpDeviceDisabled
	}
	return nil
}

func mappingAudit(tx *gorm.DB, actor contract.FrpActor, action string, m model.FrpMapping) error {
	return tx.Create(&model.OperationLog{UserID: &actor.UserID, Action: action, Detail: fmt.Sprintf("mapping_id=%d device_id=%d", m.ID, m.DeviceID), ClientIP: actor.ClientIP}).Error
}

func (r *FrpRepo) CreateMapping(ctx context.Context, req contract.FrpCreateMappingInput) (model.FrpMapping, error) {
	m := model.FrpMapping{ServerID: req.ServerID, DeviceID: req.DeviceID, LocalPort: req.LocalPort, Domain: req.Domain, Status: req.Status}
	if req.RemotePort != nil {
		m.RemotePort = *req.RemotePort
	} else if m.ServerID > 0 && strings.TrimSpace(m.Domain) != "" {
		m.RemotePort = 0
	} else {
		m.RemotePort = defaultFrpRemotePortStart
	}
	if err := normalizeMapping(&m); err != nil {
		return model.FrpMapping{}, err
	}
	err := r.frpTransaction(ctx, func(tx *gorm.DB) error {
		if err := lockMappingDevice(tx, req.FrpActor, req.DeviceID, true); err != nil {
			return err
		}
		spec, err := r.mappingServerPolicy(tx, m)
		if err != nil {
			return err
		}
		if req.RemotePort == nil && (m.ServerID == 0 || m.Domain == "") {
			port, err := allocateRemotePort(tx, m.ServerID, req.DeviceID, spec.TCPStart, spec.TCPEnd)
			if err != nil {
				return err
			}
			m.RemotePort = port
		}
		if err := checkMappingPolicy(m, spec); err != nil {
			return err
		}
		if err := checkMappingReservation(tx, m); err != nil {
			return err
		}
		// Override status inside this transaction: the legacy default:1 tag
		// otherwise turns an explicitly disabled mapping into an enabled one.
		if err := tx.Omit("Status").Create(&m).Error; err != nil {
			return mappingWriteError(err)
		}
		if err := tx.Model(&m).Update("status", req.Status).Error; err != nil {
			return err
		}
		m.Status = req.Status
		return mappingAudit(tx, req.FrpActor, "frp_mapping_create", m)
	})
	if err != nil {
		return model.FrpMapping{}, err
	}
	return m, nil
}

func (r *FrpRepo) UpdateMapping(ctx context.Context, req contract.FrpUpdateMappingInput) (model.FrpMapping, error) {
	var m model.FrpMapping
	if req.LocalPort == nil && req.RemotePort == nil && req.Domain == nil && req.Status == nil {
		return m, contract.ErrInvalidInput
	}
	err := r.frpTransaction(ctx, func(tx *gorm.DB) error {
		var hint model.FrpMapping
		if err := tx.First(&hint, "id=?", req.MappingID).Error; err != nil {
			return err
		}
		if err := lockMappingDevice(tx, req.FrpActor, hint.DeviceID, true); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&m, "id=?", req.MappingID).Error; err != nil {
			return err
		}
		if m.DeviceID != hint.DeviceID {
			return contract.ErrForbidden
		}
		old := m
		if req.LocalPort != nil {
			m.LocalPort = *req.LocalPort
		}
		if req.RemotePort != nil {
			m.RemotePort = *req.RemotePort
		}
		if req.Domain != nil {
			m.Domain = *req.Domain
		}
		if req.Status != nil {
			m.Status = *req.Status
		}
		allocate := m.ServerID > 0 && req.RemotePort == nil && old.Domain != "" && strings.TrimSpace(m.Domain) == ""
		if m.ServerID > 0 && req.RemotePort == nil {
			if strings.TrimSpace(m.Domain) != "" {
				m.RemotePort = 0
			} else if allocate {
				m.RemotePort = defaultFrpRemotePortStart
			}
		}
		if err := normalizeMapping(&m); err != nil {
			return err
		}
		spec, err := r.mappingServerPolicy(tx, m)
		if err != nil {
			return err
		}
		if allocate {
			m.RemotePort, err = allocateRemotePort(tx, m.ServerID, m.DeviceID, spec.TCPStart, spec.TCPEnd)
			if err != nil {
				return err
			}
		}
		if err := checkMappingPolicy(m, spec); err != nil {
			return err
		}
		if !slices.Equal(frpreservation.MappingKeys(old), frpreservation.MappingKeys(m)) {
			if err := checkMappingReservation(tx, m); err != nil {
				return err
			}
		}
		if err := tx.Model(&m).Updates(map[string]any{"local_port": m.LocalPort, "remote_port": m.RemotePort, "domain": m.Domain, "status": m.Status}).Error; err != nil {
			return mappingWriteError(err)
		}
		return mappingAudit(tx, req.FrpActor, "frp_mapping_update", m)
	})
	if err != nil {
		return model.FrpMapping{}, err
	}
	return m, nil
}

func (r *FrpRepo) DeleteMapping(ctx context.Context, actor contract.FrpActor, id int64) (model.FrpMapping, error) {
	var m model.FrpMapping
	err := r.frpTransaction(ctx, func(tx *gorm.DB) error {
		var hint model.FrpMapping
		if err := tx.First(&hint, "id=?", id).Error; err != nil {
			return err
		}
		// Disabled devices may still have their mappings removed by their owner.
		if err := lockMappingDevice(tx, actor, hint.DeviceID, false); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&m, "id=?", id).Error; err != nil {
			return err
		}
		if m.DeviceID != hint.DeviceID {
			return contract.ErrForbidden
		}
		if err := tx.Delete(&m).Error; err != nil {
			return err
		}
		return mappingAudit(tx, actor, "frp_mapping_delete", m)
	})
	if err != nil {
		return model.FrpMapping{}, err
	}
	return m, nil
}

// One bounded scan, including holes, instead of up to 5,000 COUNT queries.
// All supported mapping writers hold the pool row until insert/audit commit.
func allocateRemotePort(tx *gorm.DB, server, device int64, start, end int) (int, error) {
	var used []int
	q := tx.Model(&model.FrpMapping{}).Where("cn_port_key_v2 BETWEEN ? AND ?", start, end)
	if server > 0 {
		q = q.Where("server_id IN ?", []int64{0, server})
	}
	if err := q.Order("cn_port_key_v2").Pluck("cn_port_key_v2", &used).Error; err != nil {
		return 0, err
	}
	credential, err := frpreservation.Credential(tx, device)
	if err != nil {
		return 0, err
	}
	held, err := frpreservation.UsedPorts(tx, server, device, credential, start, end)
	if err != nil {
		return 0, err
	}
	used = append(used, held...)
	sort.Ints(used)
	candidate := start
	for _, port := range used {
		if port > candidate {
			break
		}
		if port == candidate {
			candidate++
		}
	}
	if candidate > end {
		return 0, contract.ErrFrpPortExhausted
	}
	return candidate, nil
}

func normalizeMapping(m *model.FrpMapping) error {
	if m.ServerID < 0 || m.DeviceID <= 0 || m.LocalPort < 1 || m.LocalPort > 65535 || (m.Status != 0 && m.Status != 1) {
		return contract.ErrInvalidInput
	}
	domain := strings.ToLower(strings.TrimSpace(m.Domain))
	if m.ServerID > 0 && domain != "" {
		if m.RemotePort != 0 {
			return contract.ErrInvalidInput
		}
	} else if m.RemotePort < 1 || m.RemotePort > 65535 {
		return contract.ErrInvalidInput
	}
	if domain == "" {
		m.Domain = ""
		return nil
	}
	secure := strings.HasPrefix(domain, "https://")
	host := strings.TrimPrefix(domain, "http://")
	if secure {
		host = strings.TrimPrefix(domain, "https://")
	}
	host = strings.TrimSuffix(host, ".")
	// Match existing HTTP/HTTPS/wildcard configuration semantics; reject paths,
	// credentials, ports, templates, non-ASCII labels and empty DNS labels.
	name := strings.TrimPrefix(host, "*.")
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return contract.ErrInvalidInput
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return contract.ErrInvalidInput
			}
		}
	}
	if secure {
		domain = "https://" + host
	} else {
		domain = host
	}
	if len(domain) > 100 {
		return contract.ErrInvalidInput
	}
	m.Domain = domain
	return nil
}

// Classify mapping writes only; a duplicate-key failure in the audit must not
// masquerade as a mapping conflict.
func mappingWriteError(err error) error {
	var dbError *driver.MySQLError
	if errors.As(err, &dbError) && dbError.Number == 1062 {
		return contract.ErrFrpMappingConflict
	}
	return err
}

func checkMappingReservation(tx *gorm.DB, m model.FrpMapping) error {
	credential, err := frpreservation.Credential(tx, m.DeviceID)
	if err != nil {
		return err
	}
	if err := frpreservation.Check(tx, m.ServerID, m.DeviceID, credential, frpreservation.MappingKeys(m)); err != nil {
		return err
	}
	return frpreservation.CheckMapping(tx, m)
}

// Caller holds the global FRP gate and owner/device locks. A server remains
// private to the device owner even when an administrator creates the mapping.
func (r *FrpRepo) mappingServerPolicy(tx *gorm.DB, m model.FrpMapping) (frpconfig.ServerSpec, error) {
	if m.ServerID == 0 {
		return frpconfig.ServerSpec{TCPStart: defaultFrpRemotePortStart, TCPEnd: defaultFrpRemotePortEnd}, nil
	}
	var device model.Device
	if err := tx.First(&device, m.DeviceID).Error; err != nil {
		return frpconfig.ServerSpec{}, err
	}
	var server model.FrpServer
	if err := tx.First(&server, m.ServerID).Error; err != nil {
		return frpconfig.ServerSpec{}, err
	}
	if server.UserID != device.UserID {
		return frpconfig.ServerSpec{}, contract.ErrForbidden
	}
	if !server.Enabled {
		return frpconfig.ServerSpec{}, contract.ErrConflict
	}
	return r.openServer(tx, server, server.RevisionID)
}
func checkMappingPolicy(m model.FrpMapping, spec frpconfig.ServerSpec) error {
	if m.ServerID == 0 {
		return nil
	}
	if m.Domain == "" {
		if m.RemotePort < spec.TCPStart || m.RemotePort > spec.TCPEnd {
			return contract.ErrConflict
		}
	} else if strings.HasPrefix(m.Domain, "https://") {
		if spec.HTTPSPort == 0 {
			return contract.ErrConflict
		}
	} else if spec.HTTPPort == 0 {
		return contract.ErrConflict
	}
	return nil
}
