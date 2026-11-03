package mysql

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	defaultFrpRemotePortStart = 20000
	defaultFrpRemotePortEnd   = 20999
	maxAllocateRetries        = 5
)

// FrpRepo 是 FRP 模块仓储实现。
type FrpRepo struct {
	db *gorm.DB
}

func NewFrpRepo(db *gorm.DB) *FrpRepo {
	return &FrpRepo{db: db}
}

func (r *FrpRepo) CreateDevice(ctx context.Context, device model.Device) (model.Device, error) {
	if err := r.db.WithContext(ctx).Create(&device).Error; err != nil {
		return model.Device{}, err
	}
	return device, nil
}

func (r *FrpRepo) ListDevices(ctx context.Context, uid int64, isAdmin bool, statusFilter *int8) ([]model.Device, error) {
	q := r.db.WithContext(ctx).Model(&model.Device{})
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

func (r *FrpRepo) GetDeviceByID(ctx context.Context, deviceID int64) (model.Device, error) {
	var device model.Device
	if err := r.db.WithContext(ctx).First(&device, "id = ?", deviceID).Error; err != nil {
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
	if err := r.db.WithContext(ctx).Where("device_id = ?", deviceID).Order("id DESC").First(&latestCfg).Error; err == nil {
		latestConfigTime = &latestCfg.CreateTime
	}

	return contract.FrpDeviceDetailMeta{MappingCount: mappingCount, LatestConfigTime: latestConfigTime}, nil
}

func (r *FrpRepo) UpdateDevice(ctx context.Context, deviceID int64, updates map[string]interface{}) (model.Device, error) {
	if err := r.db.WithContext(ctx).Model(&model.Device{}).Where("id = ?", deviceID).Updates(updates).Error; err != nil {
		return model.Device{}, err
	}
	var device model.Device
	if err := r.db.WithContext(ctx).First(&device, deviceID).Error; err != nil {
		return model.Device{}, err
	}
	return device, nil
}

func (r *FrpRepo) DeleteDevice(ctx context.Context, deviceID int64) error {
	return r.db.WithContext(ctx).Delete(&model.Device{}, "id = ?", deviceID).Error
}

func (r *FrpRepo) CreateMapping(ctx context.Context, req contract.FrpCreateMappingInput) (model.FrpMapping, error) {
	var created model.FrpMapping
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if req.Domain != "" {
			var conflictCount int64
			if err := tx.Model(&model.FrpMapping{}).Where("domain = ?", req.Domain).Count(&conflictCount).Error; err != nil {
				return err
			}
			if conflictCount > 0 {
				return fmt.Errorf("domain already exists")
			}
		}

		remotePort := 0
		if req.RemotePort != nil {
			remotePort = *req.RemotePort
		} else {
			p, err := allocateRemotePort(tx)
			if err != nil {
				return err
			}
			remotePort = p
		}

		created = model.FrpMapping{
			DeviceID:   req.DeviceID,
			LocalPort:  req.LocalPort,
			RemotePort: remotePort,
			Domain:     req.Domain,
			Status:     req.Status,
		}
		if err := tx.Create(&created).Error; err != nil {
			if isDuplicateKeyErr(err) {
				return fmt.Errorf("remote_port already exists")
			}
			return err
		}
		return nil
	})
	if err != nil {
		return model.FrpMapping{}, err
	}
	return created, nil
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

func (r *FrpRepo) UpdateMapping(ctx context.Context, req contract.FrpUpdateMappingInput) (model.FrpMapping, error) {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if req.Domain != nil {
			domain := strings.TrimSpace(*req.Domain)
			if domain != "" {
				var conflict int64
				if err := tx.Model(&model.FrpMapping{}).Where("domain = ? AND id <> ?", domain, req.MappingID).Count(&conflict).Error; err != nil {
					return err
				}
				if conflict > 0 {
					return fmt.Errorf("domain already exists")
				}
			}
		}

		updates := map[string]interface{}{}
		if req.LocalPort != nil {
			updates["local_port"] = *req.LocalPort
		}
		if req.RemotePort != nil {
			updates["remote_port"] = *req.RemotePort
		}
		if req.Domain != nil {
			updates["domain"] = strings.TrimSpace(*req.Domain)
		}
		if req.Status != nil {
			updates["status"] = *req.Status
		}

		if err := tx.Model(&model.FrpMapping{}).Where("id = ?", req.MappingID).Updates(updates).Error; err != nil {
			if isDuplicateKeyErr(err) {
				return fmt.Errorf("remote_port already exists")
			}
			return err
		}
		return nil
	})
	if err != nil {
		return model.FrpMapping{}, err
	}

	var mapping model.FrpMapping
	if err := r.db.WithContext(ctx).First(&mapping, req.MappingID).Error; err != nil {
		return model.FrpMapping{}, err
	}
	return mapping, nil
}

