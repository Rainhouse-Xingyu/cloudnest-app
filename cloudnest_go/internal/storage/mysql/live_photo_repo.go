package mysql

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"

	"gorm.io/gorm"
)

// LivePhotoRepo 是 Live Photo 模块仓储实现。
type LivePhotoRepo struct {
	db *gorm.DB
}

func NewLivePhotoRepo(db *gorm.DB) *LivePhotoRepo {
	return &LivePhotoRepo{db: db}
}

func (r *LivePhotoRepo) GetActiveFileByID(ctx context.Context, fileID int64) (model.File, error) {
	var f model.File
	err := r.db.WithContext(ctx).First(&f, "id = ? AND is_deleted = 0", fileID).Error
	if err != nil {
		return model.File{}, err
	}
	return f, nil
}

func (r *LivePhotoRepo) CountFilePermissionsByLevels(ctx context.Context, fileID int64, userID int64, permissions []string) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&model.FilePermission{}).Where("file_id = ? AND user_id = ? AND permission IN ?", fileID, userID, permissions).Count(&count).Error
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (r *LivePhotoRepo) CreateBindingWithLog(ctx context.Context, uid int64, imageFileID int64, videoFileID int64, clientIP string) (model.LivePhoto, error) {
	var created model.LivePhoto
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var exists model.LivePhoto
		err := tx.Where("image_file_id = ? OR video_file_id = ?", imageFileID, videoFileID).First(&exists).Error
		if err == nil {
			return fmt.Errorf("live photo relation already exists")
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		created = model.LivePhoto{ImageFileID: imageFileID, VideoFileID: videoFileID}
		if err := tx.Create(&created).Error; err != nil {
			return err
		}

		return tx.Create(&model.OperationLog{
			UserID:   &uid,
			FileID:   &created.ImageFileID,
			Action:   "live_photo_bind",
			Status:   "success",
			Detail:   fmt.Sprintf("live_photo_id=%d image_file_id=%d video_file_id=%d", created.ID, created.ImageFileID, created.VideoFileID),
			ClientIP: clientIP,
		}).Error
	})
	if err != nil {
		return model.LivePhoto{}, err
	}
	return created, nil
}

func (r *LivePhotoRepo) GetLivePhotoByID(ctx context.Context, livePhotoID int64) (model.LivePhoto, error) {
	var row model.LivePhoto
	err := r.db.WithContext(ctx).First(&row, "id = ?", livePhotoID).Error
	if err != nil {
		return model.LivePhoto{}, err
	}
	return row, nil
}

func (r *LivePhotoRepo) DeleteBindingWithLog(ctx context.Context, uid int64, row model.LivePhoto, clientIP string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&model.LivePhoto{}, "id = ?", row.ID).Error; err != nil {
			return err
		}
		return tx.Create(&model.OperationLog{
			UserID:   &uid,
			FileID:   &row.ImageFileID,
			Action:   "live_photo_unbind",
			Status:   "success",
			Detail:   fmt.Sprintf("live_photo_id=%d image_file_id=%d video_file_id=%d", row.ID, row.ImageFileID, row.VideoFileID),
			ClientIP: clientIP,
		}).Error
	})
}

func (r *LivePhotoRepo) ListAccessibleLivePhotos(ctx context.Context, uid int64, parentID int64, page int, limit int) ([]model.LivePhoto, int64, error) {
	offset := (page - 1) * limit

	base := r.db.WithContext(ctx).
		Table("live_photo lp").
		Joins("JOIN file fi ON fi.id = lp.image_file_id AND fi.is_deleted = 0").
		Joins("JOIN file fv ON fv.id = lp.video_file_id AND fv.is_deleted = 0").
		Joins("LEFT JOIN file_permission fpi ON fpi.file_id = fi.id AND fpi.user_id = ? AND fpi.permission IN ?", uid, []string{"read", "write"}).
		Joins("LEFT JOIN file_permission fpv ON fpv.file_id = fv.id AND fpv.user_id = ? AND fpv.permission IN ?", uid, []string{"read", "write"}).
		Where("(fi.user_id = ? OR fpi.id IS NOT NULL)", uid).
		Where("(fv.user_id = ? OR fpv.id IS NOT NULL)", uid)

	if parentID > 0 {
		base = base.Where("(fi.parent_id = ? OR fv.parent_id = ?)", parentID, parentID)
	}

	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var rows []model.LivePhoto
	if err := base.Select("lp.*").Order("lp.id DESC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, 0, err
	}

	return rows, total, nil
}

func (r *LivePhotoRepo) ListActiveFilesByIDs(ctx context.Context, fileIDs []int64) ([]model.File, error) {
	rows := make([]model.File, 0)
	if len(fileIDs) == 0 {
		return rows, nil
	}
	err := r.db.WithContext(ctx).Where("id IN ? AND is_deleted = 0", fileIDs).Order("parent_id ASC, filename ASC, id ASC").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *LivePhotoRepo) GetLatestLivePhotoByFileID(ctx context.Context, fileID int64) (model.LivePhoto, error) {
	var row model.LivePhoto
	err := r.db.WithContext(ctx).
		Where("image_file_id = ? OR video_file_id = ?", fileID, fileID).
		Order("id DESC").
		First(&row).Error
	if err != nil {
		return model.LivePhoto{}, err
	}
	return row, nil
}

func (r *LivePhotoRepo) ListWritableFilesByParent(ctx context.Context, userID int64, parentID int64) ([]model.File, error) {
	rows := make([]model.File, 0)
	q := r.db.WithContext(ctx).
		Table("file f").
		Select("f.*").
		Joins("LEFT JOIN file_permission fp ON fp.file_id = f.id AND fp.user_id = ? AND fp.permission = ?", userID, "write").
		Where("f.parent_id = ? AND f.is_deleted = 0 AND f.is_folder = 0", parentID).
		Where("f.user_id = ? OR fp.id IS NOT NULL", userID).
		Order("f.filename ASC, f.id ASC")
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *LivePhotoRepo) ListLivePhotosByFileIDs(ctx context.Context, fileIDs []int64) ([]model.LivePhoto, error) {
	rows := make([]model.LivePhoto, 0)
	if len(fileIDs) == 0 {
		return rows, nil
	}
	err := r.db.WithContext(ctx).Where("image_file_id IN ? OR video_file_id IN ?", fileIDs, fileIDs).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *LivePhotoRepo) CreateAutoBindPairsWithLog(ctx context.Context, uid int64, pairs []contract.LivePhotoBindPair, clientIP string) ([]model.LivePhoto, []contract.LivePhotoBindPair, error) {
	created := make([]model.LivePhoto, 0)
	skipped := make([]contract.LivePhotoBindPair, 0)

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, pair := range pairs {
			row := model.LivePhoto{ImageFileID: pair.ImageFileID, VideoFileID: pair.VideoFileID}
			if err := tx.Create(&row).Error; err != nil {
				e := strings.ToLower(err.Error())
				if strings.Contains(e, "duplicate") || strings.Contains(e, "1062") {
					skipped = append(skipped, pair)
					continue
				}
				return err
			}
			created = append(created, row)
		}

		detail := fmt.Sprintf("auto_bind created=%d skipped=%d", len(created), len(skipped))
		if len(detail) > 500 {
			detail = detail[:500]
		}
		return tx.Create(&model.OperationLog{UserID: &uid, Action: "live_photo_auto_bind", Status: "success", Detail: detail, ClientIP: clientIP}).Error
	})
	if err != nil {
		return nil, nil, err
	}

	return created, skipped, nil
}
