package mysql

import (
	"context"
	"fmt"
	"unicode"
	"unicode/utf8"

	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func validDeviceText(s string, limit int, empty bool) bool {
	if !utf8.ValidString(s) || (!empty && s == "") || utf8.RuneCountInString(s) > limit {
		return false
	}
	for _, c := range s {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}
func deviceAudit(tx *gorm.DB, actor contract.FrpActor, action string, id int64) error {
	detail := fmt.Sprintf("device_id=%d", id)
	if action == "frp_device_delete" {
		detail += " retired=1 reservations_retained=1"
	}
	return tx.Create(&model.OperationLog{UserID: &actor.UserID, Action: action, Detail: detail, ClientIP: actor.ClientIP}).Error
}
func (r *FrpRepo) CreateDevice(ctx context.Context, actor contract.FrpActor, d model.Device) (model.Device, error) {
	if actor.UserID <= 0 || d.UserID != actor.UserID {
		return model.Device{}, contract.ErrForbidden
	}
	if d.ID != 0 || d.RetiredAt.Valid || d.Status != 1 || !validDeviceText(d.DeviceName, 100, false) || !validDeviceText(d.DeviceIP, 50, true) {
		return model.Device{}, contract.ErrInvalidInput
	}
	err := r.frpTransaction(ctx, func(tx *gorm.DB) error {
		var owner model.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&owner, "id=?", actor.UserID).Error; err != nil {
			return err
		}
		if owner.Status != 1 {
			return contract.ErrForbidden
		}
		if err := tx.Create(&d).Error; err != nil {
			return err
		}
		return deviceAudit(tx, actor, "frp_device_create", d.ID)
	})
	if err != nil {
		return model.Device{}, err
	}
	return d, nil
}
func (r *FrpRepo) UpdateDevice(ctx context.Context, actor contract.FrpActor, id int64, updates map[string]interface{}) (model.Device, error) {
	if len(updates) == 0 {
		return model.Device{}, contract.ErrInvalidInput
	}
	for key, value := range updates {
		switch key {
		case "device_name":
			s, ok := value.(string)
			if !ok || !validDeviceText(s, 100, false) {
				return model.Device{}, contract.ErrInvalidInput
			}
		case "device_ip":
			s, ok := value.(string)
			if !ok || !validDeviceText(s, 50, true) {
				return model.Device{}, contract.ErrInvalidInput
			}
		case "status":
			n, ok := value.(int8)
			if !ok || n != 0 && n != 1 {
				return model.Device{}, contract.ErrInvalidInput
			}
		default:
			return model.Device{}, contract.ErrInvalidInput
		}
	}
	var d model.Device
	err := r.frpTransaction(ctx, func(tx *gorm.DB) error {
		if err := lockMappingDevice(tx, actor, id, false); err != nil {
			return err
		}
		if err := tx.Model(&model.Device{}).Where("id=?", id).Updates(updates).Error; err != nil {
			return err
		}
		if err := tx.First(&d, "id=?", id).Error; err != nil {
			return err
		}
		return deviceAudit(tx, actor, "frp_device_update", id)
	})
	if err != nil {
		return model.Device{}, err
	}
	return d, nil
}

// Retirement preserves identity, encrypted configurations, command history and
// mapping reservations. Revocation is not proof that an offline frpc stopped.
func (r *FrpRepo) DeleteDevice(ctx context.Context, actor contract.FrpActor, id int64) (model.Device, error) {
	var d model.Device
	err := r.frpTransaction(ctx, func(tx *gorm.DB) error {
		if err := lockMappingDevice(tx, actor, id, false); err != nil {
			return err
		}
		if err := tx.Model(&model.Device{}).Where("id=?", id).Updates(map[string]any{"status": int8(0), "retired_at": gorm.Expr("UTC_TIMESTAMP(6)")}).Error; err != nil {
			return err
		}
		// The verified retirement trigger revokes both secrets and cancels unfinished
		// commands in this transaction, including writes made outside this API.
		if err := tx.Unscoped().First(&d, "id=?", id).Error; err != nil {
			return err
		}
		return deviceAudit(tx, actor, "frp_device_delete", id)
	})
	if err != nil {
		return model.Device{}, err
	}
	return d, nil
}
