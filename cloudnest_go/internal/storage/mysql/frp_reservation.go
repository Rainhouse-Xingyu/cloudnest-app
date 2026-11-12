package mysql

import (
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	"context"
)

// Read-only ownership is retained even when a device has been retired or moved.
// The historical resource claim still belongs to its original issuing owner.
func (r *FrpRepo) ListReservations(ctx context.Context, actor contract.FrpActor, device, after int64, limit int) ([]model.FrpReservation, error) {
	if actor.UserID <= 0 || device < 0 || after < 0 || limit < 1 || limit > 100 {
		return nil, contract.ErrInvalidInput
	}
	q := r.db.WithContext(ctx).Where("id>?", after)
	if !actor.Admin {
		q = q.Where("owner_id=?", actor.UserID)
	}
	if device > 0 {
		q = q.Where("device_id=?", device)
	}
	rows := make([]model.FrpReservation, 0)
	err := q.Order("id").Limit(limit).Find(&rows).Error
	return rows, err
}
