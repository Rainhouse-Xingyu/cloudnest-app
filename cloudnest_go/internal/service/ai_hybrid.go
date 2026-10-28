package service

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cloudenest_go/internal/aiindex"
	"cloudenest_go/internal/service/contract"
)

const hybridWindow = 200

var hybridSlots = make(chan struct{}, 2)
var ErrAISearchBusy = errors.New("AI search capacity unavailable")

type hybridHit struct {
	row      contract.AIHybridRow
	lexical  float64
	semantic float64
	score    float64
	keyword  bool
	vector   bool
}

func (s *AIService) hybridSearch(parent context.Context, uid int64, question string, page, limit int) (AINLSearchResult, error) {
	question = strings.TrimSpace(question)
	if question == "" || !utf8.ValidString(question) || len(question) > 4096 || page > 10000 {
		return AINLSearchResult{}, contract.ErrInvalidInput
	}
	if page <= 0 {
		page = 1
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	select {
	case hybridSlots <- struct{}{}:
		defer func() { <-hybridSlots }()
	default:
		return AINLSearchResult{}, ErrAISearchBusy
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	filter, unsupported := hybridIntent(question, time.Now())
	result := AINLSearchResult{Query: AISearchQuery{Keyword: question, Page: page, Limit: limit}, Source: "keyword", Warnings: []string{}, Search: AISearchResult{Items: []map[string]interface{}{}}}
	if filter.UploadedAfter != nil {
		result.Query.StartTime = filter.UploadedAfter.Format(time.RFC3339)
	}
	if filter.UploadedBefore != nil {
		result.Query.EndTime = filter.UploadedBefore.Format(time.RFC3339)
	}
	if filter.CapturedAfter != nil {
		result.Query.StartTime = filter.CapturedAfter.Format("2006-01-02 15:04:05")
		result.Query.EndTime = filter.CapturedBefore.Format("2006-01-02 15:04:05")
		result.Warnings = append(result.Warnings, "capture_time_uses_exif_camera_local_calendar")
	}
	if len(filter.FileTypes) > 0 {
		result.Query.FileType = strings.Join(filter.FileTypes, ",")
	}
	if unsupported != "" {
		result.Warnings = append(result.Warnings, unsupported)
		return result, nil
	}
	model := s.cfg.AIEmbeddingModel
	if model == "" {
		model = "qwen3-embedding:0.6b"
	}
	provider, providerErr := aiindex.NewOllama(s.cfg.AIOllamaBaseURL, model)
	var query []float32
	digest := ""
	if providerErr == nil {
		op, end := context.WithTimeout(ctx, 6*time.Second)
		var vectors [][]float32
		digest, providerErr = provider.Digest(op)
		if providerErr == nil {
			vectors, providerErr = provider.Embed(op, []string{question})
		}
		if providerErr == nil {
			confirmed, e := provider.Digest(op)
			if e != nil || confirmed != digest {
				providerErr = aiindex.ErrEmbedding
			} else {
				query = vectors[0]
			}
		}
		end()
	}
	if parent.Err() != nil {
		return AINLSearchResult{}, parent.Err()
	}
	if len(query) > 0 {
		result.Source = "hybrid"
	} else {
		result.Source = "keyword_fallback"
		result.Warnings = append(result.Warnings, "embedding_unavailable")
	}
	terms := hybridTerms(question)
	minCosine, minLexical := s.cfg.SearchThresholds()
	var lexical, semantic []hybridHit
	var bestLex, bestSemantic hybridHit
	var currentID int64
	matched := 0
	flush := func() {
		if currentID == 0 {
			return
		}
		if bestLex.lexical > 0 || bestSemantic.semantic >= minCosine {
			matched++
		}
		if bestLex.lexical > 0 {
			lexical = retainHybrid(lexical, bestLex, false)
		}
		if bestSemantic.semantic >= minCosine {
			semantic = retainHybrid(semantic, bestSemantic, true)
		}
	}
	scanCtx, endScan := context.WithTimeout(ctx, 10*time.Second)
	err := s.repo.ScanHybrid(scanCtx, uid, filter, func(row contract.AIHybridRow) error {
		if row.ID != currentID {
			flush()
			currentID = row.ID
			bestLex = hybridHit{}
			bestSemantic = hybridHit{semantic: -1}
		}
		// A single generic bigram in a long query must not gain a full RRF vote.
		nameCoverage, bodyCoverage := lexicalScore(row.Filename, terms), lexicalScore(row.Content, terms)
		lex := 0.0
		if nameCoverage >= minLexical {
			lex += nameCoverage * 3
		}
		if bodyCoverage >= minLexical {
			lex += bodyCoverage
		}
		sem := -1.0
		if len(query) > 0 && row.ModelDigest == digest && row.EmbeddingModel == model && row.Dimensions == len(query) {
			sem = vectorDot(query, row.Vector)
		}
		row.Vector = nil // never retain entire vector sets in request memory
		if lex > bestLex.lexical {
			bestLex = hybridHit{row: row, lexical: lex}
		}
		if sem > bestSemantic.semantic {
			bestSemantic = hybridHit{row: row, semantic: sem}
		}
		return nil
	})
	endScan()
	if err != nil {
		return AINLSearchResult{}, err
	}
	flush()
	fused := map[int64]*hybridHit{}
	for rank, h := range lexical {
		copy := h
		copy.keyword = true
		copy.score = 1 / float64(61+rank)
		fused[h.row.ID] = &copy
	}
	for rank, h := range semantic {
		existing := fused[h.row.ID]
		if existing == nil {
			copy := h
			existing = &copy
			fused[h.row.ID] = existing
		}
		existing.vector = true
		existing.semantic = h.semantic
		existing.score += 1 / float64(61+rank)
		if !existing.keyword || lexicalScore(existing.row.Content, terms) == 0 {
			existing.row = h.row
		}
	}
	identities := make([]contract.AIHybridRow, 0, len(fused))
	for _, h := range fused {
		identities = append(identities, h.row)
	}
	valid, err := s.repo.ValidateHybrid(ctx, uid, identities)
	if err != nil {
		return AINLSearchResult{}, err
	}
	hits := make([]hybridHit, 0, len(fused))
	for id, h := range fused {
		if valid[id] {
			hits = append(hits, *h)
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score == hits[j].score {
			return hits[i].row.ID < hits[j].row.ID
		}
		return hits[i].score > hits[j].score
	})
	result.WindowLimited = matched > hybridWindow
	if len(hits) > hybridWindow {
		hits = hits[:hybridWindow]
	}
	result.Search.Total = int64(len(hits))
	start := min((page-1)*limit, len(hits))
	end := min(start+limit, len(hits))
	for _, h := range hits[start:end] {
		sources := []string{}
		if h.keyword {
			sources = append(sources, "keyword")
		}
		if h.vector {
			sources = append(sources, "semantic")
		}
		snippet, offset := hybridSnippet(h.row.Content, terms)
		item := map[string]interface{}{"id": h.row.ID, "filename": h.row.Filename, "file_type": h.row.FileType, "size": h.row.Size, "parent_id": h.row.ParentID, "user_id": h.row.UserID, "create_time": h.row.CreateTime, "update_time": h.row.UpdateTime,
			"tags": h.row.VisualTags, "description": h.row.VisualDescription, "ocr_snippet": snippet, "has_ocr_index": h.row.HasOCR,
			"extraction_kind": h.row.ExtractionKind, "vision_model": h.row.VisionModel, "vision_digest": h.row.VisionDigest, "has_text_index": h.row.Generation > 0,
			"snippet": snippet, "snippet_offset_basis": "extracted_text_runes", "snippet_start_rune": h.row.StartRune + offset, "chunk_ordinal": h.row.Ordinal, "index_generation": h.row.Generation, "match_sources": sources, "score": h.score}
		if h.row.CapturedAtLocal != nil {
			item["captured_at_local"] = h.row.CapturedAtLocal.Format("2006-01-02 15:04:05")
			item["capture_offset"] = h.row.CaptureOffset
			item["capture_time_basis"] = "exif_camera_local"
		}
		if h.vector {
			item["semantic_score"] = h.semantic
		}
		result.Search.Items = append(result.Search.Items, item)
	}
	return result, nil
}

func retainHybrid(hits []hybridHit, h hybridHit, semantic bool) []hybridHit {
	score := func(v hybridHit) float64 {
		if semantic {
			return v.semantic
		}
		return v.lexical
	}
	if len(hits) == hybridWindow && score(h) < score(hits[len(hits)-1]) {
		return hits
	}
	hits = append(hits, h)
	sort.Slice(hits, func(i, j int) bool {
		a, b := score(hits[i]), score(hits[j])
		if a == b {
			return hits[i].row.ID < hits[j].row.ID
		}
		return a > b
	})
	if len(hits) > hybridWindow {
		hits = hits[:hybridWindow]
	}
	return hits
}

func vectorDot(query []float32, raw []byte) float64 {
	if len(raw) != len(query)*4 {
		return -1
	}
	var dot, norm float64
	for i, q := range query {
		v := float64(math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:])))
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return -1
		}
		dot += float64(q) * v
		norm += v * v
	}
	if math.Abs(norm-1) > 1e-3 {
		return -1
	}
	return max(-1, min(1, dot))
}

