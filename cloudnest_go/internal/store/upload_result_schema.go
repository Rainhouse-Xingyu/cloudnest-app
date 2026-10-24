package store

import (
	"errors"

	"gorm.io/gorm"
)

func CheckUploadResultSchema(db *gorm.DB) error {
	var count int64
	if err := db.Raw(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='upload_task' AND (
 (column_name='result_version' AND data_type='smallint' AND is_nullable='NO' AND column_default='0') OR
 (column_name='result_file_id' AND data_type='bigint' AND column_type NOT LIKE '%unsigned%' AND is_nullable='YES') OR
 (column_name='result_json' AND data_type='text' AND is_nullable='YES'))`).Scan(&count).Error; err != nil {
		return err
	}
	if count != 3 {
		return errors.New("upload completion receipt schema missing or changed; review explicit migrate before starting")
	}
	return nil
}
