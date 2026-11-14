package store

import (
	"fmt"

	"gorm.io/gorm"
)

// CheckShareSchema is read-only. Explicit migrate adds the defaults used by old links.
func CheckShareSchema(db *gorm.DB) error {
	var columns []string
	if err := db.Raw(`SELECT column_name FROM information_schema.columns
		WHERE table_schema=DATABASE() AND table_name='share' AND is_nullable='NO'
		  AND generation_expression='' AND (
		    (column_name='passcode_hash' AND data_type='varchar' AND character_maximum_length=60 AND column_default='')
		    OR (column_name IN ('max_downloads','download_count') AND column_type='bigint' AND column_default='0')
		  )`).Scan(&columns).Error; err != nil {
		return err
	}
	if len(columns) != 3 {
		return fmt.Errorf("share limits schema missing or invalid (verified columns: %v); back up and run explicit migrate", columns)
	}
	return nil
}
