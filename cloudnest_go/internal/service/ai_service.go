package service

import (
	"bytes"
	"context"
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
	"unicode/utf8"

	"cloudenest_go/internal/aiindex"
	"cloudenest_go/internal/blob"
	"cloudenest_go/internal/config"
	"cloudenest_go/internal/imageprep"
	"cloudenest_go/internal/pdfextract"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"

	"gorm.io/gorm"
)

const aiReadMaxBytes int64 = 2 * 1024 * 1024

// AIService 承接 AI 模块业务逻辑。
type AIService struct {
	repo       contract.AIRepository
	cfg        config.Config
	httpClient *http.Client
}

func NewAIService(repo contract.AIRepository, cfg config.Config, httpClient *http.Client) *AIService {
	return &AIService{repo: repo, cfg: cfg, httpClient: httpClient}
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
	Metadata           model.AiMetadata
	EXIF               exifInfo
	DocSummary         string
	HasText            bool
	Model, ModelDigest string
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
	Keyword   string `json:"keyword"`
	FileType  string `json:"file_type"`
	Location  string `json:"location"`
	Landmark  string `json:"landmark"`
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
	TagOnly   bool   `json:"tag_only"`
	Page      int    `json:"page"`
	Limit     int    `json:"limit"`
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
	Search        AISearchResult
	Query         AISearchQuery
	Source        string
	Warnings      []string
	WindowLimited bool
	Rerank        AIRerankResult
}

type AIRerankResult struct {
	Status         string `json:"status"`
	FailureReason  string `json:"failure_reason,omitempty"`
	Model          string `json:"model,omitempty"`
	ModelDigest    string `json:"model_digest,omitempty"`
	EvaluatedCount int    `json:"evaluated_count"`
	WindowSize     int    `json:"window_size"`
	WindowLimited  bool   `json:"window_limited"`
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
	Latitude      *float64
	Longitude     *float64
	Altitude      *float64
	CapturedAt    string
	CameraMake    string
	CameraModel   string
	CaptureOffset string
}

// OCR 对图片执行 OCR 并写入 ai_metadata 索引。
func (s *AIService) OCR(ctx context.Context, uid int64, req AIOCRInput) (AIOCRResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	f, storage, err := s.getReadableFileAndStorage(ctx, uid, req.FileID)
	if err != nil {
		return AIOCRResult{}, err
	}

	if !aiindex.SupportsImage(filepath.Ext(f.Filename)) {
		return AIOCRResult{}, fmt.Errorf("ocr only supports image files")
	}
	if f.Size > imageprep.MaxEncodedBytes {
		return AIOCRResult{}, imageprep.ErrLimit
	}
	if len(req.Description) > 8192 || len(req.CapturedAt) > 64 || utf8.RuneCountInString(mergeTags(req.Tags, req.Location, req.Landmark)) > 255 {
		return AIOCRResult{}, contract.ErrInvalidInput
	}
	in, verified, err := blob.OpenPinnedStorage(ctx, storage)
	if err != nil {
		if ctx.Err() != nil {
			return AIOCRResult{}, ctx.Err()
		}
		return AIOCRResult{}, contract.ErrStorageIntegrity
	}
	prepared, err := imageprep.Prepare(ctx, in)
	closeErr := in.Close()
	if err != nil {
		return AIOCRResult{}, err
	}
	if closeErr != nil {
		return AIOCRResult{}, contract.ErrStorageIntegrity
	}
	exifMeta := exifInfo{Latitude: prepared.Metadata.Latitude, Longitude: prepared.Metadata.Longitude, Altitude: prepared.Metadata.Altitude, CameraMake: prepared.Metadata.CameraMake, CameraModel: prepared.Metadata.CameraModel, CaptureOffset: prepared.Metadata.Offset}
	if prepared.Metadata.CapturedLocal != nil {
		exifMeta.CapturedAt = prepared.Metadata.CapturedLocal.Format("2006-01-02 15:04:05")
	}
	exifLocationTag := formatEXIFLocationTag(exifMeta)
	tags := mergeTags(req.Tags, req.Location, req.Landmark, exifLocationTag)
	if utf8.RuneCountInString(tags) > 255 {
		return AIOCRResult{}, contract.ErrInvalidInput
	}
	vision, err := aiindex.NewOllama(s.cfg.AIOllamaBaseURL, s.cfg.AIOCRModel)
	if err != nil {
		return AIOCRResult{}, aiindex.ErrModelUnavailable
	}
	analysis, digest, err := vision.AnalyzeImage(ctx, prepared.PNG)
	if err != nil {
		code := "model_unavailable"
		switch {
		case errors.Is(err, aiindex.ErrExtractionBusy):
			code = "extraction_busy"
		case errors.Is(err, aiindex.ErrModelChanged):
			code = "model_changed"
		case errors.Is(err, aiindex.ErrVisionResponse), errors.Is(err, aiindex.ErrEmbedding):
			code = "invalid_model_response"
		}
		_ = s.createAITask(ctx, uid, req.FileID, "ocr", "failed", code)
		return AIOCRResult{}, err
	}
	ocrText := analysis.Text
	photoSummary := ""
	if analysis.HasText {
		photoSummary = analysis.Description
	}
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

	if err := blob.Check(ctx); err != nil {
		return AIOCRResult{}, err
	}
	meta, err := s.repo.PublishOCR(ctx, uid, f, verified.FileHash, tags, desc, ocrText)
	if err != nil {
		return AIOCRResult{}, err
	}

	return AIOCRResult{Metadata: meta, EXIF: exifMeta, DocSummary: photoSummary, HasText: analysis.HasText, Model: vision.Model(), ModelDigest: digest}, nil
}

