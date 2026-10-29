//go:build integration && aimodel && aieval

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"cloudenest_go/internal/aiindex"
	"cloudenest_go/internal/app"
	"cloudenest_go/internal/retrievaleval"
	"cloudenest_go/internal/service"
	"cloudenest_go/model"
)

type evalHit struct {
	ID       int64    `json:"id"`
	Document string   `json:"document"`
	Score    float64  `json:"score"`
	Semantic *float64 `json:"semantic_score"`
	Rerank   *float64 `json:"rerank_score,omitempty"`
	Sources  []string `json:"match_sources"`
	Snippet  string   `json:"snippet"`
}
type evalResponse struct {
	Items      []evalHit `json:"items"`
	Pagination struct {
		Total int `json:"total"`
	} `json:"pagination"`
	Retrieval struct {
		Mode          string                 `json:"mode"`
		Warnings      []string               `json:"warnings"`
		WindowLimited bool                   `json:"window_limited"`
		Rerank        service.AIRerankResult `json:"rerank"`
	} `json:"retrieval"`
}
type evalQueryResult struct {
	Query     retrievaleval.Query    `json:"query"`
	Response  evalResponse           `json:"response"`
	LatencyMS int64                  `json:"latency_ms"`
	Metrics   *retrievaleval.Metrics `json:"metrics,omitempty"`
}
type evalDocumentResult struct {
	ID             string    `json:"id"`
	SourceHash     string    `json:"source_sha256"`
	Uploaded       time.Time `json:"uploaded"`
	Capture        string    `json:"capture_local,omitempty"`
	ExtractionKind string    `json:"extraction_kind"`
	VisionDigest   string    `json:"vision_digest,omitempty"`
	Chunks         int       `json:"chunks"`
	LatencyMS      int64     `json:"latency_ms"`
}
type evalReport struct {
	EvaluationSplit   string                            `json:"evaluation_split"`
	QueryPolicy       string                            `json:"query_policy"`
	MinCosine         float64                           `json:"min_cosine"`
	MinLexical        float64                           `json:"min_lexical_coverage"`
	Version           int                               `json:"version"`
	Started           time.Time                         `json:"started"`
	Finished          time.Time                         `json:"finished"`
	Completed         bool                              `json:"completed"`
	Passed            bool                              `json:"passed"`
	Commit            string                            `json:"commit"`
	WorkingDiffSHA256 string                            `json:"working_diff_sha256"`
	HarnessSHA256     string                            `json:"harness_sha256"`
	ManifestSHA256    string                            `json:"manifest_sha256"`
	GoVersion         string                            `json:"go_version"`
	Platform          string                            `json:"platform"`
	OllamaVersion     string                            `json:"ollama_version"`
	EmbeddingModel    string                            `json:"embedding_model"`
	EmbeddingDigest   string                            `json:"embedding_digest"`
	VisionModel       string                            `json:"vision_model"`
	VisionDigest      string                            `json:"vision_digest"`
	RerankModel       string                            `json:"rerank_model,omitempty"`
	RerankDigest      string                            `json:"rerank_digest,omitempty"`
	Gates             retrievaleval.Gates               `json:"quality_gates"`
	Documents         []evalDocumentResult              `json:"documents"`
	Queries           []evalQueryResult                 `json:"queries"`
	Summaries         map[string]*retrievaleval.Summary `json:"summaries"`
	Boundaries        map[string]bool                   `json:"boundaries"`
	Limitations       []string                          `json:"limitations"`
}

