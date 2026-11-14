package store

import (
	"fmt"
	"strings"

	"cloudenest_go/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Hex literals avoid connection-dependent character-set introducers in MySQL's
// persisted expression. HTTP aliases share a key; HTTPS has its own listener.
const mappingDomainExpression = "nullif(concat(if(left(lower(trim(domain)),8)=0x68747470733a2f2f,0x68747470733a2f2f,0x687474703a2f2f),trim(trailing 0x2e from case when left(lower(trim(domain)),8)=0x68747470733a2f2f then substr(lower(trim(domain)),9) when left(lower(trim(domain)),7)=0x687474703a2f2f then substr(lower(trim(domain)),8) else lower(trim(domain)) end)),0x687474703a2f2f)"
const mappingDomainColumn = "cn_domain_key_v1"
const mappingDomainIndex = "uq_frp_domain_v1"

// Explicit migration only. ALTER is atomic, keeps every historical mapping,
// and fails on canonical duplicates rather than choosing an arbitrary winner.
func prepareMappingSchema(db *gorm.DB) error {
	if !db.Migrator().HasTable(&model.FrpMapping{}) {
		if err := db.Migrator().CreateTable(&model.FrpMapping{}); err != nil {
			return err
		}
		if err := db.Exec("ALTER TABLE frp_mapping ADD UNIQUE INDEX uni_frp_mapping_remote_port (remote_port)").Error; err != nil {
			return err
		}
	}
	var scope int
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='frp_mapping' AND column_name='server_id'").Scan(&scope).Error; err != nil {
		return err
	}
	if scope == 0 {
		if err := db.Exec("ALTER TABLE frp_mapping ADD COLUMN server_id BIGINT NOT NULL DEFAULT 0, ADD INDEX idx_frp_mapping_server_id(server_id)").Error; err != nil {
			return err
		}
	}

	var count int
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='frp_mapping' AND column_name=?", mappingDomainColumn).Scan(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		if err := db.Exec("ALTER TABLE frp_mapping ADD COLUMN " + mappingDomainColumn + " VARCHAR(108) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin GENERATED ALWAYS AS (" + mappingDomainExpression + ") STORED, ADD UNIQUE INDEX " + mappingDomainIndex + " (" + mappingDomainColumn + ")").Error; err != nil {
			return fmt.Errorf("FRP domain constraint migration failed; inspect canonical duplicate domains, preserve historical rows: %w", err)
		}
	}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.FrpMappingPool{ID: 1}).Error; err != nil {
		return err
	}
	var scoped int
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='frp_mapping' AND column_name=?", mappingPortColumn).Scan(&scoped).Error; err != nil {
		return err
	}
	if scoped > 0 {
		return CheckMappingSchema(db)
	}
	return checkLegacyMappingSchema(db)
}

// Read-only startup verification: index names alone do not prove uniqueness.
func checkLegacyMappingSchema(db *gorm.DB) error {
	var columns []fileNameColumn
	if err := db.Raw("SELECT column_name,data_type,column_type,is_nullable,character_maximum_length,character_set_name,collation_name,generation_expression,extra FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='frp_mapping' AND column_name=?", mappingDomainColumn).Scan(&columns).Error; err != nil {
		return err
	}
	normalize := func(s string) string {
		return normalizedSQLExpression(strings.ReplaceAll(strings.ToLower(s), "lcase(", "lower("))
	}
	if len(columns) != 1 || columns[0].DataType != "varchar" || columns[0].MaxLength.Int64 != 108 || columns[0].Collation != "utf8mb4_bin" || columns[0].Extra != "STORED GENERATED" || normalize(columns[0].Expression) != normalize(mappingDomainExpression) {
		return fmt.Errorf("FRP domain constraint missing or changed; inspect and run explicit migrate")
	}
	for _, column := range []string{mappingDomainColumn, "remote_port"} {
		var count int
		if err := db.Raw("SELECT COUNT(*) FROM (SELECT index_name FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='frp_mapping' GROUP BY index_name HAVING COUNT(*)=1 AND MAX(non_unique)=0 AND MAX(sub_part IS NOT NULL)=0 AND MAX(column_name)=? AND MIN(is_visible)='YES') verified", column).Scan(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("FRP unique index missing or changed: %s", column)
		}
	}
	var engines, primary int
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name IN ('frp_mapping','frp_mapping_pool') AND engine='InnoDB'").Scan(&engines).Error; err != nil {
		return err
	}
	if err := db.Raw("SELECT COUNT(*) FROM (SELECT index_name FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='frp_mapping_pool' AND index_name='PRIMARY' GROUP BY index_name HAVING COUNT(*)=1 AND MAX(non_unique)=0 AND MAX(column_name)='id' AND MAX(sub_part IS NOT NULL)=0) verified").Scan(&primary).Error; err != nil {
		return err
	}
	if engines != 2 || primary != 1 {
		return fmt.Errorf("FRP mapping tables require InnoDB and an exact pool primary key")
	}
	var pool model.FrpMappingPool
	if err := db.First(&pool, "id=1").Error; err != nil {
		return fmt.Errorf("FRP mapping pool missing; run explicit migrate: %w", err)
	}
	return nil
}

const mappingPortColumn = "cn_port_key_v2"
const mappingPortExpression = "if(server_id=0 or cn_domain_key_v1 is null,remote_port,NULL)"