func (s *AIService) Search(ctx context.Context, uid int64, q AISearchQuery) (AISearchResult, error) {
	items, total, err := s.executeAISearch(ctx, uid, q)
	if err != nil {
		return AISearchResult{}, err
	}
	return AISearchResult{Items: items, Total: total}, nil
}

func (s *AIService) NLSearch(ctx context.Context, uid int64, question string, page int, limit int) (AINLSearchResult, error) {
	return s.hybridSearch(ctx, uid, question, page, limit)
}

func (s *AIService) Summarize(ctx context.Context, uid int64, fileID int64, maxPoints int) (AISummaryResult, error) {
	f, storage, err := s.getReadableFileAndStorage(ctx, uid, fileID)
	if err != nil {
		return AISummaryResult{}, err
	}

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(f.Filename), "."))
	var body string
	var truncated bool
	if aiindex.SupportsOffice(ext) || ext == "pdf" {
		if f.Size > aiindex.MaxOfficeBytes {
			return AISummaryResult{}, aiindex.ErrTextLimit
		}
		parseTimeout := 30 * time.Second
		if ext == "pdf" {
			parseTimeout = 2 * time.Minute
		}
		op, end := context.WithTimeout(ctx, parseTimeout)
		defer end()
		in, _, openErr := blob.OpenPinnedStorage(op, storage)
		if openErr != nil {
			return AISummaryResult{}, fmt.Errorf("read file failed")
		}
		info, statErr := in.Stat()
		if statErr != nil {
			in.Close()
			return AISummaryResult{}, fmt.Errorf("read file failed")
		}
		if ext == "pdf" {
			vision, initErr := aiindex.NewOllama(s.cfg.AIOllamaBaseURL, s.cfg.AIOCRModel)
			if initErr != nil {
				in.Close()
				return AISummaryResult{}, pdfextract.ErrUnavailable
			}
			body, err = (pdfextract.Reader{BinDir: s.cfg.AIPDFBinDir}).Read(op, in, vision.Transcribe)
		} else {
			body, err = aiindex.ReadOffice(op, in, info.Size(), ext)
		}
		closeErr := in.Close()
		if errors.Is(err, aiindex.ErrExtractionBusy) || errors.Is(err, pdfextract.ErrUnavailable) || errors.Is(err, pdfextract.ErrBusy) || errors.Is(err, pdfextract.ErrLimit) || errors.Is(err, pdfextract.ErrInvalid) || errors.Is(err, aiindex.ErrModelUnavailable) || errors.Is(err, aiindex.ErrVisionResponse) || errors.Is(err, aiindex.ErrModelChanged) || errors.Is(err, aiindex.ErrTextLimit) || errors.Is(err, aiindex.ErrDocument) {
			return AISummaryResult{}, err
		}
		if err == nil {
			err = closeErr
		}
	} else {
		body, truncated, err = extractDocumentText(storage.FilePath, ext, aiReadMaxBytes)
	}
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
	return s.repo.ApplySettingWithAudit(ctx, uid, key, value, description, clientIP)
}

