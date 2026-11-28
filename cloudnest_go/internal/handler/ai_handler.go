package handler

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"

	"cloudenest_go/internal/aiindex"
	"cloudenest_go/internal/imageprep"
	"cloudenest_go/internal/middleware"
	"cloudenest_go/internal/pdfextract"
	"cloudenest_go/internal/service"
	"cloudenest_go/internal/service/contract"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// AIHandler 处理 AI 相关能力：OCR、智能搜索、文档摘要、助手问答。
type AIHandler struct {
	Svc *service.AIService
}

// OCRRequest OCR 识别请求。
type OCRRequest struct {
	FileID      int64  `json:"file_id" binding:"required"`
	Location    string `json:"location"`
	Landmark    string `json:"landmark"`
	CapturedAt  string `json:"captured_at"`
	Tags        string `json:"tags"`
	Description string `json:"description"`
}

// SummaryRequest 文档摘要请求。
type SummaryRequest struct {
	FileID    int64 `json:"file_id" binding:"required"`
	MaxPoints int   `json:"max_points"`
}

// AskAssistantRequest 助手问答请求。
type AskAssistantRequest struct {
	Question string `json:"question" binding:"required"`
}

// ApplySettingByAIRequest AI 协助设置变更请求。
type ApplySettingByAIRequest struct {
	SettingKey   string `json:"setting_key" binding:"required"`
	SettingValue string `json:"setting_value" binding:"required"`
	Description  string `json:"description"`
}

// AISearchQuery 智能搜索查询参数。
type AISearchQuery struct {
	Keyword   string `form:"keyword"`
	FileType  string `form:"file_type"`
	Location  string `form:"location"`
	Landmark  string `form:"landmark"`
	StartTime string `form:"start_time"`
	EndTime   string `form:"end_time"`
	TagOnly   bool   `form:"tag_only" json:"tag_only"`
	Page      int    `form:"page"`
	Limit     int    `form:"limit"`
}

// NLSearchRequest 自然语言检索请求。
type NLSearchRequest struct {
	Question string `json:"question" binding:"required"`
	Page     int    `json:"page"`
	Limit    int    `json:"limit"`
}

func (h *AIHandler) OCR(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	uid := c.GetInt64(middleware.ContextUserIDKey)

	var req OCRRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if req.FileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid file_id"})
		return
	}

	res, err := h.Svc.OCR(c.Request.Context(), uid, service.AIOCRInput{
		FileID:      req.FileID,
		Location:    req.Location,
		Landmark:    req.Landmark,
		CapturedAt:  req.CapturedAt,
		Tags:        req.Tags,
		Description: req.Description,
	})
	if err != nil {
		h.writeAIErr(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"item": res.Metadata,
		"exif": gin.H{
			"latitude":           res.EXIF.Latitude,
			"longitude":          res.EXIF.Longitude,
			"altitude":           res.EXIF.Altitude,
			"captured_at":        res.EXIF.CapturedAt,
			"capture_offset":     res.EXIF.CaptureOffset,
			"capture_time_basis": "exif_camera_local",
			"camera_make":        res.EXIF.CameraMake,
			"camera_model":       res.EXIF.CameraModel,
		},
		"doc_summary":  res.DocSummary,
		"has_text":     res.HasText,
		"model":        res.Model,
		"model_digest": res.ModelDigest,
	})
}

// Search 智能搜索（文件名 + OCR 文本 + 标签 + 描述 + 条件过滤）。
func (h *AIHandler) Search(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)

	var q AISearchQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid query"})
		return
	}
	q.Keyword = strings.TrimSpace(q.Keyword)
	q.FileType = strings.ToLower(strings.TrimSpace(q.FileType))
	q.Location = strings.TrimSpace(q.Location)
	q.Landmark = strings.TrimSpace(q.Landmark)
	q.StartTime = strings.TrimSpace(q.StartTime)
	q.EndTime = strings.TrimSpace(q.EndTime)
	if q.Page <= 0 {
		q.Page = 1
	}
	if q.Limit <= 0 || q.Limit > 100 {
		q.Limit = 20
	}

	res, err := h.Svc.Search(c.Request.Context(), uid, service.AISearchQuery{
		Keyword:   q.Keyword,
		FileType:  q.FileType,
		Location:  q.Location,
		Landmark:  q.Landmark,
		StartTime: q.StartTime,
		EndTime:   q.EndTime,
		TagOnly:   q.TagOnly,
		Page:      q.Page,
		Limit:     q.Limit,
	})
	if err != nil {
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

// NLSearch 自然语言检索：将用户问题转换为检索条件并返回文件列表。
func (h *AIHandler) NLSearch(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)

	var req NLSearchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	req.Question = strings.TrimSpace(req.Question)
	if req.Question == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "question is required"})
		return
	}

	res, err := h.Svc.NLSearch(c.Request.Context(), uid, req.Question, req.Page, req.Limit)
	if err != nil {
		if errors.Is(err, contract.ErrInvalidInput) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid search query"})
		} else {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "search temporarily unavailable"})
		}
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"items": res.Search.Items,
		"pagination": gin.H{
			"page":  res.Query.Page,
			"limit": res.Query.Limit,
			"total": res.Search.Total,
		},
		"nl_plan": gin.H{
			"source": res.Source,
			"query":  res.Query,
		},
		"retrieval": gin.H{"mode": res.Source, "warnings": res.Warnings, "window_limited": res.WindowLimited, "window_size": 200, "rerank": res.Rerank},
	})
}

