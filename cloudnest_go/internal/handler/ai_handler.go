package handler

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"cloudenest_go/internal/config"
	"cloudenest_go/internal/middleware"
	"cloudenest_go/internal/service"
	"cloudenest_go/model"

	"github.com/gin-gonic/gin"
	pdf "github.com/ledongthuc/pdf"
	"github.com/rwcarlsen/goexif/exif"
	"gorm.io/gorm"
)

const aiReadMaxBytes int64 = 2 * 1024 * 1024

// AIHandler 处理 AI 相关能力：OCR、智能搜索、文档摘要、助手问答。
type AIHandler struct {
	DB         *gorm.DB
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

type exifInfo struct {
	Latitude    *float64
	Longitude   *float64
	Altitude    *float64
	CapturedAt  string
	CameraMake  string
	CameraModel string
}

// OCR 对图片执行 OCR 并写入 ai_metadata 索引。
func (h *AIHandler) OCR(c *gin.Context) {
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
			"latitude":     res.EXIF.Latitude,
			"longitude":    res.EXIF.Longitude,
			"altitude":     res.EXIF.Altitude,
			"captured_at":  res.EXIF.CapturedAt,
			"camera_make":  res.EXIF.CameraMake,
			"camera_model": res.EXIF.CameraModel,
		},
		"doc_summary": res.DocSummary,
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
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
	})
}

func (h *AIHandler) executeAISearch(ctx context.Context, uid int64, q AISearchQuery) ([]gin.H, int64, error) {
	base := h.DB.WithContext(ctx).
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
	if q.StartTime != "" {
		startAt, err := parseFlexibleTime(q.StartTime)
		if err != nil {
			return nil, 0, err
		}
		base = base.Where("f.create_time >= ?", startAt)
	}
	if q.EndTime != "" {
		endAt, err := parseFlexibleTime(q.EndTime)
		if err != nil {
			return nil, 0, err
		}
		base = base.Where("f.create_time <= ?", endAt)
	}

	var total int64
	if err := h.DB.WithContext(ctx).Table("(?) as t", base).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	type row struct {
		ID          int64     `json:"id"`
		Filename    string    `json:"filename"`
		FileType    string    `json:"file_type"`
		Size        int64     `json:"size"`
		ParentID    int64     `json:"parent_id"`
		UserID      int64     `json:"user_id"`
		CreateTime  time.Time `json:"create_time"`
		UpdateTime  time.Time `json:"update_time"`
		Tags        string    `json:"tags"`
		Description string    `json:"description"`
		OCRText     string    `json:"ocr_text"`
	}

	offset := (q.Page - 1) * q.Limit
	rows := make([]row, 0)
	if err := base.Order("f.id DESC").Limit(q.Limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, 0, err
	}

	items := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		snippet := strings.TrimSpace(r.OCRText)
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		items = append(items, gin.H{
			"id":            r.ID,
			"filename":      r.Filename,
			"file_type":     r.FileType,
			"size":          r.Size,
			"parent_id":     r.ParentID,
			"user_id":       r.UserID,
			"create_time":   r.CreateTime,
			"update_time":   r.UpdateTime,
			"tags":          r.Tags,
			"description":   r.Description,
			"ocr_snippet":   snippet,
			"has_ocr_index": strings.TrimSpace(r.OCRText) != "",
		})
	}

	return items, total, nil
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

func extractDocumentText(filePath string, ext string, limit int64) (string, bool, error) {
	ext = strings.ToLower(strings.TrimSpace(ext))
	switch ext {
	case "txt", "md", "json", "xml", "yaml", "yml", "csv", "log", "sql", "go", "swift", "java", "py", "js", "ts", "html", "css":
		return readTextFileWithLimit(filePath, limit)
	case "pdf":
		return extractTextFromPDF(filePath, limit)
	case "docx", "xlsx", "pptx":
		return extractTextFromOpenXML(filePath, ext, limit)
	default:
		return "", false, fmt.Errorf("unsupported document format")
	}
}

