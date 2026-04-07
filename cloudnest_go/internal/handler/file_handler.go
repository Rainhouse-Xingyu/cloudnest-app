package handler

import (
	"archive/zip"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cloudenest_go/internal/config"
	"cloudenest_go/internal/middleware"
	"cloudenest_go/model"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	_ "image/gif"
	_ "image/png"
)

// FileHandler handles file system related APIs.
type FileHandler struct {
	DB         *gorm.DB
	RDB        *redis.Client
	Cfg        config.Config
	HTTPClient *http.Client
}

type autoAnalyzeLLMResult struct {
	Tags        []string `json:"tags"`
	Description string   `json:"description"`
}

type CreateFolderRequest struct {
	ParentID int64  `json:"parent_id"`
	Name     string `json:"name" binding:"required"`
}

type RenameFileRequest struct {
	Name string `json:"name" binding:"required"`
}

type InitUploadRequest struct {
	UploadID    string `json:"upload_id"`
	ParentID    int64  `json:"parent_id"`
	Filename    string `json:"filename" binding:"required"`
	TotalSize   int64  `json:"total_size" binding:"required"`
	ChunkSize   int    `json:"chunk_size"`
	TotalChunks int    `json:"total_chunks"`
}

type CompleteUploadRequest struct {
	UploadID string `json:"upload_id" binding:"required"`
	ParentID int64  `json:"parent_id"`
}

type MoveOrCopyRequest struct {
	FileIDs        []int64 `json:"file_ids" binding:"required"`
	TargetParentID int64   `json:"target_parent_id"`
}

type BatchDeleteRequest struct {
	FileIDs []int64 `json:"file_ids" binding:"required"`
}

type BatchDownloadRequest struct {
	FileIDs []int64 `json:"file_ids" binding:"required"`
}

type CreateShareRequest struct {
	ExpireHours *int `json:"expire_hours"`
}

type OperationLogQuery struct {
	Action    string `form:"action"`
	Status    string `form:"status"`
	StartTime string `form:"start_time"`
	EndTime   string `form:"end_time"`
	Page      int    `form:"page"`
	Limit     int    `form:"limit"`
	All       int    `form:"all"`
}

type FileSearchQuery struct {
	ParentID  int64  `form:"parent_id"`
	Name      string `form:"name"`
	FileType  string `form:"file_type"`
	StartTime string `form:"start_time"`
	EndTime   string `form:"end_time"`
	Page      int    `form:"page"`
	Limit     int    `form:"limit"`
}

const (
	chunkBaseDir   = "data/chunks"
	storageBaseDir = "data/storage"
	thumbBaseDir   = "data/thumbnails"

	defaultUploadRateBytesPerSec int64 = 2 * 1024 * 1024
	minUploadRateKbps            int64 = 64
	maxUploadRateKbps            int64 = 10240

	defaultDownloadRateBytesPerSec int64 = 2 * 1024 * 1024
	minDownloadRateKbps            int64 = 64
	maxDownloadRateKbps            int64 = 10240

	shareRateWindow        = 60 * time.Second
	shareInfoRateLimit     = 60
	shareDownloadRateLimit = 20
)

func (h *FileHandler) List(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	parentID, err := parseInt64WithDefault(c.Query("parent_id"), 0)
	if err != nil || parentID < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid parent_id"})
		return
	}

	if parentID > 0 {
		canRead, err := h.canAccessFile(c.Request.Context(), uid, parentID, false)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		if !canRead {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
	}

	var rows []model.File
	err = h.DB.WithContext(c.Request.Context()).
		Table("file f").
		Select("f.*").
		Joins("LEFT JOIN file_permission fp ON fp.file_id = f.id AND fp.user_id = ?", uid).
		Where("f.parent_id = ? AND f.is_deleted = 0", parentID).
		Where("f.user_id = ? OR fp.permission IN ?", uid, []string{"read", "write"}).
		Order("f.is_folder DESC, f.filename ASC").
		Find(&rows).Error
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"items": rows})
}

func (h *FileHandler) CreateFolder(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)

	var req CreateFolderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "folder name is required"})
		return
	}
	if req.ParentID < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid parent_id"})
		return
	}

	if req.ParentID > 0 {
		canWrite, err := h.canAccessFile(c.Request.Context(), uid, req.ParentID, true)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		if !canWrite {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}

		var parent model.File
		if err := h.DB.WithContext(c.Request.Context()).First(&parent, "id = ? AND is_deleted = 0", req.ParentID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "parent not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		if parent.IsFolder != 1 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "parent is not folder"})
			return
		}
	}

	var existed int64
	err := h.DB.WithContext(c.Request.Context()).
		Model(&model.File{}).
		Where("parent_id = ? AND filename = ? AND is_deleted = 0", req.ParentID, req.Name).
		Count(&existed).Error
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if existed > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "name already exists in this folder"})
		return
	}

	folder := model.File{
		UserID:   uid,
		Filename: req.Name,
		ParentID: req.ParentID,
		IsFolder: 1,
	}
	if err := h.DB.WithContext(c.Request.Context()).Create(&folder).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create folder failed"})
		return
	}
	_ = h.logOperation(c.Request.Context(), uid, &folder.ID, "create_folder", "success", fmt.Sprintf("parent_id=%d", req.ParentID), c.ClientIP())

	c.JSON(http.StatusOK, gin.H{"item": folder})
}

func (h *FileHandler) Rename(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	fileID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || fileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid file id"})
		return
	}

	var req RenameFileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}

	canWrite, err := h.canAccessFile(c.Request.Context(), uid, fileID, true)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if !canWrite {
		c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
		return
	}

	var f model.File
	if err := h.DB.WithContext(c.Request.Context()).First(&f, "id = ? AND is_deleted = 0", fileID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	var existed int64
	err = h.DB.WithContext(c.Request.Context()).
		Model(&model.File{}).
		Where("parent_id = ? AND filename = ? AND is_deleted = 0 AND id <> ?", f.ParentID, req.Name, f.ID).
		Count(&existed).Error
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if existed > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "name already exists in this folder"})
		return
	}

	if err := h.DB.WithContext(c.Request.Context()).Model(&f).Update("filename", req.Name).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "rename failed"})
		return
	}
	f.Filename = req.Name
	_ = h.logOperation(c.Request.Context(), uid, &f.ID, "rename", "success", "renamed file", c.ClientIP())

	c.JSON(http.StatusOK, gin.H{"item": f})
}

func (h *FileHandler) Delete(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	fileID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || fileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid file id"})
		return
	}

	canWrite, err := h.canAccessFile(c.Request.Context(), uid, fileID, true)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if !canWrite {
		c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
		return
	}

	var f model.File
	if err := h.DB.WithContext(c.Request.Context()).First(&f, "id = ? AND is_deleted = 0", fileID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	if f.IsFolder == 1 {
		var childCount int64
		err := h.DB.WithContext(c.Request.Context()).
			Model(&model.File{}).
			Where("parent_id = ? AND is_deleted = 0", fileID).
			Count(&childCount).Error
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		if childCount > 0 {
			c.JSON(http.StatusConflict, gin.H{"error": "folder is not empty"})
			return
		}
	}

	err = h.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&f).Update("is_deleted", 1).Error; err != nil {
			return err
		}
		// 文件删除后同步清理 Live Photo 关系，避免悬挂绑定。
		if err := h.cleanupLivePhotoByFileIDs(tx, []int64{f.ID}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete failed"})
		return
	}
	_ = h.logOperation(c.Request.Context(), uid, &f.ID, "delete", "success", "moved to recycle bin", c.ClientIP())

	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

// InitUpload 初始化分片上传任务，支持续传场景。
func (h *FileHandler) InitUpload(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)

	var req InitUploadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	req.Filename = strings.TrimSpace(req.Filename)
	if req.Filename == "" || req.TotalSize <= 0 || req.ParentID < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid upload parameters"})
		return
	}

	// 分片大小优先使用请求值；未传时读取 system_setting；都没有则回落 4MB。
	if req.ChunkSize <= 0 {
		if v, ok, err := h.getSystemSettingInt64(c.Request.Context(), "upload_default_chunk_size_mb"); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		} else if ok && v > 0 {
			req.ChunkSize = int(v * 1024 * 1024)
		}
	}
	if req.ChunkSize <= 0 {
		req.ChunkSize = 4 * 1024 * 1024
	}

	// total_chunks 未传时自动按 total_size/chunk_size 计算。
	calculatedTotalChunks := int((req.TotalSize + int64(req.ChunkSize) - 1) / int64(req.ChunkSize))
	if req.TotalChunks <= 0 {
		req.TotalChunks = calculatedTotalChunks
	}
	if req.TotalChunks <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid total_chunks"})
		return
	}

	if req.ParentID > 0 {
		canWrite, err := h.canAccessFile(c.Request.Context(), uid, req.ParentID, true)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		if !canWrite {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
	}

	if req.UploadID != "" {
		// 客户端传入 upload_id 时，直接查询任务并返回已上传分片，便于断点续传。
		task, chunkIndexes, err := h.getUploadTaskAndChunks(c.Request.Context(), uid, req.UploadID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "upload task not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"upload_id":       task.UploadID,
			"uploaded_chunks": chunkIndexes,
			"uploaded_count":  task.UploadedChunks,
			"status":          task.Status,
		})
		return
	}

	uploadID := generateUploadID(uid)
	uidCopy := uid
	totalSizeCopy := req.TotalSize
	chunkSizeCopy := req.ChunkSize
	totalChunksCopy := req.TotalChunks
	task := model.UploadTask{
		UploadID:    uploadID,
		UserID:      &uidCopy,
		Filename:    req.Filename,
		TotalSize:   &totalSizeCopy,
		ChunkSize:   &chunkSizeCopy,
		TotalChunks: &totalChunksCopy,
		Status:      0,
	}
	if err := h.DB.WithContext(c.Request.Context()).Create(&task).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create upload task failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"upload_id":       uploadID,
		"uploaded_chunks": []int{},
		"uploaded_count":  0,
		"status":          0,
	})
}

