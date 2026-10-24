package handler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cloudenest_go/internal/blob"
	"cloudenest_go/internal/config"
	"cloudenest_go/internal/middleware"
	"cloudenest_go/internal/service"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/internal/transfer"
	"cloudenest_go/model"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// FileHandler handles file system related APIs.
type FileHandler struct {
	Transfers  *transfer.Manager
	Blobs      *blob.Manager
	RDB        *redis.Client
	Cfg        config.Config
	HTTPClient *http.Client
	AISvc      *service.AIService
	Svc        *service.FileService
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
	TotalSize   *int64 `json:"total_size" binding:"required"`
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
	defaultDataDir = "data"
	storageSubDir  = "storage"

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

	rows, err := h.Svc.ListFiles(c.Request.Context(), uid, parentID)
	if err != nil {
		if errors.Is(err, service.ErrFilePermissionDenied) {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
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

	folder, err := h.Svc.CreateFolder(c.Request.Context(), uid, req.Name, req.ParentID, c.ClientIP())
	if err != nil {
		writeFileMutationError(c, err, "create folder failed")
		return
	}

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

	f, err := h.Svc.RenameFile(c.Request.Context(), uid, fileID, req.Name, c.ClientIP())
	if err != nil {
		writeFileMutationError(c, err, "rename failed")
		return
	}

	c.JSON(http.StatusOK, gin.H{"item": f})
}

func (h *FileHandler) Delete(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	fileID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || fileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid file id"})
		return
	}

	err = h.Svc.DeleteFile(c.Request.Context(), uid, fileID, c.ClientIP())
	if err != nil {
		writeFileMutationError(c, err, "delete failed")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

// InitUpload 初始化分片上传任务，支持续传场景。
func (h *FileHandler) InitUpload(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	uid := c.GetInt64(middleware.ContextUserIDKey)

	var req InitUploadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	req.Filename = strings.TrimSpace(req.Filename)
	if req.Filename == "" || req.TotalSize == nil || *req.TotalSize < 0 || req.ParentID < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid upload parameters"})
		return
	}

	res, err := h.Svc.InitUpload(c.Request.Context(), uid, service.FileUploadInitInput{
		UploadID:    req.UploadID,
		ParentID:    req.ParentID,
		Filename:    req.Filename,
		TotalSize:   *req.TotalSize,
		ChunkSize:   req.ChunkSize,
		TotalChunks: req.TotalChunks,
	})
	if err != nil {
		if isUploadContractError(err) {
			writeFileMutationError(c, err, "upload rejected")
			return
		}
		if errors.Is(err, contract.ErrInvalidInput) {
			writeFileMutationError(c, err, "invalid upload name")
			return
		}
		switch {
		case errors.Is(err, service.ErrFilePermissionDenied):
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		case errors.Is(err, service.ErrUploadTaskNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "upload task not found"})
			return
		default:
			msg := strings.ToLower(err.Error())
			if strings.Contains(msg, "invalid upload parameters") {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid upload parameters"})
				return
			}
			if strings.Contains(msg, "invalid total_chunks") {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid total_chunks"})
				return
			}
			if strings.Contains(msg, "create") {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "create upload task failed"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"upload_id":       res.UploadID,
		"transfer_epoch":  res.TransferEpoch,
		"uploaded_chunks": res.UploadedChunks,
		"uploaded_count":  res.UploadedCount,
		"status":          res.Status,
	})
}

// CompleteUpload 合并所有分片，写入 storage/file 记录并完成上传任务。
func (h *FileHandler) CompleteUpload(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
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

	snapshot, err := h.Svc.PrepareCompleteUpload(c.Request.Context(), uid, req.UploadID, req.ParentID)
	if err != nil {
		if isUploadContractError(err) {
			writeFileMutationError(c, err, "upload rejected")
			return
		}
		switch {
		case errors.Is(err, service.ErrFilePermissionDenied):
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		case errors.Is(err, service.ErrUploadTaskNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "upload task not found"})
			return
		case errors.Is(err, service.ErrUploadTaskCompleted):
			c.JSON(http.StatusConflict, gin.H{"error": "upload task already completed"})
			return
		case errors.Is(err, service.ErrUploadMetaIncomplete):
			c.JSON(http.StatusBadRequest, gin.H{"error": "upload task metadata incomplete"})
			return
		case errors.Is(err, service.ErrChunksNotComplete):
			c.JSON(http.StatusConflict, gin.H{"error": "chunks not fully uploaded"})
			return
		default:
			if strings.Contains(strings.ToLower(err.Error()), "invalid parameters") {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid parameters"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
	}

	if snapshot.CompletedFile != nil {
		c.JSON(http.StatusOK, gin.H{"item": *snapshot.CompletedFile})
		return
	}
	if err := verifyUploadEpoch(c, snapshot.Task.TransferEpoch); err != nil {
		writeFileMutationError(c, err, "upload epoch rejected")
		return
	}
	candidate, mergedSize, fileHash, err := h.mergeChunks(c.Request.Context(), uid, snapshot)
	if err != nil {
		// The winner may have committed and removed chunks after our preflight.
		// Its receipt is authoritative; a missing chunk is not a failed upload.
		latest, receiptErr := h.Svc.PrepareCompleteUpload(c.Request.Context(), uid, req.UploadID, req.ParentID)
		if receiptErr == nil && latest.CompletedFile != nil {
			c.JSON(http.StatusOK, gin.H{"item": *latest.CompletedFile})
			return
		}
		if isUploadContractError(receiptErr) {
			writeFileMutationError(c, receiptErr, "complete upload failed")
			return
		}
		if errors.Is(err, context.Canceled) && c.Request.Context().Err() == nil {
			err = contract.ErrUploadContract
		}
		writeFileMutationError(c, err, "merge chunks failed")
		return
	}

	disposable := true
	defer func() {
		if err := candidate.Close(disposable); err != nil {
			h.recordUploadCleanupFailure(context.WithoutCancel(c.Request.Context()), uid, nil, "upload_candidate_cleanup", c.ClientIP())
		}
	}()
	committed, err := h.Svc.CompleteUpload(candidate.Context(), uid, req.ParentID, req.UploadID, snapshot, candidate.Path(), candidate.Root(), mergedSize, fileHash, candidate.Token(), c.ClientIP())
	disposable = committed.CandidateDisposable
	if err != nil {
		if candidate.Context().Err() != nil && c.Request.Context().Err() == nil {
			err = contract.ErrUploadContract
		}
		writeFileMutationError(c, err, "complete upload failed")
		return
	}

	createdFile := committed.File
	if committed.Replayed {
		c.JSON(http.StatusOK, gin.H{"item": createdFile})
		return
	}

	h.cleanupChunkManifest(c.Request.Context(), uid, snapshot.Chunks, c.ClientIP())
	// 上传成功后触发 AI 异步分析：自动生成 tags/description/ocr_text 并写入 ai_metadata。
	h.triggerAsyncAutoAnalyze(createdFile, uid, c.ClientIP())
	// 上传完成后尝试自动与同目录同名文件配对绑定 Live Photo（无阻塞失败）。
	_ = h.tryAutoBindLivePhotoForFile(c.Request.Context(), uid, createdFile, c.ClientIP())

	c.JSON(http.StatusOK, gin.H{"item": createdFile})
}

// Cleanup cannot undo a committed upload. Persist or report a pending cleanup
// without including private paths or turning committed bytes into a failed task.
func (h *FileHandler) recordUploadCleanupFailure(ctx context.Context, uid int64, fileID *int64, action, clientIP string) {
	if err := h.Svc.RecordOperation(ctx, uid, fileID, action, "failed", "temporary upload cleanup pending", clientIP); err != nil {
		log.Printf("upload cleanup pending action=%s user_id=%d audit_unavailable=true", action, uid)
	}
}

// triggerAsyncAutoAnalyze 在后台执行文件自动分析，不阻塞上传接口响应。
func (h *FileHandler) triggerAsyncAutoAnalyze(file model.File, userID int64, clientIP string) {
	go func(f model.File, uid int64, ip string) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		ctx, scope := h.Blobs.Begin(ctx)
		defer func() {
			if err := scope.Close(); err != nil {
				log.Print("background storage read lease cleanup pending")
			}
		}()

		ai := &AIHandler{Cfg: h.Cfg, HTTPClient: h.HTTPClient, Svc: h.AISvc}
		_ = ai.createAITask(ctx, uid, f.ID, "auto_analyze", "processing", "queued")

		tags, desc, ocrText, err := h.buildAutoAnalyzeResult(ctx, ai, f)
		if err != nil {
			_ = ai.createAITask(ctx, uid, f.ID, "auto_analyze", "failed", err.Error())
			_ = h.Svc.RecordOperation(ctx, uid, &f.ID, "ai_auto_analyze", "failed", truncateForLog(err.Error(), 500), ip)
			return
		}

		if _, err := ai.upsertAIMetadata(ctx, f.ID, tags, desc, ocrText); err != nil {
			_ = ai.createAITask(ctx, uid, f.ID, "auto_analyze", "failed", err.Error())
			_ = h.Svc.RecordOperation(ctx, uid, &f.ID, "ai_auto_analyze", "failed", truncateForLog(err.Error(), 500), ip)
			return
		}

		result := fmt.Sprintf("tags=%s | desc_len=%d | ocr_len=%d", tags, len(desc), len(ocrText))
		_ = ai.createAITask(ctx, uid, f.ID, "auto_analyze", "success", truncateForLog(result, 500))
		_ = h.Svc.RecordOperation(ctx, uid, &f.ID, "ai_auto_analyze", "success", truncateForLog(result, 500), ip)
	}(file, userID, clientIP)
}

// buildAutoAnalyzeResult 生成 ai_metadata 三个核心字段：tags/description/ocr_text。
func (h *FileHandler) buildAutoAnalyzeResult(ctx context.Context, ai *AIHandler, f model.File) (string, string, string, error) {
	if f.StorageID == nil {
		return "", "", "", fmt.Errorf("storage id missing")
	}

	storage, err := h.Svc.GetStorageByID(ctx, *f.StorageID)
	if err != nil {
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

	res, err := h.Svc.UploadProgress(c.Request.Context(), uid, uploadID)
	if err != nil {
		if isUploadContractError(err) {
			writeFileMutationError(c, err, "upload unavailable")
			return
		}
		if errors.Is(err, service.ErrUploadTaskNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "upload task not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"upload_id":       res.UploadID,
		"transfer_epoch":  res.TransferEpoch,
		"status":          res.Status,
		"uploaded_count":  res.UploadedCount,
		"total_chunks":    res.TotalChunks,
		"uploaded_chunks": res.UploadedChunks,
		"missing_chunks":  res.MissingChunks,
		"progress":        res.Progress,
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

	f, storage, err := h.Svc.GetAccessibleFileAndStorage(c.Request.Context(), uid, fileID, false)
	if err != nil {
		if errors.Is(err, service.ErrFilePermissionDenied) {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
		if errors.Is(err, service.ErrFileNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
			return
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "storage record not found"})
			return
		}
		if errors.Is(err, os.ErrNotExist) {
			c.JSON(http.StatusNotFound, gin.H{"error": "physical file not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if f.IsFolder == 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "target is folder, use folder zip download"})
		return
	}
	rate, err := h.transferRate(c, "download")
	if err != nil {
		writeFileMutationError(c, err, "download policy unavailable")
		return
	}
	if err := h.sendFileWithRateLimit(c, storage.FilePath, f.Filename, rate, &storage); err != nil {
		writeDownloadError(c, err)
		return
	}
}

func (h *FileHandler) DownloadVersion(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	fileID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	version, parseErr := strconv.Atoi(c.Param("version"))
	if err != nil || parseErr != nil || fileID <= 0 || version <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid version parameters"})
		return
	}
	file, storage, err := h.Svc.ResolveFileVersion(c.Request.Context(), uid, fileID, version)
	if err != nil {
		writeFileMutationError(c, err, "download version failed")
		return
	}
	rate, err := h.transferRate(c, "download")
	if err != nil {
		writeFileMutationError(c, err, "download policy unavailable")
		return
	}
	if err := h.sendFileWithRateLimit(c, storage.FilePath, file.Filename, rate, &storage); err != nil {
		writeDownloadError(c, err)
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

	folder, err := h.Svc.GetAccessibleFile(c.Request.Context(), uid, folderID, false)
	if err != nil {
		if errors.Is(err, service.ErrFilePermissionDenied) {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
		if errors.Is(err, service.ErrFileNotFound) {
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

	rate, err := h.transferRate(c, "download")
	if err != nil {
		writeFileMutationError(c, err, "download policy unavailable")
		return
	}
	archive, err := h.createFolderZip(c.Request.Context(), uid, folder)
	if err != nil {
		writeFileMutationError(c, err, "create zip failed")
		return
	}
	defer releaseArchive(archive)

	zipName := sanitizeFileName(folder.Filename) + ".zip"
	if err := h.sendArchiveWithRateLimit(c, archive, zipName, rate); err != nil {
		writeDownloadError(c, err)
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

	deletedIDs, failed := h.Svc.BatchDeleteFiles(c.Request.Context(), uid, ids, c.ClientIP())

	c.JSON(http.StatusOK, gin.H{
		"deleted_ids": deletedIDs,
		"failed":      failed,
	})
}

// BatchDownload 批量下载文件和目录，返回一个总 zip 包。
func (h *FileHandler) BatchDownload(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	var req BatchDownloadRequest
	decoder := json.NewDecoder(c.Request.Body)
	decodeErr := decoder.Decode(&req)
	if decodeErr == nil {
		var tail json.RawMessage
		if err := decoder.Decode(&tail); !errors.Is(err, io.EOF) {
			decodeErr = err
			if decodeErr == nil {
				decodeErr = contract.ErrInvalidInput
			}
		}
	}
	if err := decodeErr; err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request too large"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	ids := uniqueInt64(req.FileIDs)
	if len(ids) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file_ids is required"})
		return
	}

	entries, err := h.Svc.CollectBatchDownloadEntries(c.Request.Context(), uid, ids)
	if err != nil {
		writeFileMutationError(c, err, "create zip failed")
		return
	}
	rate, err := h.transferRate(c, "download")
	if err != nil {
		writeFileMutationError(c, err, "download policy unavailable")
		return
	}
	archive, err := h.createBatchZip(c.Request.Context(), uid, entries)
	if err != nil {
		writeFileMutationError(c, err, "create zip failed")
		return
	}
	defer releaseArchive(archive)
	if err := h.sendArchiveWithRateLimit(c, archive, "batch_download.zip", rate); err != nil {
		writeDownloadError(c, err)
		return
	}
	if err := h.Svc.RecordOperation(c.Request.Context(), uid, nil, "batch_download", "success", fmt.Sprintf("count=%d", len(ids)), c.ClientIP()); err != nil {
		// The file response is already sent; report the audit failure without
		// appending JSON or claiming we can undo the completed network write.
		log.Print("batch download operation audit unavailable")
	}
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

	rate, err := h.transferRate(c, "download")
	if err != nil {
		writeFileMutationError(c, err, "stream policy unavailable")
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf("inline; filename=%q", sanitizeFileName(f.Filename)))
	if err := h.sendFileWithRateLimit(c, storage.FilePath, f.Filename, rate, &storage); err != nil {
		writeDownloadError(c, err)
	}
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

	rate, err := h.transferRate(c, "download")
	if err != nil {
		writeFileMutationError(c, err, "thumbnail policy unavailable")
		return
	}
	if storage.FileSize != nil && *storage.FileSize > thumbnailMaxEncodedBytes {
		writeThumbnailError(c, errThumbnailLimit)
		return
	}
	source, pinned, err := blob.OpenPinnedStorage(c.Request.Context(), storage)
	if err != nil {
		writeDownloadError(c, err)
		return
	}
	defer source.Close()
	thumb, err := h.buildOrGetThumbnail(c.Request.Context(), source, uid, f.ID, strings.ToLower(pinned.FileHash), thumbSize)
	if err != nil {
		writeThumbnailError(c, err)
		return
	}
	defer thumb.Close()
	c.Header("Content-Disposition", "inline")
	if err := h.serveFileIdentity(c, thumb.File, thumb.Digest, "thumbnail.jpg", rate); err != nil {
		writeDownloadError(c, err)
	}
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

	res, err := h.Svc.SearchFiles(c.Request.Context(), uid, service.FileSearchQuery{
		ParentID:  q.ParentID,
		Name:      q.Name,
		FileType:  q.FileType,
		StartTime: q.StartTime,
		EndTime:   q.EndTime,
		Page:      q.Page,
		Limit:     q.Limit,
	})
	if err != nil {
		errMsg := strings.ToLower(err.Error())
		if strings.Contains(errMsg, "invalid start_time") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid start_time"})
			return
		}
		if strings.Contains(errMsg, "invalid end_time") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid end_time"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"items": res.Items,
		"pagination": gin.H{
			"page":  q.Page,
			"limit": q.Limit,
			"total": res.Total,
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

	rate, err := h.transferRate(c, "download")
	if err != nil {
		writeFileMutationError(c, err, "preview policy unavailable")
		return
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(f.Filename), "."))
	if isTextPreviewExt(ext) {
		const maxPreviewBytes = 1024 * 1024
		source, _, err := blob.OpenPinnedStorage(c.Request.Context(), storage)
		if err != nil {
			writeDownloadError(c, err)
			return
		}
		defer source.Close()
		raw, readErr := io.ReadAll(io.LimitReader(&uploadContextReader{ctx: c.Request.Context(), in: source}, maxPreviewBytes+1))
		if readErr != nil {
			writeDownloadError(c, readErr)
			return
		}
		truncated := len(raw) > maxPreviewBytes
		if truncated {
			raw = raw[:maxPreviewBytes]
		}
		if err := h.sendPreviewJSON(c, rate, gin.H{"file_id": f.ID, "filename": f.Filename, "type": "text", "content": string(raw), "truncated": truncated}); err != nil {
			writeDownloadError(c, err)
		}
		return
	}

	if isInlinePreviewExt(ext) {
		c.Header("Content-Disposition", fmt.Sprintf("inline; filename=\"%s\"", sanitizeFileName(f.Filename)))
		if err := h.sendFileWithRateLimit(c, storage.FilePath, f.Filename, rate, &storage); err != nil {
			writeDownloadError(c, err)
		}
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
		if err := h.sendFileWithRateLimit(c, storage.FilePath, f.Filename, rate, &storage); err != nil {
			writeDownloadError(c, err)
		}
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

	var req CreateShareRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		if !errors.Is(err, io.EOF) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
			return
		}
	}

	res, err := h.Svc.CreateShare(c.Request.Context(), uid, fileID, req.ExpireHours, c.ClientIP())
	if err != nil {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "permission denied") {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
		if strings.Contains(msg, "invalid expire_hours") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "expire_hours should be between 0 and 8760"})
			return
		}
		if strings.Contains(msg, "invalid syntax") {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create share failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"share_code":  res.ShareCode,
		"expire_time": res.ExpireTime,
		"access_url":  fmt.Sprintf("/api/public/shares/%s", res.ShareCode),
	})
}

// ListShares 查看当前用户创建的分享链接列表。
func (h *FileHandler) ListShares(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)

	shares, err := h.Svc.ListShares(c.Request.Context(), uid)
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

	res, err := h.Svc.ListOperationLogs(c.Request.Context(), uid, isAdminBool, service.FileOperationLogQuery{
		Action:    q.Action,
		Status:    q.Status,
		StartTime: q.StartTime,
		EndTime:   q.EndTime,
		Page:      q.Page,
		Limit:     q.Limit,
		All:       q.All,
	})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "invalid start_time") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid start_time"})
			return
		}
		if strings.Contains(strings.ToLower(err.Error()), "invalid end_time") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid end_time"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"items": res.Items,
		"pagination": gin.H{
			"page":  q.Page,
			"limit": q.Limit,
			"total": res.Total,
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

	rows, err := h.Svc.ExportOperationLogs(c.Request.Context(), uid, isAdminBool, service.FileOperationLogQuery{
		Action:    q.Action,
		Status:    q.Status,
		StartTime: q.StartTime,
		EndTime:   q.EndTime,
		Limit:     q.Limit,
		All:       q.All,
	})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "invalid start_time") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid start_time"})
			return
		}
		if strings.Contains(strings.ToLower(err.Error()), "invalid end_time") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid end_time"})
			return
		}
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

	err := h.Svc.RevokeShare(c.Request.Context(), uid, code, c.ClientIP())
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "share not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

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

	share, file, err := h.Svc.GetPublicShareInfo(c.Request.Context(), shareCode)
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

	file, storage, creatorID, err := h.Svc.ResolvePublicShareDownload(c.Request.Context(), shareCode)
	if err != nil {
		if errors.Is(err, service.ErrStorageNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "storage record not found"})
			return
		}
		if errors.Is(err, service.ErrPhysicalFileNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "physical file not found"})
			return
		}
		h.handleShareError(c, err)
		return
	}

	rate, err := h.transferRate(c, "download")
	if err != nil {
		writeFileMutationError(c, err, "download policy unavailable")
		return
	}
	if file.IsFolder == 1 {
		archive, err := h.createFolderZip(c.Request.Context(), creatorID, file)
		if err != nil {
			writeFileMutationError(c, err, "create zip failed")
			return
		}
		defer releaseArchive(archive)
		if err := h.sendArchiveWithRateLimit(c, archive, sanitizeFileName(file.Filename)+".zip", rate); err != nil {
			writeDownloadError(c, err)
			return
		}
		return
	}

	if err := h.sendFileWithRateLimit(c, storage.FilePath, file.Filename, rate, storage); err != nil {
		writeDownloadError(c, err)
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

	updated, err := h.Svc.MoveFiles(c.Request.Context(), uid, req.FileIDs, req.TargetParentID, c.ClientIP())
	if err != nil {
		writeFileMutationError(c, err, "move failed")
		return
	}

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

	newIDs, err := h.Svc.CopyFiles(c.Request.Context(), uid, req.FileIDs, req.TargetParentID, c.ClientIP())
	if err != nil {
		writeFileMutationError(c, err, "copy failed")
		return
	}

	c.JSON(http.StatusOK, gin.H{"new_ids": newIDs})
}