func extractTextFromPDF(filePath string, limit int64) (string, bool, error) {
	f, r, err := pdf.Open(filePath)
	if err != nil {
		return "", false, err
	}
	defer f.Close()

	b, err := r.GetPlainText()
	if err != nil {
		return "", false, err
	}
	raw, err := io.ReadAll(io.LimitReader(b, limit+1))
	if err != nil {
		return "", false, err
	}
	truncated := int64(len(raw)) > limit
	if truncated {
		raw = raw[:limit]
	}
	text := normalizeWhitespace(string(raw))
	return text, truncated, nil
}

func extractTextFromOpenXML(filePath string, ext string, limit int64) (string, bool, error) {
	zr, err := zip.OpenReader(filePath)
	if err != nil {
		return "", false, err
	}
	defer zr.Close()

	builder := strings.Builder{}
	truncated := false
	appendLimited := func(s string) {
		if s == "" || truncated {
			return
		}
		remain := int(limit) - builder.Len()
		if remain <= 0 {
			truncated = true
			return
		}
		if len(s) > remain {
			builder.WriteString(s[:remain])
			truncated = true
			return
		}
		builder.WriteString(s)
		builder.WriteString("\n")
	}

	for _, f := range zr.File {
		name := strings.ToLower(f.Name)
		if !strings.HasSuffix(name, ".xml") {
			continue
		}
		if ext == "docx" && !strings.HasPrefix(name, "word/") {
			continue
		}
		if ext == "xlsx" && !(strings.HasPrefix(name, "xl/worksheets/") || strings.HasPrefix(name, "xl/sharedstrings")) {
			continue
		}
		if ext == "pptx" && !strings.HasPrefix(name, "ppt/slides/") {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(rc, 2*1024*1024))
		_ = rc.Close()

		clean := stripXMLTags(string(raw))
		clean = normalizeWhitespace(clean)
		appendLimited(clean)
		if truncated {
			break
		}
	}

	return strings.TrimSpace(builder.String()), truncated, nil
}

var xmlTagRegex = regexp.MustCompile(`<[^>]+>`)

func stripXMLTags(s string) string {
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	s = strings.ReplaceAll(s, "&apos;", "'")
	s = xmlTagRegex.ReplaceAllString(s, " ")
	return s
}

