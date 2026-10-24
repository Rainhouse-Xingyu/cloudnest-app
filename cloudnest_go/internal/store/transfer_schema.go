package store

import (
	"errors"
	"gorm.io/gorm"
)

func CheckTransferSchema(db *gorm.DB) error {
	var count int64
	if err := db.Raw(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND ((table_name='upload_task' AND ((column_name='transfer_epoch' AND data_type='bigint' AND column_type='bigint' AND is_nullable='NO' AND column_default='0') OR (column_name='transfer_protocol' AND data_type='smallint' AND is_nullable='NO' AND column_default='0') OR (column_name IN ('transfer_volume','merge_artifact') AND data_type='char' AND character_maximum_length=64 AND is_nullable='NO' AND column_default=''))) OR (table_name='upload_chunk' AND column_name='artifact_token' AND data_type='char' AND character_maximum_length=64 AND is_nullable='NO' AND column_default=''))`).Scan(&count).Error; err != nil {
		return err
	}
	if count != 5 {
		return errors.New("transfer registration columns missing or changed; review explicit migrate before starting")
	}
	for table, columns := range map[string]map[string]string{
		"transfer_volume":   {"id": "char", "root_path": "varchar", "limit_bytes": "bigint", "min_free_bytes": "bigint"},
		"transfer_artifact": {"cache_key": "char", "transfer_epoch": "bigint", "token": "char", "volume_id": "char", "relative_path": "varchar", "user_id": "bigint", "task_id": "bigint", "kind": "varchar", "state": "varchar", "bytes": "bigint", "size": "bigint", "sha256": "char", "device": "bigint", "inode": "bigint", "expires_at": "datetime", "created_at": "datetime"},
	} {
		var rows []struct {
			ColumnName, DataType, IsNullable, ColumnType string
			ColumnDefault                                *string
			DatetimePrecision                            *int64
			CharacterMaximumLength                       *int64
		}
		if err := db.Raw("SELECT column_name,data_type,is_nullable,column_type,column_default,datetime_precision,character_maximum_length FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=?", table).Scan(&rows).Error; err != nil {
			return err
		}
		if len(rows) != len(columns) {
			return errors.New("transfer registry schema missing or changed")
		}
		for _, row := range rows {
			nullable := "NO"
			if table == "transfer_artifact" && row.ColumnName == "cache_key" {
				nullable = "YES"
				if row.ColumnDefault != nil || row.CharacterMaximumLength == nil || *row.CharacterMaximumLength != 64 {
					return errors.New("thumbnail cache key schema changed")
				}
			}
			if columns[row.ColumnName] != row.DataType || row.IsNullable != nullable {
				return errors.New("transfer registry column changed")
			}
			if row.ColumnName == "transfer_epoch" && (row.ColumnType != "bigint" || row.ColumnDefault == nil || *row.ColumnDefault != "0") {
				return errors.New("transfer epoch schema changed")
			}
			if row.DataType == "datetime" && (row.DatetimePrecision == nil || *row.DatetimePrecision != 6) {
				return errors.New("transfer registry timestamp changed")
			}
		}
	}
	for _, spec := range []struct {
		table, index string
		names        []string
		nonUnique    int
	}{
		{"transfer_volume", "PRIMARY", []string{"id"}, 0}, {"transfer_artifact", "PRIMARY", []string{"token"}, 0},
		{"transfer_artifact", "idx_transfer_path", []string{"volume_id", "relative_path"}, 0},
		{"transfer_artifact", "idx_transfer_cache", []string{"volume_id", "cache_key"}, 0},
		{"transfer_artifact", "idx_transfer_reconcile", []string{"volume_id", "state", "expires_at"}, 1},
	} {
		var parts []fileNameIndexPart
		if err := db.Raw("SELECT column_name,non_unique,seq_in_index,sub_part,is_visible FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name=? AND index_name=? ORDER BY seq_in_index", spec.table, spec.index).Scan(&parts).Error; err != nil {
			return err
		}
		if len(parts) != len(spec.names) {
			return errors.New("transfer registry index missing")
		}
		for i, p := range parts {
			if p.ColumnName != spec.names[i] || p.NonUnique != spec.nonUnique || p.SeqInIndex != i+1 || p.SubPart.Valid || p.IsVisible != "YES" {
				return errors.New("transfer registry index changed")
			}
		}
	}
	return nil
}

// A restricted metadata view cannot prove absence. Runtime handlers may leave
// discarded reservations for a separate maintenance connection to reclaim.
func CheckTransferCleanupContract(db *gorm.DB) error {
	if _, err := checkSchemaVisibility(db); err != nil {
		return err
	}
	var count int64
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.triggers WHERE trigger_schema=DATABASE()").Scan(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return errors.New("transfer cleanup refuses schemas with triggers; stop writers and review metadata consumers")
	}
	return nil
}