// UploadChunk 保存单个分片到本地并更新分片状态。
func (h *FileHandler) UploadChunk(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	uploadID := strings.TrimSpace(c.PostForm("upload_id"))
	chunkIndexRaw := strings.TrimSpace(c.PostForm("chunk_index"))
	chunkMD5 := strings.ToLower(strings.TrimSpace(c.PostForm("chunk_md5")))

	if uploadID == "" || chunkIndexRaw == "" || chunkMD5 == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "upload_id, chunk_index and chunk_md5 are required"})
		return
	}

	chunkIndex, err := strconv.Atoi(chunkIndexRaw)
	if err != nil || chunkIndex < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid chunk_index"})
		return
	}

	var task model.UploadTask
	if err := h.DB.WithContext(c.Request.Context()).Where("upload_id = ?", uploadID).First(&task).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "upload task not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if task.UserID == nil || *task.UserID != uid {
		c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
		return
	}
	if task.Status == 1 {
		c.JSON(http.StatusConflict, gin.H{"error": "upload task already completed"})
		return
	}
	if task.TotalChunks != nil && chunkIndex >= *task.TotalChunks {
		c.JSON(http.StatusBadRequest, gin.H{"error": "chunk_index out of range"})
		return
	}

	chunkFile, err := c.FormFile("chunk")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "chunk file is required"})
		return
	}

	chunkDir := filepath.Join(chunkBaseDir, uploadID)
	if err := os.MkdirAll(chunkDir, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create chunk directory failed"})
		return
	}

	chunkPath := filepath.Join(chunkDir, fmt.Sprintf("%d.part", chunkIndex))
	uploadRate := h.getUploadRateBytesPerSec(c)
	if err := h.saveUploadedFileWithRateLimit(c, chunkFile, chunkPath, uploadRate); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save chunk failed"})
		return
	}

	actualMD5, err := computeFileMD5Hex(chunkPath)
	if err != nil {
		_ = os.Remove(chunkPath)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "verify chunk md5 failed"})
		return
	}
	if !strings.EqualFold(actualMD5, chunkMD5) {
		_ = os.Remove(chunkPath)
		c.JSON(http.StatusBadRequest, gin.H{"error": "chunk md5 mismatch"})
		return
	}

	// 使用事务保证分片记录与上传进度一致。
	err = h.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var row model.UploadChunk
		err := tx.Where("upload_id = ? AND chunk_index = ?", uploadID, chunkIndex).First(&row).Error
		if err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			chunkSizeVal := chunkFile.Size
			row = model.UploadChunk{
				UploadID:   uploadID,
				ChunkIndex: chunkIndex,
				ChunkSize:  &chunkSizeVal,
				ChunkPath:  chunkPath,
				Status:     1,
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		} else {
			chunkSizeVal := chunkFile.Size
			if err := tx.Model(&row).Updates(map[string]interface{}{
				"chunk_size": chunkSizeVal,
				"chunk_path": chunkPath,
				"status":     1,
			}).Error; err != nil {
				return err
			}
		}

		var doneCount int64
		if err := tx.Model(&model.UploadChunk{}).Where("upload_id = ? AND status = 1", uploadID).Count(&doneCount).Error; err != nil {
			return err
		}

		return tx.Model(&model.UploadTask{}).Where("id = ?", task.ID).Update("uploaded_chunks", int(doneCount)).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update chunk status failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "chunk uploaded"})
}

// CompleteUpload 合并所有分片，写入 storage/file 记录并完成上传任务。
func (h *FileHandler) CompleteUpload(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)

	var req CompleteUploadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	req.UploadID = strings.TrimSpace(req.UploadID)
	if req.UploadID == "" || req.ParentID < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid parameters"})
		return
	}

	if req.ParentID > 0 {
		canWrite, err := h.canAccessFile(c.Request.Context(), uid, req.ParentID, true)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		if !canWrite {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
	}

	var task model.UploadTask
	if err := h.DB.WithContext(c.Request.Context()).Where("upload_id = ?", req.UploadID).First(&task).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "upload task not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if task.UserID == nil || *task.UserID != uid {
		c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
		return
	}
	if task.Status == 1 {
		c.JSON(http.StatusConflict, gin.H{"error": "upload task already completed"})
		return
	}
	if task.TotalChunks == nil || task.ChunkSize == nil || task.TotalSize == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "upload task metadata incomplete"})
		return
	}

	var chunks []model.UploadChunk
	if err := h.DB.WithContext(c.Request.Context()).
		Where("upload_id = ? AND status = 1", req.UploadID).
		Order("chunk_index ASC").
		Find(&chunks).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if len(chunks) != *task.TotalChunks {
		c.JSON(http.StatusConflict, gin.H{"error": "chunks not fully uploaded"})
		return
	}

	storagePath, mergedSize, fileHash, err := mergeChunks(req.UploadID, task.Filename, *task.TotalChunks)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "merge chunks failed"})
		return
	}

	var createdFile model.File
	err = h.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		storageID, err := ensureStorage(tx, fileHash, storagePath, mergedSize)
		if err != nil {
			return err
		}

		// 若文件名冲突，自动追加时间戳，避免覆盖已有元数据记录。
		finalName, err := ensureUniqueFilename(tx, req.ParentID, task.Filename)
		if err != nil {
			return err
		}

		fileType := strings.TrimPrefix(strings.ToLower(filepath.Ext(finalName)), ".")
		createdFile = model.File{
			UserID:    uid,
			Filename:  finalName,
			ParentID:  req.ParentID,
			IsFolder:  0,
			FileType:  fileType,
			Size:      mergedSize,
			StorageID: &storageID,
		}
		if err := tx.Create(&createdFile).Error; err != nil {
			return err
		}

		if err := tx.Model(&model.UploadTask{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
			"status":          1,
			"uploaded_chunks": *task.TotalChunks,
		}).Error; err != nil {
			return err
		}

		if err := tx.Where("upload_id = ?", req.UploadID).Delete(&model.UploadChunk{}).Error; err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "complete upload failed"})
		return
	}

	_ = os.RemoveAll(filepath.Join(chunkBaseDir, req.UploadID))
	_ = h.logOperation(c.Request.Context(), uid, &createdFile.ID, "upload_complete", "success", fmt.Sprintf("upload_id=%s", req.UploadID), c.ClientIP())
	// 上传成功后触发 AI 异步分析：自动生成 tags/description/ocr_text 并写入 ai_metadata。
	h.triggerAsyncAutoAnalyze(createdFile, uid, c.ClientIP())
	// 上传完成后尝试自动与同目录同名文件配对绑定 Live Photo（无阻塞失败）。
	_ = h.tryAutoBindLivePhotoForFile(c.Request.Context(), uid, createdFile, c.ClientIP())

	c.JSON(http.StatusOK, gin.H{"item": createdFile})
}

// triggerAsyncAutoAnalyze 在后台执行文件自动分析，不阻塞上传接口响应。
func (h *FileHandler) triggerAsyncAutoAnalyze(file model.File, userID int64, clientIP string) {
	go func(f model.File, uid int64, ip string) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()

		ai := &AIHandler{DB: h.DB, Cfg: h.Cfg, HTTPClient: h.HTTPClient}
		_ = ai.createAITask(ctx, uid, f.ID, "auto_analyze", "processing", "queued")

		tags, desc, ocrText, err := h.buildAutoAnalyzeResult(ctx, ai, f)
		if err != nil {
			_ = ai.createAITask(ctx, uid, f.ID, "auto_analyze", "failed", err.Error())
			_ = h.logOperation(ctx, uid, &f.ID, "ai_auto_analyze", "failed", truncateForLog(err.Error(), 500), ip)
			return
		}

		if _, err := ai.upsertAIMetadata(ctx, f.ID, tags, desc, ocrText); err != nil {
			_ = ai.createAITask(ctx, uid, f.ID, "auto_analyze", "failed", err.Error())
			_ = h.logOperation(ctx, uid, &f.ID, "ai_auto_analyze", "failed", truncateForLog(err.Error(), 500), ip)
			return
		}

		result := fmt.Sprintf("tags=%s | desc_len=%d | ocr_len=%d", tags, len(desc), len(ocrText))
		_ = ai.createAITask(ctx, uid, f.ID, "auto_analyze", "success", truncateForLog(result, 500))
		_ = h.logOperation(ctx, uid, &f.ID, "ai_auto_analyze", "success", truncateForLog(result, 500), ip)
	}(file, userID, clientIP)
}

// buildAutoAnalyzeResult 生成 ai_metadata 三个核心字段：tags/description/ocr_text。
func (h *FileHandler) buildAutoAnalyzeResult(ctx context.Context, ai *AIHandler, f model.File) (string, string, string, error) {
	if f.StorageID == nil {
		return "", "", "", fmt.Errorf("storage id missing")
	}

	var storage model.Storage
	if err := h.DB.WithContext(ctx).First(&storage, "id = ?", *f.StorageID).Error; err != nil {
		return "", "", "", err
	}

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(f.Filename), "."))
	baseTags := []string{"auto", ext}
	desc := "上传后自动分析完成"
	ocrText := ""

	if isImageExt(ext) || isLivePhotoImageExt(ext) {
		baseTags = append(baseTags, "图片", "照片")
		exifMeta := extractEXIFInfo(storage.FilePath)
		if exifTag := strings.TrimSpace(formatEXIFLocationTag(exifMeta)); exifTag != "" {
			baseTags = append(baseTags, exifTag)
		}

		raw, readErr := os.ReadFile(storage.FilePath)
		if readErr == nil && len(raw) > 0 {
			prompt := "请执行OCR识别，完整提取图片中的可见文字，尽量保留原始顺序与换行。"
			if text, err := ai.callOllamaGenerate(ctx, prompt, []string{base64.StdEncoding.EncodeToString(raw)}); err == nil {
				ocrText = strings.TrimSpace(text)
			}
		}

		llmTags, llmDesc := h.autoGenerateTagsAndDescription(ctx, ai, f.Filename, ext, ocrText, exifMeta.CapturedAt, formatEXIFLocationTag(exifMeta))
		baseTags = append(baseTags, llmTags...)
		if llmDesc != "" {
			desc = llmDesc
		} else {
			if exifMeta.CapturedAt != "" {
				desc = desc + "；拍摄时间：" + exifMeta.CapturedAt
			}
			if strings.TrimSpace(ocrText) != "" {
				desc = desc + "；已提取图片文字"
			}
		}
	} else {
		body, truncated, err := extractDocumentText(storage.FilePath, ext, aiReadMaxBytes)
		if err == nil {
			ocrText = strings.TrimSpace(body)
			baseTags = append(baseTags, "文档", "文件")
			llmTags, llmDesc := h.autoGenerateTagsAndDescription(ctx, ai, f.Filename, ext, ocrText, "", "")
			baseTags = append(baseTags, llmTags...)
			if llmDesc != "" {
				desc = llmDesc
			} else {
				desc = "上传后自动分析完成：已提取文档主要内容"
			}
			if truncated {
				desc += "（内容已截断）"
			}
		} else {
			baseTags = append(baseTags, "文件")
			desc = "上传后自动分析完成：当前类型暂不支持全文提取"
		}
	}

	ocrText = strings.TrimSpace(ocrText)
	if len(ocrText) > 100000 {
		ocrText = ocrText[:100000]
	}

	return mergeTags(strings.Join(baseTags, ",")), strings.TrimSpace(desc), ocrText, nil
}