// RecycleBin 列出当前用户回收站中的文件和文件夹。
func (h *FileHandler) RecycleBin(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)

	rows, err := h.Svc.ListRecycleBin(c.Request.Context(), uid)
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

	restored, err := h.Svc.RestoreFromRecycleBin(c.Request.Context(), uid, fileID, c.ClientIP())
	if err != nil {
		writeFileMutationError(c, err, "restore failed")
		return
	}
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

	err = h.Svc.PermanentDeleteFromRecycleBin(c.Request.Context(), uid, fileID, c.ClientIP())
	if err != nil {
		writeFileMutationError(c, err, "permanent delete failed")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "permanently deleted"})
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

// tryAutoBindLivePhotoForFile 尝试将当前文件与同目录同名异类型文件自动配对绑定。
func (h *FileHandler) tryAutoBindLivePhotoForFile(ctx context.Context, userID int64, file model.File, clientIP string) error {
	return h.Svc.TryAutoBindLivePhotoForFile(ctx, userID, file, clientIP)
}

func (h *FileHandler) dataRootDir() string {
	base := strings.TrimSpace(h.Cfg.DataDir)
	if base == "" {
		return defaultDataDir
	}
	return base
}

func (h *FileHandler) storageBaseDir() string {
	return filepath.Join(h.dataRootDir(), storageSubDir)
}

