package store

import (
	"errors"
	"fmt"

	"gorm.io/gorm"
)

func livePhotoUniqueIndexes(db *gorm.DB) (int, error) {
	var count int
	err := db.Raw(`SELECT COUNT(*) FROM (
		SELECT index_name FROM information_schema.statistics
		WHERE table_schema=DATABASE() AND table_name='live_photo'
		  AND index_name IN ('uq_live_photo_image_v1','uq_live_photo_video_v1')
		GROUP BY index_name
		HAVING COUNT(*)=1 AND MAX(non_unique)=0 AND MAX(sub_part IS NOT NULL)=0 AND MIN(is_visible)='YES'
		   AND ((index_name='uq_live_photo_image_v1' AND MAX(column_name)='image_file_id')
		     OR (index_name='uq_live_photo_video_v1' AND MAX(column_name)='video_file_id'))
	) verified`).Scan(&count).Error
	return count, err
}

// CheckLivePhotoSchema is read-only. Old schemas require the explicit migrate command.
func CheckLivePhotoSchema(db *gorm.DB) error {
	count, err := livePhotoUniqueIndexes(db)
	if err != nil {
		return err
	}
	if count != 2 {
		return errors.New("Live Photo unique indexes missing or invalid; run explicit migrate")
	}
	return nil
}

func migrateLivePhotoSchema(db *gorm.DB) error {
	count, err := livePhotoUniqueIndexes(db)
	if err != nil || count == 2 {
		return err
	}
	var existing int
	if err := db.Raw(`SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema=DATABASE()
		AND table_name='live_photo' AND index_name IN ('uq_live_photo_image_v1','uq_live_photo_video_v1')`).Scan(&existing).Error; err != nil {
		return err
	}
	if existing != 0 {
		return errors.New("Live Photo unique indexes are partially installed or invalid; inspect the schema")
	}
	// Preserve historical rows. A conflicting set requires an operator decision
	// before enabling the new invariant.
	var duplicate int64
	if err := db.Raw(`SELECT COUNT(*) FROM (
		SELECT file_id FROM (
			SELECT image_file_id AS file_id FROM live_photo
			UNION ALL SELECT video_file_id FROM live_photo
		) sources GROUP BY file_id HAVING COUNT(*)>1 LIMIT 1
	) conflicts`).Scan(&duplicate).Error; err != nil {
		return err
	}
	if duplicate != 0 {
		return errors.New("Live Photo migration found a source used by multiple roles or bindings; resolve conflicts before retrying")
	}
	if err := db.Exec(`ALTER TABLE live_photo
		ADD UNIQUE INDEX uq_live_photo_image_v1 (image_file_id),
		ADD UNIQUE INDEX uq_live_photo_video_v1 (video_file_id)`).Error; err != nil {
		return fmt.Errorf("install Live Photo unique indexes: %w", err)
	}
	return CheckLivePhotoSchema(db)
}
