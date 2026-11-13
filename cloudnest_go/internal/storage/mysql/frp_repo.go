package mysql

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cloudenest_go/internal/frpconfig"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	defaultFrpRemotePortStart = 20000
	defaultFrpRemotePortEnd   = 20999
)

// FrpRepo 是 FRP 模块仓储实现。
type FrpRepo struct {
	keys *frpconfig.Keyring
	db   *gorm.DB
}

func NewFrpRepo(db *gorm.DB, keys ...*frpconfig.Keyring) *FrpRepo {
	r := &FrpRepo{db: db}
	if len(keys) > 0 {
		r.keys = keys[0]
	}
	return r
}

func (r *FrpRepo) ListDevices(ctx context.Context, uid int64, isAdmin bool, statusFilter *int8, retired bool) ([]model.Device, error) {
	q := r.db.WithContext(ctx).Model(&model.Device{})
	if retired {
		q = q.Unscoped().Where("retired_at IS NOT NULL")
	}
	if !isAdmin {
		q = q.Where("user_id = ?", uid)
	}
	if statusFilter != nil {
		q = q.Where("status = ?", *statusFilter)
	}
	rows := make([]model.Device, 0)
	if err := q.Order("id DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *FrpRepo) GetDeviceByID(ctx context.Context, deviceID int64, retired ...bool) (model.Device, error) {
	var device model.Device
	q := r.db.WithContext(ctx)
	if len(retired) > 0 && retired[0] {
		q = q.Unscoped().Where("retired_at IS NOT NULL")
	}
	if err := q.First(&device, "id = ?", deviceID).Error; err != nil {
		return model.Device{}, err
	}
	return device, nil
}

func (r *FrpRepo) GetDeviceDetailMeta(ctx context.Context, deviceID int64) (contract.FrpDeviceDetailMeta, error) {
	var mappingCount int64
	if err := r.db.WithContext(ctx).Model(&model.FrpMapping{}).Where("device_id = ?", deviceID).Count(&mappingCount).Error; err != nil {
		return contract.FrpDeviceDetailMeta{}, err
	}

	var latestCfg model.FrpConfig
	var latestConfigTime *time.Time
	if err := r.db.WithContext(ctx).Select("id,create_time").Where("device_id = ?", deviceID).Order("id DESC").First(&latestCfg).Error; err == nil {
		latestConfigTime = &latestCfg.CreateTime
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return contract.FrpDeviceDetailMeta{}, err
	}

	return contract.FrpDeviceDetailMeta{MappingCount: mappingCount, LatestConfigTime: latestConfigTime}, nil
}

func (r *FrpRepo) ListMappingsByDeviceID(ctx context.Context, deviceID int64) ([]model.FrpMapping, error) {
	rows := make([]model.FrpMapping, 0)
	if err := r.db.WithContext(ctx).Where("device_id = ?", deviceID).Order("id DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *FrpRepo) GetMappingByID(ctx context.Context, mappingID int64) (model.FrpMapping, error) {
	var mapping model.FrpMapping
	if err := r.db.WithContext(ctx).First(&mapping, "id = ?", mappingID).Error; err != nil {
		return model.FrpMapping{}, err
	}
	return mapping, nil
}

func (r *FrpRepo) ListActiveMappingsByDeviceID(ctx context.Context, deviceID int64) ([]model.FrpMapping, error) {
	rows := make([]model.FrpMapping, 0)
	if err := r.db.WithContext(ctx).Where("device_id = ? AND status = 1 AND server_id = 0", deviceID).Limit(513).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *FrpRepo) ListConfigsByDeviceID(ctx context.Context, deviceID int64) ([]model.FrpConfig, error) {
	rows := make([]model.FrpConfig, 0)
	if err := r.db.WithContext(ctx).Select("id,device_id,create_time,server_id,server_revision_id").Where("device_id = ?", deviceID).Order("id DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *FrpRepo) GetConfigByID(ctx context.Context, configID int64) (model.FrpConfig, error) {
	var cfg model.FrpConfig
	if err := r.db.WithContext(ctx).First(&cfg, "id = ?", configID).Error; err != nil {
		return model.FrpConfig{}, err
	}
	return cfg, nil
}

func (r *FrpRepo) ResolveConfigID(ctx context.Context, deviceID int64, requested *int64) (int64, error) {
	var cfg model.FrpConfig
	q := r.db.WithContext(ctx).Where("device_id = ?", deviceID)
	if requested != nil && *requested > 0 {
		q = q.Where("id = ?", *requested)
	}
	if err := q.Order("id DESC").First(&cfg).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) && (requested == nil || *requested <= 0) {
			return 0, fmt.Errorf("frp no config for device")
		}
		return 0, err
	}
	return cfg.ID, nil
}

func (r *FrpRepo) CreateOperationLog(ctx context.Context, row model.OperationLog) error {
	return r.db.WithContext(ctx).Create(&row).Error
}

func (r *FrpRepo) GetAgentHeartbeat(ctx context.Context, uid int64, admin bool, id int64) (*contract.AgentHeartbeatState, error) {
	var snapshot struct {
		OwnerID       int64
		Enabled       bool
		BindingID     *int64
		Active        bool
		LastSeenAt    *time.Time
		ClientVersion string
	}
	result := r.db.WithContext(ctx).Table("device d").Joins("LEFT JOIN agent_binding b ON b.device_id=d.id").
		Select("d.user_id AS owner_id,d.status=1 AS enabled,b.device_id AS binding_id,(b.credential_id IS NOT NULL AND b.owner_id=d.user_id AND d.status=1) AS active,b.last_seen_at,COALESCE(b.client_version,'') AS client_version").
		Where("d.id=? AND d.retired_at IS NULL", id).Scan(&snapshot)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	if !admin && snapshot.OwnerID != uid {
		return nil, contract.ErrForbidden
	}
	if snapshot.BindingID == nil {
		if snapshot.Enabled {
			return nil, nil
		}
		return &contract.AgentHeartbeatState{}, nil
	}
	return &contract.AgentHeartbeatState{Active: snapshot.Active, LastSeenAt: snapshot.LastSeenAt, ClientVersion: snapshot.ClientVersion}, nil
}

// Serialize legacy Redis-side execution with the first enrollment transaction.
// Even after revocation the binding row persists, preventing a downgrade to JWT.
func (r *FrpRepo) WithLegacyDevice(ctx context.Context, uid int64, admin bool, id int64, run func(context.Context) error) error {
	op, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return r.db.WithContext(op).Transaction(func(tx *gorm.DB) error {
		var d model.Device
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&d, "id=?", id).Error; err != nil {
			return err
		}
		if d.Status != 1 || (!admin && d.UserID != uid) {
			return contract.ErrForbidden
		}
		var count int64
		if err := tx.Model(&model.AgentBinding{}).Where("device_id=?", id).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return contract.ErrForbidden
		}
		return run(op)
	})
}
