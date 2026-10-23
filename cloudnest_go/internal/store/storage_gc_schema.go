package store

import (
	"errors"
	"gorm.io/gorm"
	"strings"
)

func CheckStorageGCSchema(db *gorm.DB) error {
	db = db.Session(&gorm.Session{NewDB: true})
	var count int64
	if err := db.Raw(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='storage' AND (
 (column_name='managed' AND data_type='tinyint' AND is_nullable='NO' AND column_default='0') OR
 (column_name IN ('managed_root','managed_path','gc_path') AND data_type='varchar' AND character_maximum_length=500 AND is_nullable='NO' AND column_default='') OR
 (column_name='gc_token' AND data_type='char' AND character_maximum_length=64 AND is_nullable='NO' AND column_default='') OR
 (column_name IN ('orphaned_at','quarantined_at') AND data_type='datetime' AND datetime_precision=6 AND is_nullable='YES'))`).Scan(&count).Error; err != nil {
		return err
	}
	if count != 7 {
		return errors.New("storage GC ownership/state schema missing or changed; review explicit migrate before starting")
	}
	if err := db.Raw(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='storage_garbage' AND (
 (column_name='token' AND data_type='char' AND character_maximum_length=64 AND is_nullable='NO') OR
 (column_name IN ('storage_id','file_size') AND data_type='bigint' AND column_type NOT LIKE '%unsigned%' AND is_nullable='NO') OR
 (column_name IN ('root','original_path','trash_path') AND data_type='varchar' AND character_maximum_length=500 AND is_nullable='NO') OR
 (column_name='file_hash' AND data_type='char' AND character_maximum_length=64 AND is_nullable='NO') OR
 (column_name='status' AND data_type='varchar' AND character_maximum_length=24 AND is_nullable='NO') OR
 (column_name='created_at' AND data_type='datetime' AND datetime_precision=6 AND is_nullable='NO') OR
 (column_name='completed_at' AND data_type='datetime' AND datetime_precision=6 AND is_nullable='YES'))`).Scan(&count).Error; err != nil {
		return err
	}
	if count != 10 {
		return errors.New("storage garbage journal schema missing or changed; review explicit migrate before starting")
	}
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.key_column_usage WHERE table_schema=DATABASE() AND table_name='storage_garbage' AND referenced_table_name IS NOT NULL").Scan(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return errors.New("storage garbage journal must survive parent removal without foreign keys")
	}
	for _, spec := range []struct {
		table, index string
		columns      []string
		unique       int
	}{
		{"storage", "idx_storage_gc", []string{"managed", "state"}, 1},
		{"storage_garbage", "PRIMARY", []string{"token"}, 0},
		{"storage_garbage", "idx_storage_garbage_storage_id", []string{"storage_id"}, 1},
		{"storage_garbage", "idx_storage_garbage_status", []string{"status"}, 1},
	} {
		var parts []fileNameIndexPart
		if err := db.Raw("SELECT column_name,non_unique,seq_in_index,sub_part,is_visible FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name=? AND index_name=? ORDER BY seq_in_index", spec.table, spec.index).Scan(&parts).Error; err != nil {
			return err
		}
		if len(parts) != len(spec.columns) {
			return errors.New("storage GC index missing or changed")
		}
		for i, p := range parts {
			if p.ColumnName != spec.columns[i] || p.NonUnique != spec.unique || p.SeqInIndex != i+1 || p.SubPart.Valid || p.IsVisible != "YES" {
				return errors.New("storage GC index missing or changed")
			}
		}
	}
	return nil
}

