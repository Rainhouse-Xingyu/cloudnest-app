package store

import (
	"cloudenest_go/model"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func installReservationSchema(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var pool model.FrpMappingPool
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&pool, "id=1").Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&model.FrpReservationSchema{}).Where("id=1").Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return CheckReservationSchema(tx)
		}
		// No plaintext or guessed server/credential associations. Existing history
		// remains opaque until an explicit operator reconciliation is performed.
		if err := tx.Exec("INSERT INTO frp_reservation (owner_id,device_id,credential_id,reference,config_id,server_id,kind,resource) SELECT COALESCE(d.user_id,0),COALESCE(c.device_id,0),'',CONCAT('legacy-config:',c.id),c.id,0,'unknown','' FROM frp_config c LEFT JOIN device d ON d.id=c.device_id").Error; err != nil {
			return err
		}
		if err := tx.Create(&model.FrpReservationSchema{ID: 1, Version: 1}).Error; err != nil {
			return err
		}
		return CheckReservationSchema(tx)
	})
}
func CheckReservationSchema(db *gorm.DB) error {
	var schema model.FrpReservationSchema
	if err := db.First(&schema, "id=1").Error; err != nil {
		return fmt.Errorf("FRP resource history migration missing; run explicit migrate: %w", err)
	}
	if schema.Version != 1 {
		return fmt.Errorf("FRP resource history version unsupported")
	}
	for _, spec := range []struct {
		table, index, columns string
		unique                int
	}{
		{"frp_reservation", "PRIMARY", "id", 0},
		{"frp_reservation_schema", "PRIMARY", "id", 0},
		{"frp_reservation", "uq_frp_reservation_reference", "reference,kind,resource", 0},
		{"frp_reservation", "ix_frp_reservation_generation", "device_id,credential_id", 1},
		{"frp_reservation", "ix_frp_reservation_resource", "server_id,kind,resource", 1},
	} {
		var count int
		if err := db.Raw("SELECT COUNT(*) FROM (SELECT index_name FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name=? AND index_name=? GROUP BY index_name HAVING MAX(non_unique)=? AND MAX(sub_part IS NOT NULL)=0 AND MIN(is_visible)='YES' AND GROUP_CONCAT(column_name ORDER BY seq_in_index SEPARATOR ',')=?) verified", spec.table, spec.index, spec.unique, spec.columns).Scan(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("FRP resource index invalid: %s.%s", spec.table, spec.index)
		}
	}
	for _, spec := range []struct{ table, column, condition string }{
		{"frp_reservation", "id", "data_type='bigint' AND extra='auto_increment'"},
		{"frp_reservation", "owner_id", "data_type='bigint' AND extra=''"},
		{"frp_reservation", "device_id", "data_type='bigint' AND extra=''"},
		{"frp_reservation", "config_id", "data_type='bigint' AND extra=''"},
		{"frp_reservation", "server_id", "data_type='bigint' AND extra=''"},
		{"frp_reservation", "credential_id", "data_type='varchar' AND character_maximum_length=32 AND extra=''"},
		{"frp_reservation", "reference", "data_type='varchar' AND character_maximum_length=64 AND extra=''"},
		{"frp_reservation", "kind", "data_type='varchar' AND character_maximum_length=8 AND extra=''"},
		{"frp_reservation", "resource", "data_type='varchar' AND character_maximum_length=253 AND extra=''"},
		{"frp_reservation_schema", "id", "data_type='bigint' AND extra=''"},
		{"frp_reservation_schema", "version", "data_type='bigint' AND extra=''"},
		{"frp_mapping", "server_id", "data_type='bigint' AND column_default='0' AND extra=''"},
	} {
		var count int
		if err := db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? AND column_name=? AND is_nullable='NO' AND generation_expression='' AND ("+spec.condition+")", spec.table, spec.column).Scan(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("FRP resource column invalid: %s.%s", spec.table, spec.column)
		}
	}
	var reconciled int
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='frp_reservation' AND column_name='reconciled_at' AND data_type='datetime' AND datetime_precision=6 AND is_nullable='YES' AND column_default IS NULL AND extra='' AND generation_expression=''").Scan(&reconciled).Error; err != nil {
		return err
	}
	if reconciled != 1 {
		return fmt.Errorf("FRP reservation reconciliation column invalid; run explicit migrate")
	}
	var engines int
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name IN ('frp_reservation','frp_reservation_schema') AND engine='InnoDB'").Scan(&engines).Error; err != nil {
		return err
	}
	if engines != 2 {
		return fmt.Errorf("FRP resource tables require InnoDB")
	}
	return nil
}
