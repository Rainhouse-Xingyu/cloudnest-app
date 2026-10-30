package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"cloudenest_go/internal/aiindex"
	"cloudenest_go/internal/config"
	"cloudenest_go/internal/imageprep"
	"cloudenest_go/internal/middleware"
	"cloudenest_go/internal/pdfextract"
	"cloudenest_go/internal/service"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const aiReadMaxBytes int64 = 2 * 1024 * 1024

// AIHandler 处理 AI 相关能力：OCR、智能搜索、文档摘要、助手问答。
type AIHandler struct {
	Cfg        config.Config
	HTTPClient *http.Client
	Svc        *service.AIService
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

type nlSearchPlan struct {
	Keyword   string `json:"keyword"`
	FileType  string `json:"file_type"`
	Location  string `json:"location"`
	Landmark  string `json:"landmark"`
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
}

type ollamaGenerateRequest struct {
	Model  string   `json:"model"`
	Prompt string   `json:"prompt"`
	Stream bool     `json:"stream"`
	Images []string `json:"images,omitempty"`
}

type ollamaGenerateResponse struct {
	Response string `json:"response"`
	Error    string `json:"error"`
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

func (h *AIHandler) executeAISearch(ctx context.Context, uid int64, q AISearchQuery) ([]gin.H, int64, error) {
	if h.Svc == nil {
		return nil, 0, fmt.Errorf("ai service unavailable")
	}
	res, err := h.Svc.Search(ctx, uid, service.AISearchQuery{
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
		return nil, 0, err
	}
	items := make([]gin.H, 0, len(res.Items))
	for _, row := range res.Items {
		items = append(items, gin.H(row))
	}
	return items, res.Total, nil
}

func (h *AIHandler) parseNLSearchPlan(ctx context.Context, question string) (nlSearchPlan, string) {
	question = strings.TrimSpace(question)
	plan := nlSearchPlan{}

	if question == "" {
		return plan, "fallback"
	}

	prompt := "你是文件检索条件解析器。请将用户问题解析为JSON，字段为keyword,file_type,location,landmark,start_time,end_time。" +
		"仅输出JSON对象，不要输出其他内容。file_type可用值示例：image,document,pdf,jpg。用户问题：" + question
	resp, err := h.callOllamaGenerate(ctx, prompt, nil)
	if err == nil {
		obj := extractJSONObject(resp)
		if obj != "" {
			var parsed nlSearchPlan
			if e := json.Unmarshal([]byte(obj), &parsed); e == nil {
				parsed = refineNLSearchPlan(parsed, question)
				return parsed, "llm"
			}
		}
	}

	// 本地兜底规则，保障常见场景可用。
	lower := strings.ToLower(question)
	if loc := extractLikelyLocationFromQuestion(question); loc != "" {
		plan.Location = loc
	}
	if strings.Contains(lower, "macbook") || strings.Contains(question, "发票") || strings.Contains(question, "票据") {
		plan.FileType = "document"
		if plan.Keyword == "" {
			plan.Keyword = "发票"
		}
	}
	if strings.Contains(question, "照片") || strings.Contains(question, "图片") || strings.Contains(question, "旅游") {
		plan.FileType = "image"
	}
	plan = refineNLSearchPlan(plan, question)

	return plan, "fallback"
}

func refineNLSearchPlan(plan nlSearchPlan, question string) nlSearchPlan {
	plan.Keyword = strings.TrimSpace(plan.Keyword)
	plan.FileType = strings.TrimSpace(plan.FileType)
	plan.Location = strings.TrimSpace(plan.Location)
	plan.Landmark = strings.TrimSpace(plan.Landmark)

	// 旅游照片场景优先使用 location/landmark 标签过滤，避免整句走 OCR 模糊匹配。
	if isTravelPhotoQuestion(question) {
		if plan.FileType == "" {
			plan.FileType = "image"
		}
		if plan.Location == "" {
			plan.Location = extractLikelyLocationFromQuestion(question)
		}
		if plan.Location != "" {
			plan.Keyword = ""
		}
	}

	if plan.Keyword == "" && plan.Location == "" && plan.Landmark == "" {
		plan.Keyword = strings.TrimSpace(question)
	}

	return plan
}

func shouldUseTagOnlyForNL(question string, q AISearchQuery) bool {
	if q.TagOnly {
		return true
	}
	if strings.TrimSpace(q.Location) != "" || strings.TrimSpace(q.Landmark) != "" {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(q.FileType), "image") && isTravelPhotoQuestion(question) {
		return true
	}
	return false
}

func isTravelPhotoQuestion(question string) bool {
	q := strings.TrimSpace(question)
	return strings.Contains(q, "旅游") || strings.Contains(q, "旅行") || strings.Contains(q, "照片") || strings.Contains(q, "图片")
}

func extractLikelyLocationFromQuestion(question string) string {
	q := strings.TrimSpace(question)
	if q == "" {
		return ""
	}
	patterns := []string{
		`在([\p{Han}A-Za-z]{2,12})(?:旅游|旅行|拍的|拍摄的|的照片|照片|图片)`,
		`([\p{Han}A-Za-z]{2,12})(?:旅游|旅行)`,
		`去([\p{Han}A-Za-z]{2,12})(?:旅游|旅行)`,
	}
	for _, p := range patterns {
		re := regexp.MustCompile(p)
		m := re.FindStringSubmatch(q)
		if len(m) > 1 {
			return strings.TrimSpace(m[1])
		}
	}
	for _, city := range []string{"天津", "石家庄", "北京", "上海", "广州", "深圳", "重庆", "成都", "杭州", "武汉", "西安", "南京", "苏州", "长沙", "郑州", "青岛", "英国", "日本", "韩国", "美国"} {
		if strings.Contains(q, city) {
			return city
		}
	}
	return ""
}

func extractJSONObject(raw string) string {
	raw = strings.TrimSpace(raw)
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return ""
	}
	return raw[start : end+1]
}
func (h *AIHandler) Summarize(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)

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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save setting failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "setting updated"})
}