// autoGenerateTagsAndDescription 调用 LLM 自动生成标签与描述。
func (h *FileHandler) autoGenerateTagsAndDescription(ctx context.Context, ai *AIHandler, filename string, ext string, text string, capturedAt string, exifLocation string) ([]string, string) {
	if ai == nil {
		return nil, ""
	}

	snippet := strings.TrimSpace(text)
	if len(snippet) > 3000 {
		snippet = snippet[:3000]
	}

	prompt := "你是 CloudNest 文件标注助手。请根据文件信息输出 JSON：" +
		"{\"tags\":[\"标签1\",\"标签2\"],\"description\":\"一句话描述\"}。" +
		"要求：tags 3-8 个，优先地点/场景/主题/实体词（如海边、风景、石家庄、天津、英国），去掉空泛词；description 用中文简洁描述文件核心内容。" +
		"只输出 JSON，不要任何额外文字。" +
		"\n文件名=" + filename +
		"\n扩展名=" + ext +
		"\n拍摄时间=" + capturedAt +
		"\nEXIF位置=" + exifLocation +
		"\n识别文本=" + snippet

	raw, err := ai.callOllamaGenerate(ctx, prompt, nil)
	if err != nil {
		return nil, ""
	}

	obj := extractJSONObject(raw)
	if obj == "" {
		return nil, ""
	}

	var out autoAnalyzeLLMResult
	if err := json.Unmarshal([]byte(obj), &out); err != nil {
		return nil, ""
	}

	cleanTags := make([]string, 0, len(out.Tags))
	for _, t := range out.Tags {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		cleanTags = append(cleanTags, t)
	}

	return cleanTags, strings.TrimSpace(out.Description)
}

func truncateForLog(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max]
}

// UploadProgress 查询上传任务进度，返回已上传分片和缺失分片索引。
func (h *FileHandler) UploadProgress(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	uploadID := strings.TrimSpace(c.Query("upload_id"))
	if uploadID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "upload_id is required"})
		return
	}

	task, uploadedChunks, err := h.getUploadTaskAndChunks(c.Request.Context(), uid, uploadID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "upload task not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	totalChunks := 0
	if task.TotalChunks != nil {
		totalChunks = *task.TotalChunks
	}

	missingChunks := make([]int, 0)
	if totalChunks > 0 {
		uploadedSet := make(map[int]bool, len(uploadedChunks))
		for _, idx := range uploadedChunks {
			uploadedSet[idx] = true
		}
		for i := 0; i < totalChunks; i++ {
			if !uploadedSet[i] {
				missingChunks = append(missingChunks, i)
			}
		}
	}

	progress := 0.0
	if totalChunks > 0 {
		progress = float64(len(uploadedChunks)) * 100 / float64(totalChunks)
	}

	c.JSON(http.StatusOK, gin.H{
		"upload_id":       task.UploadID,
		"status":          task.Status,
		"uploaded_count":  len(uploadedChunks),
		"total_chunks":    totalChunks,
		"uploaded_chunks": uploadedChunks,
		"missing_chunks":  missingChunks,
		"progress":        progress,
	})
}

// Download 下载单个文件，返回附件流给客户端。
func (h *FileHandler) Download(c *gin.Context) {
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

	var f model.File
	if err := h.DB.WithContext(c.Request.Context()).First(&f, "id = ? AND is_deleted = 0", fileID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if f.IsFolder == 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "target is folder, use folder zip download"})
		return
	}
	if f.StorageID == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "storage record not found"})
		return
	}

	var storage model.Storage
	if err := h.DB.WithContext(c.Request.Context()).First(&storage, "id = ?", *f.StorageID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "storage record not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if _, err := os.Stat(storage.FilePath); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "physical file not found"})
		return
	}
	rate := h.getDownloadRateBytesPerSec(c)
	if err := h.sendFileWithRateLimit(c, storage.FilePath, f.Filename, rate); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "download failed"})
		return
	}
}

// DownloadFolderZip 将文件夹递归打包为 zip 并下载。
func (h *FileHandler) DownloadFolderZip(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	folderID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || folderID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid file id"})
		return
	}

	canRead, err := h.canAccessFile(c.Request.Context(), uid, folderID, false)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if !canRead {
		c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
		return
	}

	var folder model.File
	if err := h.DB.WithContext(c.Request.Context()).First(&folder, "id = ? AND is_deleted = 0", folderID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "folder not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if folder.IsFolder != 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "target is not folder"})
		return
	}

	zipPath, err := h.createFolderZip(c.Request.Context(), folder)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create zip failed"})
		return
	}
	defer os.Remove(zipPath)

	zipName := sanitizeFileName(folder.Filename) + ".zip"
	rate := h.getDownloadRateBytesPerSec(c)
	if err := h.sendFileWithRateLimit(c, zipPath, zipName, rate); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "download failed"})
		return
	}
}

// BatchDelete 批量删除文件或文件夹（移动到回收站）。
func (h *FileHandler) BatchDelete(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)

	var req BatchDeleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	ids := uniqueInt64(req.FileIDs)
	if len(ids) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file_ids is required"})
		return
	}

	deletedIDs := make([]int64, 0, len(ids))
	failed := make(map[string]string)

	for _, fileID := range ids {
		if fileID <= 0 {
			failed[strconv.FormatInt(fileID, 10)] = "invalid file id"
			continue
		}

		canWrite, err := h.canAccessFile(c.Request.Context(), uid, fileID, true)
		if err != nil {
			failed[strconv.FormatInt(fileID, 10)] = "db error"
			continue
		}
		if !canWrite {
			failed[strconv.FormatInt(fileID, 10)] = "permission denied"
			continue
		}

		var f model.File
		if err := h.DB.WithContext(c.Request.Context()).First(&f, "id = ? AND is_deleted = 0", fileID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				failed[strconv.FormatInt(fileID, 10)] = "file not found"
			} else {
				failed[strconv.FormatInt(fileID, 10)] = "db error"
			}
			continue
		}

		if f.IsFolder == 1 {
			var childCount int64
			err := h.DB.WithContext(c.Request.Context()).
				Model(&model.File{}).
				Where("parent_id = ? AND is_deleted = 0", fileID).
				Count(&childCount).Error
			if err != nil {
				failed[strconv.FormatInt(fileID, 10)] = "db error"
				continue
			}
			if childCount > 0 {
				failed[strconv.FormatInt(fileID, 10)] = "folder is not empty"
				continue
			}
		}

		err = h.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&f).Update("is_deleted", 1).Error; err != nil {
				return err
			}
			// 批量删除时同步清理 Live Photo 关系，保证关系一致性。
			if err := h.cleanupLivePhotoByFileIDs(tx, []int64{f.ID}); err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			failed[strconv.FormatInt(fileID, 10)] = "delete failed"
			continue
		}

		deletedIDs = append(deletedIDs, fileID)
	}

	_ = h.logOperation(c.Request.Context(), uid, nil, "batch_delete", "success", fmt.Sprintf("requested=%d,deleted=%d", len(ids), len(deletedIDs)), c.ClientIP())

	c.JSON(http.StatusOK, gin.H{
		"deleted_ids": deletedIDs,
		"failed":      failed,
	})
}

// BatchDownload 批量下载文件和目录，返回一个总 zip 包。
func (h *FileHandler) BatchDownload(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)

	var req BatchDownloadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	ids := uniqueInt64(req.FileIDs)
	if len(ids) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file_ids is required"})
		return
	}

	tmpFile, err := os.CreateTemp("", "cloudnest-batch-*.zip")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create zip failed"})
		return
	}
	zipPath := tmpFile.Name()
	defer os.Remove(zipPath)

	zw := zip.NewWriter(tmpFile)

	addFileToZip := func(entryName string, filePath string) error {
		r, err := os.Open(filePath)
		if err != nil {
			return err
		}
		defer r.Close()

		w, err := zw.Create(entryName)
		if err != nil {
			return err
		}
		_, err = io.Copy(w, r)
		return err
	}

	usedNames := make(map[string]int)
	nextEntryName := func(raw string) string {
		name := sanitizeFileName(raw)
		if name == "" {
			name = "item"
		}
		if n, ok := usedNames[name]; !ok {
			usedNames[name] = 1
			return name
		} else {
			usedNames[name] = n + 1
			ext := filepath.Ext(name)
			base := strings.TrimSuffix(name, ext)
			return fmt.Sprintf("%s(%d)%s", base, n, ext)
		}
	}

	for _, fileID := range ids {
		if fileID <= 0 {
			continue
		}

		canRead, err := h.canAccessFile(c.Request.Context(), uid, fileID, false)
		if err != nil || !canRead {
			continue
		}

		var f model.File
		if err := h.DB.WithContext(c.Request.Context()).First(&f, "id = ? AND is_deleted = 0", fileID).Error; err != nil {
			continue
		}

		if f.IsFolder == 1 {
			folderZipPath, err := h.createFolderZip(c.Request.Context(), f)
			if err != nil {
				continue
			}
			entryName := nextEntryName(f.Filename) + ".zip"
			_ = addFileToZip(entryName, folderZipPath)
			_ = os.Remove(folderZipPath)
			continue
		}

		if f.StorageID == nil {
			continue
		}

		var storage model.Storage
		if err := h.DB.WithContext(c.Request.Context()).First(&storage, "id = ?", *f.StorageID).Error; err != nil {
			continue
		}
		if _, err := os.Stat(storage.FilePath); err != nil {
			continue
		}

		entryName := nextEntryName(f.Filename)
		_ = addFileToZip(entryName, storage.FilePath)
	}

	if err := zw.Close(); err != nil {
		_ = tmpFile.Close()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create zip failed"})
		return
	}
	if err := tmpFile.Close(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create zip failed"})
		return
	}

	rate := h.getDownloadRateBytesPerSec(c)
	if err := h.sendFileWithRateLimit(c, zipPath, "batch_download.zip", rate); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "download failed"})
		return
	}

	_ = h.logOperation(c.Request.Context(), uid, nil, "batch_download", "success", fmt.Sprintf("count=%d", len(ids)), c.ClientIP())
}

