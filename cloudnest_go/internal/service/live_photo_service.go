package service

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"cloudenest_go/model"

	"gorm.io/gorm"
)

// LivePhotoService 承接 Live Photo 模块业务逻辑。
type LivePhotoService struct {
	db *gorm.DB
}

func NewLivePhotoService(db *gorm.DB) *LivePhotoService {
	return &LivePhotoService{db: db}
}

// LivePhotoBindResult 表示绑定后的结果。
type LivePhotoBindResult struct {
	Row       model.LivePhoto
	ImageFile model.File
	VideoFile model.File
}

// LivePhotoDetailResult 表示详情结果。
type LivePhotoDetailResult struct {
	Row       model.LivePhoto
	ImageFile model.File
	VideoFile model.File
}

// LivePhotoListItem 表示列表项。
type LivePhotoListItem struct {
	Row       model.LivePhoto
	ImageFile model.File
	VideoFile model.File
}

// LivePhotoListResult 表示列表查询结果。
type LivePhotoListResult struct {
	Items []LivePhotoListItem
	Total int64
}

// LivePhotoFileLookupResult 表示按文件反查结果。
type LivePhotoFileLookupResult struct {
	Row       model.LivePhoto
	FileRole  string
	ImageFile model.File
	VideoFile model.File
}

// LivePhotoAutoBindInput 表示自动绑定输入。
type LivePhotoAutoBindInput struct {
	ParentID *int64
	FileIDs  []int64
}

// LivePhotoAutoBindResult 表示自动绑定结果。
type LivePhotoAutoBindResult struct {
	TotalCandidates int
	CreatedItems    []map[string]interface{}
	SkippedItems    []map[string]interface{}
}

// LivePhotoListInput 表示列表查询输入。
type LivePhotoListInput struct {
	ParentID int64
	Page     int
	Limit    int
}

var (
	// ErrLivePhotoPermissionDenied 表示无权限访问。
	ErrLivePhotoPermissionDenied = errors.New("live photo permission denied")
)

// Bind 手动绑定图片和视频为一个 Live Photo。
func (s *LivePhotoService) Bind(ctx context.Context, uid int64, imageFileID int64, videoFileID int64, clientIP string) (LivePhotoBindResult, error) {
	canWriteImage, err := s.canAccessFile(ctx, uid, imageFileID, true)
	if err != nil {
		return LivePhotoBindResult{}, err
	}
	canWriteVideo, err := s.canAccessFile(ctx, uid, videoFileID, true)
	if err != nil {
		return LivePhotoBindResult{}, err
	}
	if !canWriteImage || !canWriteVideo {
		return LivePhotoBindResult{}, ErrLivePhotoPermissionDenied
	}

	imageFile, videoFile, err := s.loadLivePhotoFiles(ctx, imageFileID, videoFileID)
	if err != nil {
		return LivePhotoBindResult{}, err
	}

	var created model.LivePhoto
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
		return LivePhotoBindResult{}, err
	}

	return LivePhotoBindResult{Row: created, ImageFile: imageFile, VideoFile: videoFile}, nil
}

