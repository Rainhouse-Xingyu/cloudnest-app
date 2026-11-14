package store

import (
	"fmt"
	"gorm.io/gorm"
)

// CheckServerSchema is read-only. Legacy configs retain zero associations;
// explicit migration never infers an owner/server from historical plaintext.
func CheckServerSchema(db *gorm.DB) error {
	var engines int
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name IN ('frp_server','frp_server_revision','frp_config') AND engine='InnoDB'").Scan(&engines).Error; err != nil {
		return err
	}
	if engines != 3 {
		return fmt.Errorf("FRP server tables require InnoDB; run explicit migrate")
	}
	for _, table := range []string{"frp_server", "frp_server_revision"} {
		var count int
		if err := db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? AND column_name='id' AND data_type='bigint' AND is_nullable='NO' AND extra='auto_increment'", table).Scan(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("FRP server identity schema invalid: %s", table)
		}
	}
	for _, spec := range []struct{ table, column, condition string }{
		{"frp_server", "user_id", "data_type='bigint'"},
		{"frp_server", "address", "data_type='varchar' AND character_maximum_length=253"},
		{"frp_server", "port", "data_type='bigint'"},
		{"frp_server", "revision_id", "data_type='bigint' AND column_default='0'"},
		{"frp_server", "enabled", "data_type='tinyint'"},
		{"frp_server", "created_at", "data_type='datetime' AND datetime_precision=6"},
		{"frp_server_revision", "server_id", "data_type='bigint'"},
		{"frp_server_revision", "data", "data_type='text'"},
		{"frp_server_revision", "created_at", "data_type='datetime' AND datetime_precision=6"},
		{"frp_config", "server_id", "data_type='bigint' AND column_default='0'"},
		{"frp_config", "server_revision_id", "data_type='bigint' AND column_default='0'"},
	} {
		var count int
		if err := db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? AND column_name=? AND is_nullable='NO' AND extra='' AND generation_expression='' AND ("+spec.condition+")", spec.table, spec.column).Scan(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("FRP server column invalid: %s.%s; run explicit migrate", spec.table, spec.column)
		}
	}
	for _, spec := range []struct{ table, index, columns string }{
		{"frp_server", "PRIMARY", "id"}, {"frp_server_revision", "PRIMARY", "id"}, {"frp_server", "uq_frp_server_endpoint", "address,port"},
	} {
		var count int
		if err := db.Raw("SELECT COUNT(*) FROM (SELECT index_name FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name=? AND index_name=? GROUP BY index_name HAVING MAX(non_unique)=0 AND MAX(sub_part IS NOT NULL)=0 AND MIN(is_visible)='YES' AND GROUP_CONCAT(column_name ORDER BY seq_in_index SEPARATOR ',')=?) verified", spec.table, spec.index, spec.columns).Scan(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("FRP server index invalid: %s.%s", spec.table, spec.index)
		}
	}
	return nil
}