func (r *FrpRepo) DeleteMapping(ctx context.Context, mappingID int64) error {
	return r.db.WithContext(ctx).Delete(&model.FrpMapping{}, "id = ?", mappingID).Error
}

func (r *FrpRepo) ListActiveMappingsByDeviceID(ctx context.Context, deviceID int64) ([]model.FrpMapping, error) {
	rows := make([]model.FrpMapping, 0)
	if err := r.db.WithContext(ctx).Where("device_id = ? AND status = 1", deviceID).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *FrpRepo) CreateConfig(ctx context.Context, cfg model.FrpConfig) (model.FrpConfig, error) {
	if err := r.db.WithContext(ctx).Create(&cfg).Error; err != nil {
		return model.FrpConfig{}, err
	}
	return cfg, nil
}

func (r *FrpRepo) ListConfigsByDeviceID(ctx context.Context, deviceID int64) ([]model.FrpConfig, error) {
	rows := make([]model.FrpConfig, 0)
	if err := r.db.WithContext(ctx).Select("id,device_id,create_time").Where("device_id = ?", deviceID).Order("id DESC").Find(&rows).Error; err != nil {
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

func allocateRemotePort(tx *gorm.DB) (int, error) {
	for i := 0; i < maxAllocateRetries; i++ {
		var maxUsed int
		if err := tx.Model(&model.FrpMapping{}).
			Where("remote_port BETWEEN ? AND ?", defaultFrpRemotePortStart, defaultFrpRemotePortEnd).
			Select("COALESCE(MAX(remote_port), 0)").
			Scan(&maxUsed).Error; err != nil {
			return 0, err
		}

		candidate := defaultFrpRemotePortStart
		if maxUsed >= defaultFrpRemotePortStart && maxUsed < defaultFrpRemotePortEnd {
			candidate = maxUsed + 1
		}

		if maxUsed >= defaultFrpRemotePortEnd {
			for p := defaultFrpRemotePortStart; p <= defaultFrpRemotePortEnd; p++ {
				var count int64
				if err := tx.Model(&model.FrpMapping{}).Where("remote_port = ?", p).Count(&count).Error; err != nil {
					return 0, err
				}
				if count == 0 {
					candidate = p
					break
				}
			}
		}

		if candidate < defaultFrpRemotePortStart || candidate > defaultFrpRemotePortEnd {
			return 0, fmt.Errorf("remote port pool exhausted")
		}

		var occupied int64
		if err := tx.Model(&model.FrpMapping{}).Where("remote_port = ?", candidate).Count(&occupied).Error; err != nil {
			return 0, err
		}
		if occupied == 0 {
			return candidate, nil
		}
	}

	return 0, fmt.Errorf("allocate remote port failed after retries")
}

func isDuplicateKeyErr(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "duplicate") || strings.Contains(text, "1062")
}

func (r *FrpRepo) GetAgentHeartbeat(ctx context.Context, uid int64, admin bool, id int64) (*contract.AgentHeartbeatState, error) {
	var snapshot struct {
		OwnerID       int64
		BindingID     *int64
		Active        bool
		LastSeenAt    *time.Time
		ClientVersion string
	}
	result := r.db.WithContext(ctx).Table("device d").Joins("LEFT JOIN agent_binding b ON b.device_id=d.id").
		Select("d.user_id AS owner_id,b.device_id AS binding_id,(b.credential_id IS NOT NULL AND b.owner_id=d.user_id AND d.status=1) AS active,b.last_seen_at,COALESCE(b.client_version,'') AS client_version").
		Where("d.id=?", id).Scan(&snapshot)
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
		return nil, nil
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
		if !admin && d.UserID != uid {
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