func normalizeWhitespace(s string) string {
	fields := strings.Fields(s)
	return strings.TrimSpace(strings.Join(fields, " "))
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
	canRead, err := h.canAccessFile(ctx, userID, fileID, false)
	if err != nil {
		return model.File{}, model.Storage{}, err
	}
	if !canRead {
		return model.File{}, model.Storage{}, fmt.Errorf("permission denied")
	}

	var f model.File
	if err := h.DB.WithContext(ctx).First(&f, "id = ? AND is_deleted = 0", fileID).Error; err != nil {
		return model.File{}, model.Storage{}, err
	}
	if f.IsFolder == 1 {
		return model.File{}, model.Storage{}, fmt.Errorf("target is folder")
	}
	if f.StorageID == nil {
		return model.File{}, model.Storage{}, gorm.ErrRecordNotFound
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

func (h *AIHandler) canAccessFile(ctx context.Context, userID int64, fileID int64, requireWrite bool) (bool, error) {
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

func (h *AIHandler) upsertAIMetadata(ctx context.Context, fileID int64, tags string, desc string, ocrText string) (model.AiMetadata, error) {
	tags = strings.TrimSpace(tags)
	desc = strings.TrimSpace(desc)
	ocrText = strings.TrimSpace(ocrText)

	var row model.AiMetadata
	err := h.DB.WithContext(ctx).Where("file_id = ?", fileID).First(&row).Error
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return model.AiMetadata{}, err
		}
		row = model.AiMetadata{
			FileID:      fileID,
			Tags:        tags,
			Description: desc,
			OcrText:     ocrText,
		}
		if err := h.DB.WithContext(ctx).Create(&row).Error; err != nil {
			return model.AiMetadata{}, err
		}
		return row, nil
	}

	if err := h.DB.WithContext(ctx).Model(&row).Updates(map[string]interface{}{
		"tags":        tags,
		"description": desc,
		"ocr_text":    ocrText,
	}).Error; err != nil {
		return model.AiMetadata{}, err
	}
	if err := h.DB.WithContext(ctx).First(&row, row.ID).Error; err != nil {
		return model.AiMetadata{}, err
	}
	return row, nil
}

func (h *AIHandler) createAITask(ctx context.Context, userID int64, fileID int64, taskType string, status string, result string) error {
	uid := userID
	fid := fileID
	row := model.AiTask{
		UserID:   &uid,
		FileID:   &fid,
		TaskType: strings.TrimSpace(taskType),
		Status:   strings.TrimSpace(status),
		Result:   strings.TrimSpace(result),
	}
	if row.TaskType == "" {
		row.TaskType = "unknown"
	}
	if row.Status == "" {
		row.Status = "success"
	}
	return h.DB.WithContext(ctx).Create(&row).Error
}

func (h *AIHandler) writeAIErr(c *gin.Context, err error) {
	if err == nil {
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

// extractEXIFInfo 解析图片 EXIF 信息；解析失败时返回空结构，不中断主流程。
func extractEXIFInfo(imagePath string) exifInfo {
	info := exifInfo{}
	f, err := os.Open(imagePath)
	if err != nil {
		return info
	}
	defer f.Close()

	x, err := exif.Decode(f)
	if err != nil {
		return info
	}

	if lat, lon, err := x.LatLong(); err == nil {
		latCopy := lat
		lonCopy := lon
		info.Latitude = &latCopy
		info.Longitude = &lonCopy
	}

	if dt, err := x.DateTime(); err == nil {
		info.CapturedAt = dt.Format("2006-01-02 15:04:05")
	}

	if tag, err := x.Get(exif.Make); err == nil {
		if v, e := tag.StringVal(); e == nil {
			info.CameraMake = strings.TrimSpace(v)
		}
	}
	if tag, err := x.Get(exif.Model); err == nil {
		if v, e := tag.StringVal(); e == nil {
			info.CameraModel = strings.TrimSpace(v)
		}
	}
	if tag, err := x.Get(exif.GPSAltitude); err == nil {
		if n, d, e := tag.Rat2(0); e == nil && d != 0 {
			alt := float64(n) / float64(d)
			info.Altitude = &alt
		}
	}

	return info
}

func formatEXIFLocationTag(info exifInfo) string {
	if info.Latitude == nil || info.Longitude == nil {
		return ""
	}
	lat := strconv.FormatFloat(*info.Latitude, 'f', 6, 64)
	lon := strconv.FormatFloat(*info.Longitude, 'f', 6, 64)
	if info.Altitude == nil {
		return "gps:" + lat + "," + lon
	}
	alt := strconv.FormatFloat(*info.Altitude, 'f', 1, 64)
	return "gps:" + lat + "," + lon + ",alt=" + alt
}

// summarizeDocumentPhoto 从 OCR 文本提炼关键信息，便于文档照片场景快速浏览。
func (h *AIHandler) summarizeDocumentPhoto(ctx context.Context, ocrText string) string {
	ocrText = strings.TrimSpace(ocrText)
	if ocrText == "" || ocrText == "未识别到有效文本" {
		return ""
	}
	// 文本较短时直接截断返回，避免不必要的模型调用。
	if len(ocrText) < 120 {
		if len(ocrText) > 80 {
			return ocrText[:80]
		}
		return ocrText
	}

	source := ocrText
	if len(source) > 4000 {
		source = source[:4000]
	}
	prompt := "请将以下OCR文本提炼为3-5条中文关键点，适合文档照片快速阅读；仅输出要点，不要解释。\\n\\n" + source
	res, err := h.callOllamaGenerate(ctx, prompt, nil)
	if err != nil {
		fallback := source
		if len(fallback) > 200 {
			fallback = fallback[:200]
		}
		return strings.TrimSpace(fallback)
	}
	res = strings.TrimSpace(res)
	if len(res) > 500 {
		res = res[:500]
	}
	return res
}
