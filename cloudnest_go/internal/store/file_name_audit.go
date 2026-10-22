package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"gorm.io/gorm"
)

// ErrFileNameAuditFindings distinguishes a completed audit with findings from a
// query failure. Neither result authorizes or performs a repair.
var ErrFileNameAuditFindings = errors.New("file-name audit found conflicts or invalid scalar metadata; no data was changed")

const fileNameAuditSampleLimit = 100

type FileNameConflict struct {
	ParentID    int64  `json:"parent_id"`
	RootOwnerID int64  `json:"root_owner_id"`
	ExampleName string `json:"example_name"`
	EntryCount  int64  `json:"entry_count"`
	FirstFileID int64  `json:"first_file_id"`
	LastFileID  int64  `json:"last_file_id"`
}

type FileNameAudit struct {
	GeneratedAt       time.Time          `json:"generated_at"`
	FilenameCollation string             `json:"filename_collation"`
	TotalFiles        int64              `json:"total_files"`
	ActiveFiles       int64              `json:"active_files"`
	ConflictGroups    int64              `json:"conflict_groups"`
	ConflictingFiles  int64              `json:"conflicting_files"`
	InvalidScalarRows int64              `json:"invalid_scalar_rows"`
	Conflicts         []FileNameConflict `json:"conflicts"`
	SampleLimit       int                `json:"sample_limit"`
	Truncated         bool               `json:"truncated"`
}

func (r FileNameAudit) HasFindings() bool { return r.ConflictGroups > 0 || r.InvalidScalarRows > 0 }

// AuditFileNames never migrates, locks rows for writes, touches disk, or changes
// metadata. SQL GROUP BY deliberately uses filename's actual database collation,
// matching the application's name lookup rather than Go string equality.
// It is a namespace preflight, not a general integrity/ACL/storage audit.
func AuditFileNames(ctx context.Context, db *gorm.DB) (FileNameAudit, error) {
	report := FileNameAudit{GeneratedAt: time.Now().UTC(), SampleLimit: fileNameAuditSampleLimit, Conflicts: []FileNameConflict{}}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Raw("SELECT collation_name FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'file' AND column_name = 'filename'").Scan(&report.FilenameCollation).Error; err != nil {
			return err
		}
		if report.FilenameCollation == "" {
			return errors.New("file filename schema is missing or not a collated text column")
		}
		if err := tx.Table("file").Count(&report.TotalFiles).Error; err != nil {
			return err
		}
		if err := tx.Table("file").Where("is_deleted = 0").Count(&report.ActiveFiles).Error; err != nil {
			return err
		}
		// NULL scalar values and invalid enum/ID values must not become an accidental
		// escape hatch in a future generated-column unique constraint.
		if err := tx.Table("file").Where("user_id IS NULL OR user_id <= 0 OR parent_id IS NULL OR parent_id < 0 OR is_deleted IS NULL OR is_deleted NOT IN (0,1) OR is_folder IS NULL OR is_folder NOT IN (0,1) OR filename IS NULL OR CHAR_LENGTH(filename) = 0").Count(&report.InvalidScalarRows).Error; err != nil {
			return err
		}
		groups := tx.Table("file").Select("parent_id, CASE WHEN parent_id = 0 THEN user_id ELSE 0 END AS root_owner_id, MIN(filename) AS example_name, COUNT(*) AS entry_count, MIN(id) AS first_file_id, MAX(id) AS last_file_id").Where("is_deleted = 0 AND user_id > 0 AND parent_id >= 0 AND is_folder IN (0,1) AND filename IS NOT NULL AND CHAR_LENGTH(filename) > 0").Group("parent_id, CASE WHEN parent_id = 0 THEN user_id ELSE 0 END, filename").Having("COUNT(*) > 1")
		var summary struct {
			Groups int64
			Files  int64
		}
		if err := tx.Table("(?) AS conflicts", groups).Select("COUNT(*) AS `groups`, COALESCE(SUM(entry_count),0) AS files").Scan(&summary).Error; err != nil {
			return err
		}
		report.ConflictGroups, report.ConflictingFiles = summary.Groups, summary.Files
		if err := groups.Order("parent_id, root_owner_id, first_file_id").Limit(fileNameAuditSampleLimit).Scan(&report.Conflicts).Error; err != nil {
			return err
		}
		report.Truncated = report.ConflictGroups > int64(len(report.Conflicts))
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return FileNameAudit{}, err
	}
	return report, nil
}
