package mysql

import (
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	"context"
	"database/sql"
	"fmt"
	"gorm.io/gorm"
)

func operationLogScope(db *gorm.DB, uid int64, isAdmin bool, q contract.FileOperationLogQuery) (*gorm.DB, error) {
	db = db.Model(&model.OperationLog{})
	if !(isAdmin && q.All == 1) {
		db = db.Where("user_id = ?", uid)
	}
	if q.Action != "" {
		db = db.Where("action = ?", q.Action)
	}
	if q.Status != "" {
		db = db.Where("status = ?", q.Status)
	}
	if q.StartTime != "" {
		start, err := parseFlexibleTimeForFileRepo(q.StartTime)
		if err != nil {
			return nil, fmt.Errorf("invalid start_time")
		}
		db = db.Where("create_time >= ?", start)
	}
	if q.EndTime != "" {
		end, err := parseFlexibleTimeForFileRepo(q.EndTime)
		if err != nil {
			return nil, fmt.Errorf("invalid end_time")
		}
		db = db.Where("create_time <= ?", end)
	}
	if q.SnapshotID > 0 {
		db = db.Where("id <= ?", q.SnapshotID)
	}
	return db, nil
}

// ID bounds hold the browsing range stable across new audit inserts. Each
// response uses a repeatable-read snapshot; historical retention may still
// remove rows between requests. No server-side cursor or long transaction lives
// beyond this response.
func (r *FileRepo) ListOperationLogCursor(ctx context.Context, uid int64, isAdmin bool, q contract.OperationLogCursorQuery) (result contract.OperationLogCursorPage, err error) {
	result.Items = make([]model.OperationLog, 0)
	if uid <= 0 || q.Limit < 1 || q.Limit > 200 || q.BeforeID < 0 || q.AfterID < 0 || q.SnapshotID < 0 || q.BeforeID > 0 && q.AfterID > 0 || (q.BeforeID > 0 || q.AfterID > 0) && q.SnapshotID == 0 {
		return result, contract.ErrInvalidInput
	}
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query, err := operationLogScope(tx, uid, isAdmin, q.FileOperationLogQuery)
		if err != nil {
			return err
		}
		snapshot := q.SnapshotID
		if snapshot == 0 {
			if err := query.Select("COALESCE(MAX(id),0)").Scan(&snapshot).Error; err != nil {
				return err
			}
		}
		result.SnapshotID = snapshot
		if snapshot == 0 {
			return nil
		}
		q.SnapshotID = snapshot
		base := func() *gorm.DB { db, _ := operationLogScope(tx, uid, isAdmin, q.FileOperationLogQuery); return db }
		if err := base().Count(&result.Total).Error; err != nil {
			return err
		}
		page := base()
		if q.BeforeID > 0 {
			page = page.Where("id < ?", q.BeforeID)
		}
		if q.AfterID > 0 {
			page = page.Where("id > ?", q.AfterID).Order("id ASC")
		} else {
			page = page.Order("id DESC")
		}
		if err := page.Limit(q.Limit).Find(&result.Items).Error; err != nil {
			return err
		}
		if q.AfterID > 0 {
			for left, right := 0, len(result.Items)-1; left < right; left, right = left+1, right-1 {
				result.Items[left], result.Items[right] = result.Items[right], result.Items[left]
			}
		}
		if len(result.Items) == 0 {
			return nil
		}
		first, last := result.Items[0].ID, result.Items[len(result.Items)-1].ID
		var higher, lower []int64
		if err := base().Where("id > ?", first).Limit(1).Pluck("id", &higher).Error; err != nil {
			return err
		}
		if err := base().Where("id < ?", last).Limit(1).Pluck("id", &lower).Error; err != nil {
			return err
		}
		if len(higher) > 0 {
			result.NextAfterID = first
		}
		if len(lower) > 0 {
			result.NextBeforeID = last
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return result, err
}