// Resource history is installed before removing the legacy global indexes.
// This single ALTER preserves all rows and keeps namespace zero global in app
// transactions; direct SQL must not bypass the shared allocation protocol.
func upgradeScopedMappingSchema(db *gorm.DB) error {
	if err := CheckReservationSchema(db); err != nil {
		return err
	}
	var present int
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='frp_mapping' AND column_name=?", mappingPortColumn).Scan(&present).Error; err != nil {
		return err
	}
	if present > 0 {
		return CheckMappingSchema(db)
	}
	if err := checkLegacyMappingSchema(db); err != nil {
		return err
	}
	var nonlegacy int64
	if err := db.Model(&model.FrpMapping{}).Where("server_id<>0").Count(&nonlegacy).Error; err != nil {
		return err
	}
	if nonlegacy != 0 {
		return fmt.Errorf("unrecognized historical mapping server associations; inspect before migration")
	}
	var portIndex string
	if err := db.Raw("SELECT index_name FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='frp_mapping' GROUP BY index_name HAVING COUNT(*)=1 AND MAX(non_unique)=0 AND MAX(sub_part IS NOT NULL)=0 AND MAX(column_name)='remote_port'").Scan(&portIndex).Error; err != nil {
		return err
	}
	quote := func(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }
	sql := "ALTER TABLE frp_mapping ADD COLUMN " + mappingPortColumn + " BIGINT GENERATED ALWAYS AS (" + mappingPortExpression + ") STORED, ADD UNIQUE INDEX uq_frp_port_v2(server_id," + mappingPortColumn + "), ADD UNIQUE INDEX uq_frp_domain_v2(server_id," + mappingDomainColumn + "), DROP INDEX " + quote(portIndex) + ", DROP INDEX " + quote(mappingDomainIndex)
	if err := db.Exec(sql).Error; err != nil {
		return fmt.Errorf("FRP scoped mapping migration failed without deleting historical rows: %w", err)
	}
	return CheckMappingSchema(db)
}

func CheckMappingSchema(db *gorm.DB) error {
	var scope int
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='frp_mapping' AND column_name='server_id' AND data_type='bigint' AND column_type NOT LIKE '%unsigned%' AND is_nullable='NO' AND column_default='0' AND extra='' AND generation_expression=''").Scan(&scope).Error; err != nil {
		return err
	}
	if scope != 1 {
		return fmt.Errorf("FRP mapping server scope missing or changed; run explicit migrate")
	}
	var columns []fileNameColumn
	if err := db.Raw("SELECT column_name,data_type,column_type,is_nullable,character_maximum_length,character_set_name,collation_name,generation_expression,extra FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='frp_mapping' AND column_name IN (?,?)", mappingDomainColumn, mappingPortColumn).Scan(&columns).Error; err != nil {
		return err
	}
	normalize := func(s string) string {
		return normalizedSQLExpression(strings.ReplaceAll(strings.ToLower(s), "lcase(", "lower("))
	}
	if len(columns) != 2 {
		return fmt.Errorf("FRP scoped mapping columns missing; run explicit migrate")
	}
	for _, column := range columns {
		switch column.Name {
		case mappingDomainColumn:
			if column.DataType != "varchar" || column.MaxLength.Int64 != 108 || column.Collation != "utf8mb4_bin" || column.Extra != "STORED GENERATED" || normalize(column.Expression) != normalize(mappingDomainExpression) {
				return fmt.Errorf("FRP domain constraint missing or changed")
			}
		case mappingPortColumn:
			if column.DataType != "bigint" || column.Extra != "STORED GENERATED" || normalize(column.Expression) != normalize(mappingPortExpression) {
				return fmt.Errorf("FRP port scope constraint missing or changed")
			}
		}
	}
	for _, index := range []struct{ name, columns string }{{"uq_frp_port_v2", "server_id," + mappingPortColumn}, {"uq_frp_domain_v2", "server_id," + mappingDomainColumn}} {
		var count int
		if err := db.Raw("SELECT COUNT(*) FROM (SELECT index_name FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='frp_mapping' AND index_name=? GROUP BY index_name HAVING MAX(non_unique)=0 AND MAX(sub_part IS NOT NULL)=0 AND MIN(is_visible)='YES' AND GROUP_CONCAT(column_name ORDER BY seq_in_index SEPARATOR ',')=?) verified", index.name, index.columns).Scan(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("FRP scoped unique index invalid: %s", index.name)
		}
	}
	var legacy int
	if err := db.Raw("SELECT COUNT(*) FROM (SELECT index_name FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='frp_mapping' GROUP BY index_name HAVING COUNT(*)=1 AND MAX(non_unique)=0 AND MAX(column_name) IN ('remote_port',?)) verified", mappingDomainColumn).Scan(&legacy).Error; err != nil {
		return err
	}
	if legacy != 0 {
		return fmt.Errorf("legacy global FRP indexes remain after scope migration")
	}
	var engines, primary int
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name IN ('frp_mapping','frp_mapping_pool') AND engine='InnoDB'").Scan(&engines).Error; err != nil {
		return err
	}
	if err := db.Raw("SELECT COUNT(*) FROM (SELECT index_name FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='frp_mapping_pool' AND index_name='PRIMARY' GROUP BY index_name HAVING COUNT(*)=1 AND MAX(non_unique)=0 AND MAX(column_name)='id' AND MAX(sub_part IS NOT NULL)=0) verified").Scan(&primary).Error; err != nil {
		return err
	}
	if engines != 2 || primary != 1 {
		return fmt.Errorf("FRP mapping gate requires InnoDB and exact primary key")
	}
	var pool model.FrpMappingPool
	if err := db.First(&pool, "id=1").Error; err != nil {
		return fmt.Errorf("FRP mapping gate missing: %w", err)
	}
	return nil
}