func (h *FileHandler) mergeChunks(ctx context.Context, uid int64, snapshot service.FileUploadTaskSnapshot) (candidate *transfer.Handle, size int64, digest string, result error) {
	candidate, err := h.Transfers.Create(ctx, uid, snapshot.Task.ID, "merge", max(snapshot.TotalSize, int64(1)), snapshot.Task.TransferEpoch)
	if err != nil {
		return nil, 0, "", err
	}
	complete := false
	owned := candidate
	defer func() {
		if !complete {
			result = errors.Join(result, owned.Close(true))
		}
	}()
	hash := sha256.New()
	writer := io.MultiWriter(candidate, hash)
	for _, chunk := range snapshot.Chunks {
		in, err := h.Transfers.OpenChunk(candidate.Context(), uid, snapshot.Task.ID, chunk)
		if err != nil {
			return nil, 0, "", err
		}
		info, err := in.Stat()
		if err != nil || chunk.ChunkSize == nil || info.Size() != *chunk.ChunkSize {
			in.Close()
			return nil, 0, "", contract.ErrInvalidInput
		}
		partHash := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(writer, partHash), io.LimitReader(&uploadContextReader{ctx: candidate.Context(), in: in}, *chunk.ChunkSize+1))
		closeErr := in.Close()
		size += n
		if copyErr != nil {
			return nil, 0, "", copyErr
		}
		if closeErr != nil {
			return nil, 0, "", closeErr
		}
		if n != *chunk.ChunkSize {
			return nil, 0, "", contract.ErrInvalidInput
		}
		if hex.EncodeToString(partHash.Sum(nil)) != chunk.SHA256 {
			return nil, 0, "", contract.ErrStorageIntegrity
		}
	}
	digest = hex.EncodeToString(hash.Sum(nil))
	if err := candidate.Seal(size, digest); err != nil {
		return nil, 0, "", err
	}
	complete = true
	return candidate, size, digest, nil
}

