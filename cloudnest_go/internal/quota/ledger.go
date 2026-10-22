// Package quota coordinates logical retained bytes and upload reservations.
package quota

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func LockPolicy(tx *gorm.DB, write bool) error {
	strength := "SHARE"
	if write {
		strength = "UPDATE"
	}
	var row model.StorageQuotaPolicy
	if err := tx.Clauses(clause.Locking{Strength: strength}).First(&row, 1).Error; err != nil {
		return fmt.Errorf("%w: policy lock unavailable", contract.ErrQuotaAccounting)
	}
	return nil
}

// Begin must precede namespace/task/file locks in every content mutation. The
// shared policy lock permits independent users to allocate concurrently.
func Begin(tx *gorm.DB, uid int64) error {
	if uid <= 0 {
		return contract.ErrForbidden
	}
	if err := LockPolicy(tx, false); err != nil {
		return err
	}
	row := model.UserStorageLedger{UserID: uid}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return err
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, "user_id=?", uid).Error; err != nil {
		return err
	}
	if row.Revision == math.MaxUint64 {
		return contract.ErrQuotaAccounting
	}
	return nil
}

// Snapshot uses authoritative metadata, never the cached ledger. Current content
// and verified retained history are grouped by (file ID, storage ID). Unknown
// historical associations remain retained but unmetered, without revealing the
// foreign object's size or assigning it to a guessed owner.
func Snapshot(tx *gorm.DB, uid int64) (contract.StorageUsage, error) { return snapshot(tx, uid, true) }

func snapshot(tx *gorm.DB, uid int64, withLimit bool) (contract.StorageUsage, error) {
	tx = tx.Session(&gorm.Session{NewDB: true})
	var u contract.StorageUsage
	err := tx.Raw(`SELECT
 COALESCE(SUM(CASE WHEN r.verified=1 AND s.file_size>=0 THEN s.file_size ELSE 0 END),0) AS used_bytes,
 COALESCE(SUM(CASE WHEN r.verified=1 AND s.file_size>=0 AND f.is_deleted=0 AND f.storage_id=r.storage_id THEN s.file_size ELSE 0 END),0) AS active_bytes,
 COALESCE(SUM(CASE WHEN r.verified=1 AND s.file_size>=0 AND f.is_deleted=1 THEN s.file_size ELSE 0 END),0) AS recycle_bytes,
 COALESCE(SUM(CASE WHEN r.verified=1 AND s.file_size>=0 AND f.is_deleted=0 AND (f.storage_id IS NULL OR f.storage_id<>r.storage_id) THEN s.file_size ELSE 0 END),0) AS history_bytes,
 COALESCE(SUM(CASE WHEN r.verified=0 THEN 1 ELSE 0 END),0) AS unmetered_refs,
 COALESCE(SUM(CASE WHEN r.verified=1 AND (s.id IS NULL OR s.file_size IS NULL OR s.file_size<0) THEN 1 ELSE 0 END),0) AS broken_refs
 FROM (
  SELECT file_id,storage_id,MAX(verified) AS verified FROM (
   SELECT id AS file_id,storage_id,IF(is_folder=0,1,0) AS verified FROM file WHERE user_id=? AND storage_id IS NOT NULL
   UNION ALL
   SELECT v.file_id,v.storage_id,IF(f.is_folder=0 AND v.source_verified=1,1,0) AS verified FROM file_version v JOIN file f ON f.id=v.file_id WHERE f.user_id=?
  ) edges GROUP BY file_id,storage_id
 ) r JOIN file f ON f.id=r.file_id LEFT JOIN storage s ON s.id=r.storage_id`, uid, uid).Scan(&u).Error
	if err != nil {
		return u, fmt.Errorf("%w: retained usage could not be calculated", contract.ErrQuotaAccounting)
	}
	var reservations struct {
		ReservedBytes int64
		Broken        int64
	}
	if err := tx.Raw(`SELECT COALESCE(SUM(reserved_bytes),0) AS reserved_bytes,
 COALESCE(SUM(IF(reserved_bytes<=0 OR total_size IS NULL OR reserved_bytes<>total_size,1,0)),0) AS broken
 FROM upload_task WHERE user_id=? AND contract_version=1 AND status IN (0,2) AND expires_at>UTC_TIMESTAMP(6)`, uid).Scan(&reservations).Error; err != nil {
		return u, contract.ErrQuotaAccounting
	}
	u.ReservedBytes = reservations.ReservedBytes
	u.BrokenRefs += reservations.Broken
	if u.UsedBytes < 0 || u.ReservedBytes < 0 || u.UsedBytes > math.MaxInt64-u.ReservedBytes {
		return u, contract.ErrQuotaAccounting
	}
	var errLimit error
	if withLimit {
		u.LimitBytes, u.QuotaSource, errLimit = readLimit(tx)
	}
	return u, errLimit
}
func readLimit(tx *gorm.DB) (int64, string, error) {
	for _, key := range []string{"storage_quota_bytes", "storage_default_quota_bytes"} {
		var s model.SystemSetting
		err := tx.Where("setting_key=?", key).First(&s).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			return 0, "", err
		}
		size, err := strconv.ParseInt(strings.TrimSpace(s.SettingValue), 10, 64)
		if err != nil || size <= 0 || size > 1<<62 {
			return 0, "", contract.ErrQuotaAccounting
		}
		return size, key, nil
	}
	return 0, "none", nil
}

// Refresh commits only inside its caller's content transaction. Cancellation or
// permanent deletion may recover space even when older usage metadata is broken;
// no new allocation is allowed against an invalid or over-quota snapshot.
func Refresh(tx *gorm.DB, uid int64, enforce bool) error {
	u, err := snapshot(tx, uid, enforce)
	if err != nil {
		return err
	}
	if enforce {
		if u.BrokenRefs != 0 {
			return contract.ErrQuotaAccounting
		}
		if u.LimitBytes > 0 && u.UsedBytes+u.ReservedBytes > u.LimitBytes {
			return contract.ErrQuotaExceeded
		}
	}
	if err := tx.Model(&model.UserStorageLedger{}).Where("user_id=?", uid).Updates(map[string]any{
		"used_bytes": u.UsedBytes, "reserved_bytes": u.ReservedBytes, "unmetered_refs": u.UnmeteredRefs, "accounting_valid": u.BrokenRefs == 0, "revision": gorm.Expr("revision+1"), "updated_at": gorm.Expr("UTC_TIMESTAMP(6)"),
	}).Error; err != nil {
		return err
	}
	var revision uint64
	if err := tx.Model(&model.UserStorageLedger{}).Where("user_id=?", uid).Pluck("revision", &revision).Error; err != nil {
		return err
	}
	return tx.Create(&model.OperationLog{UserID: &uid, Action: "quota_accounting", Status: "success", Detail: fmt.Sprintf("revision=%d used=%d reserved=%d unmetered_refs=%d valid=%t", revision, u.UsedBytes, u.ReservedBytes, u.UnmeteredRefs, u.BrokenRefs == 0)}).Error
}
func RequireLiveTask(tx *gorm.DB, t model.UploadTask) error {
	if t.ContractVersion != 1 || t.Status != 0 || t.TotalSize == nil || t.ReservedBytes != *t.TotalSize || t.ReservedBytes <= 0 || t.ExpiresAt == nil {
		return contract.ErrUploadContract
	}
	var live int64
	if err := tx.Model(&model.UploadTask{}).Where("id=? AND expires_at>UTC_TIMESTAMP(6)", t.ID).Count(&live).Error; err != nil {
		return err
	}
	if live != 1 {
		return contract.ErrUploadContract
	}
	return nil
}
