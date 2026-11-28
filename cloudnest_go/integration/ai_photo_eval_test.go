//go:build integration && aimodel && aieval

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"image/png"
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
	"cloudenest_go/internal/config"
	"cloudenest_go/internal/retrievaleval"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
)

type photoEvalDocument struct {
	ID             string                `json:"id"`
	Source         string                `json:"source"`
	OriginalSHA256 string                `json:"original_sha256"`
	UploadSHA256   string                `json:"upload_sha256"`
	Index          model.AIIndexDocument `json:"index"`
	Status         string                `json:"status"`
	ErrorCode      string                `json:"error_code,omitempty"`
	Capture        string                `json:"capture_local,omitempty"`
	LatencyMS      int64                 `json:"latency_ms"`
}

type photoEvalReport struct {
	evalReport
	Sources           []retrievaleval.PhotoSource `json:"sources"`
	Photos            []photoEvalDocument         `json:"photos"`
	RebuildGeneration int64                       `json:"rebuild_generation,omitempty"`
}

// Opt-in quality test: independent natural photographs, actual production
// worker/search, real local vision/embedding and fresh disposable databases.
func TestAINaturalPhotoRetrievalEvaluation(t *testing.T) {
	if os.Getenv("CLOUDNEST_TEST_AI_MODEL") != "1" || os.Getenv("CLOUDNEST_TEST_AI_EVAL") != "1" {
		t.Fatal("explicit real-model/evaluation opt-in required")
	}
	raw, err := os.ReadFile("testdata/retrieval/dataset-photos-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	dataset, err := retrievaleval.ParsePhotos(raw)
	if err != nil {
		t.Fatal(err)
	}
	root := os.Getenv("CLOUDNEST_AI_PHOTO_ROOT")
	info, err := os.Lstat(root)
	if !filepath.IsAbs(root) || err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		t.Fatal("CLOUDNEST_AI_PHOTO_ROOT must be an absolute private directory")
	}
	// Validate all sources before invoking any model. JPEG decode/re-encode strips
	// EXIF, comments, original names and source captions, retaining only pixels.
	pixels := map[string][]byte{}
	sources := map[string]retrievaleval.PhotoSource{}
	for _, source := range dataset.Sources {
		path := filepath.Join(root, source.ID+".jpg")
		info, e := os.Lstat(path)
		if e != nil || !info.Mode().IsRegular() || info.Size() != source.Bytes {
			t.Fatal("source missing or wrong size", source.ID, e)
		}
		body, e := os.ReadFile(path)
		if e != nil || fmt.Sprintf("%x", sha256.Sum256(body)) != source.SHA256 {
			t.Fatal("source hash changed", source.ID, e)
		}
		header, e := jpeg.DecodeConfig(bytes.NewReader(body))
		if e != nil || header.Width != source.Width || header.Height != source.Height {
			t.Fatal("source dimensions changed", source.ID, e)
		}
		image, e := jpeg.Decode(bytes.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		var encoded bytes.Buffer
		if e := png.Encode(&encoded, image); e != nil {
			t.Fatal(e)
		}
		pixels[source.ID], sources[source.ID] = encoded.Bytes(), source
	}
	reportPath := os.Getenv("CLOUDNEST_AI_EVAL_REPORT")
	if !filepath.IsAbs(reportPath) {
		t.Fatal("new absolute report path required")
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
	report := photoEvalReport{evalReport: evalReport{DatasetName: "photos-v1", EvaluationSplit: "all", Version: 1, Started: anchor,
		ManifestSHA256: fmt.Sprintf("%x", sha256.Sum256(raw)), GoVersion: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH,
		Gates: dataset.Gates, Summaries: map[string]*retrievaleval.Summary{}, Boundaries: map[string]bool{}, Limitations: []string{
			"Six selected independent natural photographs, not production generalization or a blind external test.",
			"Seven uploads include one private near-duplicate; it is not counted as an independent photograph.",
			"Original metadata stripped via JPEG decode/PNG encode; synthetic capture dates do not describe the original photographs.",
			"All labels fixed by visual inspection before inference. Source observations and attributions are never indexed.",
			"A single serial worker and local warm disk on this host; not a concurrency, cold-disk or model-memory SLA.",
		}}, Sources: dataset.Sources}
	save := func() {
		body, e := json.MarshalIndent(report, "", "  ")
		if e != nil {
			t.Error(e)
			return
		}
		if e = out.Truncate(0); e == nil {
			_, e = out.WriteAt(append(body, '\n'), 0)
		}
		if e == nil {
			e = out.Sync()
		}
		if e != nil {
			t.Error(e)
		}
	}
	defer func() { report.Finished = time.Now(); report.Passed = report.Completed && !t.Failed(); save() }()
	git := func(args ...string) []byte {
		b, e := exec.Command("git", args...).Output()
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	report.Commit = stringTrim(git("rev-parse", "HEAD"))
	report.WorkingDiffSHA256 = fmt.Sprintf("%x", sha256.Sum256(git("diff", "HEAD", "--")))
	harness, err := os.ReadFile("ai_photo_eval_test.go")
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
		t.Fatal("Ollama preflight", err, res.StatusCode)
	}
	report.OllamaVersion, report.EmbeddingModel = version.Version, "qwen3-embedding:0.6b"
	report.VisionModel = strings.TrimSpace(os.Getenv("CLOUDNEST_TEST_IMAGE_VISION_MODEL"))
	if report.VisionModel == "" {
		report.VisionModel = config.DefaultVisionModel
	}
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
	report.EmbeddingDigest, report.VisionDigest = digest(report.EmbeddingModel), digest(report.VisionModel)
	save()
	f, _ := indexFixture(t)
	f.cfg.AIOllamaBaseURL, f.cfg.AIEmbeddingModel, f.cfg.AIOCRModel = base, report.EmbeddingModel, report.VisionModel
	f.router = fixtureRouter(app.NewContainer(f.cfg, f.db, f.rdb).NewRouter())
	report.MinCosine, report.MinLexical = f.cfg.SearchThresholds()
	report.QueryPolicy = "production defaults; qwen3-task-instruction-calendar-separated-v1"
	ids, files, allowed := map[int64]string{}, map[string]model.File{}, map[string]bool{}
	for i, d := range dataset.Documents {
		started := time.Now()
		body := append([]byte(nil), pixels[d.Source]...)
		capture := ""
		if d.Capture != "" {
			year := anchor.Year()
			if d.Capture == "previous_year" {
				year--
			}
			date := time.Date(year, 6, 15, 10, 20, 30, 0, time.UTC)
			body, capture = syntheticCapturePNG(t, body, date), date.Format("2006-01-02 15:04:05")
		}
		token := f.alice
		if d.Owner == "bob" {
			token = f.bob
		}
		file := f.upload(t, token, fmt.Sprintf("entry-%02d.png", i+1), body)
		if d.ShareRead {
			if err := f.db.Create(&model.FilePermission{FileID: file.ID, UserID: f.aliceID, Permission: "read"}).Error; err != nil {
				t.Fatal(err)
			}
		}
		worked, workErr := textWorkerWithVision(t, f, base, report.VisionModel).Step(context.Background())
		job := readIndex(t, f, file.ID)
		var doc model.AIIndexDocument
		docErr := f.db.First(&doc, "file_id=?", file.ID).Error
		result := photoEvalDocument{ID: d.ID, Source: d.Source, OriginalSHA256: sources[d.Source].SHA256, UploadSHA256: fmt.Sprintf("%x", sha256.Sum256(body)), Index: doc, Status: job.Status, ErrorCode: job.ErrorCode, Capture: capture, LatencyMS: time.Since(started).Milliseconds()}
		report.Photos = append(report.Photos, result)
		save()
		if !worked || workErr != nil || docErr != nil || job.Status != "done" {
			t.Fatalf("actual photo index %s: worked=%v error=%v doc=%v status=%s/%s", d.ID, worked, workErr, docErr, job.Status, job.ErrorCode)
		}
		if doc.ExtractionKind != "image" || doc.Generation != job.Generation || doc.SourceHash != result.UploadSHA256 || doc.VisionModel != report.VisionModel || doc.VisionDigest != report.VisionDigest || doc.ModelDigest != report.EmbeddingDigest || doc.Dimensions != 1024 || doc.VisualDescription == "" || doc.VisualTags == "" {
			t.Fatal("photo index provenance mismatch", d.ID)
		}
		if capture == "" && doc.CapturedAtLocal != nil {
			t.Fatal("stripped capture date invented", d.ID)
		}
		if capture != "" && (doc.CapturedAtLocal == nil || doc.CapturedAtLocal.Format("2006-01-02 15:04:05") != capture || doc.CaptureOffset != "+08:00") {
			t.Fatal("synthetic capture lost", d.ID)
		}
		var chunks []model.AIIndexChunk
		if err := f.db.Where("file_id=?", file.ID).Find(&chunks).Error; err != nil || len(chunks) == 0 || len(chunks) != doc.ChunkCount {
			t.Fatal("missing photo vectors", d.ID, err)
		}
		for _, chunk := range chunks {
			decodeVector(t, chunk.Vector)
		}
		ids[file.ID], files[d.ID], allowed[d.ID] = d.ID, file, d.Owner == "alice" || d.ShareRead
		waitNoReadLeases(t, f, *file.StorageID)
		t.Logf("natural photo %d/%d %s indexed in %dms", i+1, len(dataset.Documents), d.ID, result.LatencyMS)
	}
	search := func(token, question string, authorized map[string]bool) evalResponse {
		var r evalResponse
		decode(t, f.json(t, "POST", "/api/ai/search/nl", token, map[string]any{"question": question, "limit": 100}, 200), &r)
		for i := range r.Items {
			id, ok := ids[r.Items[i].ID]
			if !ok || !authorized[id] {
				t.Fatal("unauthorized/unknown photo result", r.Items[i].ID)
			}
			r.Items[i].Document = id
		}
		if r.Retrieval.WindowLimited || r.Pagination.Total != len(r.Items) {
			t.Fatal("photo evaluation truncated")
		}
		return r
	}
	has := func(r evalResponse, id string) bool {
		for _, h := range r.Items {
			if h.Document == id {
				return true
			}
		}
		return false
	}
	for _, q := range dataset.Queries {
		started := time.Now()
		r := search(f.alice, q.Question, allowed)
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
			t.Errorf("query %s missed live embedding: %s", q.ID, r.Retrieval.Mode)
		}
		t.Logf("photo query %s hits=%v metrics=%+v", q.ID, ranked, result.Metrics)
	}
	report.Boundaries["all_quality_queries_authorized"] = true
	bobAllowed := map[string]bool{"photo-private-sea": true, "photo-tree-shared": true}
	if len(search(f.bob, "去年拍的海边照片", bobAllowed).Items) != 0 {
		t.Fatal("foreign capture metadata leaked")
	}
	report.Boundaries["cross_user_calendar"] = true
	shared := files["photo-tree-shared"]
	if !has(search(f.alice, "巨大的树干和木栈道的照片", allowed), "photo-tree-shared") {
		t.Fatal("shared photo was not retrievable")
	}
	report.Boundaries["shared_read"] = true
	if err := f.db.Where("file_id=? AND user_id=?", shared.ID, f.aliceID).Delete(&model.FilePermission{}).Error; err != nil {
		t.Fatal(err)
	}
	allowed["photo-tree-shared"] = false
	search(f.alice, "巨大的树干和木栈道的照片", allowed)
	report.Boundaries["revocation"] = true
	photo := files["photo-sea-last"]
	f.json(t, "DELETE", fmt.Sprintf("/api/files/%d", photo.ID), f.alice, nil, 200)
	allowed["photo-sea-last"] = false
	if len(search(f.alice, "去年拍的海边照片", allowed).Items) != 0 {
		t.Fatal("recycled/unknown-calendar photograph returned")
	}
	report.Boundaries["recycle_and_unknown_capture"] = true
	city := files["photo-skyline"]
	generation := readIndex(t, f, city.ID).Generation
	f.json(t, "POST", "/api/ai/index/rebuild", f.alice, map[string]any{"items": []contract.AIRebuildItem{{FileID: city.ID, Generation: generation}}}, 202)
	allowed["photo-skyline"] = false
	search(f.alice, "城市高楼天际线照片", allowed)
	if worked, e := textWorkerWithVision(t, f, base, report.VisionModel).Step(context.Background()); !worked || e != nil {
		t.Fatal("real photo rebuild", worked, e)
	}
	job := readIndex(t, f, city.ID)
	if job.Status != "done" || job.Generation != generation+1 {
		t.Fatal("rebuild generation/status", job)
	}
	report.RebuildGeneration = job.Generation
	allowed["photo-skyline"] = true
	if !has(search(f.alice, "城市高楼天际线照片", allowed), "photo-skyline") {
		t.Fatal("rebuilt photo not searchable")
	}
	waitNoReadLeases(t, f, *city.StorageID)
	report.Boundaries["real_image_rebuild_fences_old_generation"] = true
	outage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer outage.Close()
	f.cfg.AIOllamaBaseURL = outage.URL
	f.router = fixtureRouter(app.NewContainer(f.cfg, f.db, f.rdb).NewRouter())
	fallback := search(f.alice, "海边照片", allowed)
	if fallback.Retrieval.Mode != "keyword_fallback" || len(fallback.Retrieval.Warnings) == 0 {
		t.Fatal("model outage not disclosed", fallback)
	}
	report.Boundaries["model_outage_disclosed_and_authorized"] = true
	if digest(report.EmbeddingModel) != report.EmbeddingDigest || digest(report.VisionModel) != report.VisionDigest {
		t.Fatal("models changed during evaluation")
	}
	if time.Now().In(loc).Year() != anchor.Year() {
		t.Fatal("year boundary crossed")
	}
	report.Completed = true
	for _, split := range []string{"dev", "heldout"} {
		if !dataset.Gates.Pass(*report.Summaries[split]) {
			t.Errorf("natural photo quality gate FAILED %s: %+v", split, *report.Summaries[split])
		} else {
			t.Logf("natural photo quality gate passed %s: %+v", split, *report.Summaries[split])
		}
	}
	t.Logf("natural photo report: %s", reportPath)
}
