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
func installMappingSchema(db *gorm.DB) error {
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
	return CheckMappingSchema(db)
}

// Read-only startup verification: index names alone do not prove uniqueness.
func CheckMappingSchema(db *gorm.DB) error {
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
