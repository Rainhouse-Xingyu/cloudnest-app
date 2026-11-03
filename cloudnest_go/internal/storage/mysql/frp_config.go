package mysql

import (
	"context"
	"fmt"

	"cloudenest_go/internal/frpconfig"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func lockConfigDevice(tx *gorm.DB, uid int64, admin bool, deviceID int64) error {
	var device model.Device
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&device, deviceID).Error; err != nil {
		return err
	}
	if device.Status != 1 || (!admin && device.UserID != uid) {
		return contract.ErrForbidden
	}
	return nil
}

// Reserve the version ID inside the transaction. The empty placeholder cannot
// become visible on commit unless sealing and the audit both succeed.
func (r *FrpRepo) insertSealedConfig(tx *gorm.DB, deviceID int64, plain string) (model.FrpConfig, error) {
	if r.keys == nil {
		return model.FrpConfig{}, frpconfig.ErrUnavailable
	}
	cfg := model.FrpConfig{DeviceID: &deviceID}
	if err := tx.Create(&cfg).Error; err != nil {
		return model.FrpConfig{}, err
	}
	sealed, err := r.keys.Seal(deviceID, cfg.ID, plain)
	if err != nil {
		return model.FrpConfig{}, err
	}
	if err := tx.Model(&cfg).Update("config_text", sealed).Error; err != nil {
		return model.FrpConfig{}, err
	}
	cfg.ConfigText = sealed
	return cfg, nil
}

func (r *FrpRepo) CreateConfig(ctx context.Context, uid int64, admin bool, cfg model.FrpConfig, clientIP ...string) (model.FrpConfig, error) {
	var created model.FrpConfig
	if cfg.DeviceID == nil || *cfg.DeviceID <= 0 {
		return created, frpconfig.ErrInvalid
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockConfigDevice(tx, uid, admin, *cfg.DeviceID); err != nil {
			return err
		}
		var err error
		created, err = r.insertSealedConfig(tx, *cfg.DeviceID, cfg.ConfigText)
		if err != nil {
			return err
		}
		return tx.Create(&model.OperationLog{UserID: &uid, ClientIP: configAuditIP(clientIP), Action: "frp_config_save", Detail: fmt.Sprintf("config_id=%d device_id=%d", created.ID, *created.DeviceID)}).Error
	})
	if err != nil {
		return model.FrpConfig{}, err
	}
	return created, nil
}

func (r *FrpRepo) RollbackConfig(ctx context.Context, uid int64, admin bool, id int64, clientIP ...string) (model.FrpConfig, model.FrpConfig, error) {
	var old, created model.FrpConfig
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var lookup model.FrpConfig
		if err := tx.Select("id,device_id").First(&lookup, id).Error; err != nil {
			return err
		}
		if lookup.DeviceID == nil || *lookup.DeviceID <= 0 {
			return frpconfig.ErrInvalid
		}
		if err := lockConfigDevice(tx, uid, admin, *lookup.DeviceID); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&old, id).Error; err != nil {
			return err
		}
		if old.DeviceID == nil || *old.DeviceID != *lookup.DeviceID {
			return frpconfig.ErrInvalid
		}
		plain, err := r.keys.Open(*old.DeviceID, old.ID, old.ConfigText)
		if err != nil {
			return err
		}
		created, err = r.insertSealedConfig(tx, *old.DeviceID, plain)
		if err != nil {
			return err
		}
		return tx.Create(&model.OperationLog{UserID: &uid, ClientIP: configAuditIP(clientIP), Action: "frp_config_rollback", Detail: fmt.Sprintf("from_config_id=%d to_config_id=%d device_id=%d", old.ID, created.ID, *old.DeviceID)}).Error
	})
	if err != nil {
		return model.FrpConfig{}, model.FrpConfig{}, err
	}
	return old, created, nil
}

type FRPEncryptionReport struct {
	Scanned   int64 `json:"scanned"`
	Legacy    int64 `json:"legacy"`
	OldKey    int64 `json:"old_key"`
	Current   int64 `json:"current"`
	Rewritten int64 `json:"rewritten"`
}

// ReencryptConfigs is an explicit, resumable maintenance operation. Each row
// and its audit commit together. Dry runs authenticate all encrypted rows too.
// Stop old writers and retain the old keys and backup until verification ends.
func (r *FrpRepo) ReencryptConfigs(ctx context.Context, dryRun bool) (FRPEncryptionReport, error) {
	var report FRPEncryptionReport
	if r.keys == nil {
		return report, frpconfig.ErrUnavailable
	}
	var last int64
	for {
		var ids []int64
		if err := r.db.WithContext(ctx).Model(&model.FrpConfig{}).Where("id > ?", last).Order("id").Limit(100).Pluck("id", &ids).Error; err != nil {
			return report, err
		}
		if len(ids) == 0 {
			return report, nil
		}
		for _, id := range ids {
			var delta FRPEncryptionReport
			err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				var row model.FrpConfig
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, id).Error; err != nil {
					return err
				}
				if row.DeviceID == nil || *row.DeviceID <= 0 {
					return frpconfig.ErrInvalid
				}
				plain := row.ConfigText
				if frpconfig.IsEnvelope(plain) {
					var err error
					plain, err = r.keys.Open(*row.DeviceID, row.ID, row.ConfigText)
					if err != nil {
						return err
					}
					if r.keys.IsCurrent(row.ConfigText) {
						delta.Current = 1
						return nil
					}
					delta.OldKey = 1
				} else {
					delta.Legacy = 1
				}
				sealed, err := r.keys.Seal(*row.DeviceID, row.ID, plain)
				if err != nil {
					return err
				}
				if dryRun {
					return nil
				}
				if err := tx.Model(&row).Update("config_text", sealed).Error; err != nil {
					return err
				}
				if err := tx.Create(&model.OperationLog{Action: "frp_config_encrypt", Detail: fmt.Sprintf("config_id=%d device_id=%d", row.ID, *row.DeviceID)}).Error; err != nil {
					return err
				}
				delta.Rewritten = 1
				return nil
			})
			if err != nil {
				return report, fmt.Errorf("FRP configuration migration stopped at id=%d: %w", id, err)
			}
			report.Scanned++
			report.Legacy += delta.Legacy
			report.OldKey += delta.OldKey
			report.Current += delta.Current
			report.Rewritten += delta.Rewritten
			last = id
		}
	}
}

func configAuditIP(values []string) string {
	if len(values) > 0 {
		return values[0]
	}
	return ""
}