// CheckStorageGCReferenceGraph additionally rejects unknown incoming edges,
// especially CASCADE relationships that could silently erase another product's
// references. Ordinary startup validates the known graph; destructive GC needs
// this complete foreign-key inventory as well.
func CheckStorageGCReferenceGraph(db *gorm.DB) error {
	db = db.Session(&gorm.Session{NewDB: true})
	if err := checkGCMetadataVisibility(db); err != nil {
		return err
	}
	var columns []string
	if err := db.Raw("SELECT table_name FROM information_schema.columns WHERE table_schema=DATABASE() AND column_name='storage_id' ORDER BY table_name").Scan(&columns).Error; err != nil {
		return err
	}
	expectedColumns := []string{"file", "file_version", "storage_garbage", "storage_read_lease"}
	if len(columns) != len(expectedColumns) {
		return errors.New("storage GC refuses unknown or missing storage_id reference columns")
	}
	for i, table := range columns {
		if table != expectedColumns[i] {
			return errors.New("storage GC refuses an unknown storage_id reference table")
		}
	}
	var triggers int64
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.triggers WHERE trigger_schema=DATABASE() AND event_object_table IN ('storage','file','file_version','storage_read_lease','storage_garbage','operation_log')").Scan(&triggers).Error; err != nil {
		return err
	}
	if triggers != 0 {
		return errors.New("storage GC refuses unreviewed triggers on object, reference, journal or audit tables")
	}

	var edges []struct {
		TableName, ColumnName, DeleteRule, UpdateRule string
		OrdinalPosition                               int
	}
	if err := db.Raw(`SELECT k.table_name,k.column_name,k.ordinal_position,r.delete_rule,r.update_rule FROM information_schema.key_column_usage k
 JOIN information_schema.referential_constraints r ON r.constraint_schema=k.constraint_schema AND r.constraint_name=k.constraint_name AND r.table_name=k.table_name
 WHERE k.referenced_table_schema=DATABASE() AND k.referenced_table_name='storage' ORDER BY k.table_name,k.constraint_name,k.ordinal_position`).Scan(&edges).Error; err != nil {
		return err
	}
	if len(edges) != 3 {
		return errors.New("storage GC refuses an unknown or incomplete incoming foreign-key graph")
	}
	expected := map[string]bool{"file": true, "file_version": true, "storage_read_lease": true}
	for _, edge := range edges {
		if !expected[edge.TableName] || edge.ColumnName != "storage_id" || edge.OrdinalPosition != 1 || edge.DeleteRule != "RESTRICT" || edge.UpdateRule != "RESTRICT" {
			return errors.New("storage GC refuses an unknown or unsafe reference edge")
		}
		delete(expected, edge.TableName)
	}
	return nil
}

// Metadata queries can silently hide objects from underprivileged accounts.
// Require direct whole-schema visibility plus the global InnoDB FK inventory;
// do not interpret an incomplete INFORMATION_SCHEMA view as an empty graph.
func checkSchemaVisibility(db *gorm.DB) (string, error) {
	var identity, database string
	if err := db.Raw("SELECT CURRENT_USER()").Scan(&identity).Error; err != nil {
		return "", err
	}
	if err := db.Raw("SELECT DATABASE()").Scan(&database).Error; err != nil {
		return "", err
	}
	if !safeSQLIdentifier.MatchString(database) {
		return "", errors.New("GC requires a simple ASCII database identifier for InnoDB inventory matching")
	}
	at := strings.LastIndexByte(identity, '@')
	if at <= 0 {
		return "", errors.New("GC cannot determine metadata visibility")
	}
	user, host := identity[:at], identity[at+1:]
	var privileges []string
	if err := db.Raw(`SELECT privilege_type FROM information_schema.schema_privileges WHERE CAST(DATABASE() AS BINARY) LIKE CAST(table_schema AS BINARY) ESCAPE ? AND grantee=CONCAT(QUOTE(?),'@',QUOTE(?))
 UNION SELECT privilege_type FROM information_schema.user_privileges WHERE grantee=CONCAT(QUOTE(?),'@',QUOTE(?))`, "\\", user, host, user, host).Scan(&privileges).Error; err != nil {
		return "", err
	}
	visible := map[string]bool{}
	for _, p := range privileges {
		visible[p] = true
	}
	if !visible["SELECT"] || !visible["TRIGGER"] {
		return "", errors.New("GC requires directly granted whole-schema SELECT/TRIGGER metadata visibility; use a dedicated maintenance account")
	}
	return database, nil
}

func checkGCMetadataVisibility(db *gorm.DB) error {
	database, err := checkSchemaVisibility(db)
	if err != nil {
		return err
	}
	var edges []struct {
		ForName     string
		NCols, Type int
	}
	if err := db.Raw("SELECT for_name,n_cols,type FROM information_schema.innodb_foreign WHERE ref_name=? ORDER BY for_name", database+"/storage").Scan(&edges).Error; err != nil {
		return errors.New("GC requires PROCESS access to the global InnoDB foreign-key inventory")
	}
	expected := map[string]bool{database + "/file": true, database + "/file_version": true, database + "/storage_read_lease": true}
	if len(edges) != 3 {
		return errors.New("GC refuses unknown or cross-database InnoDB storage references")
	}
	for _, edge := range edges {
		if !expected[edge.ForName] || edge.NCols != 1 || edge.Type != 0 {
			return errors.New("GC refuses unknown or cascading InnoDB storage references")
		}
		delete(expected, edge.ForName)
	}
	return nil
}
