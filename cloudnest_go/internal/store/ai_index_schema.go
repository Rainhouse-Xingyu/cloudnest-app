package store

import (
	"errors"
	"gorm.io/gorm"
)

// CheckAIIndexSchema never creates or repairs tables at normal startup.
func CheckAIIndexSchema(db *gorm.DB) error {
	var count int64
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='ai_index_job' AND engine='InnoDB'").Scan(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return errors.New("AI index jobs require an InnoDB table; review explicit migrate before starting")
	}
	if err := db.Raw(`SELECT COUNT(*) FROM information_schema.columns
WHERE table_schema=DATABASE() AND table_name='ai_index_job' AND (
 (column_name IN ('file_id','generation') AND column_type='bigint' AND is_nullable='NO') OR
 (column_name IN ('source_hash','lease_token') AND data_type='char' AND character_maximum_length=64 AND is_nullable='NO') OR
 (column_name IN ('pipeline','error_code') AND data_type='varchar' AND character_maximum_length=64 AND is_nullable='NO') OR
 (column_name='status' AND data_type='varchar' AND character_maximum_length=20 AND is_nullable='NO') OR
 (column_name='attempts' AND column_type='bigint' AND is_nullable='NO') OR
 (column_name IN ('available_at','updated_at') AND data_type='datetime' AND datetime_precision=6 AND is_nullable='NO') OR
 (column_name='lease_until' AND data_type='datetime' AND datetime_precision=6 AND is_nullable='YES'))`).Scan(&count).Error; err != nil {
		return err
	}
	if count != 11 {
		return errors.New("AI index job schema missing or changed; back up and review explicit migrate before starting")
	}
	if err := db.Raw(`SELECT COUNT(*) FROM (
 SELECT index_name FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='ai_index_job'
 GROUP BY index_name HAVING
 (index_name='PRIMARY' AND MAX(non_unique)=0 AND GROUP_CONCAT(column_name ORDER BY seq_in_index)='file_id') OR
 (index_name='idx_ai_index_ready' AND GROUP_CONCAT(column_name ORDER BY seq_in_index)='pipeline,status,available_at') OR
 (index_name='idx_ai_index_expiry' AND GROUP_CONCAT(column_name ORDER BY seq_in_index)='lease_until')
) indexes_checked`).Scan(&count).Error; err != nil {
		return err
	}
	if count != 3 {
		return errors.New("AI index job indexes missing or changed; review explicit migrate before starting")
	}
	return nil
}
