package store

import (
	"errors"
	"gorm.io/gorm"
)

// Explicit base migration adds these internal objects; normal startup only reads.
func CheckStorageLeaseSchema(db *gorm.DB) error {
	var count int64
	if err := db.Raw(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='storage' AND column_name='state' AND data_type='varchar' AND character_maximum_length=24 AND is_nullable='NO' AND column_default='active'`).Scan(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return errors.New("storage lifecycle state schema missing or changed; review and run explicit migrate before starting")
	}
	// Exact columns, indexes and RESTRICT parent retention are required: leases
	// without a parent constraint could be silently discarded during maintenance.
	if err := db.Raw(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='storage_read_lease' AND (
 (column_name='token' AND data_type='char' AND character_maximum_length=64 AND is_nullable='NO' AND column_key='PRI') OR
 (column_name='storage_id' AND data_type='bigint' AND column_type NOT LIKE '%unsigned%' AND is_nullable='NO') OR
 (column_name='expires_at' AND data_type='datetime' AND datetime_precision=6 AND is_nullable='NO'))`).Scan(&count).Error; err != nil {
		return err
	}
	if count != 3 {
		return errors.New("storage read lease schema missing or changed; review and run explicit migrate before starting")
	}
	var keys []storageForeignKey
	if err := db.Raw(`SELECT k.constraint_name,k.column_name,k.referenced_table_schema,k.referenced_table_name,k.referenced_column_name,k.ordinal_position,r.delete_rule,r.update_rule
 FROM information_schema.key_column_usage k JOIN information_schema.referential_constraints r ON r.constraint_schema=k.constraint_schema AND r.constraint_name=k.constraint_name AND r.table_name=k.table_name
 WHERE k.table_schema=DATABASE() AND k.table_name='storage_read_lease' ORDER BY k.constraint_name,k.ordinal_position`).Scan(&keys).Error; err != nil {
		return err
	}
	var database string
	if err := db.Raw("SELECT DATABASE()").Scan(&database).Error; err != nil {
		return err
	}
	if len(keys) != 1 || keys[0].ColumnName != "storage_id" || keys[0].ReferencedTableSchema != database || keys[0].ReferencedTableName != "storage" || keys[0].ReferencedColumnName != "id" || keys[0].DeleteRule != "RESTRICT" || keys[0].UpdateRule != "RESTRICT" || keys[0].OrdinalPosition != 1 {
		return errors.New("storage read lease RESTRICT foreign key missing or changed")
	}
	for _, spec := range []struct {
		index, column string
		unique        int
	}{
		{"PRIMARY", "token", 0}, {"idx_storage_read_lease_storage_id", "storage_id", 1}, {"idx_storage_read_lease_expires_at", "expires_at", 1},
	} {
		var parts []fileNameIndexPart
		if err := db.Raw("SELECT column_name,non_unique,seq_in_index,sub_part,is_visible FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='storage_read_lease' AND index_name=? ORDER BY seq_in_index", spec.index).Scan(&parts).Error; err != nil {
			return err
		}
		if len(parts) != 1 || parts[0].ColumnName != spec.column || parts[0].NonUnique != spec.unique || parts[0].SubPart.Valid || parts[0].IsVisible != "YES" {
			return errors.New("storage read lease index missing or changed")
		}
	}
	return nil
}
