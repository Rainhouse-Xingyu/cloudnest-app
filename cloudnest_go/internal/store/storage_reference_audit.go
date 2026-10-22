package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"gorm.io/gorm"
)

var ErrStorageReferenceFindings = errors.New("storage-reference audit found dangling references; no historical rows were changed")

const storageReferenceSampleLimit = 100

type DanglingStorageReference struct {
	Source    string `json:"source"`
	RowID     int64  `json:"row_id"`
	StorageID int64  `json:"storage_id"`
}

type StorageReferenceAudit struct {
	GeneratedAt         time.Time                  `json:"generated_at"`
	StorageObjects      int64                      `json:"storage_objects"`
	FileReferences      int64                      `json:"file_references"`
	VersionReferences   int64                      `json:"version_references"`
	DanglingFiles       int64                      `json:"dangling_files"`
	DanglingVersions    int64                      `json:"dangling_versions"`
	UnreferencedObjects int64                      `json:"unreferenced_objects"`
	DanglingReferences  []DanglingStorageReference `json:"dangling_references"`
	SampleLimit         int                        `json:"sample_limit"`
	Truncated           bool                       `json:"truncated"`
}

func (r StorageReferenceAudit) HasFindings() bool {
	return r.DanglingFiles > 0 || r.DanglingVersions > 0
}

// AuditStorageReferences reads a single snapshot. All file rows (including
// recycle-bin entries and folder pointers) and all versions (including unknown
// provenance) retain their objects. Unreferenced is informational, never proof
// that bytes may be deleted. No filesystem paths or filenames are emitted.
func AuditStorageReferences(ctx context.Context, db *gorm.DB) (StorageReferenceAudit, error) {
	r := StorageReferenceAudit{GeneratedAt: time.Now().UTC(), SampleLimit: storageReferenceSampleLimit, DanglingReferences: []DanglingStorageReference{}}
	err := db.WithContext(ctx).Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		tx = tx.Session(&gorm.Session{NewDB: true})
		queries := []struct {
			sql string
			out *int64
		}{
			{"SELECT COUNT(*) FROM storage", &r.StorageObjects},
			{"SELECT COUNT(*) FROM file WHERE storage_id IS NOT NULL", &r.FileReferences},
			{"SELECT COUNT(*) FROM file_version WHERE storage_id IS NOT NULL", &r.VersionReferences},
			{"SELECT COUNT(*) FROM file r LEFT JOIN storage s ON s.id=r.storage_id WHERE r.storage_id IS NOT NULL AND s.id IS NULL", &r.DanglingFiles},
			{"SELECT COUNT(*) FROM file_version r LEFT JOIN storage s ON s.id=r.storage_id WHERE r.storage_id IS NOT NULL AND s.id IS NULL", &r.DanglingVersions},
			{"SELECT COUNT(*) FROM storage s WHERE NOT EXISTS (SELECT 1 FROM file f WHERE f.storage_id=s.id) AND NOT EXISTS (SELECT 1 FROM file_version v WHERE v.storage_id=s.id)", &r.UnreferencedObjects},
		}
		for _, q := range queries {
			if err := tx.Raw(q.sql).Scan(q.out).Error; err != nil {
				return err
			}
		}
		if err := tx.Raw(`SELECT source,row_id,storage_id FROM (
			SELECT 'file' AS source,r.id AS row_id,r.storage_id FROM file r LEFT JOIN storage s ON s.id=r.storage_id WHERE r.storage_id IS NOT NULL AND s.id IS NULL
			UNION ALL
			SELECT 'file_version' AS source,r.id AS row_id,r.storage_id FROM file_version r LEFT JOIN storage s ON s.id=r.storage_id WHERE r.storage_id IS NOT NULL AND s.id IS NULL
		) refs ORDER BY source,row_id LIMIT ?`, storageReferenceSampleLimit).Scan(&r.DanglingReferences).Error; err != nil {
			return err
		}
		r.Truncated = r.DanglingFiles+r.DanglingVersions > int64(len(r.DanglingReferences))
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return StorageReferenceAudit{}, err
	}
	return r, nil
}
