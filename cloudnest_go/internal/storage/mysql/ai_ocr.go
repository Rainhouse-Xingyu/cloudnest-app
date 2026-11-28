package mysql

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PublishOCR serializes manual results on the current file. Model IO happens
// before this transaction. Revoke/delete/source changes cannot commit stale
// metadata or a success history row after the publication check.
func (r *AIRepo) PublishOCR(ctx context.Context, uid int64, source model.File, hash, tags, description, text string) (model.AiMetadata, error) {
	return r.publishManualAnalysis(ctx, uid, source, hash, tags, description, text, "ocr", text, "ai_ocr", "manual OCR published from verified image")
}

func (r *AIRepo) PublishSummary(ctx context.Context, uid int64, source model.File, hash, tags, description, text, summary string) (model.AiMetadata, error) {
	return r.publishManualAnalysis(ctx, uid, source, hash, tags, description, text, "document_summary", summary, "ai_summary", "manual summary published from verified source")
}

func (r *AIRepo) publishManualAnalysis(ctx context.Context, uid int64, source model.File, hash, tags, description, text, taskType, taskResult, action, detail string) (model.AiMetadata, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var result model.AiMetadata
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.File
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "id=? AND is_deleted=0 AND is_folder=0", source.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return contract.ErrConflict
			}
			return err
		}
		if current.StorageID == nil || source.StorageID == nil || *current.StorageID != *source.StorageID || current.Filename != source.Filename {
			return contract.ErrConflict
		}
		ws, err := fileWorkspace(tx, current)
		if err != nil {
			return err
		}
		if err := requireFilePermission(tx, uid, current, ws.ID != 0); err != nil {
			return err
		}
		var storage model.Storage
		if err := tx.First(&storage, *current.StorageID).Error; err != nil {
			return err
		}
		if storage.FileHash != hash {
			return contract.ErrConflict
		}
		err = tx.Where("file_id=?", current.ID).Order("id").First(&result).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			result = model.AiMetadata{FileID: current.ID, SourceHash: hash, Tags: tags, Description: description, OcrText: text}
			if err := tx.Create(&result).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			if err := tx.Model(&result).Updates(map[string]any{"source_hash": hash, "tags": tags, "description": description, "ocr_text": text}).Error; err != nil {
				return err
			}
			result.Tags, result.Description, result.OcrText = tags, description, text
			result.SourceHash = hash
		}
		if err := tx.Create(&model.AiTask{UserID: &uid, FileID: &current.ID, TaskType: taskType, Status: "success", Result: taskResult}).Error; err != nil {
			return err
		}
		return tx.Create(&model.OperationLog{UserID: &uid, FileID: &current.ID, Action: action, Status: "success", Detail: detail}).Error
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return model.AiMetadata{}, err
	}
	return result, nil
}
