package transfer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"cloudenest_go/internal/service/contract"
	"cloudenest_go/internal/store"
	"cloudenest_go/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (m *Manager) Audit(ctx context.Context) (Report, error) {
	var report Report
	if err := store.CheckTransferCleanupContract(m.db.WithContext(ctx)); err != nil {
		return report, unavailable(err)
	}
	root, err := m.openRoot(ctx, false)
	if err != nil {
		return report, unavailable(err)
	}
	defer root.Close()
	var volume model.TransferVolume
	if err := m.db.WithContext(ctx).First(&volume, "id=? AND root_path=?", root.id, root.path).Error; err != nil {
		return report, unavailable(err)
	}
	report.VolumeID = root.id
	report.LimitBytes = volume.LimitBytes
	report.Usage, err = usage(m.db.WithContext(ctx), root.id)
	if err != nil {
		return report, err
	}
	var blocked int64
	if err := m.db.WithContext(ctx).Model(&model.TransferArtifact{}).Where("volume_id=? AND state='blocked'", root.id).Count(&blocked).Error; err != nil {
		return report, err
	}
	report.Blocked = int(blocked)
	if volume.LimitBytes <= 0 || volume.LimitBytes > 4<<40 || volume.MinFreeBytes < 0 {
		report.Usage.Broken++
	}
	if blocked > 0 || report.Usage.Broken > 0 {
		return report, ErrPending
	}
	return report, nil
}
func (m *Manager) SetLimit(ctx context.Context, limit int64) error {
	if limit <= 0 || limit > 4<<40 {
		return contract.ErrInvalidInput
	}
	root, err := m.openRoot(ctx, true)
	if err != nil {
		return unavailable(err)
	}
	defer root.Close()
	return transactionError(m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		value := model.TransferVolume{ID: root.id, RootPath: root.path, LimitBytes: limit, MinFreeBytes: DefaultMinFree}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&value).Error; err != nil {
			return err
		}
		var volume model.TransferVolume
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&volume, "id=?", root.id).Error; err != nil {
			return err
		}
		if volume.RootPath != root.path {
			return contract.ErrTransferUnavailable
		}
		if err := tx.Model(&volume).Update("limit_bytes", limit).Error; err != nil {
			return err
		}
		return tx.Create(&model.OperationLog{Action: "transfer_limit", Status: "success", Detail: fmt.Sprintf("volume=%s limit_bytes=%d", root.id, limit)}).Error
	}))
}

// Run is bounded and does not initialize an unused root. A normal request first
// registers that volume. Repeated unchanged pending states remain quiet.
func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	last := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(m.rootPath, ".transfer-volume-v1")); errors.Is(err, os.ErrNotExist) {
				continue
			}
			pass, stop := context.WithTimeout(ctx, 30*time.Second)
			report, err := m.Reconcile(pass)
			stop()
			if ctx.Err() != nil {
				return
			}
			key := fmt.Sprintf("%t/%d/%d/%d", err != nil, report.Blocked, report.Busy, report.Usage.Broken)
			if report.Removed > 0 {
				log.Printf("transfer reconciliation removed=%d charged_bytes=%d", report.Removed, report.Usage.Charged)
			}
			if err != nil && key != last {
				log.Printf("transfer reconciliation pending blocked=%d busy=%d accounting_findings=%d", report.Blocked, report.Busy, report.Usage.Broken)
			}
			last = key
		}
	}
}
