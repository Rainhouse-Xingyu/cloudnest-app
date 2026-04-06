package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"cloudenest_go/internal/middleware"
	"cloudenest_go/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// LivePhotoHandler 负责 Live Photo 绑定关系的管理接口。
type LivePhotoHandler struct {
	DB *gorm.DB
}

// BindLivePhotoRequest 手动绑定 Live Photo 的请求体。
type BindLivePhotoRequest struct {
	ImageFileID int64 `json:"image_file_id" binding:"required"`
	VideoFileID int64 `json:"video_file_id" binding:"required"`
}

// LivePhotoListQuery Live Photo 列表查询参数。
type LivePhotoListQuery struct {
	ParentID int64 `form:"parent_id"`
	Page     int   `form:"page"`
	Limit    int   `form:"limit"`
}

// AutoBindLivePhotoRequest 自动识别绑定请求。
type AutoBindLivePhotoRequest struct {
	ParentID *int64  `json:"parent_id"`
	FileIDs  []int64 `json:"file_ids"`
}

// Bind 手动绑定图片和视频为一个 Live Photo。
func (h *LivePhotoHandler) Bind(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)

	var req BindLivePhotoRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if req.ImageFileID <= 0 || req.VideoFileID <= 0 || req.ImageFileID == req.VideoFileID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid file ids"})
		return
	}

	// 绑定操作要求对两个文件都具备写权限。
	canWriteImage, err := h.canAccessFile(c.Request.Context(), uid, req.ImageFileID, true)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	canWriteVideo, err := h.canAccessFile(c.Request.Context(), uid, req.VideoFileID, true)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if !canWriteImage || !canWriteVideo {
		c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
		return
	}

	imageFile, videoFile, err := h.loadLivePhotoFiles(c.Request.Context(), req.ImageFileID, req.VideoFileID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var created model.LivePhoto
	err = h.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		// 业务层去重：同一图片或同一视频只能属于一个 Live Photo。
		var exists model.LivePhoto
		err := tx.Where("image_file_id = ? OR video_file_id = ?", req.ImageFileID, req.VideoFileID).First(&exists).Error
		if err == nil {
			return fmt.Errorf("live photo relation already exists")
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		created = model.LivePhoto{
			ImageFileID: req.ImageFileID,
			VideoFileID: req.VideoFileID,
		}
		if err := tx.Create(&created).Error; err != nil {
			return err
		}

		return tx.Create(&model.OperationLog{
			UserID:   &uid,
			FileID:   &created.ImageFileID,
			Action:   "live_photo_bind",
			Status:   "success",
			Detail:   fmt.Sprintf("live_photo_id=%d image_file_id=%d video_file_id=%d", created.ID, created.ImageFileID, created.VideoFileID),
			ClientIP: c.ClientIP(),
		}).Error
	})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "already exists") {
			c.JSON(http.StatusConflict, gin.H{"error": "live photo relation already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "bind live photo failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"item": gin.H{
			"id":            created.ID,
			"image_file_id": created.ImageFileID,
			"video_file_id": created.VideoFileID,
			"create_time":   created.CreateTime,
			"image_file":    toSimpleFileView(imageFile),
			"video_file":    toSimpleFileView(videoFile),
		},
	})
}

// Unbind 删除 Live Photo 绑定关系，不删除原始文件。
func (h *LivePhotoHandler) Unbind(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	livePhotoID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || livePhotoID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid live photo id"})
		return
	}

	var row model.LivePhoto
	if err := h.DB.WithContext(c.Request.Context()).First(&row, "id = ?", livePhotoID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "live photo not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	// 解绑要求对两个文件都具备写权限。
	canWriteImage, err := h.canAccessFile(c.Request.Context(), uid, row.ImageFileID, true)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	canWriteVideo, err := h.canAccessFile(c.Request.Context(), uid, row.VideoFileID, true)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if !canWriteImage || !canWriteVideo {
		c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
		return
	}

	err = h.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&model.LivePhoto{}, "id = ?", livePhotoID).Error; err != nil {
			return err
		}
		return tx.Create(&model.OperationLog{
			UserID:   &uid,
			FileID:   &row.ImageFileID,
			Action:   "live_photo_unbind",
			Status:   "success",
			Detail:   fmt.Sprintf("live_photo_id=%d image_file_id=%d video_file_id=%d", row.ID, row.ImageFileID, row.VideoFileID),
			ClientIP: c.ClientIP(),
		}).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "unbind live photo failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "unbound"})
}