// StreamVideo 视频流媒体播放接口，支持 Range 请求和拖拽进度。
func (h *FileHandler) StreamVideo(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	fileID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || fileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid file id"})
		return
	}

	f, storage, err := h.getAccessibleFileAndStorage(c.Request.Context(), uid, fileID, false)
	if err != nil {
		h.handleFileAccessError(c, err)
		return
	}
	if f.IsFolder == 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "folder cannot be streamed"})
		return
	}

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(f.Filename), "."))
	if !isVideoExt(ext) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file is not a video"})
		return
	}

	file, err := os.Open(storage.FilePath)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "physical file not found"})
		return
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "read file info failed"})
		return
	}

	contentType := mime.TypeByExtension(filepath.Ext(f.Filename))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// ServeContent 内置处理 Range，能自动返回 206 Partial Content。
	c.Header("Accept-Ranges", "bytes")
	c.Header("Content-Type", contentType)
	http.ServeContent(c.Writer, c.Request, f.Filename, stat.ModTime(), file)
}

// Thumbnail 生成并缓存图片缩略图，重复请求直接命中缓存。
func (h *FileHandler) Thumbnail(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	fileID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || fileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid file id"})
		return
	}

	f, storage, err := h.getAccessibleFileAndStorage(c.Request.Context(), uid, fileID, false)
	if err != nil {
		h.handleFileAccessError(c, err)
		return
	}
	if f.IsFolder == 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "folder has no thumbnail"})
		return
	}

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(f.Filename), "."))
	if !isImageExt(ext) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file is not an image"})
		return
	}

	thumbSize := 256
	if raw := strings.TrimSpace(c.Query("size")); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr == nil && parsed >= 64 && parsed <= 1024 {
			thumbSize = parsed
		}
	}

	thumbPath, err := h.buildOrGetThumbnail(storage.FilePath, f.ID, f.UpdateTime.Unix(), thumbSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "generate thumbnail failed"})
		return
	}

	c.Header("Cache-Control", "public, max-age=86400")
	c.File(thumbPath)
}

// SearchFiles 按名称/类型/时间进行检索和筛选，支持分页。
func (h *FileHandler) SearchFiles(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)

	var q FileSearchQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid query"})
		return
	}
	q.Name = strings.TrimSpace(q.Name)
	q.FileType = strings.ToLower(strings.TrimSpace(q.FileType))
	q.StartTime = strings.TrimSpace(q.StartTime)
	q.EndTime = strings.TrimSpace(q.EndTime)
	if q.Page <= 0 {
		q.Page = 1
	}
	if q.Limit <= 0 || q.Limit > 200 {
		q.Limit = 50
	}

	dbq := h.DB.WithContext(c.Request.Context()).
		Table("file f").
		Select("f.*").
		Joins("LEFT JOIN file_permission fp ON fp.file_id = f.id AND fp.user_id = ?", uid).
		Where("f.is_deleted = 0").
		Where("f.user_id = ? OR fp.permission IN ?", uid, []string{"read", "write"})

	if q.ParentID > 0 {
		dbq = dbq.Where("f.parent_id = ?", q.ParentID)
	}
	if q.Name != "" {
		dbq = dbq.Where("f.filename LIKE ?", "%"+q.Name+"%")
	}
	if q.FileType != "" {
		dbq = dbq.Where("LOWER(f.file_type) = ?", q.FileType)
	}
	if q.StartTime != "" {
		startAt, err := parseFlexibleTime(q.StartTime)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid start_time"})
			return
		}
		dbq = dbq.Where("f.update_time >= ?", startAt)
	}
	if q.EndTime != "" {
		endAt, err := parseFlexibleTime(q.EndTime)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid end_time"})
			return
		}
		dbq = dbq.Where("f.update_time <= ?", endAt)
	}

	var total int64
	if err := h.DB.WithContext(c.Request.Context()).
		Table("(?) as t", dbq).
		Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	offset := (q.Page - 1) * q.Limit
	var rows []model.File
	if err := dbq.Order("f.is_folder DESC, f.filename ASC").Limit(q.Limit).Offset(offset).Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"items": rows,
		"pagination": gin.H{
			"page":  q.Page,
			"limit": q.Limit,
			"total": total,
		},
	})
}

// Preview 在线预览文件内容：
// 1) 文本类返回内容字符串；
// 2) 图片/PDF 以内联方式直接输出；
// 3) Office 文档采用基础预览策略（inline + 正确 MIME），便于客户端使用系统能力预览。
func (h *FileHandler) Preview(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	fileID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || fileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid file id"})
		return
	}

	f, storage, err := h.getAccessibleFileAndStorage(c.Request.Context(), uid, fileID, false)
	if err != nil {
		h.handleFileAccessError(c, err)
		return
	}
	if f.IsFolder == 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "folder cannot be previewed"})
		return
	}

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(f.Filename), "."))
	if isTextPreviewExt(ext) {
		const maxPreviewBytes = 1024 * 1024
		content, truncated, readErr := readTextFileWithLimit(storage.FilePath, maxPreviewBytes)
		if readErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "read file failed"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"file_id":   f.ID,
			"filename":  f.Filename,
			"type":      "text",
			"content":   content,
			"truncated": truncated,
		})
		return
	}

	if isInlinePreviewExt(ext) {
		c.Header("Content-Disposition", fmt.Sprintf("inline; filename=\"%s\"", sanitizeFileName(f.Filename)))
		c.File(storage.FilePath)
		return
	}

	if isOfficePreviewExt(ext) {
		mimeType := officeMimeType(ext)
		c.Header("Content-Disposition", fmt.Sprintf("inline; filename=\"%s\"", sanitizeFileName(f.Filename)))
		if mimeType != "" {
			c.Header("Content-Type", mimeType)
		}
		// 提示前端当前使用的是 Office 基础预览策略。
		c.Header("X-Preview-Strategy", "office-inline")
		c.File(storage.FilePath)
		return
	}

	c.JSON(http.StatusBadRequest, gin.H{"error": "file type not supported for preview"})
}

// CreateShare 创建分享链接，可设置过期小时数（0表示永不过期）。
func (h *FileHandler) CreateShare(c *gin.Context) {
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

	var req CreateShareRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		if !errors.Is(err, io.EOF) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
			return
		}
	}

	// 默认过期时间来自 system_setting，可被请求参数覆盖。
	expireHours := int64(0)
	if req.ExpireHours != nil {
		expireHours = int64(*req.ExpireHours)
	} else {
		if v, ok, err := h.getSystemSettingInt64(c.Request.Context(), "share_default_expire_hours"); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		} else if ok {
			expireHours = v
		}
	}

	if expireHours < 0 || expireHours > 24*365 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "expire_hours should be between 0 and 8760"})
		return
	}

	shareCode, err := generateShareCode(12)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "generate share code failed"})
		return
	}

	fileIDCopy := fileID
	uidCopy := uid
	var expireAt *time.Time
	if expireHours > 0 {
		t := time.Now().Add(time.Duration(expireHours) * time.Hour)
		expireAt = &t
	}

	share := model.Share{
		FileID:     &fileIDCopy,
		UserID:     &uidCopy,
		ShareCode:  shareCode,
		ExpireTime: expireAt,
	}
	if err := h.DB.WithContext(c.Request.Context()).Create(&share).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create share failed"})
		return
	}
	_ = h.logOperation(c.Request.Context(), uid, &fileID, "create_share", "success", fmt.Sprintf("share_code=%s", shareCode), c.ClientIP())

	c.JSON(http.StatusOK, gin.H{
		"share_code":  share.ShareCode,
		"expire_time": share.ExpireTime,
		"access_url":  fmt.Sprintf("/api/public/shares/%s", share.ShareCode),
	})
}

// ListShares 查看当前用户创建的分享链接列表。
func (h *FileHandler) ListShares(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)

	var shares []model.Share
	err := h.DB.WithContext(c.Request.Context()).
		Where("user_id = ?", uid).
		Order("create_time DESC").
		Find(&shares).Error
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"items": shares})
}

// ListOperationLogs 查询操作日志，默认仅返回当前用户，管理员可通过 all=1 查看全部。
func (h *FileHandler) ListOperationLogs(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	isAdmin, _ := c.Get(middleware.ContextIsAdminKey)
	isAdminBool, _ := isAdmin.(bool)

	var q OperationLogQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid query"})
		return
	}
	q.Action = strings.TrimSpace(q.Action)
	q.Status = strings.TrimSpace(q.Status)
	q.StartTime = strings.TrimSpace(q.StartTime)
	q.EndTime = strings.TrimSpace(q.EndTime)
	if q.Page <= 0 {
		q.Page = 1
	}
	if q.Limit <= 0 || q.Limit > 200 {
		q.Limit = 50
	}

	dbq := h.DB.WithContext(c.Request.Context()).Model(&model.OperationLog{})
	if !(isAdminBool && q.All == 1) {
		dbq = dbq.Where("user_id = ?", uid)
	}
	if q.Action != "" {
		dbq = dbq.Where("action = ?", q.Action)
	}
	if q.Status != "" {
		dbq = dbq.Where("status = ?", q.Status)
	}
	if q.StartTime != "" {
		startAt, err := parseFlexibleTime(q.StartTime)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid start_time"})
			return
		}
		dbq = dbq.Where("create_time >= ?", startAt)
	}
	if q.EndTime != "" {
		endAt, err := parseFlexibleTime(q.EndTime)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid end_time"})
			return
		}
		dbq = dbq.Where("create_time <= ?", endAt)
	}

	var total int64
	if err := dbq.Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	offset := (q.Page - 1) * q.Limit

	var rows []model.OperationLog
	if err := dbq.Order("id DESC").Limit(q.Limit).Offset(offset).Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"items": rows,
		"pagination": gin.H{
			"page":  q.Page,
			"limit": q.Limit,
			"total": total,
		},
	})
}

// ExportOperationLogs 导出操作日志为 CSV，便于汇报或排查问题。
func (h *FileHandler) ExportOperationLogs(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	isAdmin, _ := c.Get(middleware.ContextIsAdminKey)
	isAdminBool, _ := isAdmin.(bool)

	var q OperationLogQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid query"})
		return
	}
	q.Action = strings.TrimSpace(q.Action)
	q.Status = strings.TrimSpace(q.Status)
	q.StartTime = strings.TrimSpace(q.StartTime)
	q.EndTime = strings.TrimSpace(q.EndTime)

	maxExportRows := 5000
	if q.Limit <= 0 || q.Limit > maxExportRows {
		q.Limit = maxExportRows
	}

	dbq := h.DB.WithContext(c.Request.Context()).Model(&model.OperationLog{})
	if !(isAdminBool && q.All == 1) {
		dbq = dbq.Where("user_id = ?", uid)
	}
	if q.Action != "" {
		dbq = dbq.Where("action = ?", q.Action)
	}
	if q.Status != "" {
		dbq = dbq.Where("status = ?", q.Status)
	}
	if q.StartTime != "" {
		startAt, err := parseFlexibleTime(q.StartTime)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid start_time"})
			return
		}
		dbq = dbq.Where("create_time >= ?", startAt)
	}
	if q.EndTime != "" {
		endAt, err := parseFlexibleTime(q.EndTime)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid end_time"})
			return
		}
		dbq = dbq.Where("create_time <= ?", endAt)
	}

	var rows []model.OperationLog
	if err := dbq.Order("id DESC").Limit(q.Limit).Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	fileName := fmt.Sprintf("operation_logs_%s.csv", time.Now().Format("20060102150405"))
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", fileName))

	w := csv.NewWriter(c.Writer)
	defer w.Flush()

	_ = w.Write([]string{"id", "user_id", "file_id", "action", "status", "detail", "client_ip", "create_time"})
	for _, row := range rows {
		userID := ""
		fileID := ""
		if row.UserID != nil {
			userID = strconv.FormatInt(*row.UserID, 10)
		}
		if row.FileID != nil {
			fileID = strconv.FormatInt(*row.FileID, 10)
		}
		_ = w.Write([]string{
			strconv.FormatInt(row.ID, 10),
			userID,
			fileID,
			row.Action,
			row.Status,
			row.Detail,
			row.ClientIP,
			row.CreateTime.Format("2006-01-02 15:04:05"),
		})
	}
}

