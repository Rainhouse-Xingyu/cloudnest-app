package service

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"cloudenest_go/internal/config"
	"cloudenest_go/model"

	pdf "github.com/ledongthuc/pdf"
	"github.com/rwcarlsen/goexif/exif"
	"gorm.io/gorm"
)

const aiReadMaxBytes int64 = 2 * 1024 * 1024

// AIService 承接 AI 模块业务逻辑。
type AIService struct {
	db         *gorm.DB
	cfg        config.Config
	httpClient *http.Client
}

func NewAIService(db *gorm.DB, cfg config.Config, httpClient *http.Client) *AIService {
	return &AIService{db: db, cfg: cfg, httpClient: httpClient}
}

type AIOCRInput struct {
	FileID      int64
	Location    string
	Landmark    string
	CapturedAt  string
	Tags        string
	Description string
}

type AIOCRResult struct {
	Metadata   model.AiMetadata
	EXIF       exifInfo
	DocSummary string
}

type AISummaryResult struct {
	FileID    int64
	Filename  string
	Summary   string
	Truncated bool
	MaxPoints int
	Model     string
}

type AISearchQuery struct {
	Keyword   string
	FileType  string
	Location  string
	Landmark  string
	StartTime string
	EndTime   string
	TagOnly   bool
	Page      int
	Limit     int
}

type NLSearchPlan struct {
	Keyword   string `json:"keyword"`
	FileType  string `json:"file_type"`
	Location  string `json:"location"`
	Landmark  string `json:"landmark"`
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
}

type AISearchResult struct {
	Items []map[string]interface{}
	Total int64
}

type AINLSearchResult struct {
	Search AISearchResult
	Query  AISearchQuery
	Source string
}

type AIAskResult struct {
	Answer         string
	Model          string
	GuideDocLoaded bool
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
func (s *AIService) OCR(ctx context.Context, uid int64, req AIOCRInput) (AIOCRResult, error) {
	f, storage, err := s.getReadableFileAndStorage(ctx, uid, req.FileID)
	if err != nil {
		return AIOCRResult{}, err
	}

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(f.Filename), "."))
	if !isImageExt(ext) && !isAIImageExt(ext) {
		return AIOCRResult{}, fmt.Errorf("ocr only supports image files")
	}

	raw, err := os.ReadFile(storage.FilePath)
	if err != nil {
		return AIOCRResult{}, fmt.Errorf("read image failed")
	}

	exifMeta := extractEXIFInfo(storage.FilePath)
	prompt := "请执行OCR识别，完整提取图片中的可见文字，尽量保留原始顺序与换行。"
	ocrText, callErr := s.callOllamaGenerate(ctx, prompt, []string{base64.StdEncoding.EncodeToString(raw)})
	if callErr != nil {
		_ = s.createAITask(ctx, uid, req.FileID, "ocr", "failed", callErr.Error())
		return AIOCRResult{}, fmt.Errorf("ollama call failed")
	}
	ocrText = strings.TrimSpace(ocrText)
	if ocrText == "" {
		ocrText = "未识别到有效文本"
	}

	photoSummary := s.summarizeDocumentPhoto(ctx, ocrText)
	exifLocationTag := formatEXIFLocationTag(exifMeta)
	tags := mergeTags(req.Tags, req.Location, req.Landmark, exifLocationTag)
	desc := strings.TrimSpace(req.Description)
	if capAt := strings.TrimSpace(req.CapturedAt); capAt != "" {
		if desc == "" {
			desc = "captured_at=" + capAt
		} else {
			desc = desc + " | captured_at=" + capAt
		}
	}
	if exifMeta.CapturedAt != "" {
		if desc == "" {
			desc = "exif_captured_at=" + exifMeta.CapturedAt
		} else {
			desc = desc + " | exif_captured_at=" + exifMeta.CapturedAt
		}
	}
	if exifMeta.CameraMake != "" || exifMeta.CameraModel != "" {
		camera := strings.TrimSpace(strings.TrimSpace(exifMeta.CameraMake) + " " + strings.TrimSpace(exifMeta.CameraModel))
		if camera != "" {
			if desc == "" {
				desc = "camera=" + camera
			} else {
				desc = desc + " | camera=" + camera
			}
		}
	}
	if photoSummary != "" {
		if desc == "" {
			desc = "doc_summary=" + photoSummary
		} else {
			desc = desc + " | doc_summary=" + photoSummary
		}
	}

	meta, err := s.upsertAIMetadata(ctx, req.FileID, tags, desc, ocrText)
	if err != nil {
		return AIOCRResult{}, fmt.Errorf("save ai metadata failed")
	}
	_ = s.createAITask(ctx, uid, req.FileID, "ocr", "success", ocrText)

	return AIOCRResult{Metadata: meta, EXIF: exifMeta, DocSummary: photoSummary}, nil
}

