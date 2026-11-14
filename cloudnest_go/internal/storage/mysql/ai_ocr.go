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
		if current.UserID != uid {
			var grant model.FilePermission
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("file_id=? AND user_id=? AND permission IN ?", current.ID, uid, []string{"read", "write"}).First(&grant).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return contract.ErrForbidden
				}
				return err
			}
		}
		var storage model.Storage
		if err := tx.First(&storage, *current.StorageID).Error; err != nil {
			return err
		}
		if storage.FileHash != hash {
			return contract.ErrConflict
		}
		err := tx.Where("file_id=?", current.ID).Order("id").First(&result).Error
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
		if err := tx.Create(&model.AiTask{UserID: &uid, FileID: &current.ID, TaskType: "ocr", Status: "success", Result: text}).Error; err != nil {
			return err
		}
		return tx.Create(&model.OperationLog{UserID: &uid, FileID: &current.ID, Action: "ai_ocr", Status: "success", Detail: "manual OCR published from verified image"}).Error
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return model.AiMetadata{}, err
	}
	return result, nil
}