// Detail 查询单个 Live Photo 的详情。
func (h *LivePhotoHandler) Detail(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	livePhotoID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || livePhotoID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid live photo id"})
		return
	}

	var row model.LivePhoto
	if err := h.DB.WithContext(c.Request.Context()).First(&row, "id = ?", livePhotoID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "live photo not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	// 详情查询要求当前用户对两个文件都具备读权限。
	canReadImage, err := h.canAccessFile(c.Request.Context(), uid, row.ImageFileID, false)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	canReadVideo, err := h.canAccessFile(c.Request.Context(), uid, row.VideoFileID, false)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if !canReadImage || !canReadVideo {
		c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
		return
	}

	var imageFile model.File
	if err := h.DB.WithContext(c.Request.Context()).First(&imageFile, "id = ? AND is_deleted = 0", row.ImageFileID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "image file not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	var videoFile model.File
	if err := h.DB.WithContext(c.Request.Context()).First(&videoFile, "id = ? AND is_deleted = 0", row.VideoFileID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "video file not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"item": gin.H{
			"id":            row.ID,
			"image_file_id": row.ImageFileID,
			"video_file_id": row.VideoFileID,
			"create_time":   row.CreateTime,
			"image_file":    toSimpleFileView(imageFile),
			"video_file":    toSimpleFileView(videoFile),
		},
	})
}

// List 分页查询 Live Photo 列表，支持按目录过滤。
func (h *LivePhotoHandler) List(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)

	var q LivePhotoListQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid query"})
		return
	}
	if q.ParentID < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid parent_id"})
		return
	}
	if q.Page <= 0 {
		q.Page = 1
	}
	if q.Limit <= 0 {
		q.Limit = 20
	}
	if q.Limit > 100 {
		q.Limit = 100
	}
	offset := (q.Page - 1) * q.Limit

	// 列表查询同时校验图片文件与视频文件的可读权限。
	base := h.DB.WithContext(c.Request.Context()).
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	var rows []model.LivePhoto
	if err := base.Select("lp.*").Order("lp.id DESC").Limit(q.Limit).Offset(offset).Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	// 批量加载关联文件信息，减少 N+1 查询。
	fileIDs := make([]int64, 0, len(rows)*2)
	for _, row := range rows {
		fileIDs = append(fileIDs, row.ImageFileID, row.VideoFileID)
	}

	fileMap := map[int64]model.File{}
	if len(fileIDs) > 0 {
		var files []model.File
		if err := h.DB.WithContext(c.Request.Context()).Where("id IN ? AND is_deleted = 0", uniqueInt64(fileIDs)).Find(&files).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		for _, f := range files {
			fileMap[f.ID] = f
		}
	}

	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		imageFile, okImage := fileMap[row.ImageFileID]
		videoFile, okVideo := fileMap[row.VideoFileID]
		if !okImage || !okVideo {
			continue
		}
		items = append(items, gin.H{
			"id":            row.ID,
			"image_file_id": row.ImageFileID,
			"video_file_id": row.VideoFileID,
			"create_time":   row.CreateTime,
			"image_file":    toSimpleFileView(imageFile),
			"video_file":    toSimpleFileView(videoFile),
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"items": items,
		"pagination": gin.H{
			"page":  q.Page,
			"limit": q.Limit,
			"total": total,
		},
	})
}