func (h *AIHandler) callOllamaGenerate(ctx context.Context, prompt string, images []string) (string, error) {
	baseURL := strings.TrimSpace(h.Cfg.AIOllamaBaseURL)
	if baseURL == "" {
		baseURL = "http://127.0.0.1:11434"
	}
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "http://" + baseURL
	}
	modelName := strings.TrimSpace(h.Cfg.AIOllamaModel)
	if modelName == "" {
		modelName = "qwen3-vl:4b"
	}

	body := ollamaGenerateRequest{
		Model:  modelName,
		Prompt: strings.TrimSpace(prompt),
		Stream: false,
		Images: images,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	timeoutSec := h.Cfg.AITimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 120
	}

	client := h.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: time.Duration(timeoutSec) * time.Second}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/api/generate", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("ollama status=%d body=%s", resp.StatusCode, string(b))
	}

	var out ollamaGenerateResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.Error) != "" {
		return "", errors.New(out.Error)
	}
	return out.Response, nil
}

func (h *AIHandler) getReadableFileAndStorage(ctx context.Context, userID int64, fileID int64) (model.File, model.Storage, error) {
	if h.Svc == nil {
		return model.File{}, model.Storage{}, fmt.Errorf("ai service unavailable")
	}
	return h.Svc.GetReadableFileAndStorage(ctx, userID, fileID)
}

func (h *AIHandler) canAccessFile(ctx context.Context, userID int64, fileID int64, requireWrite bool) (bool, error) {
	if h.Svc == nil {
		return false, fmt.Errorf("ai service unavailable")
	}
	return h.Svc.CanAccessFile(ctx, userID, fileID, requireWrite)
}

func (h *AIHandler) upsertAIMetadata(ctx context.Context, fileID int64, tags string, desc string, ocrText string) (model.AiMetadata, error) {
	if h.Svc == nil {
		return model.AiMetadata{}, fmt.Errorf("ai service unavailable")
	}
	return h.Svc.UpsertAIMetadata(ctx, fileID, tags, desc, ocrText)
}

func (h *AIHandler) createAITask(ctx context.Context, userID int64, fileID int64, taskType string, status string, result string) error {
	if h.Svc == nil {
		return fmt.Errorf("ai service unavailable")
	}
	return h.Svc.CreateAITask(ctx, userID, fileID, taskType, status, result)
}

func (h *AIHandler) writeAIErr(c *gin.Context, err error) {
	if err == nil {
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "AI operation timed out"})
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

func mergeTags(parts ...string) string {
	seen := map[string]bool{}
	out := make([]string, 0)
	for _, p := range parts {
		chunks := strings.Split(strings.TrimSpace(p), ",")
		for _, c := range chunks {
			v := strings.TrimSpace(c)
			if v == "" {
				continue
			}
			k := strings.ToLower(v)
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, v)
		}
	}
	return strings.Join(out, ",")
}
