// Package frpreservation tracks possible remote use independently of editable
// mappings. All writers hold the global FRP gate before owner/device locks.
package frpreservation

import (
	"errors"
	"strconv"

	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func Lock(tx *gorm.DB) error {
	var pool model.FrpMappingPool
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&pool, "id=1").Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return contract.ErrFrpMappingBusy
		}
		return err
	}
	var schema model.FrpReservationSchema
	if err := tx.First(&schema, "id=1").Error; err != nil {
		return err
	}
	if schema.Version != 1 {
		return contract.ErrFrpMappingBusy
	}
	return nil
}
func namespace(q *gorm.DB, server int64) *gorm.DB {
	if server > 0 {
		return q.Where("server_id IN ?", []int64{0, server})
	}
	return q
}
func matching(tx *gorm.DB, keys []Key) *gorm.DB {
	q := tx.Where("kind='unknown'")
	for _, kind := range []string{"tcp", "http", "https"} {
		var values []string
		for _, key := range keys {
			if key.Kind == "unknown" {
				return tx
			}
			if key.Kind == kind {
				values = append(values, key.Resource)
			}
		}
		if len(values) > 0 {
			q = q.Or("kind=? AND resource IN ?", kind, values)
		}
	}
	return q
}
func excludeCurrent(q *gorm.DB, device int64, credential string) *gorm.DB {
	// An opaque historical generation is never transferable to a new enrollment.
	if credential != "" {
		return q.Where("NOT(device_id=? AND credential_id=?)", device, credential)
	}
	return q
}
func Check(tx *gorm.DB, server, device int64, credential string, keys []Key) error {
	if len(keys) == 0 {
		return nil
	}
	q := namespace(tx.Model(&model.FrpReservation{}), server)
	q = excludeCurrent(q, device, credential).Where(matching(tx.Session(&gorm.Session{NewDB: true}), keys))
	var found model.FrpReservation
	err := q.Select("id").Limit(1).Take(&found).Error
	if err == nil {
		return contract.ErrFrpMappingConflict
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return nil
}
func mappingConflicts(tx *gorm.DB, server, device int64, keys []Key) error {
	var ports []int
	var domains []string
	unknown := false
	for _, key := range keys {
		switch key.Kind {
		case "unknown":
			unknown = true
		case "tcp":
			n, _ := strconv.Atoi(key.Resource)
			ports = append(ports, n)
		case "http", "https":
			domains = append(domains, key.Kind+"://"+key.Resource)
		}
	}
	q := namespace(tx.Model(&model.FrpMapping{}), server).Where("device_id<>?", device)
	if !unknown {
		filter := tx.Session(&gorm.Session{NewDB: true}).Where("1=0")
		if len(ports) > 0 {
			filter = filter.Or("remote_port IN ? AND (server_id=0 OR COALESCE(domain,'')='')", ports)
		}
		if len(domains) > 0 {
			filter = filter.Or("cn_domain_key_v1 IN ?", domains)
		}
		q = q.Where(filter)
	}
	var found model.FrpMapping
	err := q.Select("id").Limit(1).Take(&found).Error
	if err == nil {
		return contract.ErrFrpMappingConflict
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return nil
}
func Acquire(tx *gorm.DB, owner, device int64, credential, command string, cfg model.FrpConfig, plain string) error {
	if device <= 0 || owner <= 0 || credential == "" || command == "" {
		return contract.ErrInvalidInput
	}
	keys := Keys(plain)
	if len(keys) == 0 {
		return nil
	}
	if err := Check(tx, cfg.ServerID, device, credential, keys); err != nil {
		return err
	}
	if err := mappingConflicts(tx, cfg.ServerID, device, keys); err != nil {
		return err
	}
	rows := make([]model.FrpReservation, 0, len(keys))
	for _, key := range keys {
		rows = append(rows, model.FrpReservation{OwnerID: owner, DeviceID: device, CredentialID: credential, Reference: command, ConfigID: cfg.ID, ServerID: cfg.ServerID, Kind: key.Kind, Resource: key.Resource})
	}
	return tx.CreateInBatches(&rows, 100).Error
}

// Release is called only inside the first successful forget receipt transaction,
// after authorization, lease validation and idempotency checks, before audit.
func Release(tx *gorm.DB, device int64, credential string) error {
	if device <= 0 || credential == "" {
		return contract.ErrInvalidInput
	}
	return tx.Where("device_id=? AND credential_id=?", device, credential).Delete(&model.FrpReservation{}).Error
}
func Credential(tx *gorm.DB, device int64) (string, error) {
	var row model.AgentBinding
	err := tx.Select("credential_id").Where("device_id=?", device).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if row.CredentialID == nil {
		return "", nil
	}
	return *row.CredentialID, nil
}
func MappingKeys(m model.FrpMapping) []Key {
	keys := []Key{}
	if m.ServerID == 0 || m.Domain == "" {
		keys = append(keys, Key{"tcp", strconv.Itoa(m.RemotePort)})
	}
	if m.Domain != "" {
		kind, domain := "http", m.Domain
		if len(domain) >= 8 && domain[:8] == "https://" {
			kind, domain = "https", domain[8:]
		} else if len(domain) >= 7 && domain[:7] == "http://" {
			domain = domain[7:]
		}
		keys = append(keys, Key{kind, domain})
	}
	return keys
}
func UsedPorts(tx *gorm.DB, server, device int64, credential string, start, end int) ([]int, error) {
	var unknown model.FrpReservation
	q := excludeCurrent(namespace(tx.Model(&model.FrpReservation{}), server), device, credential)
	err := q.Where("kind='unknown'").Select("id").Take(&unknown).Error
	if err == nil {
		return nil, contract.ErrFrpMappingConflict
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	var values []string
	q = excludeCurrent(namespace(tx.Model(&model.FrpReservation{}), server), device, credential)
	if err := q.Where("kind='tcp' AND CAST(resource AS UNSIGNED) BETWEEN ? AND ?", start, end).Distinct().Pluck("resource", &values).Error; err != nil {
		return nil, err
	}
	ports := make([]int, 0, len(values))
	for _, value := range values {
		n, err := strconv.Atoi(value)
		if err != nil {
			return nil, contract.ErrFrpMappingBusy
		}
		ports = append(ports, n)
	}
	return ports, nil
}