// GetByFileID 根据文件 ID 反查其所属的 Live Photo。
func (h *LivePhotoHandler) GetByFileID(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	fileID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || fileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid file id"})
		return
	}

	canRead, err := h.canAccessFile(c.Request.Context(), uid, fileID, false)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if !canRead {
		c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
		return
	}

	var row model.LivePhoto
	err = h.DB.WithContext(c.Request.Context()).
		Where("image_file_id = ? OR video_file_id = ?", fileID, fileID).
		Order("id DESC").
		First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusOK, gin.H{"item": nil})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	// 反查时同样校验组合中另一个文件是否可读，避免越权泄露。
	otherID := row.ImageFileID
	role := "video"
	if row.ImageFileID == fileID {
		otherID = row.VideoFileID
		role = "image"
	}
	canReadOther, err := h.canAccessFile(c.Request.Context(), uid, otherID, false)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if !canReadOther {
		c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
		return
	}

	var imageFile model.File
	if err := h.DB.WithContext(c.Request.Context()).First(&imageFile, "id = ? AND is_deleted = 0", row.ImageFileID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "image file not found"})
		return
	}
	var videoFile model.File
	if err := h.DB.WithContext(c.Request.Context()).First(&videoFile, "id = ? AND is_deleted = 0", row.VideoFileID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "video file not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"item": gin.H{
			"id":            row.ID,
			"file_role":     role,
			"image_file_id": row.ImageFileID,
			"video_file_id": row.VideoFileID,
			"create_time":   row.CreateTime,
			"image_file":    toSimpleFileView(imageFile),
			"video_file":    toSimpleFileView(videoFile),
		},
	})
}

// AutoBind 按同目录+同名主干规则自动识别并绑定 Live Photo。
func (h *LivePhotoHandler) AutoBind(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)

	var req AutoBindLivePhotoRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	if req.ParentID == nil && len(req.FileIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "parent_id or file_ids is required"})
		return
	}

	// 加载候选文件（仅可写文件，避免越权绑定）。
	candidates, err := h.loadAutoBindCandidates(c.Request.Context(), uid, req)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "permission denied") {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if len(candidates) == 0 {
		c.JSON(http.StatusOK, gin.H{
			"summary":       gin.H{"total_candidates": 0, "created": 0, "skipped": 0},
			"created_items": []gin.H{},
			"skipped_items": []gin.H{},
		})
		return
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

	// 查询候选文件中已经绑定的记录，避免重复绑定。
	candidateIDs := make([]int64, 0, len(candidates))
	for _, f := range candidates {
		candidateIDs = append(candidateIDs, f.ID)
	}

	boundImage := make(map[int64]bool)
	boundVideo := make(map[int64]bool)
	if len(candidateIDs) > 0 {
		var existed []model.LivePhoto
		if err := h.DB.WithContext(c.Request.Context()).
			Where("image_file_id IN ? OR video_file_id IN ?", candidateIDs, candidateIDs).
			Find(&existed).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		for _, row := range existed {
			boundImage[row.ImageFileID] = true
			boundVideo[row.VideoFileID] = true
		}
	}

	createdItems := make([]gin.H, 0)
	skippedItems := make([]gin.H, 0)

	err = h.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		for key, g := range groups {
			if len(g.images) == 0 || len(g.videos) == 0 {
				skippedItems = append(skippedItems, gin.H{"key": key, "reason": "pair not found"})
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
					skippedItems = append(skippedItems, gin.H{
						"image_file_id": img.ID,
						"video_file_id": vid.ID,
						"reason":        "already bound",
					})
					continue
				}

				row := model.LivePhoto{ImageFileID: img.ID, VideoFileID: vid.ID}
				if err := tx.Create(&row).Error; err != nil {
					if strings.Contains(strings.ToLower(err.Error()), "duplicate") || strings.Contains(strings.ToLower(err.Error()), "1062") {
						skippedItems = append(skippedItems, gin.H{
							"image_file_id": img.ID,
							"video_file_id": vid.ID,
							"reason":        "already bound",
						})
						continue
					}
					return err
				}

				boundImage[img.ID] = true
				boundVideo[vid.ID] = true
				createdItems = append(createdItems, gin.H{
					"id":            row.ID,
					"image_file_id": row.ImageFileID,
					"video_file_id": row.VideoFileID,
					"create_time":   row.CreateTime,
				})
			}
		}

		detail := fmt.Sprintf("auto_bind created=%d skipped=%d", len(createdItems), len(skippedItems))
		if len(detail) > 500 {
			detail = detail[:500]
		}
		return tx.Create(&model.OperationLog{
			UserID:   &uid,
			Action:   "live_photo_auto_bind",
			Status:   "success",
			Detail:   detail,
			ClientIP: c.ClientIP(),
		}).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "auto bind failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"summary": gin.H{
			"total_candidates": len(candidates),
			"created":          len(createdItems),
			"skipped":          len(skippedItems),
		},
		"created_items": createdItems,
		"skipped_items": skippedItems,
	})
}