func lexicalScore(text string, terms []string) float64 {
	if len(terms) == 0 {
		return 0
	}
	text = strings.ToLower(text)
	score := 0.0
	for _, term := range terms {
		if strings.Contains(text, term) {
			score++
		}
	}
	return score / float64(len(terms))
}

func hybridTerms(question string) []string {
	question = strings.ToLower(question)
	for _, filler := range []string{"帮我", "找出", "找到", "寻找", "查找", "搜索", "相关的", "相关", "有哪些", "之前", "上传的", "上个月", "上月", "本月", "上一年", "去年", "今年", "拍摄的", "的文档", "的资料", "的文件", "请"} {
		question = strings.ReplaceAll(question, filler, " ")
	}
	seen := map[string]bool{}
	var result []string
	add := func(s string) {
		if len(result) < 32 && s != "" && !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	for _, word := range strings.FieldsFunc(question, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '.' && r != '_' }) {
		var ascii []rune
		var han []rune
		flushASCII := func() {
			w := string(ascii)
			ascii = nil
			switch w {
			case "find", "me", "the", "file", "files", "about", "show", "which", "documents", "document", "please", "and", "for", "to", "a", "of", "in":
				return
			}
			if len(w) > 1 {
				add(w)
			}
		}
		flushHan := func() {
			if len(han) == 1 && !strings.ContainsRune("的和与", han[0]) {
				add(string(han))
			}
			for i := 0; i+1 < len(han); i++ {
				add(string(han[i : i+2]))
			}
			han = nil
		}
		for _, r := range word {
			if unicode.Is(unicode.Han, r) {
				flushASCII()
				han = append(han, r)
			} else {
				flushHan()
				ascii = append(ascii, r)
			}
		}
		flushASCII()
		flushHan()
	}
	return result
}