// Unbind 删除 Live Photo 绑定关系，不删除原始文件。
func (s *LivePhotoService) Unbind(ctx context.Context, uid int64, livePhotoID int64, clientIP string) error {
	var row model.LivePhoto
	if err := s.db.WithContext(ctx).First(&row, "id = ?", livePhotoID).Error; err != nil {
		return err
	}

	canWriteImage, err := s.canAccessFile(ctx, uid, row.ImageFileID, true)
	if err != nil {
		return err
	}
	canWriteVideo, err := s.canAccessFile(ctx, uid, row.VideoFileID, true)
	if err != nil {
		return err
	}
	if !canWriteImage || !canWriteVideo {
		return ErrLivePhotoPermissionDenied
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&model.LivePhoto{}, "id = ?", livePhotoID).Error; err != nil {
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

// Detail 查询单个 Live Photo 的详情。
func (s *LivePhotoService) Detail(ctx context.Context, uid int64, livePhotoID int64) (LivePhotoDetailResult, error) {
	var row model.LivePhoto
	if err := s.db.WithContext(ctx).First(&row, "id = ?", livePhotoID).Error; err != nil {
		return LivePhotoDetailResult{}, err
	}

	canReadImage, err := s.canAccessFile(ctx, uid, row.ImageFileID, false)
	if err != nil {
		return LivePhotoDetailResult{}, err
	}
	canReadVideo, err := s.canAccessFile(ctx, uid, row.VideoFileID, false)
	if err != nil {
		return LivePhotoDetailResult{}, err
	}
	if !canReadImage || !canReadVideo {
		return LivePhotoDetailResult{}, ErrLivePhotoPermissionDenied
	}

	var imageFile model.File
	if err := s.db.WithContext(ctx).First(&imageFile, "id = ? AND is_deleted = 0", row.ImageFileID).Error; err != nil {
		return LivePhotoDetailResult{}, err
	}
	var videoFile model.File
	if err := s.db.WithContext(ctx).First(&videoFile, "id = ? AND is_deleted = 0", row.VideoFileID).Error; err != nil {
		return LivePhotoDetailResult{}, err
	}

	return LivePhotoDetailResult{Row: row, ImageFile: imageFile, VideoFile: videoFile}, nil
}

// List 分页查询 Live Photo 列表，支持按目录过滤。
func (s *LivePhotoService) List(ctx context.Context, uid int64, q LivePhotoListInput) (LivePhotoListResult, error) {
	offset := (q.Page - 1) * q.Limit

	base := s.db.WithContext(ctx).
		Table("live_photo lp").
		Joins("JOIN file fi ON fi.id = lp.image_file_id AND fi.is_deleted = 0").
		Joins("JOIN file fv ON fv.id = lp.video_file_id AND fv.is_deleted = 0").
		Joins("LEFT JOIN file_permission fpi ON fpi.file_id = fi.id AND fpi.user_id = ? AND fpi.permission IN ?", uid, []string{"read", "write"}).
		Joins("LEFT JOIN file_permission fpv ON fpv.file_id = fv.id AND fpv.user_id = ? AND fpv.permission IN ?", uid, []string{"read", "write"}).
		Where("(fi.user_id = ? OR fpi.id IS NOT NULL)", uid).
		Where("(fv.user_id = ? OR fpv.id IS NOT NULL)", uid)

	if q.ParentID > 0 {
		base = base.Where("(fi.parent_id = ? OR fv.parent_id = ?)", q.ParentID, q.ParentID)
	}

	var total int64
	if err := base.Count(&total).Error; err != nil {
		return LivePhotoListResult{}, err
	}

	var rows []model.LivePhoto
	if err := base.Select("lp.*").Order("lp.id DESC").Limit(q.Limit).Offset(offset).Find(&rows).Error; err != nil {
		return LivePhotoListResult{}, err
	}

	fileIDs := make([]int64, 0, len(rows)*2)
	for _, row := range rows {
		fileIDs = append(fileIDs, row.ImageFileID, row.VideoFileID)
	}

	fileMap := map[int64]model.File{}
	if len(fileIDs) > 0 {
		var files []model.File
		if err := s.db.WithContext(ctx).Where("id IN ? AND is_deleted = 0", uniqueInt64(fileIDs)).Find(&files).Error; err != nil {
			return LivePhotoListResult{}, err
		}
		for _, f := range files {
			fileMap[f.ID] = f
		}
	}

	items := make([]LivePhotoListItem, 0, len(rows))
	for _, row := range rows {
		imageFile, okImage := fileMap[row.ImageFileID]
		videoFile, okVideo := fileMap[row.VideoFileID]
		if !okImage || !okVideo {
			continue
		}
		items = append(items, LivePhotoListItem{Row: row, ImageFile: imageFile, VideoFile: videoFile})
	}

	return LivePhotoListResult{Items: items, Total: total}, nil
}

// GetByFileID 根据文件 ID 反查其所属的 Live Photo。
func (s *LivePhotoService) GetByFileID(ctx context.Context, uid int64, fileID int64) (*LivePhotoFileLookupResult, error) {
	canRead, err := s.canAccessFile(ctx, uid, fileID, false)
	if err != nil {
		return nil, err
	}
	if !canRead {
		return nil, ErrLivePhotoPermissionDenied
	}

	var row model.LivePhoto
	err = s.db.WithContext(ctx).
		Where("image_file_id = ? OR video_file_id = ?", fileID, fileID).
		Order("id DESC").
		First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}

	otherID := row.ImageFileID
	role := "video"
	if row.ImageFileID == fileID {
		otherID = row.VideoFileID
		role = "image"
	}
	canReadOther, err := s.canAccessFile(ctx, uid, otherID, false)
	if err != nil {
		return nil, err
	}
	if !canReadOther {
		return nil, ErrLivePhotoPermissionDenied
	}

	var imageFile model.File
	if err := s.db.WithContext(ctx).First(&imageFile, "id = ? AND is_deleted = 0", row.ImageFileID).Error; err != nil {
		return nil, err
	}
	var videoFile model.File
	if err := s.db.WithContext(ctx).First(&videoFile, "id = ? AND is_deleted = 0", row.VideoFileID).Error; err != nil {
		return nil, err
	}

	return &LivePhotoFileLookupResult{Row: row, FileRole: role, ImageFile: imageFile, VideoFile: videoFile}, nil
}

// AutoBind 按同目录+同名主干规则自动识别并绑定 Live Photo。
func (s *LivePhotoService) AutoBind(ctx context.Context, uid int64, req LivePhotoAutoBindInput, clientIP string) (LivePhotoAutoBindResult, error) {
	candidates, err := s.loadAutoBindCandidates(ctx, uid, req)
	if err != nil {
		return LivePhotoAutoBindResult{}, err
	}
	if len(candidates) == 0 {
		return LivePhotoAutoBindResult{TotalCandidates: 0, CreatedItems: []map[string]interface{}{}, SkippedItems: []map[string]interface{}{}}, nil
	}

	type pairGroup struct {
		images []model.File
		videos []model.File
	}
	groups := make(map[string]*pairGroup)
	for _, f := range candidates {
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(f.Filename), "."))
		key := buildAutoBindKey(f.ParentID, f.Filename)
		if key == "" {
			continue
		}
		if groups[key] == nil {
			groups[key] = &pairGroup{images: []model.File{}, videos: []model.File{}}
		}
		if isLivePhotoImageExt(ext) {
			groups[key].images = append(groups[key].images, f)
		} else if isLivePhotoVideoExt(ext) {
			groups[key].videos = append(groups[key].videos, f)
		}
	}

	candidateIDs := make([]int64, 0, len(candidates))
	for _, f := range candidates {
		candidateIDs = append(candidateIDs, f.ID)
	}

	boundImage := make(map[int64]bool)
	boundVideo := make(map[int64]bool)
	if len(candidateIDs) > 0 {
		var existed []model.LivePhoto
		if err := s.db.WithContext(ctx).Where("image_file_id IN ? OR video_file_id IN ?", candidateIDs, candidateIDs).Find(&existed).Error; err != nil {
			return LivePhotoAutoBindResult{}, err
		}
		for _, row := range existed {
			boundImage[row.ImageFileID] = true
			boundVideo[row.VideoFileID] = true
		}
	}

	createdItems := make([]map[string]interface{}, 0)
	skippedItems := make([]map[string]interface{}, 0)
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for key, g := range groups {
			if len(g.images) == 0 || len(g.videos) == 0 {
				skippedItems = append(skippedItems, map[string]interface{}{"key": key, "reason": "pair not found"})
				continue
			}

			pairCount := len(g.images)
			if len(g.videos) < pairCount {
				pairCount = len(g.videos)
			}

			for i := 0; i < pairCount; i++ {
				img := g.images[i]
				vid := g.videos[i]

				if boundImage[img.ID] || boundVideo[vid.ID] {
					skippedItems = append(skippedItems, map[string]interface{}{"image_file_id": img.ID, "video_file_id": vid.ID, "reason": "already bound"})
					continue
				}

				row := model.LivePhoto{ImageFileID: img.ID, VideoFileID: vid.ID}
				if err := tx.Create(&row).Error; err != nil {
					if strings.Contains(strings.ToLower(err.Error()), "duplicate") || strings.Contains(strings.ToLower(err.Error()), "1062") {
						skippedItems = append(skippedItems, map[string]interface{}{"image_file_id": img.ID, "video_file_id": vid.ID, "reason": "already bound"})
						continue
					}
					return err
				}

				boundImage[img.ID] = true
				boundVideo[vid.ID] = true
				createdItems = append(createdItems, map[string]interface{}{"id": row.ID, "image_file_id": row.ImageFileID, "video_file_id": row.VideoFileID, "create_time": row.CreateTime})
			}
		}

		detail := fmt.Sprintf("auto_bind created=%d skipped=%d", len(createdItems), len(skippedItems))
		if len(detail) > 500 {
			detail = detail[:500]
		}
		return tx.Create(&model.OperationLog{UserID: &uid, Action: "live_photo_auto_bind", Status: "success", Detail: detail, ClientIP: clientIP}).Error
	})
	if err != nil {
		return LivePhotoAutoBindResult{}, err
	}

	return LivePhotoAutoBindResult{TotalCandidates: len(candidates), CreatedItems: createdItems, SkippedItems: skippedItems}, nil
}