// RevokeShare 撤销分享链接，撤销后链接不可访问。
func (h *FileHandler) RevokeShare(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	code := strings.TrimSpace(c.Param("code"))
	if code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid share code"})
		return
	}

	res := h.DB.WithContext(c.Request.Context()).Where("share_code = ? AND user_id = ?", code, uid).Delete(&model.Share{})
	if res.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if res.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "share not found"})
		return
	}
	_ = h.logOperation(c.Request.Context(), uid, nil, "revoke_share", "success", fmt.Sprintf("share_code=%s", code), c.ClientIP())

	c.JSON(http.StatusOK, gin.H{"message": "share revoked"})
}

// PublicShareInfo 公开查看分享信息（无需登录）。
func (h *FileHandler) PublicShareInfo(c *gin.Context) {
	shareCode := strings.TrimSpace(c.Param("code"))
	ok, err := h.checkShareAccessRateLimit(c.Request.Context(), shareCode, c.ClientIP(), "info", shareInfoRateLimit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "rate limiter error"})
		return
	}
	if !ok {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many requests"})
		return
	}

	share, file, err := h.resolveValidShare(c.Request.Context(), shareCode)
	if err != nil {
		h.handleShareError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"share_code":  share.ShareCode,
		"expire_time": share.ExpireTime,
		"file": gin.H{
			"id":          file.ID,
			"filename":    file.Filename,
			"is_folder":   file.IsFolder,
			"file_type":   file.FileType,
			"size":        file.Size,
			"create_time": file.CreateTime,
		},
	})
}

// PublicShareDownload 通过分享码下载文件或目录压缩包（无需登录）。
func (h *FileHandler) PublicShareDownload(c *gin.Context) {
	shareCode := strings.TrimSpace(c.Param("code"))
	ok, err := h.checkShareAccessRateLimit(c.Request.Context(), shareCode, c.ClientIP(), "download", shareDownloadRateLimit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "rate limiter error"})
		return
	}
	if !ok {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many requests"})
		return
	}

	_, file, err := h.resolveValidShare(c.Request.Context(), shareCode)
	if err != nil {
		h.handleShareError(c, err)
		return
	}

	if file.IsFolder == 1 {
		zipPath, err := h.createFolderZip(c.Request.Context(), file)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "create zip failed"})
			return
		}
		defer os.Remove(zipPath)
		rate := h.getDownloadRateBytesPerSec(c)
		if err := h.sendFileWithRateLimit(c, zipPath, sanitizeFileName(file.Filename)+".zip", rate); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "download failed"})
			return
		}
		return
	}

	if file.StorageID == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "storage record not found"})
		return
	}

	var storage model.Storage
	if err := h.DB.WithContext(c.Request.Context()).First(&storage, "id = ?", *file.StorageID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "storage record not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if _, err := os.Stat(storage.FilePath); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "physical file not found"})
		return
	}
	rate := h.getDownloadRateBytesPerSec(c)
	if err := h.sendFileWithRateLimit(c, storage.FilePath, file.Filename, rate); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "download failed"})
		return
	}
}

// Move 批量移动文件或文件夹到目标目录。
func (h *FileHandler) Move(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)

	var req MoveOrCopyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	if len(req.FileIDs) == 0 || req.TargetParentID < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid move parameters"})
		return
	}

	if req.TargetParentID > 0 {
		canWrite, err := h.canAccessFile(c.Request.Context(), uid, req.TargetParentID, true)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		if !canWrite {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}

		var target model.File
		if err := h.DB.WithContext(c.Request.Context()).First(&target, "id = ? AND is_deleted = 0", req.TargetParentID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "target folder not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		if target.IsFolder != 1 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "target is not folder"})
			return
		}
	}

	updated := make([]int64, 0, len(req.FileIDs))
	err := h.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		for _, fileID := range uniqueInt64(req.FileIDs) {
			if fileID <= 0 {
				return fmt.Errorf("invalid file id")
			}

			canWrite, err := h.canAccessFile(tx.Statement.Context, uid, fileID, true)
			if err != nil {
				return err
			}
			if !canWrite {
				return fmt.Errorf("permission denied")
			}

			var src model.File
			if err := tx.First(&src, "id = ? AND is_deleted = 0", fileID).Error; err != nil {
				return err
			}

			if src.ID == req.TargetParentID {
				return fmt.Errorf("cannot move to itself")
			}

			// 防止将目录移动到其后代目录下，避免形成循环。
			if src.IsFolder == 1 && req.TargetParentID > 0 {
				isDescendant, err := h.isDescendantFolder(tx.Statement.Context, req.TargetParentID, src.ID)
				if err != nil {
					return err
				}
				if isDescendant {
					return fmt.Errorf("cannot move folder into its descendant")
				}
			}

			finalName, err := ensureUniqueFilename(tx, req.TargetParentID, src.Filename)
			if err != nil {
				return err
			}

			if err := tx.Model(&src).Updates(map[string]interface{}{
				"parent_id": req.TargetParentID,
				"filename":  finalName,
			}).Error; err != nil {
				return err
			}

			updated = append(updated, src.ID)
		}
		return nil
	})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "permission denied") {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
		if strings.Contains(strings.ToLower(err.Error()), "cannot move") {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "move failed"})
		return
	}
	_ = h.logOperation(c.Request.Context(), uid, nil, "move", "success", fmt.Sprintf("count=%d,target_parent=%d", len(updated), req.TargetParentID), c.ClientIP())

	c.JSON(http.StatusOK, gin.H{"updated_ids": updated})
}

// Copy 批量复制文件或文件夹，文件内容复用同一 storage 记录。
func (h *FileHandler) Copy(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)

	var req MoveOrCopyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	if len(req.FileIDs) == 0 || req.TargetParentID < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid copy parameters"})
		return
	}

	if req.TargetParentID > 0 {
		canWrite, err := h.canAccessFile(c.Request.Context(), uid, req.TargetParentID, true)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		if !canWrite {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
	}

	newIDs := make([]int64, 0)
	err := h.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		for _, fileID := range uniqueInt64(req.FileIDs) {
			if fileID <= 0 {
				return fmt.Errorf("invalid file id")
			}

			canRead, err := h.canAccessFile(tx.Statement.Context, uid, fileID, false)
			if err != nil {
				return err
			}
			if !canRead {
				return fmt.Errorf("permission denied")
			}

			var src model.File
			if err := tx.First(&src, "id = ? AND is_deleted = 0", fileID).Error; err != nil {
				return err
			}

			copiedID, err := h.copyNode(tx, uid, src, req.TargetParentID)
			if err != nil {
				return err
			}
			newIDs = append(newIDs, copiedID)
		}
		return nil
	})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "permission denied") {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "copy failed"})
		return
	}
	_ = h.logOperation(c.Request.Context(), uid, nil, "copy", "success", fmt.Sprintf("count=%d,target_parent=%d", len(newIDs), req.TargetParentID), c.ClientIP())

	c.JSON(http.StatusOK, gin.H{"new_ids": newIDs})
}

// RecycleBin 列出当前用户回收站中的文件和文件夹。
func (h *FileHandler) RecycleBin(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)

	var rows []model.File
	err := h.DB.WithContext(c.Request.Context()).
		Where("user_id = ? AND is_deleted = 1", uid).
		Order("update_time DESC").
		Find(&rows).Error
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"items": rows})
}

// Restore 恢复回收站中的条目到原目录（如重名则自动改名）。
func (h *FileHandler) Restore(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	fileID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || fileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid file id"})
		return
	}

	var restored model.File
	err = h.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&restored, "id = ? AND is_deleted = 1", fileID).Error; err != nil {
			return err
		}
		if restored.UserID != uid {
			return fmt.Errorf("permission denied")
		}

		finalName, err := ensureUniqueFilename(tx, restored.ParentID, restored.Filename)
		if err != nil {
			return err
		}

		if err := tx.Model(&restored).Updates(map[string]interface{}{
			"is_deleted": 0,
			"filename":   finalName,
		}).Error; err != nil {
			return err
		}
		restored.Filename = finalName
		restored.IsDeleted = 0
		return nil
	})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "permission denied") {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "recycle item not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "restore failed"})
		return
	}
	_ = h.logOperation(c.Request.Context(), uid, &restored.ID, "restore", "success", "restore from recycle bin", c.ClientIP())
	// 恢复后尝试按同目录同名规则自动重建 Live Photo 绑定（无阻塞失败）。
	_ = h.tryAutoBindLivePhotoForFile(c.Request.Context(), uid, restored, c.ClientIP())

	c.JSON(http.StatusOK, gin.H{"item": restored})
}

// PermanentDelete 彻底删除条目（会级联删除子目录元数据，不删除去重存储物理文件）。
func (h *FileHandler) PermanentDelete(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	fileID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || fileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid file id"})
		return
	}

	err = h.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var root model.File
		if err := tx.First(&root, "id = ? AND is_deleted = 1", fileID).Error; err != nil {
			return err
		}
		if root.UserID != uid {
			return fmt.Errorf("permission denied")
		}

		ids, err := h.collectDescendantIDs(tx.Statement.Context, root.ID)
		if err != nil {
			return err
		}
		ids = append(ids, root.ID)
		// 彻底删除前清理所有相关 Live Photo 关系。
		if err := h.cleanupLivePhotoByFileIDs(tx, ids); err != nil {
			return err
		}

		return tx.Where("id IN ?", ids).Delete(&model.File{}).Error
	})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "permission denied") {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "recycle item not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "permanent delete failed"})
		return
	}
	_ = h.logOperation(c.Request.Context(), uid, &fileID, "permanent_delete", "success", "delete from recycle bin", c.ClientIP())

	c.JSON(http.StatusOK, gin.H{"message": "permanently deleted"})
}

