package service

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"cloudenest_go/internal/config"
	"cloudenest_go/internal/service/contract"
)

func TestHybridTermsAndSnippetPreserveMultilingualSource(t *testing.T) {
	terms := hybridTerms("帮我寻找和 Spring Boot 权限管理相关的资料")
	for _, want := range []string{"spring", "boot", "权限", "管理"} {
		found := false
		for _, term := range terms {
			if term == want {
				found = true
			}
		}
		if !found {
			t.Fatal("lost query term", want, terms)
		}
	}
	if terms := hybridTerms("猫"); len(terms) != 1 || terms[0] != "猫" {
		t.Fatal(terms)
	}
	text := strings.Repeat("前文🚀", 100) + "权限管理" + strings.Repeat("后文", 150)
	snippet, offset := hybridSnippet(text, []string{"权限"})
	if !utf8.ValidString(snippet) || !strings.Contains(snippet, "权限") || snippet != string([]rune(text)[offset:offset+len([]rune(snippet))]) || len([]rune(snippet)) > 240 {
		t.Fatal("invalid source excerpt", offset, snippet)
	}
}

func TestHybridPlanKeepsClientSnakeCaseContract(t *testing.T) {
	raw, err := json.Marshal(AISearchQuery{Keyword: "migration", Page: 1, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"keyword", "file_type", "location", "landmark", "start_time", "end_time", "tag_only", "page", "limit"} {
		if _, ok := fields[key]; !ok {
			t.Fatal("missing existing client key", key, string(raw))
		}
	}
	if len(fields) != 9 {
		t.Fatal("unexpected query wire contract", string(raw))
	}
}

func TestHybridIntentUsesUploadCalendarAndDoesNotInventCaptureTime(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	f, warning := hybridIntent("上个月上传的合同有哪些", now)
	if warning != "" || f.UploadedAfter == nil || f.UploadedBefore == nil || f.UploadedAfter.Format(time.RFC3339) != "2025-11-30T16:00:00Z" || f.UploadedBefore.Format(time.RFC3339) != "2025-12-31T16:00:00Z" {
		t.Fatal(f, warning)
	}
	f, warning = hybridIntent("去年拍摄的海边照片", now)
	if warning != "" || f.UploadedAfter != nil || len(f.FileTypes) == 0 || f.CapturedAfter == nil || f.CapturedAfter.Year() != now.Year()-1 || f.CapturedBefore == nil || f.CapturedBefore.Year() != now.Year() {
		t.Fatal("capture time silently became upload time")
	}
}

func TestHybridVectorValidationAndBoundedRanking(t *testing.T) {
	raw := make([]byte, 8)
	binary.LittleEndian.PutUint32(raw, math.Float32bits(.6))
	binary.LittleEndian.PutUint32(raw[4:], math.Float32bits(.8))
	if got := vectorDot([]float32{.6, .8}, raw); math.Abs(got-1) > 1e-6 {
		t.Fatal(got)
	}
	binary.LittleEndian.PutUint32(raw, math.Float32bits(float32(math.NaN())))
	if got := vectorDot([]float32{1, 0}, raw); got != -1 {
		t.Fatal("bad vector accepted", got)
	}
	var hits []hybridHit
	for i := 1; i <= 1000; i++ {
		hits = retainHybrid(hits, hybridHit{row: contract.AIHybridRow{ID: int64(i)}, lexical: float64(i)}, false)
	}
	if len(hits) != hybridWindow || hits[0].row.ID != 1000 || hits[len(hits)-1].row.ID != 801 {
		t.Fatal("ranking limited the input prefix")
	}
}

type hybridFixtureRepo struct {
	contract.AIRepository
	rows    []contract.AIHybridRow
	allowed map[int64]bool
}

func (r hybridFixtureRepo) ScanHybrid(ctx context.Context, uid int64, f contract.AIHybridFilter, visit func(contract.AIHybridRow) error) error {
	for _, row := range r.rows {
		if err := visit(row); err != nil {
			return err
		}
	}
	return nil
}
func (r hybridFixtureRepo) ValidateHybrid(context.Context, int64, []contract.AIHybridRow) (map[int64]bool, error) {
	return r.allowed, nil
}

func TestHybridKeywordFallbackRevalidatesBeforeReturningSnippets(t *testing.T) {
	repo := hybridFixtureRepo{rows: []contract.AIHybridRow{{ID: 1, Filename: "safe.md", Content: "数据库迁移", Generation: 1}, {ID: 2, Filename: "revoked.md", Content: "数据库迁移 secret", Generation: 1}}, allowed: map[int64]bool{1: true}}
	s := NewAIService(repo, config.Config{}, nil)
	result, err := s.NLSearch(context.Background(), 1, "数据库迁移", 1, 20)
	if err != nil || result.Source != "keyword_fallback" || len(result.Search.Items) != 1 || result.Search.Items[0]["id"] != int64(1) {
		t.Fatal(result, err)
	}
	if result.Search.Items[0]["snippet_offset_basis"] != "extracted_text_runes" {
		t.Fatal("ambiguous snippet coordinate system")
	}
	if strings.Contains(result.Search.Items[0]["snippet"].(string), "secret") {
		t.Fatal("revoked excerpt leaked")
	}
}

func TestHybridGenericFragmentsDoNotOutrankCompleteSubject(t *testing.T) {
	repo := hybridFixtureRepo{rows: []contract.AIHybridRow{
		{ID: 1, Filename: "neutral.md", Content: "季度报告：系统维护工作已经结束。", Generation: 1},
		{ID: 2, Filename: "neutral.md", Content: "水稻基因测序实验报告。", Generation: 1},
	}, allowed: map[int64]bool{1: true, 2: true}}
	s := NewAIService(repo, config.Config{}, nil)
	result, err := s.NLSearch(context.Background(), 1, "寻找水稻基因测序实验报告", 1, 20)
	if err != nil || len(result.Search.Items) != 1 || result.Search.Items[0]["id"] != int64(2) {
		t.Fatal("generic report fragment survived", result, err)
	}
	// A short direct keyword still works without an available model.
	result, err = s.NLSearch(context.Background(), 1, "报告", 1, 20)
	if err != nil || len(result.Search.Items) != 2 {
		t.Fatal("direct keyword lost", result, err)
	}
}

func TestHybridConfigurationDatesStayUploadDates(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	for _, q := range []string{"上个月上传的配置文件", "上月的设置文件"} {
		f, _ := hybridIntent(q, now)
		if len(f.FileTypes) == 0 || f.CapturedAfter != nil || f.UploadedAfter == nil || f.UploadedAfter.Format(time.RFC3339) != "2025-11-30T16:00:00Z" {
			t.Fatal("configuration date treated as capture", q, f)
		}
	}
	f, _ := hybridIntent("上一年拍下的照片", now)
	if f.CapturedAfter == nil || f.CapturedAfter.Year() != 2025 || f.UploadedAfter != nil {
		t.Fatal("previous-year calendar lost", f)
	}
}
