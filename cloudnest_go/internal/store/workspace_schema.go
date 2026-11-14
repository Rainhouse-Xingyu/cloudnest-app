package store

import (
	"errors"

	"gorm.io/gorm"
)

func CheckWorkspaceRootSchema(db *gorm.DB) error {
	var count int
	err := db.Raw(`SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema=DATABASE() AND table_name='workspace'
		AND column_name='root_file_id' AND column_type='bigint' AND is_nullable='YES'
		AND column_default IS NULL AND generation_expression=''`).Scan(&count).Error
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("workspace root schema missing or invalid; run explicit migrate")
	}
	err = db.Raw(`SELECT COUNT(*) FROM (
		SELECT index_name FROM information_schema.statistics
		WHERE table_schema=DATABASE() AND table_name='workspace' AND index_name='uq_workspace_root_v1'
		GROUP BY index_name HAVING COUNT(*)=1 AND MAX(non_unique)=0 AND MAX(sub_part IS NOT NULL)=0
		AND MIN(is_visible)='YES' AND MAX(column_name)='root_file_id'
	) verified`).Scan(&count).Error
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("workspace root unique index missing or invalid; run explicit migrate")
	}
	return nil
}