func (h *FileHandler) canAccessFile(ctx context.Context, userID int64, fileID int64, requireWrite bool) (bool, error) {
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

func parseInt64WithDefault(raw string, defaultValue int64) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultValue, nil
	}
	return strconv.ParseInt(raw, 10, 64)
}

func generateUploadID(userID int64) string {
	return fmt.Sprintf("u%d_%d", userID, time.Now().UnixNano())
}

// cleanupLivePhotoByFileIDs 根据文件 ID 清理 Live Photo 绑定关系。
func (h *FileHandler) cleanupLivePhotoByFileIDs(tx *gorm.DB, fileIDs []int64) error {
	ids := uniqueInt64(fileIDs)
	if len(ids) == 0 {
		return nil
	}
	return tx.Where("image_file_id IN ? OR video_file_id IN ?", ids, ids).Delete(&model.LivePhoto{}).Error
}

// tryAutoBindLivePhotoForFile 尝试将当前文件与同目录同名异类型文件自动配对绑定。
func (h *FileHandler) tryAutoBindLivePhotoForFile(ctx context.Context, userID int64, file model.File, clientIP string) error {
	if file.IsFolder == 1 || file.IsDeleted == 1 {
		return nil
	}

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(file.Filename), "."))
	isImage := isLivePhotoImageExt(ext)
	isVideo := isLivePhotoVideoExt(ext)
	if !isImage && !isVideo {
		return nil
	}

	base := strings.TrimSpace(strings.TrimSuffix(file.Filename, filepath.Ext(file.Filename)))
	if base == "" {
		return nil
	}

	var siblingFiles []model.File
	if err := h.DB.WithContext(ctx).
		Where("parent_id = ? AND is_deleted = 0 AND is_folder = 0 AND id <> ?", file.ParentID, file.ID).
		Order("id ASC").
		Find(&siblingFiles).Error; err != nil {
		return err
	}

	var matched *model.File
	for i := range siblingFiles {
		s := siblingFiles[i]
		sBase := strings.TrimSpace(strings.TrimSuffix(s.Filename, filepath.Ext(s.Filename)))
		if !strings.EqualFold(sBase, base) {
			continue
		}
		sExt := strings.ToLower(strings.TrimPrefix(filepath.Ext(s.Filename), "."))
		if isImage && isLivePhotoVideoExt(sExt) {
			matched = &s
			break
		}
		if isVideo && isLivePhotoImageExt(sExt) {
			matched = &s
			break
		}
	}
	if matched == nil {
		return nil
	}

	// 自动绑定要求对双方文件都具备写权限。
	canWriteSelf, err := h.canAccessFile(ctx, userID, file.ID, true)
	if err != nil || !canWriteSelf {
		return err
	}
	canWriteOther, err := h.canAccessFile(ctx, userID, matched.ID, true)
	if err != nil || !canWriteOther {
		return err
	}

	imageID := file.ID
	videoID := matched.ID
	if isVideo {
		imageID = matched.ID
		videoID = file.ID
	}

	return h.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var exists model.LivePhoto
		err := tx.Where("image_file_id = ? OR video_file_id = ?", imageID, videoID).First(&exists).Error
		if err == nil {
			return nil
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		row := model.LivePhoto{ImageFileID: imageID, VideoFileID: videoID}
		if err := tx.Create(&row).Error; err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "duplicate") || strings.Contains(strings.ToLower(err.Error()), "1062") {
				return nil
			}
			return err
		}

		detail := fmt.Sprintf("live_photo_id=%d image_file_id=%d video_file_id=%d", row.ID, row.ImageFileID, row.VideoFileID)
		if len(detail) > 500 {
			detail = detail[:500]
		}
		return tx.Create(&model.OperationLog{
			UserID:   &userID,
			FileID:   &row.ImageFileID,
			Action:   "live_photo_auto_bind",
			Status:   "success",
			Detail:   detail,
			ClientIP: strings.TrimSpace(clientIP),
		}).Error
	})
}

func (h *FileHandler) getUploadTaskAndChunks(ctx context.Context, userID int64, uploadID string) (model.UploadTask, []int, error) {
	var task model.UploadTask
	err := h.DB.WithContext(ctx).Where("upload_id = ?", uploadID).First(&task).Error
	if err != nil {
		return model.UploadTask{}, nil, err
	}
	if task.UserID == nil || *task.UserID != userID {
		return model.UploadTask{}, nil, gorm.ErrRecordNotFound
	}

	var rows []model.UploadChunk
	err = h.DB.WithContext(ctx).
		Where("upload_id = ? AND status = 1", uploadID).
		Order("chunk_index ASC").
		Find(&rows).Error
	if err != nil {
		return model.UploadTask{}, nil, err
	}

	chunkIndexes := make([]int, 0, len(rows))
	for _, row := range rows {
		chunkIndexes = append(chunkIndexes, row.ChunkIndex)
	}

	return task, chunkIndexes, nil
}

func mergeChunks(uploadID string, filename string, totalChunks int) (string, int64, string, error) {
	dateDir := time.Now().Format("200601")
	storageDir := filepath.Join(storageBaseDir, dateDir)
	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		return "", 0, "", err
	}

	outputName := fmt.Sprintf("%s_%s", uploadID, sanitizeFileName(filename))
	outputPath := filepath.Join(storageDir, outputName)
	outFile, err := os.Create(outputPath)
	if err != nil {
		return "", 0, "", err
	}
	defer outFile.Close()

	hasher := sha256.New()
	writer := io.MultiWriter(outFile, hasher)

	for i := 0; i < totalChunks; i++ {
		chunkPath := filepath.Join(chunkBaseDir, uploadID, fmt.Sprintf("%d.part", i))
		inFile, err := os.Open(chunkPath)
		if err != nil {
			return "", 0, "", err
		}
		if _, err := io.Copy(writer, inFile); err != nil {
			_ = inFile.Close()
			return "", 0, "", err
		}
		_ = inFile.Close()
	}

	stat, err := outFile.Stat()
	if err != nil {
		return "", 0, "", err
	}

	return outputPath, stat.Size(), hex.EncodeToString(hasher.Sum(nil)), nil
}

func ensureStorage(tx *gorm.DB, fileHash string, storagePath string, size int64) (int64, error) {
	var existing model.Storage
	err := tx.Where("file_hash = ?", fileHash).First(&existing).Error
	if err == nil {
		_ = os.Remove(storagePath)
		return existing.ID, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, err
	}

	sizeCopy := size
	storage := model.Storage{
		FileHash: fileHash,
		FilePath: storagePath,
		FileSize: &sizeCopy,
	}
	if err := tx.Create(&storage).Error; err != nil {
		return 0, err
	}
	return storage.ID, nil
}

func ensureUniqueFilename(tx *gorm.DB, parentID int64, baseName string) (string, error) {
	finalName := baseName
	for i := 0; i < 100; i++ {
		var count int64
		err := tx.Model(&model.File{}).
			Where("parent_id = ? AND filename = ? AND is_deleted = 0", parentID, finalName).
			Count(&count).Error
		if err != nil {
			return "", err
		}
		if count == 0 {
			return finalName, nil
		}
		finalName = appendNameSuffix(baseName, time.Now().Format("20060102150405"))
	}
	return "", fmt.Errorf("failed to allocate unique filename")
}

func appendNameSuffix(name string, suffix string) string {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	if ext == "" {
		return fmt.Sprintf("%s_%s", base, suffix)
	}
	return fmt.Sprintf("%s_%s%s", base, suffix, ext)
}

func sanitizeFileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "unnamed.bin"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", "..", "_", " ", "_")
	return replacer.Replace(name)
}

// copyNode 递归复制目录树，文件复用 storage_id 以减少重复存储。
func (h *FileHandler) copyNode(tx *gorm.DB, newOwnerID int64, src model.File, targetParentID int64) (int64, error) {
	finalName, err := ensureUniqueFilename(tx, targetParentID, src.Filename)
	if err != nil {
		return 0, err
	}

	newNode := model.File{
		UserID:         newOwnerID,
		Filename:       finalName,
		ParentID:       targetParentID,
		IsFolder:       src.IsFolder,
		FileType:       src.FileType,
		Size:           src.Size,
		StorageID:      src.StorageID,
		WorkspaceID:    src.WorkspaceID,
		CurrentVersion: src.CurrentVersion,
		IsDeleted:      0,
	}
	if err := tx.Create(&newNode).Error; err != nil {
		return 0, err
	}

	if src.IsFolder == 1 {
		var children []model.File
		if err := tx.Where("parent_id = ? AND is_deleted = 0", src.ID).Order("id ASC").Find(&children).Error; err != nil {
			return 0, err
		}
		for _, child := range children {
			if _, err := h.copyNode(tx, newOwnerID, child, newNode.ID); err != nil {
				return 0, err
			}
		}
	}

	return newNode.ID, nil
}

// isDescendantFolder 检查 candidateID 是否是 ancestorID 的后代目录。
func (h *FileHandler) isDescendantFolder(ctx context.Context, candidateID int64, ancestorID int64) (bool, error) {
	if candidateID <= 0 || ancestorID <= 0 {
		return false, nil
	}

	current := candidateID
	for current > 0 {
		if current == ancestorID {
			return true, nil
		}
		var f model.File
		err := h.DB.WithContext(ctx).Select("id", "parent_id").First(&f, "id = ? AND is_deleted = 0", current).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return false, nil
			}
			return false, err
		}
		if f.ParentID == current {
			return false, nil
		}
		current = f.ParentID
	}

	return false, nil
}

// collectDescendantIDs 收集某目录下所有后代节点的 ID。
func (h *FileHandler) collectDescendantIDs(ctx context.Context, rootID int64) ([]int64, error) {
	ids := make([]int64, 0)
	queue := []int64{rootID}

	for len(queue) > 0 {
		parentID := queue[0]
		queue = queue[1:]

		var children []model.File
		err := h.DB.WithContext(ctx).
			Select("id", "is_folder").
			Where("parent_id = ?", parentID).
			Find(&children).Error
		if err != nil {
			return nil, err
		}

		for _, child := range children {
			ids = append(ids, child.ID)
			if child.IsFolder == 1 {
				queue = append(queue, child.ID)
			}
		}
	}

	return ids, nil
}