func (s *AIService) Search(ctx context.Context, uid int64, q AISearchQuery) (AISearchResult, error) {
	items, total, err := s.executeAISearch(ctx, uid, q)
	if err != nil {
		return AISearchResult{}, err
	}
	return AISearchResult{Items: items, Total: total}, nil
}

func (s *AIService) NLSearch(ctx context.Context, uid int64, question string, page int, limit int) (AINLSearchResult, error) {
	plan, source := s.parseNLSearchPlan(ctx, question)
	q := AISearchQuery{
		Keyword:   strings.TrimSpace(plan.Keyword),
		FileType:  strings.ToLower(strings.TrimSpace(plan.FileType)),
		Location:  strings.TrimSpace(plan.Location),
		Landmark:  strings.TrimSpace(plan.Landmark),
		StartTime: strings.TrimSpace(plan.StartTime),
		EndTime:   strings.TrimSpace(plan.EndTime),
		Page:      page,
		Limit:     limit,
	}
	q.TagOnly = shouldUseTagOnlyForNL(question, q)
	if q.Page <= 0 {
		q.Page = 1
	}
	if q.Limit <= 0 || q.Limit > 100 {
		q.Limit = 20
	}
	items, total, err := s.executeAISearch(ctx, uid, q)
	if err != nil {
		return AINLSearchResult{}, err
	}
	return AINLSearchResult{Search: AISearchResult{Items: items, Total: total}, Query: q, Source: source}, nil
}

func (s *AIService) Summarize(ctx context.Context, uid int64, fileID int64, maxPoints int) (AISummaryResult, error) {
	f, storage, err := s.getReadableFileAndStorage(ctx, uid, fileID)
	if err != nil {
		return AISummaryResult{}, err
	}

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(f.Filename), "."))
	body, truncated, err := extractDocumentText(storage.FilePath, ext, aiReadMaxBytes)
	if err != nil {
		return AISummaryResult{}, fmt.Errorf("read file failed")
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return AISummaryResult{}, fmt.Errorf("document content is empty or unsupported")
	}

	prompt := fmt.Sprintf("请对下面文档生成中文摘要：\\n1) 输出%d条关键点（编号列表）\\n2) 最后补充一段不超过120字的总览\\n3) 只输出结果，不要解释过程。\\n\\n文档内容：\\n%s", maxPoints, body)
	summary, callErr := s.callOllamaGenerate(ctx, prompt, nil)
	if callErr != nil {
		_ = s.createAITask(ctx, uid, fileID, "document_summary", "failed", callErr.Error())
		return AISummaryResult{}, fmt.Errorf("ollama call failed")
	}
	summary = strings.TrimSpace(summary)

	metaDesc := "doc_summary=" + summary
	if truncated {
		metaDesc += " | content_truncated=1"
	}
	indexedText := body
	if len(indexedText) > 100000 {
		indexedText = indexedText[:100000]
	}
	if _, err := s.upsertAIMetadata(ctx, fileID, mergeTags("doc", ext), metaDesc, indexedText); err != nil {
		return AISummaryResult{}, fmt.Errorf("save ai metadata failed")
	}
	_ = s.createAITask(ctx, uid, fileID, "document_summary", "success", summary)

	return AISummaryResult{FileID: fileID, Filename: f.Filename, Summary: summary, Truncated: truncated, MaxPoints: maxPoints, Model: s.cfg.AIOllamaModel}, nil
}