// This is an intentionally separate, expensive quality gate. All vectors and
// visual descriptions come from live models; only input documents are synthetic.
func TestAIRetrievalEvaluation(t *testing.T) {
	if os.Getenv("CLOUDNEST_TEST_AI_MODEL") != "1" || os.Getenv("CLOUDNEST_TEST_AI_EVAL") != "1" {
		t.Fatal("run scripts/test-ai-retrieval-eval.sh")
	}
	split := os.Getenv("CLOUDNEST_AI_EVAL_SPLIT")
	if split == "" {
		split = "all"
	}
	if split != "all" && split != "dev" {
		t.Fatal("CLOUDNEST_AI_EVAL_SPLIT must be all or dev")
	}
	raw, err := os.ReadFile("testdata/retrieval/dataset-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	dataset, err := retrievaleval.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	reportPath := os.Getenv("CLOUDNEST_AI_EVAL_REPORT")
	if !filepath.IsAbs(reportPath) {
		t.Fatal("CLOUDNEST_AI_EVAL_REPORT must be a new absolute output path")
	}
	out, err := os.OpenFile(reportPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	anchor := time.Now().In(loc)
	report := evalReport{EvaluationSplit: split, Version: 1, Started: anchor, ManifestSHA256: fmt.Sprintf("%x", sha256.Sum256(raw)), GoVersion: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH, Gates: dataset.Gates, Summaries: map[string]*retrievaleval.Summary{}, Boundaries: map[string]bool{}, Limitations: []string{
		"Synthetic development corpus, not production generalization or an SLA.",
		"Three beach files share pixels and differ only in synthetic EXIF; scanned invoice and invoice image reuse pixels.",
		"Labels and splits fixed before execution; heldout queries visible to implementer, not a blind external test.",
		"Relative dates anchored to Asia/Shanghai at run start; model outputs can vary across runs.",
	}}
	save := func() {
		raw, e := json.MarshalIndent(report, "", "  ")
		if e != nil {
			t.Error(e)
			return
		}
		if e = out.Truncate(0); e != nil {
			t.Error(e)
			return
		}
		if _, e = out.WriteAt(append(raw, '\n'), 0); e != nil {
			t.Error(e)
			return
		}
		if e = out.Sync(); e != nil {
			t.Error(e)
		}
	}
	defer func() { report.Finished = time.Now(); save() }()
	git := func(args ...string) []byte {
		b, e := exec.Command("git", args...).Output()
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	report.Commit = stringTrim(git("rev-parse", "HEAD"))
	report.WorkingDiffSHA256 = fmt.Sprintf("%x", sha256.Sum256(git("diff", "HEAD", "--")))
	harness, err := os.ReadFile("ai_retrieval_eval_test.go")
	if err != nil {
		t.Fatal(err)
	}
	report.HarnessSHA256 = fmt.Sprintf("%x", sha256.Sum256(harness))
	const base = "http://127.0.0.1:11434"
	client := http.Client{Timeout: 5 * time.Second}
	res, err := client.Get(base + "/api/version")
	if err != nil {
		t.Fatal(err)
	}
	var version struct {
		Version string `json:"version"`
	}
	err = json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&version)
	res.Body.Close()
	if err != nil || res.StatusCode != 200 || version.Version == "" {
		t.Fatal("Ollama version", err, res.StatusCode)
	}
	report.OllamaVersion = version.Version
	report.EmbeddingModel = "qwen3-embedding:0.6b"
	report.VisionModel = "qwen3-vl:4b"
	digest := func(name string) string {
		p, e := aiindex.NewOllama(base, name)
		if e != nil {
			t.Fatal(e)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		d, e := p.Digest(ctx)
		if e != nil {
			t.Fatal(e)
		}
		return d
	}
	report.EmbeddingDigest = digest(report.EmbeddingModel)
	report.VisionDigest = digest(report.VisionModel)
	report.RerankModel = strings.TrimSpace(os.Getenv("CLOUDNEST_AI_EVAL_RERANK_MODEL"))
	if report.RerankModel != "" {
		report.RerankDigest = digest(report.RerankModel)
	}
	save()
	f, _ := indexFixture(t)
	f.cfg.AIOllamaBaseURL = base
	f.cfg.AIEmbeddingModel = report.EmbeddingModel
	f.cfg.AIOCRModel = report.VisionModel
	f.cfg.AIRerankModel = report.RerankModel
	report.MinCosine, report.MinLexical = f.cfg.SearchThresholds()
	report.QueryPolicy = "qwen3-task-instruction-calendar-separated-v1"
	f.cfg.AIPDFBinDir = os.Getenv("CLOUDNEST_TEST_POPPLER_BIN")
	if f.cfg.AIPDFBinDir == "" {
		t.Fatal("CLOUDNEST_TEST_POPPLER_BIN required")
	}
	f.router = fixtureRouter(app.NewContainer(f.cfg, f.db, f.rdb).NewRouter())
	ids := map[int64]string{}
	files := map[string]model.File{}
	allowed := map[string]bool{}
	for i, d := range dataset.Documents {
		started := time.Now()
		body := []byte(d.Text)
		if d.Fixture != "" {
			body, err = os.ReadFile(filepath.Join("testdata", filepath.FromSlash(d.Fixture)))
			if err != nil {
				t.Fatal(err)
			}
		}
		capture := ""
		if d.Capture != "" {
			year := anchor.Year()
			if d.Capture == "previous_year" {
				year--
			}
			date := time.Date(year, 6, 15, 10, 20, 30, 0, time.UTC)
			body = syntheticCapturePNG(t, body, date)
			capture = date.Format("2006-01-02 15:04:05")
		}
		token := f.alice
		if d.Owner == "bob" {
			token = f.bob
		}
		file := f.upload(t, token, fmt.Sprintf("entry-%02d.%s", i+1, d.Extension), body)
		uploaded := anchor
		month := time.Date(anchor.Year(), anchor.Month(), 15, 12, 0, 0, 0, loc)
		if d.Uploaded == "previous_month" {
			uploaded = month.AddDate(0, -1, 0)
		}
		if d.Uploaded == "two_months_ago" {
			uploaded = month.AddDate(0, -2, 0)
		}
		if err = f.db.Model(&model.File{}).Where("id=?", file.ID).Update("create_time", uploaded.UTC()).Error; err != nil {
			t.Fatal(err)
		}
		if d.ShareRead {
			if err = f.db.Create(&model.FilePermission{FileID: file.ID, UserID: f.aliceID, Permission: "read"}).Error; err != nil {
				t.Fatal(err)
			}
		}
		if worked, e := textWorker(t, f, base).Step(context.Background()); !worked || e != nil {
			t.Fatalf("index %s: worked=%v error=%v", d.ID, worked, e)
		}
		job := readIndex(t, f, file.ID)
		var doc model.AIIndexDocument
		if err = f.db.First(&doc, "file_id=?", file.ID).Error; err != nil {
			t.Fatal(err)
		}
		sourceHash := fmt.Sprintf("%x", sha256.Sum256(body))
		if job.Status != "done" || doc.Generation != job.Generation || doc.SourceHash != sourceHash || doc.Dimensions != 1024 || doc.ModelDigest != report.EmbeddingDigest || (doc.VisionDigest != "" && doc.VisionDigest != report.VisionDigest) {
			t.Fatalf("index provenance mismatch for %s", d.ID)
		}
		if capture != "" && (doc.CapturedAtLocal == nil || doc.CapturedAtLocal.Format("2006-01-02 15:04:05") != capture || doc.CaptureOffset != "+08:00") {
			t.Fatal("synthetic capture not preserved", d.ID)
		}
		if d.ID == "photo-unknown" && doc.CapturedAtLocal != nil {
			t.Fatal("capture invented from upload")
		}
		var chunks []model.AIIndexChunk
		if err = f.db.Where("file_id=?", file.ID).Find(&chunks).Error; err != nil {
			t.Fatal(err)
		}
		if len(chunks) == 0 || len(chunks) != doc.ChunkCount {
			t.Fatal("missing chunks", d.ID)
		}
		for _, chunk := range chunks {
			decodeVector(t, chunk.Vector)
		}
		ids[file.ID] = d.ID
		files[d.ID] = file
		allowed[d.ID] = d.Owner == "alice" || d.ShareRead
		report.Documents = append(report.Documents, evalDocumentResult{d.ID, sourceHash, uploaded.UTC(), capture, doc.ExtractionKind, doc.VisionDigest, len(chunks), time.Since(started).Milliseconds()})
		save()
		t.Logf("indexed %d/%d %s in %s", i+1, len(dataset.Documents), d.ID, time.Since(started).Round(time.Millisecond))
	}
	search := func(question string) evalResponse {
		var r evalResponse
		decode(t, f.json(t, "POST", "/api/ai/search/nl", f.alice, map[string]any{"question": question, "limit": 100}, 200), &r)
		for i := range r.Items {
			id, ok := ids[r.Items[i].ID]
			if !ok || !allowed[id] {
				t.Fatalf("unknown or unauthorized search hit: %d", r.Items[i].ID)
			}
			r.Items[i].Document = id
			if strings.Contains(r.Items[i].Snippet, "PRIVATE_ONLY_") {
				t.Fatal("private excerpt leaked")
			}
		}
		if r.Retrieval.WindowLimited || r.Pagination.Total != len(r.Items) {
			t.Fatal("evaluation results truncated", r.Pagination.Total, len(r.Items))
		}
		return r
	}
	report.Boundaries["relative_calendar_filters"] = true
	for _, q := range dataset.Queries {
		if split == "dev" && q.Split != "dev" {
			continue
		}
		started := time.Now()
		r := search(q.Question)
		for _, h := range r.Items {
			badDate := q.Family == "beach" && h.Document != "photo-last"
			if q.Family == "contracts" {
				for _, d := range dataset.Documents {
					if d.ID == h.Document && d.Uploaded != "previous_month" {
						badDate = true
					}
				}
			}
			if badDate {
				report.Boundaries["relative_calendar_filters"] = false
				t.Errorf("query %s returned wrong/unknown calendar: %s", q.ID, h.Document)
			}
		}
		ranked := make([]string, len(r.Items))
		for i, h := range r.Items {
			ranked[i] = h.Document
		}
		result := evalQueryResult{Query: q, Response: r, LatencyMS: time.Since(started).Milliseconds()}
		if len(q.Relevant) > 0 {
			m, e := retrievaleval.Measure(q.Relevant, ranked)
			if e != nil {
				t.Fatal(e)
			}
			result.Metrics = &m
		}
		for _, key := range []string{q.Split, "all", "family/" + q.Family} {
			if report.Summaries[key] == nil {
				report.Summaries[key] = &retrievaleval.Summary{}
			}
			if e := report.Summaries[key].Add(q.Relevant, ranked); e != nil {
				t.Fatal(e)
			}
		}
		report.Queries = append(report.Queries, result)
		save()
		if r.Retrieval.Mode != "hybrid" {
			t.Errorf("quality query %s did not use live embedding: %s", q.ID, r.Retrieval.Mode)
		}
		if report.RerankModel != "" && len(r.Items) > 1 && (r.Retrieval.Rerank.Status != "applied" || r.Retrieval.Rerank.ModelDigest != report.RerankDigest) {
			t.Errorf("quality query %s did not apply the requested real reranker: %+v", q.ID, r.Retrieval.Rerank)
		}
		t.Logf("query %s hits=%d metrics=%+v", q.ID, len(r.Items), result.Metrics)
	}
	report.Boundaries["all_queries_authorized"] = true
	shared := files["shared-migration"]
	visible := false
	for _, h := range search("共享数据库搬迁检查单").Items {
		if h.Document == "shared-migration" {
			visible = true
		}
	}
	if !visible {
		t.Fatal("explicitly shared current document was not retrievable")
	}
	report.Boundaries["explicit_shared_read"] = true
	if err = f.db.Where("file_id=? AND user_id=?", shared.ID, f.aliceID).Delete(&model.FilePermission{}).Error; err != nil {
		t.Fatal(err)
	}
	allowed["shared-migration"] = false
	search("共享数据库搬迁检查单")
	report.Boundaries["revocation"] = true
	photo := files["photo-last"]
	f.json(t, "DELETE", fmt.Sprintf("/api/files/%d", photo.ID), f.alice, nil, 200)
	allowed["photo-last"] = false
	search("去年拍摄的海边照片")
	report.Boundaries["recycle"] = true
	outage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer outage.Close()
	f.cfg.AIOllamaBaseURL = outage.URL
	f.router = fixtureRouter(app.NewContainer(f.cfg, f.db, f.rdb).NewRouter())
	fallback := search("SERVER_IP")
	found := false
	for _, h := range fallback.Items {
		if h.Document == "config-staging" {
			found = true
		}
	}
	if fallback.Retrieval.Mode != "keyword_fallback" || !found || len(fallback.Retrieval.Warnings) == 0 {
		t.Fatal("actual-text fallback lost", fallback)
	}
	report.Boundaries["model_outage_keyword_fallback"] = true
	if digest(report.EmbeddingModel) != report.EmbeddingDigest || digest(report.VisionModel) != report.VisionDigest {
		t.Fatal("models changed during evaluation")
	}
	if report.RerankModel != "" && digest(report.RerankModel) != report.RerankDigest {
		t.Fatal("reranker changed during evaluation")
	}
	if time.Now().In(loc).Format("2006-01") != anchor.Format("2006-01") {
		t.Fatal("calendar boundary crossed; rerun with a fresh anchor")
	}
	report.Completed = true
	splits := []string{"dev", "heldout"}
	if split == "dev" {
		splits = []string{"dev"}
	}
	for _, split := range splits {
		s := *report.Summaries[split]
		if !dataset.Gates.Pass(s) {
			t.Errorf("quality gate FAILED %s: %+v", split, s)
		} else {
			t.Logf("quality gate passed %s: %+v", split, s)
		}
	}
	report.Passed = !t.Failed()
	t.Logf("report: %s", reportPath)
}

func stringTrim(b []byte) string {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return string(b)
}

func TestAIEvaluationResponsePagination(t *testing.T) {
	var r evalResponse
	err := json.Unmarshal([]byte(`{"items":[{"id":1}],"pagination":{"page":1,"limit":100,"total":1},"retrieval":{"mode":"hybrid","window_limited":false}}`), &r)
	if err != nil || r.Pagination.Total != len(r.Items) || r.Pagination.Total != 1 {
		t.Fatal(r, err)
	}
}