func uniqueInt64(input []int64) []int64 {
	seen := make(map[int64]bool, len(input))
	result := make([]int64, 0, len(input))
	for _, v := range input {
		if seen[v] {
			continue
		}
		seen[v] = true
		result = append(result, v)
	}
	return result
}

// logOperation 写入操作日志，失败时不影响主流程。
func (h *FileHandler) logOperation(ctx context.Context, userID int64, fileID *int64, action string, status string, detail string, clientIP string) error {
	uid := userID
	row := model.OperationLog{
		UserID:   &uid,
		FileID:   fileID,
		Action:   strings.TrimSpace(action),
		Status:   strings.TrimSpace(status),
		Detail:   strings.TrimSpace(detail),
		ClientIP: strings.TrimSpace(clientIP),
	}
	if row.Action == "" {
		row.Action = "unknown"
	}
	if row.Status == "" {
		row.Status = "success"
	}
	return h.DB.WithContext(ctx).Create(&row).Error
}

func isTextPreviewExt(ext string) bool {
	textExts := map[string]bool{
		"txt": true, "md": true, "json": true, "xml": true, "yaml": true, "yml": true,
		"csv": true, "log": true, "go": true, "swift": true, "java": true, "py": true,
		"js": true, "ts": true, "html": true, "css": true, "sql": true,
	}
	return textExts[ext]
}

func isVideoExt(ext string) bool {
	videos := map[string]bool{
		"mp4": true, "mov": true, "m4v": true, "webm": true,
	}
	return videos[ext]
}

func isImageExt(ext string) bool {
	images := map[string]bool{
		"jpg": true, "jpeg": true, "png": true, "gif": true, "webp": true,
	}
	return images[ext]
}

func isInlinePreviewExt(ext string) bool {
	inlineExts := map[string]bool{
		"png": true, "jpg": true, "jpeg": true, "gif": true, "webp": true,
		"pdf": true,
	}
	return inlineExts[ext]
}

func isOfficePreviewExt(ext string) bool {
	officeExts := map[string]bool{
		"doc": true, "docx": true,
		"xls": true, "xlsx": true,
		"ppt": true, "pptx": true,
	}
	return officeExts[ext]
}

func officeMimeType(ext string) string {
	switch strings.ToLower(strings.TrimSpace(ext)) {
	case "doc":
		return "application/msword"
	case "docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case "xls":
		return "application/vnd.ms-excel"
	case "xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case "ppt":
		return "application/vnd.ms-powerpoint"
	case "pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	default:
		return ""
	}
}

func readTextFileWithLimit(path string, limit int64) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()

	buf := make([]byte, limit+1)
	n, err := f.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", false, err
	}

	truncated := int64(n) > limit
	if truncated {
		return string(buf[:limit]), true, nil
	}
	return string(buf[:n]), false, nil
}

// buildOrGetThumbnail 根据源文件生成并缓存缩略图。
func (h *FileHandler) buildOrGetThumbnail(sourcePath string, fileID int64, versionTag int64, size int) (string, error) {
	if err := os.MkdirAll(thumbBaseDir, 0o755); err != nil {
		return "", err
	}

	cacheName := fmt.Sprintf("thumb_%d_%d_%d.jpg", fileID, versionTag, size)
	cachePath := filepath.Join(thumbBaseDir, cacheName)
	if _, err := os.Stat(cachePath); err == nil {
		return cachePath, nil
	}

	srcFile, err := os.Open(sourcePath)
	if err != nil {
		return "", err
	}
	defer srcFile.Close()

	img, _, err := image.Decode(srcFile)
	if err != nil {
		return "", err
	}

	thumb := resizeToMaxEdge(img, size)
	outFile, err := os.Create(cachePath)
	if err != nil {
		return "", err
	}
	defer outFile.Close()

	if err := jpeg.Encode(outFile, thumb, &jpeg.Options{Quality: 82}); err != nil {
		return "", err
	}

	return cachePath, nil
}

// resizeToMaxEdge 将图片按比例缩放到指定最长边，避免原图过大导致预览缓慢。
func resizeToMaxEdge(src image.Image, maxEdge int) image.Image {
	b := src.Bounds()
	sw := b.Dx()
	sh := b.Dy()
	if sw <= 0 || sh <= 0 || maxEdge <= 0 {
		return src
	}

	if sw <= maxEdge && sh <= maxEdge {
		return src
	}

	var dw, dh int
	if sw >= sh {
		dw = maxEdge
		dh = sh * maxEdge / sw
	} else {
		dh = maxEdge
		dw = sw * maxEdge / sh
	}
	if dw <= 0 {
		dw = 1
	}
	if dh <= 0 {
		dh = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	// 先填充白底，避免透明图转 jpg 后出现黑底。
	draw.Draw(dst, dst.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	for y := 0; y < dh; y++ {
		sy := b.Min.Y + y*sh/dh
		for x := 0; x < dw; x++ {
			sx := b.Min.X + x*sw/dw
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	return dst
}

func generateShareCode(length int) (string, error) {
	const alphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
	if length <= 0 {
		length = 10
	}
	b := make([]byte, length)
	randBytes := make([]byte, length)
	if _, err := rand.Read(randBytes); err != nil {
		return "", err
	}
	for i := 0; i < length; i++ {
		b[i] = alphabet[int(randBytes[i])%len(alphabet)]
	}
	return string(b), nil
}

var errShareExpired = errors.New("share expired")

func (h *FileHandler) resolveValidShare(ctx context.Context, code string) (model.Share, model.File, error) {
	if code == "" {
		return model.Share{}, model.File{}, gorm.ErrRecordNotFound
	}

	var share model.Share
	if err := h.DB.WithContext(ctx).Where("share_code = ?", code).First(&share).Error; err != nil {
		return model.Share{}, model.File{}, err
	}
	if share.ExpireTime != nil && time.Now().After(*share.ExpireTime) {
		return model.Share{}, model.File{}, errShareExpired
	}
	if share.FileID == nil {
		return model.Share{}, model.File{}, gorm.ErrRecordNotFound
	}

	var file model.File
	if err := h.DB.WithContext(ctx).First(&file, "id = ? AND is_deleted = 0", *share.FileID).Error; err != nil {
		return model.Share{}, model.File{}, err
	}

	return share, file, nil
}

func (h *FileHandler) handleShareError(c *gin.Context, err error) {
	if errors.Is(err, errShareExpired) {
		c.JSON(http.StatusGone, gin.H{"error": "share expired"})
		return
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "share not found"})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
}

func (h *FileHandler) getAccessibleFileAndStorage(ctx context.Context, uid int64, fileID int64, requireWrite bool) (model.File, model.Storage, error) {
	canAccess, err := h.canAccessFile(ctx, uid, fileID, requireWrite)
	if err != nil {
		return model.File{}, model.Storage{}, err
	}
	if !canAccess {
		return model.File{}, model.Storage{}, fmt.Errorf("permission denied")
	}

	var f model.File
	if err := h.DB.WithContext(ctx).First(&f, "id = ? AND is_deleted = 0", fileID).Error; err != nil {
		return model.File{}, model.Storage{}, err
	}

	if f.StorageID == nil {
		return f, model.Storage{}, gorm.ErrRecordNotFound
	}

	var storage model.Storage
	if err := h.DB.WithContext(ctx).First(&storage, "id = ?", *f.StorageID).Error; err != nil {
		return model.File{}, model.Storage{}, err
	}
	if _, err := os.Stat(storage.FilePath); err != nil {
		return model.File{}, model.Storage{}, os.ErrNotExist
	}

	return f, storage, nil
}

func (h *FileHandler) handleFileAccessError(c *gin.Context, err error) {
	if strings.Contains(strings.ToLower(err.Error()), "permission denied") {
		c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
		return
	}
	if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, os.ErrNotExist) {
		c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
}

// parseFlexibleTime 支持多种时间格式，便于前端传参筛选日志。
func parseFlexibleTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	layouts := []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
	}
	for _, layout := range layouts {
		t, err := time.ParseInLocation(layout, raw, time.Local)
		if err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time format")
}

// checkShareAccessRateLimit 对公开分享接口做限流，防止短时间被刷。
func (h *FileHandler) checkShareAccessRateLimit(ctx context.Context, shareCode string, clientIP string, action string, maxInWindow int) (bool, error) {
	if h.RDB == nil {
		return true, nil
	}
	shareCode = strings.TrimSpace(shareCode)
	clientIP = strings.TrimSpace(clientIP)
	action = strings.TrimSpace(action)
	if shareCode == "" || action == "" || maxInWindow <= 0 {
		return true, nil
	}
	if clientIP == "" {
		clientIP = "unknown"
	}

	key := fmt.Sprintf("share:rate:%s:%s:%s", action, shareCode, clientIP)
	count, err := h.RDB.Incr(ctx, key).Result()
	if err != nil {
		return false, err
	}
	if count == 1 {
		if err := h.RDB.Expire(ctx, key, shareRateWindow).Err(); err != nil {
			return false, err
		}
	}

	return count <= int64(maxInWindow), nil
}

// getUploadRateBytesPerSec 获取上传限速值：query 参数优先，其次读取 system_setting。
func (h *FileHandler) getUploadRateBytesPerSec(c *gin.Context) int64 {
	rateKbpsRaw := strings.TrimSpace(c.Query("upload_rate_kbps"))
	uid := c.GetInt64(middleware.ContextUserIDKey)
	ctx := c.Request.Context()
	if rateKbpsRaw == "" {
		if v, ok, err := h.getSystemSettingInt64(ctx, "upload_speed_limit_kbps"); err == nil && ok && v > 0 {
			rateKbpsRaw = strconv.FormatInt(v, 10)
		}
		if inPeak, err := h.isInPeakWindow(ctx, uid); err == nil && inPeak {
			if v, ok, err := h.getSystemSettingInt64(ctx, "upload_peak_speed_limit_kbps"); err == nil && ok && v > 0 {
				rateKbpsRaw = strconv.FormatInt(v, 10)
			}
		}
	}
	if rateKbpsRaw == "" {
		return defaultUploadRateBytesPerSec
	}
	rateKbps, err := strconv.ParseInt(rateKbpsRaw, 10, 64)
	if err != nil {
		return defaultUploadRateBytesPerSec
	}
	if rateKbps < minUploadRateKbps {
		rateKbps = minUploadRateKbps
	}
	if rateKbps > maxUploadRateKbps {
		rateKbps = maxUploadRateKbps
	}
	return rateKbps * 1024
}

// saveUploadedFileWithRateLimit 以限速方式写入上传分片，防止上传突发占满带宽。
func (h *FileHandler) saveUploadedFileWithRateLimit(c *gin.Context, fh *multipart.FileHeader, dstPath string, bytesPerSec int64) error {
	src, err := fh.Open()
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	defer func() {
		_ = dst.Close()
	}()

	if bytesPerSec <= 0 {
		_, copyErr := io.Copy(dst, src)
		return copyErr
	}

	const chunkSize = 32 * 1024
	buf := make([]byte, chunkSize)
	start := time.Now()
	var writtenTotal int64

	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			w, writeErr := dst.Write(buf[:n])
			if writeErr != nil {
				return writeErr
			}
			writtenTotal += int64(w)

			elapsed := time.Since(start)
			expected := time.Duration(float64(writtenTotal) / float64(bytesPerSec) * float64(time.Second))
			if expected > elapsed {
				time.Sleep(expected - elapsed)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return readErr
		}

		if c.Request.Context().Err() != nil {
			return c.Request.Context().Err()
		}
	}

	return nil
}

// getSystemSettingInt64 读取整数配置项，返回值、是否存在、错误。
func (h *FileHandler) getSystemSettingInt64(ctx context.Context, key string) (int64, bool, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return 0, false, nil
	}

	var row model.SystemSetting
	err := h.DB.WithContext(ctx).Where("setting_key = ?", key).First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, false, nil
		}
		return 0, false, err
	}

	v := strings.TrimSpace(row.SettingValue)
	if v == "" {
		return 0, false, nil
	}
	n, parseErr := strconv.ParseInt(v, 10, 64)
	if parseErr != nil {
		return 0, false, nil
	}
	return n, true, nil
}

