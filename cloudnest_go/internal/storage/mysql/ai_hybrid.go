package mysql

import (
	"context"
	"fmt"
	"strings"

	"cloudenest_go/internal/service/contract"
	"gorm.io/gorm"
)

func hybridBase(db *gorm.DB, uid int64) *gorm.DB {
	return db.Table("file f").
		Joins("LEFT JOIN storage s ON s.id=f.storage_id").
		Joins("LEFT JOIN ai_index_job j ON j.file_id=f.id AND j.status='done' AND j.source_hash=s.file_hash").
		Joins("LEFT JOIN ai_index_document d ON d.file_id=f.id AND d.generation=j.generation AND d.source_hash=s.file_hash AND d.pipeline=j.pipeline").
		Where("f.is_deleted=0 AND f.is_folder=0").
		Where("f.user_id=? OR EXISTS (SELECT 1 FROM file_permission p WHERE p.file_id=f.id AND p.user_id=? AND p.permission IN ('read','write'))", uid, uid)
}

// ScanHybrid streams the full authorized candidate set. No prefix LIMIT silently
// drops relevant vectors. The caller bounds time and retains only top file hits.
func (r *AIRepo) ScanHybrid(ctx context.Context, uid int64, filter contract.AIHybridFilter, visit func(contract.AIHybridRow) error) error {
	query := hybridBase(r.db.WithContext(ctx), uid).
		Joins("LEFT JOIN ai_index_chunk c ON c.file_id=d.file_id").
		Select(`f.id,f.filename,f.file_type,f.size,f.parent_id,f.user_id,f.create_time,f.update_time,
 COALESCE(s.file_hash,'') AS source_hash,COALESCE(d.generation,0) AS generation,
 COALESCE(d.embedding_model,'') AS embedding_model,COALESCE(d.model_digest,'') AS model_digest,
 COALESCE(d.dimensions,0) AS dimensions,COALESCE(c.ordinal,0) AS ordinal,
 COALESCE(c.start_rune,0) AS start_rune,COALESCE(c.content,'') AS content,c.vector`).
		Order("f.id,c.ordinal")
	if len(filter.FileTypes) > 0 {
		query = query.Where("LOWER(f.file_type) IN ?", filter.FileTypes)
	}
	if filter.UploadedAfter != nil {
		query = query.Where("f.create_time>=?", *filter.UploadedAfter)
	}
	if filter.UploadedBefore != nil {
		query = query.Where("f.create_time<?", *filter.UploadedBefore)
	}
	rows, err := query.Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var row contract.AIHybridRow
		if err := r.db.ScanRows(rows, &row); err != nil {
			return err
		}
		if err := visit(row); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Revalidate immediately before returning snippets. Revocation, recycle, source
// changes, rename and generation changes invalidate the previously scored hit.
func (r *AIRepo) ValidateHybrid(ctx context.Context, uid int64, rows []contract.AIHybridRow) (map[int64]bool, error) {
	valid := make(map[int64]bool)
	if len(rows) == 0 {
		return valid, nil
	}
	if len(rows) > 400 {
		return nil, fmt.Errorf("too many hybrid candidates")
	}
	var predicates []string
	var args []any
	for _, row := range rows {
		predicates = append(predicates, "(f.id=? AND f.filename=? AND COALESCE(s.file_hash,'')=? AND COALESCE(d.generation,0)=?)")
		args = append(args, row.ID, row.Filename, row.SourceHash, row.Generation)
	}
	var ids []int64
	if err := hybridBase(r.db.WithContext(ctx), uid).Where(strings.Join(predicates, " OR "), args...).Pluck("f.id", &ids).Error; err != nil {
		return nil, err
	}
	for _, id := range ids {
		valid[id] = true
	}
	return valid, nil
}