// canAccessFile 复用 file + file_permission 的权限模型判断文件访问权限。
func (s *LivePhotoService) canAccessFile(ctx context.Context, userID int64, fileID int64, requireWrite bool) (bool, error) {
	var f model.File
	err := s.db.WithContext(ctx).First(&f, "id = ? AND is_deleted = 0", fileID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}

	if f.UserID == userID {
		return true, nil
	}

	permissions := []string{"read", "write"}
	if requireWrite {
		permissions = []string{"write"}
	}

	var count int64
	err = s.db.WithContext(ctx).Model(&model.FilePermission{}).Where("file_id = ? AND user_id = ? AND permission IN ?", fileID, userID, permissions).Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// loadLivePhotoFiles 校验文件类型并返回图片文件和视频文件。
func (s *LivePhotoService) loadLivePhotoFiles(ctx context.Context, imageFileID int64, videoFileID int64) (model.File, model.File, error) {
	var imageFile model.File
	if err := s.db.WithContext(ctx).First(&imageFile, "id = ? AND is_deleted = 0", imageFileID).Error; err != nil {
		return model.File{}, model.File{}, err
	}
	var videoFile model.File
	if err := s.db.WithContext(ctx).First(&videoFile, "id = ? AND is_deleted = 0", videoFileID).Error; err != nil {
		return model.File{}, model.File{}, err
	}

	if imageFile.IsFolder == 1 || videoFile.IsFolder == 1 {
		return model.File{}, model.File{}, fmt.Errorf("folder cannot be bound")
	}

	imageExt := strings.ToLower(strings.TrimPrefix(filepath.Ext(imageFile.Filename), "."))
	videoExt := strings.ToLower(strings.TrimPrefix(filepath.Ext(videoFile.Filename), "."))
	if !isLivePhotoImageExt(imageExt) {
		return model.File{}, model.File{}, fmt.Errorf("image file type not supported")
	}
	if !isLivePhotoVideoExt(videoExt) {
		return model.File{}, model.File{}, fmt.Errorf("video file type not supported")
	}

	return imageFile, videoFile, nil
}

// loadAutoBindCandidates 根据请求加载自动绑定候选文件。
func (s *LivePhotoService) loadAutoBindCandidates(ctx context.Context, userID int64, req LivePhotoAutoBindInput) ([]model.File, error) {
	if len(req.FileIDs) > 0 {
		ids := uniqueInt64(req.FileIDs)
		for _, id := range ids {
			if id <= 0 {
				return nil, fmt.Errorf("invalid file id")
			}
			canWrite, err := s.canAccessFile(ctx, userID, id, true)
			if err != nil {
				return nil, err
			}
			if !canWrite {
				return nil, fmt.Errorf("permission denied")
			}
		}

		var rows []model.File
		if err := s.db.WithContext(ctx).Where("id IN ? AND is_deleted = 0 AND is_folder = 0", ids).Order("parent_id ASC, filename ASC, id ASC").Find(&rows).Error; err != nil {
			return nil, err
		}
		return rows, nil
	}

	if req.ParentID != nil {
		if *req.ParentID < 0 {
			return nil, fmt.Errorf("invalid parent_id")
		}
		if *req.ParentID > 0 {
			canWrite, err := s.canAccessFile(ctx, userID, *req.ParentID, true)
			if err != nil {
				return nil, err
			}
			if !canWrite {
				return nil, fmt.Errorf("permission denied")
			}
		}

		query := s.db.WithContext(ctx).
			Table("file f").
			Select("f.*").
			Joins("LEFT JOIN file_permission fp ON fp.file_id = f.id AND fp.user_id = ? AND fp.permission = ?", userID, "write").
			Where("f.parent_id = ? AND f.is_deleted = 0 AND f.is_folder = 0", *req.ParentID).
			Where("f.user_id = ? OR fp.id IS NOT NULL", userID).
			Order("f.filename ASC, f.id ASC")

		var rows []model.File
		if err := query.Find(&rows).Error; err != nil {
			return nil, err
		}
		return rows, nil
	}

	return []model.File{}, nil
}

func isLivePhotoImageExt(ext string) bool {
	allowed := map[string]bool{"jpg": true, "jpeg": true, "heic": true, "heif": true}
	return allowed[ext]
}

func isLivePhotoVideoExt(ext string) bool {
	allowed := map[string]bool{"mov": true}
	return allowed[ext]
}

func buildAutoBindKey(parentID int64, filename string) string {
	base := strings.TrimSpace(strings.TrimSuffix(filename, filepath.Ext(filename)))
	if base == "" {
		return ""
	}
	return fmt.Sprintf("%d::%s", parentID, strings.ToLower(base))
}

func uniqueInt64(input []int64) []int64 {
	set := make(map[int64]struct{}, len(input))
	out := make([]int64, 0, len(input))
	for _, v := range input {
		if _, ok := set[v]; ok {
			continue
		}
		set[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
