package mysql

import (
	"cloudenest_go/internal/indexqueue"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func indexStatusBase(db *gorm.DB) *gorm.DB {
	return db.Table("file f").
		Joins("LEFT JOIN storage s ON s.id=f.storage_id").
		Joins("LEFT JOIN ai_index_job j ON j.file_id=f.id").
		Joins("LEFT JOIN ai_index_document d ON d.file_id=f.id AND j.status='done' AND d.generation=j.generation AND d.source_hash=s.file_hash AND j.source_hash=s.file_hash AND d.pipeline=j.pipeline AND j.pipeline=?", indexqueue.Pipeline).
		Where("f.is_deleted=0 AND f.is_folder=0").
		Select(`f.id AS file_id, f.filename, COALESCE(j.generation,0) AS generation,
 COALESCE(j.status,'not_indexed') AS status, COALESCE(j.attempts,0) AS attempts,
 COALESCE(j.error_code,'') AS error_code, j.available_at, j.updated_at,
 COALESCE(j.source_hash=s.file_hash AND j.pipeline=?,0) AS source_current,
 d.file_id IS NOT NULL AS indexed_current, COALESCE(d.chunk_count,0) AS chunk_count, d.indexed_at`, indexqueue.Pipeline)
}

func (r *AIRepo) ListIndex(ctx context.Context, uid, after int64, limit int) ([]contract.AIIndexStatus, error) {
	rows := []contract.AIIndexStatus{}
	err := indexStatusBase(r.db.WithContext(ctx)).Where("f.user_id=? AND f.id>?", uid, after).Order("f.id").Limit(limit).Scan(&rows).Error
	return rows, err
}

func (r *AIRepo) IndexStatus(ctx context.Context, uid, id int64) (contract.AIIndexStatus, error) {
	var row contract.AIIndexStatus
	err := indexStatusBase(r.db.WithContext(ctx)).Where("f.id=?", id).
		Where("f.user_id=? OR EXISTS (SELECT 1 FROM file_permission p WHERE p.file_id=f.id AND p.user_id=? AND p.permission IN ('read','write'))", uid, uid).Scan(&row).Error
	if err == nil && row.FileID == 0 {
		err = contract.ErrNotFound
	}
	return row, err
}

func (r *AIRepo) RebuildIndex(ctx context.Context, uid int64, items []contract.AIRebuildItem, ip string) ([]contract.AIRebuildItem, error) {
	result := make([]contract.AIRebuildItem, 0, len(items))
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Lock all target files in ascending order before any jobs. Ownership is
		// checked under the same mutation lock used by publish/recycle/delete.
		for _, item := range items {
			var f model.File
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&f, "id=? AND user_id=? AND is_deleted=0 AND is_folder=0", item.FileID, uid).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return contract.ErrNotFound
				}
				return err
			}
		}
		for _, item := range items {
			var j model.AIIndexJob
			err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&j, "file_id=?", item.FileID).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if j.Generation != item.Generation {
				return contract.ErrConflict
			}
			if err := indexqueue.Enqueue(tx, item.FileID, true); err != nil {
				if errors.Is(err, indexqueue.ErrSourceChanged) || errors.Is(err, indexqueue.ErrInvalid) {
					return contract.ErrConflict
				}
				return err
			}
			next := contract.AIRebuildItem{FileID: item.FileID, Generation: item.Generation + 1}
			if err := tx.Create(&model.OperationLog{UserID: &uid, FileID: &next.FileID, Action: "ai_rebuild", Status: "success", Detail: fmt.Sprintf("index generation %d queued", next.Generation), ClientIP: ip}).Error; err != nil {
				return err
			}
			result = append(result, next)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	return result, nil
}