func (h *AIHandler) Summarize(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)

	var req SummaryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if req.FileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid file_id"})
		return
	}
	if req.MaxPoints <= 0 || req.MaxPoints > 20 {
		req.MaxPoints = 6
	}

	res, err := h.Svc.Summarize(c.Request.Context(), uid, req.FileID, req.MaxPoints)
	if err != nil {
		if errors.Is(err, contract.ErrConflict) {
			c.JSON(http.StatusConflict, gin.H{"error": "file changed during summary"})
			return
		}
		h.writeAIErr(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"item": gin.H{
			"file_id":    res.FileID,
			"filename":   res.Filename,
			"summary":    res.Summary,
			"truncated":  res.Truncated,
			"max_points": res.MaxPoints,
			"model":      res.Model,
		},
	})
}

// AskAssistant 针对项目操作与设置进行问答。
func (h *AIHandler) AskAssistant(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	var req AskAssistantRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	question := strings.TrimSpace(req.Question)
	if question == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "question is required"})
		return
	}

	res, err := h.Svc.AskAssistant(c.Request.Context(), question)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "ollama call failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"item": gin.H{
			"answer":           strings.TrimSpace(res.Answer),
			"model":            res.Model,
			"guide_doc_loaded": res.GuideDocLoaded,
		},
	})
}

// ApplySetting 结合 AI 场景提供“设置自动修改”能力（管理员权限）。
func (h *AIHandler) ApplySetting(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	isAdmin, _ := c.Get(middleware.ContextIsAdminKey)
	if admin, ok := isAdmin.(bool); !ok || !admin {
		c.JSON(http.StatusForbidden, gin.H{"error": "admin only"})
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var req ApplySettingByAIRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	req.SettingKey = strings.TrimSpace(req.SettingKey)
	req.SettingValue = strings.TrimSpace(req.SettingValue)
	req.Description = strings.TrimSpace(req.Description)
	if req.SettingKey == "" || req.SettingValue == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "setting_key and setting_value are required"})
		return
	}

	err := h.Svc.ApplySetting(c.Request.Context(), uid, req.SettingKey, req.SettingValue, req.Description, c.ClientIP())
	if err != nil {
		if errors.Is(err, gorm.ErrInvalidData) || errors.Is(err, gorm.ErrInvalidField) || strings.HasPrefix(err.Error(), "invalid value for setting_key:") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid setting key or value"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save setting failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "setting updated"})
}

func (h *AIHandler) writeAIErr(c *gin.Context, err error) {
	if err == nil {
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI operation timed out"})
		return
	}
	if errors.Is(err, imageprep.ErrDecoderUnavailable) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "image decoder unavailable"})
		return
	}
	if errors.Is(err, pdfextract.ErrUnavailable) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "PDF extraction unavailable"})
		return
	}
	if errors.Is(err, aiindex.ErrModelUnavailable) || errors.Is(err, aiindex.ErrVisionResponse) || errors.Is(err, aiindex.ErrModelChanged) || errors.Is(err, aiindex.ErrEmbedding) {
		c.JSON(http.StatusBadGateway, gin.H{"error": "OCR model unavailable"})
		return
	}
	if errors.Is(err, aiindex.ErrExtractionBusy) || errors.Is(err, pdfextract.ErrBusy) || errors.Is(err, imageprep.ErrBusy) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "document extraction busy"})
		return
	}
	if errors.Is(err, aiindex.ErrTextLimit) || errors.Is(err, pdfextract.ErrLimit) || errors.Is(err, imageprep.ErrLimit) {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "document extraction limit exceeded"})
		return
	}
	if errors.Is(err, aiindex.ErrDocument) || errors.Is(err, pdfextract.ErrInvalid) || errors.Is(err, imageprep.ErrInvalid) || errors.Is(err, imageprep.ErrFormat) {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "document content cannot be extracted"})
		return
	}
	if errors.Is(err, contract.ErrConflict) {
		c.JSON(http.StatusConflict, gin.H{"error": "file changed during OCR"})
		return
	}
	if errors.Is(err, contract.ErrForbidden) {
		c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
		return
	}
	if errors.Is(err, contract.ErrInvalidInput) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid OCR metadata"})
		return
	}
	if errors.Is(err, contract.ErrStorageIntegrity) {
		c.JSON(http.StatusConflict, gin.H{"error": "stored content failed verification"})
		return
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "ollama call failed") {
		c.JSON(http.StatusBadGateway, gin.H{"error": "ollama call failed"})
		return
	}
	if strings.Contains(msg, "ocr only supports image files") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ocr only supports image files"})
		return
	}
	if strings.Contains(msg, "document content is empty or unsupported") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "document content is empty or unsupported"})
		return
	}
	if strings.Contains(msg, "read image failed") {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "read image failed"})
		return
	}
	if strings.Contains(msg, "read file failed") {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "read file failed"})
		return
	}
	if strings.Contains(msg, "save ai metadata failed") {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save ai metadata failed"})
		return
	}
	if strings.Contains(msg, "permission denied") {
		c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
		return
	}
	if strings.Contains(msg, "target is folder") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "target is folder"})
		return
	}
	if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, os.ErrNotExist) {
		c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
}