func (s *AIService) AskAssistant(ctx context.Context, question string) (AIAskResult, error) {
	guideText := ""
	if strings.TrimSpace(s.cfg.AIGuideDocPath) != "" {
		b, err := os.ReadFile(strings.TrimSpace(s.cfg.AIGuideDocPath))
		if err == nil {
			guideText = string(b)
			if len(guideText) > 24000 {
				guideText = guideText[:24000]
			}
		}
	}

	prompt := "你是 CloudNest 系统助手，请根据提供的项目文档和当前问题，用中文给出准确可执行的答复。"
	if guideText != "" {
		prompt += "\\n\\n项目文档：\\n" + guideText
	}
	prompt += "\\n\\n用户问题：" + question + "\\n\\n请直接给出答案。"

	answer, err := s.callOllamaGenerate(ctx, prompt, nil)
	if err != nil {
		return AIAskResult{}, fmt.Errorf("ollama call failed")
	}
	return AIAskResult{Answer: strings.TrimSpace(answer), Model: s.cfg.AIOllamaModel, GuideDocLoaded: guideText != ""}, nil
}

func (s *AIService) ApplySetting(ctx context.Context, uid int64, key string, value string, description string, clientIP string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row model.SystemSetting
		err := tx.Where("setting_key = ?", key).First(&row).Error
		if err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			row = model.SystemSetting{SettingKey: key, SettingValue: value, Description: description}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		} else {
			updates := map[string]interface{}{"setting_value": value}
			if description != "" {
				updates["description"] = description
			}
			if err := tx.Model(&row).Updates(updates).Error; err != nil {
				return err
			}
		}

		detail := fmt.Sprintf("ai_apply_setting key=%s value=%s", key, value)
		if len(detail) > 500 {
			detail = detail[:500]
		}
		return tx.Create(&model.OperationLog{UserID: &uid, Action: "ai_apply_setting", Status: "success", Detail: detail, ClientIP: clientIP}).Error
	})
}

