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
	"cloudenest_go/internal/service"
	"cloudenest_go/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// LivePhotoHandler 负责 Live Photo 绑定关系的管理接口。
type LivePhotoHandler struct {
	DB  *gorm.DB
	Svc *service.LivePhotoService
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

	res, err := h.Svc.Bind(c.Request.Context(), uid, req.ImageFileID, req.VideoFileID, c.ClientIP())
	if err != nil {
		if errors.Is(err, service.ErrLivePhotoPermissionDenied) {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
			return
		}
		if strings.Contains(strings.ToLower(err.Error()), "already exists") {
			c.JSON(http.StatusConflict, gin.H{"error": "live photo relation already exists"})
			return
		}
		if strings.Contains(strings.ToLower(err.Error()), "folder cannot be bound") ||
			strings.Contains(strings.ToLower(err.Error()), "image file type not supported") ||
			strings.Contains(strings.ToLower(err.Error()), "video file type not supported") {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "bind live photo failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"item": gin.H{
			"id":            res.Row.ID,
			"image_file_id": res.Row.ImageFileID,
			"video_file_id": res.Row.VideoFileID,
			"create_time":   res.Row.CreateTime,
			"image_file":    toSimpleFileView(res.ImageFile),
			"video_file":    toSimpleFileView(res.VideoFile),
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

	err = h.Svc.Unbind(c.Request.Context(), uid, livePhotoID, c.ClientIP())
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "live photo not found"})
			return
		}
		if errors.Is(err, service.ErrLivePhotoPermissionDenied) {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
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

	res, err := h.Svc.Detail(c.Request.Context(), uid, livePhotoID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "live photo not found"})
			return
		}
		if errors.Is(err, service.ErrLivePhotoPermissionDenied) {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"item": gin.H{
			"id":            res.Row.ID,
			"image_file_id": res.Row.ImageFileID,
			"video_file_id": res.Row.VideoFileID,
			"create_time":   res.Row.CreateTime,
			"image_file":    toSimpleFileView(res.ImageFile),
			"video_file":    toSimpleFileView(res.VideoFile),
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
	res, err := h.Svc.List(c.Request.Context(), uid, service.LivePhotoListInput{ParentID: q.ParentID, Page: q.Page, Limit: q.Limit})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	items := make([]gin.H, 0, len(res.Items))
	for _, item := range res.Items {
		items = append(items, gin.H{
			"id":            item.Row.ID,
			"image_file_id": item.Row.ImageFileID,
			"video_file_id": item.Row.VideoFileID,
			"create_time":   item.Row.CreateTime,
			"image_file":    toSimpleFileView(item.ImageFile),
			"video_file":    toSimpleFileView(item.VideoFile),
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"items": items,
		"pagination": gin.H{
			"page":  q.Page,
			"limit": q.Limit,
			"total": res.Total,
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

	res, err := h.Svc.GetByFileID(c.Request.Context(), uid, fileID)
	if err != nil {
		if errors.Is(err, service.ErrLivePhotoPermissionDenied) {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "image file not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if res == nil {
		c.JSON(http.StatusOK, gin.H{"item": nil})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"item": gin.H{
			"id":            res.Row.ID,
			"file_role":     res.FileRole,
			"image_file_id": res.Row.ImageFileID,
			"video_file_id": res.Row.VideoFileID,
			"create_time":   res.Row.CreateTime,
			"image_file":    toSimpleFileView(res.ImageFile),
			"video_file":    toSimpleFileView(res.VideoFile),
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

	res, err := h.Svc.AutoBind(c.Request.Context(), uid, service.LivePhotoAutoBindInput{ParentID: req.ParentID, FileIDs: req.FileIDs}, c.ClientIP())
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "permission denied") || errors.Is(err, service.ErrLivePhotoPermissionDenied) {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "auto bind failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"summary": gin.H{
			"total_candidates": res.TotalCandidates,
			"created":          len(res.CreatedItems),
			"skipped":          len(res.SkippedItems),
		},
		"created_items": res.CreatedItems,
		"skipped_items": res.SkippedItems,
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
