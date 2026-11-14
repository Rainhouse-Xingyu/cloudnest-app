package mysql

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

func (r *LivePhotoRepo) CanAccessFile(ctx context.Context, uid, id int64, write bool) (bool, error) {
	return canAccessFileForFileRepo(ctx, r.db, uid, id, write)
}

func lockLivePhotoSources(tx *gorm.DB, uid, imageID, videoID int64) (model.File, model.File, error) {
	if imageID <= 0 || videoID <= 0 || imageID == videoID {
		return model.File{}, model.File{}, contract.ErrInvalidInput
	}
	var files []model.File
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id IN ? AND is_deleted = 0", []int64{imageID, videoID}).Order("id ASC").Find(&files).Error
	if err != nil {
		return model.File{}, model.File{}, err
	}
	if len(files) != 2 {
		return model.File{}, model.File{}, gorm.ErrRecordNotFound
	}
	for _, file := range files {
		if err := requireFilePermission(tx, uid, file, true); err != nil {
			return model.File{}, model.File{}, err
		}
	}
	if files[0].ID == imageID {
		return files[0], files[1], nil
	}
	return files[1], files[0], nil
}

func insertLivePhoto(tx *gorm.DB, uid, imageID, videoID int64, automatic bool) (model.LivePhoto, error) {
	image, video, err := lockLivePhotoSources(tx, uid, imageID, videoID)
	if err != nil {
		return model.LivePhoto{}, err
	}
	if image.IsFolder != 0 || video.IsFolder != 0 {
		return model.LivePhoto{}, fmt.Errorf("folder cannot be bound")
	}
	imageExt, videoExt := filepath.Ext(image.Filename), filepath.Ext(video.Filename)
	switch strings.ToLower(imageExt) {
	case ".jpg", ".jpeg", ".heic", ".heif":
	default:
		return model.LivePhoto{}, fmt.Errorf("image file type not supported")
	}
	if !strings.EqualFold(videoExt, ".mov") {
		return model.LivePhoto{}, fmt.Errorf("video file type not supported")
	}
	if automatic && (image.ParentID != video.ParentID || (image.ParentID == 0 && image.UserID != video.UserID) ||
		!strings.EqualFold(strings.TrimSpace(strings.TrimSuffix(image.Filename, imageExt)),
			strings.TrimSpace(strings.TrimSuffix(video.Filename, videoExt)))) {
		return model.LivePhoto{}, contract.ErrConflict
	}

	// A renamed source can change roles. Check both columns while holding both
	// file locks, including when another caller binds in the opposite role.
	ids := []int64{imageID, videoID}
	var count int64
	if err := tx.Model(&model.LivePhoto{}).Where("image_file_id IN ? OR video_file_id IN ?", ids, ids).Count(&count).Error; err != nil {
		return model.LivePhoto{}, err
	}
	if count != 0 {
		return model.LivePhoto{}, contract.ErrLivePhotoBound
	}
	row := model.LivePhoto{ImageFileID: imageID, VideoFileID: videoID}
	if err := tx.Create(&row).Error; err != nil {
		var sqlErr *mysqlDriver.MySQLError
		if errors.As(err, &sqlErr) && sqlErr.Number == 1062 {
			return model.LivePhoto{}, contract.ErrLivePhotoBound
		}
		return model.LivePhoto{}, err
	}
	return row, nil
}

func (r *LivePhotoRepo) CreateBindingWithLog(ctx context.Context, uid int64, imageFileID int64, videoFileID int64, clientIP string) (model.LivePhoto, error) {
	var created model.LivePhoto
	err := fileMetadataTransaction(ctx, r.db, func(tx *gorm.DB) error {
		var err error
		created, err = insertLivePhoto(tx, uid, imageFileID, videoFileID, false)
		if err != nil {
			return err
		}
		return writeLivePhotoAudit(tx, uid, created, "live_photo_bind", clientIP)
	})
	if err != nil {
		return model.LivePhoto{}, err
	}
	return created, nil
}

func writeLivePhotoAudit(tx *gorm.DB, uid int64, row model.LivePhoto, action, clientIP string) error {
	return tx.Create(&model.OperationLog{
		UserID: &uid, FileID: &row.ImageFileID, Action: action, Status: "success",
		Detail:   fmt.Sprintf("live_photo_id=%d image_file_id=%d video_file_id=%d", row.ID, row.ImageFileID, row.VideoFileID),
		ClientIP: clientIP,
	}).Error
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
	return fileMetadataTransaction(ctx, r.db, func(tx *gorm.DB) error {
		if _, _, err := lockLivePhotoSources(tx, uid, row.ImageFileID, row.VideoFileID); err != nil {
			return err
		}
		result := tx.Where("id = ? AND image_file_id = ? AND video_file_id = ?", row.ID, row.ImageFileID, row.VideoFileID).Delete(&model.LivePhoto{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return writeLivePhotoAudit(tx, uid, row, "live_photo_unbind", clientIP)
	})
}

func (r *LivePhotoRepo) ListAccessibleLivePhotos(ctx context.Context, uid int64, parentID int64, page int, limit int) ([]model.LivePhoto, int64, error) {
	offset := (page - 1) * limit

	base := r.db.WithContext(ctx).
		Table("live_photo lp").
		Joins("JOIN file fi ON fi.id = lp.image_file_id AND fi.is_deleted = 0").
		Joins("JOIN file fv ON fv.id = lp.video_file_id AND fv.is_deleted = 0")
	base = fileAccessScope(fileAccessScope(base, "fi", uid, false), "fv", uid, false)

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
		Where("f.parent_id = ? AND f.is_deleted = 0 AND f.is_folder = 0", parentID).
		Order("f.filename ASC, f.id ASC")
	q = fileAccessScope(q, "f", userID, true)
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

	err := fileMetadataTransaction(ctx, r.db, func(tx *gorm.DB) error {
		created = created[:0]
		skipped = skipped[:0]
		for _, pair := range pairs {
			row, err := insertLivePhoto(tx, uid, pair.ImageFileID, pair.VideoFileID, true)
			if errors.Is(err, contract.ErrLivePhotoBound) {
				skipped = append(skipped, pair)
				continue
			}
			if err != nil {
				return err
			}
			created = append(created, row)
		}

		detail := fmt.Sprintf("auto_bind created=%d skipped=%d", len(created), len(skipped))
		return tx.Create(&model.OperationLog{UserID: &uid, Action: "live_photo_auto_bind", Status: "success", Detail: detail, ClientIP: clientIP}).Error
	})
	if err != nil {
		return nil, nil, err
	}

	return created, skipped, nil
}