type uploadContextReader struct {
	ctx context.Context
	in  io.Reader
}

func (r *uploadContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.in.Read(p)
}

func sanitizeFileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "unnamed.bin"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", "..", "_", " ", "_")
	return replacer.Replace(name)
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
	return h.Svc.RecordOperation(ctx, userID, fileID, action, status, detail, clientIP)
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
		"png": true, "jpg": true, "jpeg": true, "gif": true, "webp": true, "heic": true,
		"pdf": true, "mp4": true, "mov": true, "m4v": true, "webm": true,
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

func (h *FileHandler) resolveValidShare(ctx context.Context, code string) (model.Share, model.File, error) {
	// 兼容旧调用路径，当前实现已委托给 service。
	return h.Svc.GetPublicShareInfo(ctx, code)
}

func (h *FileHandler) handleShareError(c *gin.Context, err error) {
	if errors.Is(err, service.ErrShareExpired) {
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
	return h.Svc.GetAccessibleFileAndStorage(ctx, uid, fileID, requireWrite)
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

func writeFileMutationError(c *gin.Context, err error, fallback string) {
	status, message := http.StatusInternalServerError, fallback
	switch {
	case errors.Is(err, contract.ErrRatePolicy):
		status, message = http.StatusServiceUnavailable, "transfer rate policy unavailable"
	case errors.Is(err, contract.ErrTransferSpace):
		status, message = http.StatusInsufficientStorage, "temporary storage capacity unavailable"
	case errors.Is(err, contract.ErrTransferBusy):
		c.Header("Retry-After", "1")
		status, message = http.StatusConflict, "transfer already in progress; retry later"
	case errors.Is(err, contract.ErrTransferUnavailable):
		status, message = http.StatusServiceUnavailable, "transfer storage unavailable"
	case errors.Is(err, contract.ErrQuotaExceeded):
		status, message = http.StatusInsufficientStorage, "storage quota exceeded"
	case errors.Is(err, contract.ErrQuotaAccounting):
		status, message = http.StatusServiceUnavailable, "storage accounting unavailable"
	case errors.Is(err, contract.ErrUploadReceipt):
		status, message = http.StatusServiceUnavailable, "upload completion receipt unavailable"
	case errors.Is(err, contract.ErrUploadReceiptMissing):
		status, message = http.StatusConflict, "upload already completed; original result unavailable"
	case errors.Is(err, contract.ErrUploadContract):
		status, message = http.StatusConflict, "upload contract changed, expired or not reserved"
	case errors.Is(err, contract.ErrStorageIntegrity):
		status, message = http.StatusConflict, "stored content failed integrity verification"
	case errors.Is(err, contract.ErrForbidden), errors.Is(err, service.ErrFilePermissionDenied):
		status, message = http.StatusForbidden, "permission denied"
	case errors.Is(err, contract.ErrInvalidInput), errors.Is(err, contract.ErrTargetNotFolder), errors.Is(err, service.ErrTargetNotFolder), errors.Is(err, service.ErrFileParentNotFolder):
		status, message = http.StatusBadRequest, "invalid name or target folder"
	case errors.Is(err, os.ErrNotExist), errors.Is(err, gorm.ErrRecordNotFound), errors.Is(err, contract.ErrNotFound), errors.Is(err, contract.ErrTargetNotFound), errors.Is(err, service.ErrFileParentNotFound), errors.Is(err, service.ErrFileNotFound), errors.Is(err, service.ErrTargetFolderNotFound), errors.Is(err, service.ErrRecycleItemNotFound):
		status, message = http.StatusNotFound, "file or folder not found"
	case errors.Is(err, contract.ErrConflict), errors.Is(err, contract.ErrCannotMoveToItself), errors.Is(err, contract.ErrCannotMoveToDesc), errors.Is(err, contract.ErrFolderNotEmpty), errors.Is(err, service.ErrFileNameConflict), errors.Is(err, service.ErrCannotMoveToItself), errors.Is(err, service.ErrCannotMoveToDesc), errors.Is(err, service.ErrFileFolderNotEmpty):
		status, message = http.StatusConflict, "file conflict; refresh and retry"
	}
	c.JSON(status, gin.H{"error": message})
}

func isUploadContractError(err error) bool {
	return errors.Is(err, contract.ErrQuotaExceeded) || errors.Is(err, contract.ErrQuotaAccounting) || errors.Is(err, contract.ErrUploadContract) || errors.Is(err, contract.ErrUploadReceipt) || errors.Is(err, contract.ErrUploadReceiptMissing)
}
func (h *FileHandler) CancelUpload(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	var req struct {
		UploadID string `json:"upload_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.UploadID) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "upload_id required"})
		return
	}
	if err := h.Svc.CancelUpload(c.Request.Context(), c.GetInt64(middleware.ContextUserIDKey), strings.TrimSpace(req.UploadID), c.ClientIP()); err != nil {
		writeFileMutationError(c, err, "cancel upload failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": 3, "message": "upload canceled"})
}