// getSystemSettingString 读取字符串配置项，返回值、是否存在、错误。
func (h *FileHandler) getSystemSettingString(ctx context.Context, key string) (string, bool, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", false, nil
	}

	var row model.SystemSetting
	err := h.DB.WithContext(ctx).Where("setting_key = ?", key).First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	return strings.TrimSpace(row.SettingValue), true, nil
}

// getDownloadRateBytesPerSec 获取下载限速值：query 参数优先，其次读取 system_setting。
func (h *FileHandler) getDownloadRateBytesPerSec(c *gin.Context) int64 {
	rateKbpsRaw := strings.TrimSpace(c.Query("rate_kbps"))
	uid := c.GetInt64(middleware.ContextUserIDKey)
	ctx := c.Request.Context()
	if rateKbpsRaw == "" {
		if v, ok, err := h.getSystemSettingInt64(ctx, "download_speed_limit_kbps"); err == nil && ok && v > 0 {
			rateKbpsRaw = strconv.FormatInt(v, 10)
		}
		if inPeak, err := h.isInPeakWindow(ctx, uid); err == nil && inPeak {
			if v, ok, err := h.getSystemSettingInt64(ctx, "download_peak_speed_limit_kbps"); err == nil && ok && v > 0 {
				rateKbpsRaw = strconv.FormatInt(v, 10)
			}
		}
	}
	if rateKbpsRaw == "" {
		return defaultDownloadRateBytesPerSec
	}
	rateKbps, err := strconv.ParseInt(rateKbpsRaw, 10, 64)
	if err != nil {
		return defaultDownloadRateBytesPerSec
	}
	if rateKbps < minDownloadRateKbps {
		rateKbps = minDownloadRateKbps
	}
	if rateKbps > maxDownloadRateKbps {
		rateKbps = maxDownloadRateKbps
	}
	return rateKbps * 1024
}

// isInPeakWindow 判断当前请求是否处于高峰时段。
func (h *FileHandler) isInPeakWindow(ctx context.Context, uid int64) (bool, error) {
	enabled, start, end, err := h.resolvePeakWindowSetting(ctx, uid)
	if err != nil {
		return false, err
	}
	if !enabled {
		return false, nil
	}
	startMin, okStart := parseHHMMToMinute(start)
	endMin, okEnd := parseHHMMToMinute(end)
	if !okStart || !okEnd {
		return false, nil
	}

	now := time.Now()
	nowMin := now.Hour()*60 + now.Minute()
	if startMin == endMin {
		return true, nil
	}
	if startMin < endMin {
		return nowMin >= startMin && nowMin < endMin, nil
	}
	// 跨天时段，如 22:00-07:00。
	return nowMin >= startMin || nowMin < endMin, nil
}

func (h *FileHandler) resolvePeakWindowSetting(ctx context.Context, uid int64) (bool, string, string, error) {
	// 1) 先读用户自定义高峰时段。
	if uid > 0 {
		enabledKey := "peak_window_enabled_user_" + strconv.FormatInt(uid, 10)
		startKey := "peak_window_start_user_" + strconv.FormatInt(uid, 10)
		endKey := "peak_window_end_user_" + strconv.FormatInt(uid, 10)

		if enabledVal, exists, err := h.getSystemSettingString(ctx, enabledKey); err != nil {
			return false, "", "", err
		} else if exists {
			enabled := strings.TrimSpace(enabledVal) == "1"
			startVal, _, errStart := h.getSystemSettingString(ctx, startKey)
			if errStart != nil {
				return false, "", "", errStart
			}
			endVal, _, errEnd := h.getSystemSettingString(ctx, endKey)
			if errEnd != nil {
				return false, "", "", errEnd
			}
			return enabled, startVal, endVal, nil
		}
	}

	// 2) 用户未配置时，回落全局配置。
	enabledGlobal, _, err := h.getSystemSettingInt64(ctx, "peak_window_enabled")
	if err != nil {
		return false, "", "", err
	}
	startGlobal, _, err := h.getSystemSettingString(ctx, "peak_window_start")
	if err != nil {
		return false, "", "", err
	}
	endGlobal, _, err := h.getSystemSettingString(ctx, "peak_window_end")
	if err != nil {
		return false, "", "", err
	}
	return enabledGlobal == 1, startGlobal, endGlobal, nil
}

func parseHHMMToMinute(v string) (int, bool) {
	v = strings.TrimSpace(v)
	if len(v) != 5 || v[2] != ':' {
		return 0, false
	}
	h, errH := strconv.Atoi(v[:2])
	m, errM := strconv.Atoi(v[3:])
	if errH != nil || errM != nil {
		return 0, false
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// sendFileWithRateLimit 以限速方式输出文件流，避免下载占满带宽。
func (h *FileHandler) sendFileWithRateLimit(c *gin.Context, filePath string, downloadName string, bytesPerSec int64) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return err
	}

	c.Header("Accept-Ranges", "bytes")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", sanitizeFileName(downloadName)))

	if strings.TrimSpace(c.GetHeader("Range")) != "" {
		http.ServeContent(c.Writer, c.Request, sanitizeFileName(downloadName), stat.ModTime(), f)
		return nil
	}

	if bytesPerSec <= 0 {
		c.Header("Content-Type", "application/octet-stream")
		c.Header("Content-Length", strconv.FormatInt(stat.Size(), 10))
		_, err = io.Copy(c.Writer, f)
		return err
	}

	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Length", strconv.FormatInt(stat.Size(), 10))

	const chunkSize = 32 * 1024
	buf := make([]byte, chunkSize)
	start := time.Now()
	var sent int64

	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			written, writeErr := c.Writer.Write(buf[:n])
			if writeErr != nil {
				return writeErr
			}
			sent += int64(written)

			// 按累计发送量控制整体速率，避免短时突发。
			expectedElapsed := time.Duration(float64(sent) / float64(bytesPerSec) * float64(time.Second))
			actualElapsed := time.Since(start)
			if expectedElapsed > actualElapsed {
				time.Sleep(expectedElapsed - actualElapsed)
			}

			if flusher, ok := c.Writer.(http.Flusher); ok {
				flusher.Flush()
			}
		}

		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return readErr
		}
	}

	return nil
}

func computeFileMD5Hex(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// zipQueueNode 用于文件夹广度遍历时保存节点路径信息。
type zipQueueNode struct {
	FolderID int64
	RelPath  string
}

// createFolderZip 递归读取目录树并生成临时 zip 文件。
func (h *FileHandler) createFolderZip(ctx context.Context, root model.File) (string, error) {
	tmpFile, err := os.CreateTemp("", "cloudnest-folder-*.zip")
	if err != nil {
		return "", err
	}
	zipPath := tmpFile.Name()

	zw := zip.NewWriter(tmpFile)
	defer func() {
		_ = zw.Close()
		_ = tmpFile.Close()
	}()

	storageCache := make(map[int64]model.Storage)
	queue := []zipQueueNode{{FolderID: root.ID, RelPath: sanitizeFileName(root.Filename)}}

	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]

		var children []model.File
		err := h.DB.WithContext(ctx).
			Where("parent_id = ? AND is_deleted = 0", node.FolderID).
			Order("is_folder DESC, filename ASC").
			Find(&children).Error
		if err != nil {
			_ = os.Remove(zipPath)
			return "", err
		}

		if len(children) == 0 {
			if err := addZipDir(zw, node.RelPath); err != nil {
				_ = os.Remove(zipPath)
				return "", err
			}
			continue
		}

		for _, child := range children {
			childRelPath := filepath.ToSlash(filepath.Join(node.RelPath, sanitizeFileName(child.Filename)))
			if child.IsFolder == 1 {
				queue = append(queue, zipQueueNode{FolderID: child.ID, RelPath: childRelPath})
				continue
			}

			if child.StorageID == nil {
				continue
			}

			storage, ok := storageCache[*child.StorageID]
			if !ok {
				var s model.Storage
				if err := h.DB.WithContext(ctx).First(&s, "id = ?", *child.StorageID).Error; err != nil {
					if errors.Is(err, gorm.ErrRecordNotFound) {
						continue
					}
					_ = os.Remove(zipPath)
					return "", err
				}
				storage = s
				storageCache[*child.StorageID] = s
			}

			if _, err := os.Stat(storage.FilePath); err != nil {
				continue
			}
			if err := addZipFileFromPath(zw, childRelPath, storage.FilePath); err != nil {
				_ = os.Remove(zipPath)
				return "", err
			}
		}
	}

	if err := zw.Close(); err != nil {
		_ = os.Remove(zipPath)
		return "", err
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(zipPath)
		return "", err
	}

	return zipPath, nil
}

// addZipDir 在 zip 中写入目录项，兼容空目录下载。
func addZipDir(zw *zip.Writer, relPath string) error {
	entry := filepath.ToSlash(strings.TrimSuffix(relPath, "/")) + "/"
	_, err := zw.Create(entry)
	return err
}

// addZipFileFromPath 读取磁盘文件并写入 zip 指定路径。
func addZipFileFromPath(zw *zip.Writer, relPath string, sourcePath string) error {
	in, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer in.Close()

	w, err := zw.Create(filepath.ToSlash(relPath))
	if err != nil {
		return err
	}

	_, err = io.Copy(w, in)
	return err
}