func (s *AIService) executeAISearch(ctx context.Context, uid int64, q AISearchQuery) ([]map[string]interface{}, int64, error) {
	base := s.db.WithContext(ctx).
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
	if err := s.db.WithContext(ctx).Table("(?) as t", base).Count(&total).Error; err != nil {
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

	items := make([]map[string]interface{}, 0, len(rows))
	for _, r := range rows {
		snippet := strings.TrimSpace(r.OCRText)
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		items = append(items, map[string]interface{}{
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

func (s *AIService) parseNLSearchPlan(ctx context.Context, question string) (NLSearchPlan, string) {
	question = strings.TrimSpace(question)
	plan := NLSearchPlan{}
	if question == "" {
		return plan, "fallback"
	}

	prompt := "你是文件检索条件解析器。请将用户问题解析为JSON，字段为keyword,file_type,location,landmark,start_time,end_time。" +
		"仅输出JSON对象，不要输出其他内容。file_type可用值示例：image,document,pdf,jpg。用户问题：" + question
	resp, err := s.callOllamaGenerate(ctx, prompt, nil)
	if err == nil {
		obj := extractJSONObject(resp)
		if obj != "" {
			var parsed NLSearchPlan
			if e := json.Unmarshal([]byte(obj), &parsed); e == nil {
				parsed = refineNLSearchPlan(parsed, question)
				return parsed, "llm"
			}
		}
	}

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

func (s *AIService) callOllamaGenerate(ctx context.Context, prompt string, images []string) (string, error) {
	baseURL := strings.TrimSpace(s.cfg.AIOllamaBaseURL)
	if baseURL == "" {
		baseURL = "http://127.0.0.1:11434"
	}
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "http://" + baseURL
	}
	modelName := strings.TrimSpace(s.cfg.AIOllamaModel)
	if modelName == "" {
		modelName = "qwen3-vl:4b"
	}

	body := ollamaGenerateRequest{Model: modelName, Prompt: strings.TrimSpace(prompt), Stream: false, Images: images}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	timeoutSec := s.cfg.AITimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 120
	}
	client := s.httpClient
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

func (s *AIService) getReadableFileAndStorage(ctx context.Context, userID int64, fileID int64) (model.File, model.Storage, error) {
	canRead, err := s.canAccessFile(ctx, userID, fileID, false)
	if err != nil {
		return model.File{}, model.Storage{}, err
	}
	if !canRead {
		return model.File{}, model.Storage{}, fmt.Errorf("permission denied")
	}

	var f model.File
	if err := s.db.WithContext(ctx).First(&f, "id = ? AND is_deleted = 0", fileID).Error; err != nil {
		return model.File{}, model.Storage{}, err
	}
	if f.IsFolder == 1 {
		return model.File{}, model.Storage{}, fmt.Errorf("target is folder")
	}
	if f.StorageID == nil {
		return model.File{}, model.Storage{}, gorm.ErrRecordNotFound
	}

	var storage model.Storage
	if err := s.db.WithContext(ctx).First(&storage, "id = ?", *f.StorageID).Error; err != nil {
		return model.File{}, model.Storage{}, err
	}
	if _, err := os.Stat(storage.FilePath); err != nil {
		return model.File{}, model.Storage{}, os.ErrNotExist
	}
	return f, storage, nil
}

func (s *AIService) canAccessFile(ctx context.Context, userID int64, fileID int64, requireWrite bool) (bool, error) {
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

func (s *AIService) upsertAIMetadata(ctx context.Context, fileID int64, tags string, desc string, ocrText string) (model.AiMetadata, error) {
	tags = strings.TrimSpace(tags)
	desc = strings.TrimSpace(desc)
	ocrText = strings.TrimSpace(ocrText)

	var row model.AiMetadata
	err := s.db.WithContext(ctx).Where("file_id = ?", fileID).First(&row).Error
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return model.AiMetadata{}, err
		}
		row = model.AiMetadata{FileID: fileID, Tags: tags, Description: desc, OcrText: ocrText}
		if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
			return model.AiMetadata{}, err
		}
		return row, nil
	}
	if err := s.db.WithContext(ctx).Model(&row).Updates(map[string]interface{}{"tags": tags, "description": desc, "ocr_text": ocrText}).Error; err != nil {
		return model.AiMetadata{}, err
	}
	if err := s.db.WithContext(ctx).First(&row, row.ID).Error; err != nil {
		return model.AiMetadata{}, err
	}
	return row, nil
}

func (s *AIService) createAITask(ctx context.Context, userID int64, fileID int64, taskType string, status string, result string) error {
	uid := userID
	fid := fileID
	row := model.AiTask{UserID: &uid, FileID: &fid, TaskType: strings.TrimSpace(taskType), Status: strings.TrimSpace(status), Result: strings.TrimSpace(result)}
	if row.TaskType == "" {
		row.TaskType = "unknown"
	}
	if row.Status == "" {
		row.Status = "success"
	}
	return s.db.WithContext(ctx).Create(&row).Error
}

func (s *AIService) summarizeDocumentPhoto(ctx context.Context, ocrText string) string {
	ocrText = strings.TrimSpace(ocrText)
	if ocrText == "" || ocrText == "未识别到有效文本" {
		return ""
	}
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
	res, err := s.callOllamaGenerate(ctx, prompt, nil)
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

func refineNLSearchPlan(plan NLSearchPlan, question string) NLSearchPlan {
	plan.Keyword = strings.TrimSpace(plan.Keyword)
	plan.FileType = strings.TrimSpace(plan.FileType)
	plan.Location = strings.TrimSpace(plan.Location)
	plan.Landmark = strings.TrimSpace(plan.Landmark)
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
	patterns := []string{`在([\p{Han}A-Za-z]{2,12})(?:旅游|旅行|拍的|拍摄的|的照片|照片|图片)`, `([\p{Han}A-Za-z]{2,12})(?:旅游|旅行)`, `去([\p{Han}A-Za-z]{2,12})(?:旅游|旅行)`}
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

func readTextFileWithLimit(filePath string, limit int64) (string, bool, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return "", false, err
	}
	truncated := int64(len(raw)) > limit
	if truncated {
		raw = raw[:limit]
	}
	return strings.TrimSpace(string(raw)), truncated, nil
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

func isImageExt(ext string) bool {
	allowed := map[string]bool{"jpg": true, "jpeg": true, "png": true, "gif": true, "webp": true, "heic": true, "heif": true}
	return allowed[strings.ToLower(strings.TrimSpace(ext))]
}

func isAIImageExt(ext string) bool {
	allowed := map[string]bool{"jpg": true, "jpeg": true, "heic": true, "heif": true}
	return allowed[ext]
}

func parseFlexibleTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, fmt.Errorf("empty time")
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			if layout == "2006-01-02" {
				return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local), nil
			}
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time")
}
