package store

import (
	"errors"
	"gorm.io/gorm"
)

func CheckVersionUploadSchema(db *gorm.DB) error {
	var count int64
	if err := db.Raw(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='upload_task' AND (
 (column_name IN ('version_file_id','quota_user_id') AND data_type='bigint' AND column_type NOT LIKE '%unsigned%' AND is_nullable='YES') OR
 (column_name='version_base' AND data_type='bigint' AND column_type NOT LIKE '%unsigned%' AND is_nullable='NO' AND column_default='0') OR
 (column_name='version_comment' AND data_type='varchar' AND character_maximum_length=255 AND is_nullable='NO' AND column_default=''))`).Scan(&count).Error; err != nil {
		return err
	}
	if count != 4 {
		return errors.New("version upload binding schema missing or changed; review explicit migrate before starting")
	}
	for _, spec := range []struct {
		name    string
		columns []string
	}{
		{"idx_version_upload_quota", []string{"quota_user_id", "contract_version", "status", "expires_at"}},
		{"idx_version_upload_target", []string{"version_file_id", "status", "expires_at"}},
	} {
		var parts []fileNameIndexPart
		if err := db.Raw("SELECT column_name,non_unique,seq_in_index,sub_part,is_visible FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='upload_task' AND index_name=? ORDER BY seq_in_index", spec.name).Scan(&parts).Error; err != nil {
			return err
		}
		if len(parts) != len(spec.columns) {
			return errors.New("version upload binding index missing or changed")
		}
		for i, part := range parts {
			if part.ColumnName != spec.columns[i] || part.NonUnique != 1 || part.SeqInIndex != i+1 || part.SubPart.Valid || part.IsVisible != "YES" {
				return errors.New("version upload binding index missing or changed")
			}
		}
	}
	return nil
}
