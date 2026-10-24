package store

import (
	"errors"
	"gorm.io/gorm"
)

func CheckUploadChunkSchema(db *gorm.DB) error {
	var count int64
	if err := db.Raw(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND ((table_name='upload_task' AND column_name='chunk_protocol' AND data_type='smallint' AND is_nullable='NO' AND column_default='0') OR (table_name='upload_chunk' AND is_nullable='NO' AND column_default='' AND ((column_name='sha256' AND data_type='char' AND character_maximum_length=64) OR (column_name='md5' AND data_type='char' AND character_maximum_length=32))))`).Scan(&count).Error; err != nil {
		return err
	}
	if count != 3 {
		return errors.New("immutable chunk schema missing or changed; review explicit migrate before starting")
	}
	var parts []fileNameIndexPart
	if err := db.Raw("SELECT column_name,non_unique,seq_in_index,sub_part,is_visible FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='upload_chunk' AND index_name='idx_upload_chunk' ORDER BY seq_in_index").Scan(&parts).Error; err != nil {
		return err
	}
	if len(parts) != 2 {
		return errors.New("chunk identity unique index missing")
	}
	for i, p := range parts {
		if p.ColumnName != []string{"upload_id", "chunk_index"}[i] || p.NonUnique != 0 || p.SeqInIndex != i+1 || p.SubPart.Valid || p.IsVisible != "YES" {
			return errors.New("chunk identity unique index changed")
		}
	}
	return nil
}