func hybridSnippet(text string, terms []string) (string, int) {
	runes := []rune(text)
	start := 0
	lower := strings.ToLower(text)
	for _, term := range terms {
		if at := strings.Index(lower, term); at >= 0 {
			start = max(0, utf8.RuneCountInString(lower[:at])-60)
			break
		}
	}
	end := min(start+240, len(runes))
	if start > len(runes) {
		start = 0
		end = min(240, len(runes))
	}
	return string(runes[start:end]), start
}

// Relative upload filters use the product's Asia/Shanghai calendar, converted
// to UTC before SQL. Capture time is not substituted with upload time.
func hybridIntent(question string, now time.Time) (contract.AIHybridFilter, string) {
	var f contract.AIHybridFilter
	imageQuery := strings.Contains(question, "照片") || strings.Contains(question, "图片")
	if imageQuery {
		f.FileTypes = []string{"jpg", "jpeg", "png", "gif", "webp", "heic", "heif"}
	}
	if !imageQuery && (strings.Contains(question, "配置文件") || strings.Contains(question, "设置文件")) {
		f.FileTypes = []string{"json", "xml", "yaml", "yml", "toml", "ini", "conf", "config", "env", "properties", "txt"}
	}
	zone := time.FixedZone("Asia/Shanghai", 8*3600)
	now = now.In(zone)
	var start, end time.Time
	switch {
	case strings.Contains(question, "上个月") || strings.Contains(question, "上月"):
		end = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, zone)
		start = end.AddDate(0, -1, 0)
	case strings.Contains(question, "本月"):
		start = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, zone)
		end = start.AddDate(0, 1, 0)
	case strings.Contains(question, "去年") || strings.Contains(question, "上一年"):
		start = time.Date(now.Year()-1, 1, 1, 0, 0, 0, 0, zone)
		end = start.AddDate(1, 0, 0)
	case strings.Contains(question, "今年"):
		start = time.Date(now.Year(), 1, 1, 0, 0, 0, 0, zone)
		end = start.AddDate(1, 0, 0)
	}
	if !start.IsZero() {
		if !strings.Contains(question, "上传") && (strings.Contains(question, "拍摄") || strings.Contains(question, "拍的") || imageQuery) {
			f.CapturedAfter = &start
			f.CapturedBefore = &end
			return f, ""
		}
		a, b := start.UTC(), end.UTC()
		f.UploadedAfter = &a
		f.UploadedBefore = &b
	}
	return f, ""
}
