package store

import (
	"errors"
	"gorm.io/gorm"
)

func CheckQuotaSchema(db *gorm.DB) error {
	var count int64
	if err := db.Raw(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='upload_task' AND (
 (column_name='contract_version' AND data_type='smallint' AND is_nullable='NO' AND column_default='0') OR
 (column_name IN ('reserved_bytes','parent_id') AND data_type='bigint' AND column_type NOT LIKE '%unsigned%' AND is_nullable='NO' AND column_default='0') OR
 (column_name='expires_at' AND data_type='datetime' AND datetime_precision=6 AND is_nullable='YES'))`).Scan(&count).Error; err != nil {
		return err
	}
	if count != 4 {
		return errors.New("upload reservation schema missing or changed; review explicit migrate before starting")
	}
	if err := db.Raw(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='user_storage_ledger' AND (
 (column_name='user_id' AND data_type='bigint' AND column_key='PRI' AND is_nullable='NO' AND column_type NOT LIKE '%unsigned%') OR
 (column_name IN ('used_bytes','reserved_bytes','unmetered_refs') AND data_type='bigint' AND is_nullable='NO' AND column_default='0' AND column_type NOT LIKE '%unsigned%') OR
 (column_name='accounting_valid' AND data_type='tinyint' AND is_nullable='NO' AND column_default='0') OR
 (column_name='revision' AND data_type='bigint' AND column_type LIKE '%unsigned%' AND is_nullable='NO' AND column_default='0') OR
 (column_name='updated_at' AND data_type='datetime' AND datetime_precision=6 AND is_nullable='NO'))`).Scan(&count).Error; err != nil {
		return err
	}
	if count != 7 {
		return errors.New("storage quota ledger schema missing or changed; review explicit migrate before starting")
	}
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='storage_quota_policy' AND column_name='id' AND data_type='tinyint' AND column_key='PRI' AND is_nullable='NO'").Scan(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return errors.New("storage quota policy lock missing; review explicit migrate before starting")
	}
	if err := db.Raw("SELECT COUNT(*) FROM storage_quota_policy WHERE id=1").Scan(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return errors.New("storage quota policy singleton missing; explicit migrate is required")
	}
	for _, spec := range []struct {
		table, index string
		columns      []string
		unique       int
	}{
		{"storage_quota_policy", "PRIMARY", []string{"id"}, 0},
		{"user_storage_ledger", "PRIMARY", []string{"user_id"}, 0},
		{"upload_task", "idx_upload_task_expires_at", []string{"expires_at"}, 1},
		{"upload_task", "idx_upload_quota", []string{"user_id", "contract_version", "status", "expires_at"}, 1},
	} {
		var parts []fileNameIndexPart
		if err := db.Raw("SELECT column_name,non_unique,seq_in_index,sub_part,is_visible FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name=? AND index_name=? ORDER BY seq_in_index", spec.table, spec.index).Scan(&parts).Error; err != nil {
			return err
		}
		if len(parts) != len(spec.columns) {
			return errors.New("storage quota key/index missing or changed")
		}
		for i, p := range parts {
			if p.ColumnName != spec.columns[i] || p.SeqInIndex != i+1 || p.NonUnique != spec.unique || p.SubPart.Valid || p.IsVisible != "YES" {
				return errors.New("storage quota key/index missing or changed")
			}
		}
	}
	return nil
}