// canAccessFile 复用 file + file_permission 的权限模型判断文件访问权限。
func (h *LivePhotoHandler) canAccessFile(ctx context.Context, userID int64, fileID int64, requireWrite bool) (bool, error) {
	var f model.File
	err := h.DB.WithContext(ctx).First(&f, "id = ? AND is_deleted = 0", fileID).Error
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
	err = h.DB.WithContext(ctx).
		Model(&model.FilePermission{}).
		Where("file_id = ? AND user_id = ? AND permission IN ?", fileID, userID, permissions).
		Count(&count).Error
	if err != nil {
		return false, err
	}

	return count > 0, nil
}

// loadLivePhotoFiles 校验文件类型并返回图片文件和视频文件。
func (h *LivePhotoHandler) loadLivePhotoFiles(ctx context.Context, imageFileID int64, videoFileID int64) (model.File, model.File, error) {
	var imageFile model.File
	if err := h.DB.WithContext(ctx).First(&imageFile, "id = ? AND is_deleted = 0", imageFileID).Error; err != nil {
		return model.File{}, model.File{}, err
	}
	var videoFile model.File
	if err := h.DB.WithContext(ctx).First(&videoFile, "id = ? AND is_deleted = 0", videoFileID).Error; err != nil {
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

// isLivePhotoImageExt 判断 Live Photo 支持的图片扩展名。
func isLivePhotoImageExt(ext string) bool {
	allowed := map[string]bool{
		"jpg":  true,
		"jpeg": true,
		"heic": true,
		"heif": true,
	}
	return allowed[ext]
}

// isLivePhotoVideoExt 判断 Live Photo 支持的视频扩展名。
func isLivePhotoVideoExt(ext string) bool {
	allowed := map[string]bool{
		"mov": true,
	}
	return allowed[ext]
}

// toSimpleFileView 返回文件基础信息，便于前端展示。
func toSimpleFileView(f model.File) gin.H {
	return gin.H{
		"id":          f.ID,
		"filename":    f.Filename,
		"parent_id":   f.ParentID,
		"file_type":   f.FileType,
		"size":        f.Size,
		"user_id":     f.UserID,
		"create_time": f.CreateTime,
		"update_time": f.UpdateTime,
	}
}

// loadAutoBindCandidates 根据请求加载自动绑定候选文件。
func (h *LivePhotoHandler) loadAutoBindCandidates(ctx context.Context, userID int64, req AutoBindLivePhotoRequest) ([]model.File, error) {
	if len(req.FileIDs) > 0 {
		ids := uniqueInt64(req.FileIDs)
		for _, id := range ids {
			if id <= 0 {
				return nil, fmt.Errorf("invalid file id")
			}
			canWrite, err := h.canAccessFile(ctx, userID, id, true)
			if err != nil {
				return nil, err
			}
			if !canWrite {
				return nil, fmt.Errorf("permission denied")
			}
		}

		var rows []model.File
		if err := h.DB.WithContext(ctx).Where("id IN ? AND is_deleted = 0 AND is_folder = 0", ids).Order("parent_id ASC, filename ASC, id ASC").Find(&rows).Error; err != nil {
			return nil, err
		}
		return rows, nil
	}

	if req.ParentID != nil {
		if *req.ParentID < 0 {
			return nil, fmt.Errorf("invalid parent_id")
		}
		if *req.ParentID > 0 {
			canWrite, err := h.canAccessFile(ctx, userID, *req.ParentID, true)
			if err != nil {
				return nil, err
			}
			if !canWrite {
				return nil, fmt.Errorf("permission denied")
			}
		}

		query := h.DB.WithContext(ctx).
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

// buildAutoBindKey 生成自动配对分组键：parent_id + 文件名主干（去扩展名）。
func buildAutoBindKey(parentID int64, filename string) string {
	base := strings.TrimSpace(strings.TrimSuffix(filename, filepath.Ext(filename)))
	if base == "" {
		return ""
	}
	return fmt.Sprintf("%d::%s", parentID, strings.ToLower(base))
}