// GetReadableFileAndStorage 获取用户可读文件及其存储记录。
func (s *AIService) GetReadableFileAndStorage(ctx context.Context, userID int64, fileID int64) (model.File, model.Storage, error) {
	return s.getReadableFileAndStorage(ctx, userID, fileID)
}

// CanAccessFile 判断用户是否具备文件访问权限。
func (s *AIService) CanAccessFile(ctx context.Context, userID int64, fileID int64, requireWrite bool) (bool, error) {
	return s.canAccessFile(ctx, userID, fileID, requireWrite)
}

// UpsertAIMetadata 写入或更新 AI 元数据。
func (s *AIService) UpsertAIMetadata(ctx context.Context, fileID int64, tags string, desc string, ocrText string) (model.AiMetadata, error) {
	return s.upsertAIMetadata(ctx, fileID, tags, desc, ocrText)
}

// CreateAITask 创建 AI 任务记录。
func (s *AIService) CreateAITask(ctx context.Context, userID int64, fileID int64, taskType string, status string, result string) error {
	return s.createAITask(ctx, userID, fileID, taskType, status, result)
}

func (s *AIService) executeAISearch(ctx context.Context, uid int64, q AISearchQuery) ([]map[string]interface{}, int64, error) {
	var startAt *time.Time
	if q.StartTime != "" {
		t, err := parseFlexibleTime(q.StartTime)
		if err != nil {
			return nil, 0, err
		}
		startAt = &t
	}
	var endAt *time.Time
	if q.EndTime != "" {
		t, err := parseFlexibleTime(q.EndTime)
		if err != nil {
			return nil, 0, err
		}
		endAt = &t
	}

	rows, total, err := s.repo.Search(ctx, uid, contract.AISearchQuery{
		Keyword:   q.Keyword,
		FileType:  q.FileType,
		Location:  q.Location,
		Landmark:  q.Landmark,
		StartTime: startAt,
		EndTime:   endAt,
		TagOnly:   q.TagOnly,
		Page:      q.Page,
		Limit:     q.Limit,
	})
	if err != nil {
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

	f, err := s.repo.GetActiveFileByID(ctx, fileID)
	if err != nil {
		return model.File{}, model.Storage{}, err
	}
	if f.IsFolder == 1 {
		return model.File{}, model.Storage{}, fmt.Errorf("target is folder")
	}
	if f.StorageID == nil {
		return model.File{}, model.Storage{}, gorm.ErrRecordNotFound
	}

	storage, err := s.repo.GetStorageByID(ctx, *f.StorageID)
	if err != nil {
		return model.File{}, model.Storage{}, err
	}
	storage, err = blob.Pin(ctx, storage)
	if err != nil {
		return model.File{}, model.Storage{}, err
	}
	if _, err := os.Stat(storage.FilePath); err != nil {
		return model.File{}, model.Storage{}, os.ErrNotExist
	}
	return f, storage, nil
}

func (s *AIService) canAccessFile(ctx context.Context, userID int64, fileID int64, requireWrite bool) (bool, error) {
	f, err := s.repo.GetActiveFileByID(ctx, fileID)
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
	count, err := s.repo.CountFilePermissionsByLevels(ctx, fileID, userID, permissions)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *AIService) upsertAIMetadata(ctx context.Context, fileID int64, tags string, desc string, ocrText string) (model.AiMetadata, error) {
	tags = strings.TrimSpace(tags)
	desc = strings.TrimSpace(desc)
	ocrText = strings.TrimSpace(ocrText)

	row, err := s.repo.GetAIMetadataByFileID(ctx, fileID)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return model.AiMetadata{}, err
		}
		return s.repo.CreateAIMetadata(ctx, model.AiMetadata{FileID: fileID, Tags: tags, Description: desc, OcrText: ocrText})
	}
	return s.repo.UpdateAIMetadata(ctx, row, tags, desc, ocrText)
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
	return s.repo.CreateAITask(ctx, row)
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
