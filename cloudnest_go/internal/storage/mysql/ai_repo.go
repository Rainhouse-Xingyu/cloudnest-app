package mysql

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"cloudenest_go/internal/quota"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"

	"gorm.io/gorm"
)

// AIRepo 是 AI 模块仓储实现。
type AIRepo struct {
	db *gorm.DB
}

func NewAIRepo(db *gorm.DB) *AIRepo {
	return &AIRepo{db: db}
}

func (r *AIRepo) ApplySettingWithAudit(ctx context.Context, uid int64, key string, value string, description string, clientIP string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := quota.LockPolicy(tx, true); err != nil {
			return err
		}
		var row model.SystemSetting
		err := tx.Where("setting_key = ?", key).First(&row).Error
		if err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			row = model.SystemSetting{SettingKey: key, SettingValue: value, Description: description}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		} else {
			updates := map[string]interface{}{"setting_value": value}
			if description != "" {
				updates["description"] = description
			}
			if err := tx.Model(&row).Updates(updates).Error; err != nil {
				return err
			}
		}

		detail := fmt.Sprintf("ai_apply_setting key=%s value=%s", key, value)
		if len(detail) > 500 {
			detail = detail[:500]
		}
		return tx.Create(&model.OperationLog{UserID: &uid, Action: "ai_apply_setting", Status: "success", Detail: detail, ClientIP: clientIP}).Error
	})
}

func (r *AIRepo) Search(ctx context.Context, uid int64, q contract.AISearchQuery) ([]contract.AISearchRow, int64, error) {
	base := r.db.WithContext(ctx).
		Table("file f").
		Select("f.id, f.filename, f.file_type, f.size, f.parent_id, f.user_id, f.create_time, f.update_time, am.tags, am.description, am.ocr_text").
		Joins("LEFT JOIN file_permission fp ON fp.file_id = f.id AND fp.user_id = ?", uid).
		Joins("LEFT JOIN ai_metadata am ON am.file_id = f.id").
		Where("f.is_deleted = 0 AND f.is_folder = 0").
		Where("f.user_id = ? OR fp.permission IN ?", uid, []string{"read", "write"})

	if q.Keyword != "" {
		like := "%" + q.Keyword + "%"
		if q.TagOnly {
			base = base.Where("am.tags LIKE ?", like)
		} else {
			base = base.Where("f.filename LIKE ? OR am.tags LIKE ? OR am.description LIKE ? OR am.ocr_text LIKE ?", like, like, like, like)
		}
	}
	if q.FileType != "" {
		switch strings.ToLower(q.FileType) {
		case "image", "photo", "picture", "照片", "图片":
			base = base.Where("LOWER(f.file_type) IN ?", []string{"jpg", "jpeg", "png", "gif", "webp", "heic", "heif"})
		case "document", "doc", "文档", "票据", "发票":
			base = base.Where("LOWER(f.file_type) IN ?", []string{"pdf", "docx", "xlsx", "pptx", "txt", "md", "csv", "json", "xml", "yaml", "yml"})
		default:
			base = base.Where("LOWER(f.file_type) = ?", strings.ToLower(q.FileType))
		}
	}
	if q.Location != "" {
		like := "%" + q.Location + "%"
		if q.TagOnly {
			base = base.Where("am.tags LIKE ?", like)
		} else {
			base = base.Where("am.tags LIKE ? OR am.description LIKE ? OR am.ocr_text LIKE ?", like, like, like)
		}
	}
	if q.Landmark != "" {
		like := "%" + q.Landmark + "%"
		if q.TagOnly {
			base = base.Where("am.tags LIKE ?", like)
		} else {
			base = base.Where("am.tags LIKE ? OR am.description LIKE ? OR am.ocr_text LIKE ?", like, like, like)
		}
	}
	if q.StartTime != nil {
		base = base.Where("f.create_time >= ?", *q.StartTime)
	}
	if q.EndTime != nil {
		base = base.Where("f.create_time <= ?", *q.EndTime)
	}

	var total int64
	if err := r.db.WithContext(ctx).Table("(?) as t", base).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	offset := (q.Page - 1) * q.Limit
	rows := make([]contract.AISearchRow, 0)
	if err := base.Order("f.id DESC").Limit(q.Limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func (r *AIRepo) GetActiveFileByID(ctx context.Context, fileID int64) (model.File, error) {
	var f model.File
	if err := r.db.WithContext(ctx).First(&f, "id = ? AND is_deleted = 0", fileID).Error; err != nil {
		return model.File{}, err
	}
	return f, nil
}

func (r *AIRepo) GetStorageByID(ctx context.Context, storageID int64) (model.Storage, error) {
	var storage model.Storage
	if err := r.db.WithContext(ctx).First(&storage, "id = ?", storageID).Error; err != nil {
		return model.Storage{}, err
	}
	return storage, nil
}

func (r *AIRepo) CountFilePermissionsByLevels(ctx context.Context, fileID int64, userID int64, permissions []string) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&model.FilePermission{}).Where("file_id = ? AND user_id = ? AND permission IN ?", fileID, userID, permissions).Count(&count).Error
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (r *AIRepo) GetAIMetadataByFileID(ctx context.Context, fileID int64) (model.AiMetadata, error) {
	var row model.AiMetadata
	if err := r.db.WithContext(ctx).Where("file_id = ?", fileID).First(&row).Error; err != nil {
		return model.AiMetadata{}, err
	}
	return row, nil
}

func (r *AIRepo) CreateAIMetadata(ctx context.Context, row model.AiMetadata) (model.AiMetadata, error) {
	if err := r.db.WithContext(ctx).Create(&row).Error; err != nil {
		return model.AiMetadata{}, err
	}
	return row, nil
}

func (r *AIRepo) UpdateAIMetadata(ctx context.Context, row model.AiMetadata, tags string, desc string, ocrText string) (model.AiMetadata, error) {
	if err := r.db.WithContext(ctx).Model(&row).Updates(map[string]interface{}{"tags": tags, "description": desc, "ocr_text": ocrText}).Error; err != nil {
		return model.AiMetadata{}, err
	}
	if err := r.db.WithContext(ctx).First(&row, row.ID).Error; err != nil {
		return model.AiMetadata{}, err
	}
	return row, nil
}

func (r *AIRepo) CreateAITask(ctx context.Context, row model.AiTask) error {
	return r.db.WithContext(ctx).Create(&row).Error
}
