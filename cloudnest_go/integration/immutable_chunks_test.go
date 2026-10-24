//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cloudenest_go/internal/app"
	"cloudenest_go/internal/middleware"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/internal/store"
	"cloudenest_go/model"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

func immutableInit(t *testing.T, f *fixture, total, chunk int) string {
	t.Helper()
	var r struct {
		UploadID string `json:"upload_id"`
	}
	decode(t, f.json(t, "POST", "/api/files/upload/init", f.alice, map[string]any{"filename": "immutable.bin", "total_size": total, "chunk_size": chunk, "total_chunks": 1 + (total-1)/chunk}, 200), &r)
	return r.UploadID
}
func chunkBody(t *testing.T, id string, index int, payload []byte, digest string) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if digest == "" {
		digest = fmt.Sprintf("%x", md5.Sum(payload))
	}
	for _, field := range [][2]string{{"upload_id", id}, {"chunk_index", strconv.Itoa(index)}, {"chunk_md5", digest}} {
		if err := mw.WriteField(field[0], field[1]); err != nil {
			t.Fatal(err)
		}
	}
	file, err := mw.CreateFormFile("chunk", "ignored-path.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &body, mw.FormDataContentType()
}
func sendChunk(t *testing.T, f *fixture, id string, index int, payload []byte, digest string, want int) *httptest.ResponseRecorder {
	t.Helper()
	body, ct := chunkBody(t, id, index, payload, digest)
	return f.request(t, "POST", "/api/files/upload/chunk", f.alice, ct, body, want)
}
func chunkRow(t *testing.T, f *fixture, id string) model.UploadChunk {
	t.Helper()
	var row model.UploadChunk
	if err := f.db.First(&row, "upload_id=?", id).Error; err != nil {
		t.Fatal(err)
	}
	return row
}
func chunkFiles(t *testing.T, f *fixture) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(f.cfg.DataDir, "storage", ".chunks-v1"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}
func TestImmutableChunkRetriesCannotOverwriteOrDeleteAcceptedBytes(t *testing.T) {
	f := newFixture(t)
	id := immutableInit(t, f, 4, 4)
	payload := []byte("AAAA")
	sendChunk(t, f, id, 0, payload, "", 200)
	original := chunkRow(t, f, id)
	sendChunk(t, f, id, 0, payload, "", 200)
	sendChunk(t, f, id, 0, []byte("BBBB"), "", 409)
	sendChunk(t, f, id, 0, []byte("CCCC"), strings.Repeat("0", 32), 400)
	row := chunkRow(t, f, id)
	data, err := os.ReadFile(row.ChunkPath)
	if err != nil || row.ChunkPath != original.ChunkPath || row.SHA256 != fmt.Sprintf("%x", sha256.Sum256(payload)) || !bytes.Equal(data, payload) || chunkFiles(t, f) != 1 {
		t.Fatal("retry changed or leaked accepted chunk", err)
	}
	file := completedBlob(t, f, completeUploadRecorder(f, f.alice, id))
	if w := f.json(t, "GET", fmt.Sprintf("/api/files/%d/download", file.ID), f.alice, nil, 200); !bytes.Equal(w.Body.Bytes(), payload) {
		t.Fatal("completion used changed bytes")
	}
	if chunkFiles(t, f) != 0 {
		t.Fatal("confirmed completion did not clean only its manifest")
	}
}

// stagedChunkBody pauses at the payload's closing MIME boundary, after the
// producer has opened/locked its registered file but before any publication.
type stagedChunkBody struct {
	first, tail      *bytes.Reader
	blocked, release chan struct{}
	signal, unblock  sync.Once
}

func (s *stagedChunkBody) Read(p []byte) (int, error) {
	if s.first.Len() > 0 {
		return s.first.Read(p)
	}
	s.signal.Do(func() { close(s.blocked) })
	<-s.release
	return s.tail.Read(p)
}
func (s *stagedChunkBody) Close() error { s.unblock.Do(func() { close(s.release) }); return nil }
func stagedBody(t *testing.T, id string, payload string) (*stagedChunkBody, string) {
	body, ct := chunkBody(t, id, 0, []byte(payload), "")
	raw := append([]byte(nil), body.Bytes()...)
	split := bytes.LastIndex(raw, []byte("\r\n--"))
	if split < 0 {
		t.Fatal("missing boundary")
	}
	return &stagedChunkBody{first: bytes.NewReader(raw[:split]), tail: bytes.NewReader(raw[split:]), blocked: make(chan struct{}), release: make(chan struct{})}, ct
}
func TestImmutableChunkDifferentConcurrentBodiesPublishOneIdentity(t *testing.T) {
	f := newFixture(t)
	id := immutableInit(t, f, 4, 4)
	body, ct := stagedBody(t, id, "AAAA")
	defer body.Close()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		request := httptest.NewRequest("POST", "/api/files/upload/chunk", body)
		request.Header.Set("Authorization", "Bearer "+f.alice)
		request.Header.Set("Content-Type", ct)
		w := httptest.NewRecorder()
		f.router.ServeHTTP(w, request)
		done <- w
	}()
	select {
	case <-body.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("first body did not reach streaming window")
	}
	sendChunk(t, f, id, 0, []byte("BBBB"), "", 200)
	body.Close()
	if w := <-done; w.Code != 409 {
		t.Fatal("different concurrent body overwrote winner", w.Code, w.Body.String())
	}
	if chunkFiles(t, f) != 1 {
		t.Fatal("losing candidate leaked")
	}
	row := chunkRow(t, f, id)
	data, _ := os.ReadFile(row.ChunkPath)
	if string(data) != "BBBB" {
		t.Fatal("loser changed winner bytes")
	}
}
func TestImmutableChunkIdenticalConcurrentRetriesAcrossRouters(t *testing.T) {
	f := newFixture(t)
	id := immutableInit(t, f, 4, 4)
	other := fixtureRouter(app.NewContainer(f.cfg, f.db, f.rdb).NewRouter())
	results := make(chan *httptest.ResponseRecorder, 2)
	start := make(chan struct{})
	for _, router := range []http.Handler{f.router, other} {
		go func(router http.Handler) {
			<-start
			body, ct := chunkBody(t, id, 0, []byte("same"), "")
			req := httptest.NewRequest("POST", "/api/files/upload/chunk", body)
			req.RemoteAddr = f.remoteAddr
			req.Header.Set("Authorization", "Bearer "+f.alice)
			req.Header.Set("Content-Type", ct)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			results <- w
		}(router)
	}
	close(start)
	for range 2 {
		if w := <-results; w.Code != 200 {
			t.Fatalf("identical retry %d %s", w.Code, w.Body.String())
		}
	}
	if chunkFiles(t, f) != 1 {
		t.Fatal("identical retries stored multiple candidates")
	}
	if task := receiptTask(t, f, id); task.UploadedChunks != 1 {
		t.Fatal("retry advanced progress twice")
	}
}
func TestImmutableChunkExactLengthsAndRemainder(t *testing.T) {
	f := newFixture(t)
	id := immutableInit(t, f, 7, 3)
	for _, c := range []struct {
		i int
		p string
	}{{0, "ab"}, {0, "abcd"}, {2, "xy"}, {3, "x"}, {-1, "x"}} {
		sendChunk(t, f, id, c.i, []byte(c.p), "", 400)
	}
	for i, p := range []string{"aab", "ccd", "x"} {
		sendChunk(t, f, id, i, []byte(p), "", 200)
	}
	file := completedBlob(t, f, completeUploadRecorder(f, f.alice, id))
	w := f.json(t, "GET", fmt.Sprintf("/api/files/%d/download", file.ID), f.alice, nil, 200)
	if w.Body.String() != "aabccdx" {
		t.Fatal("manifest order/remainder changed bytes")
	}
}
func TestImmutableChunkRejectsAmbiguousMultipart(t *testing.T) {
	f := newFixture(t)
	id := immutableInit(t, f, 4, 4)
	for _, variant := range []string{"duplicate", "unknown", "file-first", "two-files", "large-field", "bad-md5", "truncated"} {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		if variant == "file-first" {
			p, _ := mw.CreateFormFile("chunk", "x")
			_, _ = p.Write([]byte("AAAA"))
		}
		value := id
		if variant == "large-field" {
			value = strings.Repeat("x", 1025)
		}
		_ = mw.WriteField("upload_id", value)
		_ = mw.WriteField("chunk_index", "0")
		digest := fmt.Sprintf("%x", md5.Sum([]byte("AAAA")))
		if variant == "bad-md5" {
			digest = "wrong"
		}
		_ = mw.WriteField("chunk_md5", digest)
		if variant == "duplicate" {
			_ = mw.WriteField("upload_id", id)
		}
		if variant == "unknown" {
			_ = mw.WriteField("surprise", "x")
		}
		p, _ := mw.CreateFormFile("chunk", "x")
		_, _ = p.Write([]byte("AAAA"))
		if variant == "two-files" {
			p, _ = mw.CreateFormFile("chunk", "y")
			_, _ = p.Write([]byte("AAAA"))
		}
		_ = mw.Close()
		raw := body.Bytes()
		if variant == "truncated" {
			raw = raw[:len(raw)-8]
		}
		f.request(t, "POST", "/api/files/upload/chunk", f.alice, mw.FormDataContentType(), bytes.NewReader(raw), 400)
		if chunkFiles(t, f) != 0 {
			t.Fatal("malformed body retained candidate", variant)
		}
	}
	if task := receiptTask(t, f, id); task.UploadedChunks != 0 {
		t.Fatal("malformed body committed chunk")
	}
}
func TestImmutableChunkManifestDetectsDiskAndPathTampering(t *testing.T) {
	f := newFixture(t)
	payload := []byte("manifest")
	id := fixturePendingUpload(t, f, f.alice, "manifest.bin", payload)
	row := chunkRow(t, f, id)
	originalPath := row.ChunkPath
	if err := os.WriteFile(row.ChunkPath, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if w := completeUploadRecorder(f, f.alice, id); w.Code != 409 {
		t.Fatal("same-size mutation accepted", w.Code)
	}
	if err := os.WriteFile(row.ChunkPath, payload, 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, payload, 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&row).Update("chunk_path", outside).Error; err != nil {
		t.Fatal(err)
	}
	if w := completeUploadRecorder(f, f.alice, id); w.Code != 409 {
		t.Fatal("outside manifest path accepted", w.Code)
	}
	if err := f.db.Model(&row).Update("chunk_path", originalPath).Error; err != nil {
		t.Fatal(err)
	}
	heldPath := originalPath + ".held"
	if err := os.Rename(originalPath, heldPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, originalPath); err != nil {
		t.Fatal(err)
	}
	if w := completeUploadRecorder(f, f.alice, id); w.Code != 409 {
		t.Fatal("symlink manifest path accepted", w.Code)
	}
	if err := os.Remove(originalPath); err != nil {
		t.Fatal(err)
	}
	// Restore the registered inode, rather than silently adopting a replacement.
	if err := os.Rename(heldPath, originalPath); err != nil {
		t.Fatal(err)
	}
	completedBlob(t, f, completeUploadRecorder(f, f.alice, id))
	data, err := os.ReadFile(outside)
	if err != nil || !bytes.Equal(data, payload) {
		t.Fatal("outside data changed")
	}
}
func TestImmutableChunkMetadataFailuresLeaveRetryableTask(t *testing.T) {
	f := newFixture(t)
	id := immutableInit(t, f, 4, 4)
	const hook = "test:chunk_metadata_rollback"
	if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if row, ok := tx.Statement.Dest.(*model.UploadChunk); ok && row.UploadID == id {
			tx.AddError(errors.New("injected chunk insert failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	sendChunk(t, f, id, 0, []byte("AAAA"), "", 500)
	f.db.Callback().Create().Remove(hook)
	if chunkFiles(t, f) != 0 || receiptTask(t, f, id).UploadedChunks != 0 {
		t.Fatal("known rollback retained/published candidate")
	}
	sendChunk(t, f, id, 0, []byte("AAAA"), "", 200)
}
func TestImmutableChunkUpgradeNeverAdoptsLegacyPhysicalData(t *testing.T) {
	f := newFixture(t)
	done := fixturePendingUpload(t, f, f.alice, "completed-before-upgrade.bin", []byte("done"))
	original := completeUploadRecorder(f, f.alice, done)
	file := completedBlob(t, f, original)
	waitNoReadLeases(t, f, *file.StorageID)
	id := fixturePendingUpload(t, f, f.alice, "legacy-chunk.bin", []byte("kept"))
	row := chunkRow(t, f, id)
	if err := f.db.Exec("ALTER TABLE upload_task DROP COLUMN chunk_protocol").Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec("ALTER TABLE upload_chunk DROP COLUMN sha256, DROP COLUMN md5").Error; err != nil {
		t.Fatal(err)
	}
	if err := store.CheckFileSchema(f.db); err == nil {
		t.Fatal("old schema started")
	}
	if err := store.Migrate(f.db); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckFileSchema(f.db); err != nil {
		t.Fatal(err)
	}
	if task := receiptTask(t, f, id); task.ChunkProtocol != 0 {
		t.Fatal("migration invented chunk provenance")
	}
	sendChunk(t, f, id, 0, []byte("kept"), "", 409)
	f.json(t, "POST", "/api/files/upload/cancel", f.alice, map[string]any{"upload_id": id}, 200)
	data, err := os.ReadFile(row.ChunkPath)
	if err != nil || string(data) != "kept" {
		t.Fatal("upgrade/cancel removed legacy bytes", err)
	}
	// New admission ceilings must not invalidate a durable old confirmation.
	if err := f.db.Model(&model.UploadTask{}).Where("upload_id=?", done).Update("total_chunks", contract.MaxUploadChunks+1).Error; err != nil {
		t.Fatal(err)
	}
	assertReceiptReply(t, f, done, original)
}

type countedUploadReader struct {
	io.Reader
	count int
}

func (r *countedUploadReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.count += n
	return n, err
}
func TestUploadShapeAndJSONBodyBudgets(t *testing.T) {
	f := newFixture(t)
	for _, shape := range []map[string]any{
		{"filename": "too-big", "total_size": contract.MaxUploadFileBytes + 1, "chunk_size": 64 << 20},
		{"filename": "too-many", "total_size": contract.MaxUploadChunks + 1, "chunk_size": 1},
		{"filename": "too-wide", "total_size": contract.MaxUploadChunkBytes + 1, "chunk_size": contract.MaxUploadChunkBytes + 1},
	} {
		f.json(t, "POST", "/api/files/upload/init", f.alice, shape, 400)
	}
	for _, path := range []string{"init", "complete", "cancel"} {
		reader := &countedUploadReader{Reader: strings.NewReader(`{"upload_id":"` + strings.Repeat("x", 20<<10) + `"}`)}
		f.request(t, "POST", "/api/files/upload/"+path, f.alice, "application/json", reader, 400)
		if reader.count > (16<<10)+1 {
			t.Fatal("JSON body was fully consumed before rejection", path, reader.count)
		}
	}
	req := httptest.NewRequest("POST", "/api/files/upload/chunk", strings.NewReader("unread"))
	req.RemoteAddr = f.remoteAddr
	req.ContentLength = middleware.MaxUploadBodyBytes + 1
	req.Header.Set("Authorization", "Bearer "+f.alice)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	if w.Code != 413 {
		t.Fatal("declared body exceeded cap", w.Code)
	}

	smallID := immutableInit(t, f, 4, 4)
	var framed bytes.Buffer
	frame := multipart.NewWriter(&framed)
	_ = frame.WriteField("upload_id", smallID)
	_ = frame.WriteField("chunk_index", "0")
	_ = frame.WriteField("chunk_md5", fmt.Sprintf("%x", md5.Sum([]byte("AAAA"))))
	_, _ = fmt.Fprintf(&framed, "\r\n--%s\r\nContent-Disposition: form-data; name=\"chunk\"; filename=\"x\"\r\nX-Large: %s\r\n\r\nAAAA\r\n--%s--\r\n", frame.Boundary(), strings.Repeat("x", 100<<10), frame.Boundary())
	metered := &countedUploadReader{Reader: bytes.NewReader(framed.Bytes())}
	unknown := httptest.NewRequest("POST", "/api/files/upload/chunk", metered)
	unknown.ContentLength = -1
	unknown.Header.Set("Authorization", "Bearer "+f.alice)
	unknown.Header.Set("Content-Type", frame.FormDataContentType())
	oversized := httptest.NewRecorder()
	f.router.ServeHTTP(oversized, unknown)
	if oversized.Code != 413 || metered.count > 4+(64<<10)+1 {
		t.Fatal("unknown-length multipart exceeded framing budget", oversized.Code, metered.count)
	}
	server := httptest.NewServer(f.router)
	network, err := http.NewRequest("POST", server.URL+"/api/files/upload/chunk", io.NopCloser(bytes.NewReader(framed.Bytes())))
	if err != nil {
		t.Fatal(err)
	}
	network.Header.Set("Authorization", "Bearer "+f.alice)
	network.Header.Set("Content-Type", frame.FormDataContentType())
	response, err := server.Client().Do(network)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	server.Close()
	if response.StatusCode != 413 {
		t.Fatal("actual chunked TCP body bypassed framing budget", response.StatusCode)
	}
	id := immutableInit(t, f, contract.MaxUploadChunks, 1)
	f.json(t, "GET", "/api/files/upload/progress?upload_id="+id, f.alice, nil, 200)
	if err := f.db.Model(&model.UploadTask{}).Where("upload_id=?", id).Update("total_chunks", 1<<30).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "GET", "/api/files/upload/progress?upload_id="+id, f.alice, nil, 409)
}
func waitUploadSlots(t *testing.T, f *fixture, uid int64, want int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		n, err := f.rdb.ZCard(context.Background(), middleware.UploadBodyUserKey(uid)).Result()
		if err != nil {
			t.Fatal(err)
		}
		if n == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("upload slots did not settle", want)
}
func TestUploadBodyAdmissionSharedAndExpiredSlotsRecover(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	now, err := f.rdb.Time(ctx).Result()
	if err != nil {
		t.Fatal(err)
	}
	members := []redis.Z{{Score: float64(now.Add(time.Minute).UnixMilli()), Member: "test-body-one"}, {Score: float64(now.Add(time.Minute).UnixMilli()), Member: "test-body-two"}}
	userKey := middleware.UploadBodyUserKey(f.aliceID)
	if err := f.rdb.ZAdd(ctx, userKey, members...).Err(); err != nil {
		t.Fatal(err)
	}
	defer f.rdb.Del(ctx, userKey)
	id := immutableInit(t, f, 4, 4)
	sendChunk(t, f, id, 0, []byte("AAAA"), "", 429)
	other := fixtureRouter(app.NewContainer(f.cfg, f.db, f.rdb).NewRouter())
	body, ct := chunkBody(t, id, 0, []byte("AAAA"), "")
	req := httptest.NewRequest("POST", "/api/files/upload/chunk", body)
	req.Header.Set("Authorization", "Bearer "+f.alice)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	other.ServeHTTP(w, req)
	if w.Code != 429 {
		t.Fatal("other instance bypassed admission", w.Code)
	}
	for i := range members {
		members[i].Score = float64(now.Add(-time.Second).UnixMilli())
	}
	if err := f.rdb.ZAdd(ctx, userKey, members...).Err(); err != nil {
		t.Fatal(err)
	}
	sendChunk(t, f, id, 0, []byte("AAAA"), "", 200)
	waitUploadSlots(t, f, f.aliceID, 0)
	var globals []redis.Z
	var tokens []any
	for i := range 16 {
		token := fmt.Sprintf("test-global-%d-%d", f.aliceID, i)
		tokens = append(tokens, token)
		globals = append(globals, redis.Z{Score: float64(now.Add(time.Minute).UnixMilli()), Member: token})
	}
	if err := f.rdb.ZAdd(ctx, middleware.UploadBodyGlobalKey, globals...).Err(); err != nil {
		t.Fatal(err)
	}
	defer f.rdb.ZRem(ctx, middleware.UploadBodyGlobalKey, tokens...)
	sendChunk(t, f, id, 0, []byte("AAAA"), "", 429)
}
func deadlineChunkRouter(f *fixture, timeout time.Duration) http.Handler {
	container := app.NewContainer(f.cfg, f.db, f.rdb)
	router := gin.New()
	router.POST("/chunk", func(c *gin.Context) { c.Set(middleware.ContextUserIDKey, f.aliceID); c.Next() }, middleware.UploadBodyBudget(f.rdb, timeout), container.FileHandler.UploadChunk)
	return router
}
func TestUploadBodyActualTCPDeadlineReleasesAdmission(t *testing.T) {
	f := newFixture(t)
	id := immutableInit(t, f, 4, 4)
	server := httptest.NewServer(deadlineChunkRouter(f, 500*time.Millisecond))
	defer server.Close()
	pipe, writer := io.Pipe()
	defer writer.Close()
	defer pipe.Close()
	var prefix bytes.Buffer
	mw := multipart.NewWriter(&prefix)
	_ = mw.WriteField("upload_id", id)
	_ = mw.WriteField("chunk_index", "0")
	_ = mw.WriteField("chunk_md5", fmt.Sprintf("%x", md5.Sum([]byte("AAAA"))))
	_, _ = mw.CreateFormFile("chunk", "x")
	req, err := http.NewRequest("POST", server.URL+"/chunk", pipe)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	result := make(chan *http.Response, 1)
	requestError := make(chan error, 1)
	go func() {
		r, e := server.Client().Do(req)
		if e != nil {
			requestError <- e
		} else {
			result <- r
		}
	}()
	if _, err := writer.Write(prefix.Bytes()); err != nil {
		t.Fatal(err)
	}
	waitUploadSlots(t, f, f.aliceID, 1)
	select {
	case r := <-result:
		defer r.Body.Close()
		_, _ = io.Copy(io.Discard, r.Body)
		if r.StatusCode != 408 {
			t.Fatal("slow body not rejected", r.StatusCode)
		}
	case e := <-requestError:
		t.Fatal(e)
	case <-time.After(3 * time.Second):
		t.Fatal("read deadline did not stop actual TCP body")
	}
	waitUploadSlots(t, f, f.aliceID, 0)
	if chunkFiles(t, f) != 0 {
		t.Fatal("timed-out body retained candidate")
	}
}
func TestUploadBodyCancellationInterruptsRateWait(t *testing.T) {
	f := newFixture(t)
	id := immutableInit(t, f, 64<<10, 64<<10)
	payload := bytes.Repeat([]byte("x"), 64<<10)
	body, ct := chunkBody(t, id, 0, payload, "")
	router := fixtureRouter(deadlineChunkRouter(f, 100*time.Millisecond))
	req := httptest.NewRequest("POST", "/chunk?upload_rate_kbps=64", body)
	req.Header.Set("Content-Type", ct)
	start := time.Now()
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 408 || time.Since(start) > 400*time.Millisecond {
		t.Fatal("rate wait ignored cancellation", w.Code, time.Since(start))
	}
	waitUploadSlots(t, f, f.aliceID, 0)
	if chunkFiles(t, f) != 0 {
		t.Fatal("canceled rate wait leaked candidate")
	}
}
func TestUploadBodyRedisScriptDenialFailsClosed(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	user := "upload-gate-" + strconv.FormatInt(f.aliceID, 10)
	password := "disposable-upload-gate-test"
	if err := f.rdb.Do(ctx, "ACL", "SETUSER", user, "on", ">"+password, "~*", "+get").Err(); err != nil {
		t.Fatal(err)
	}
	defer f.rdb.Do(ctx, "ACL", "DELUSER", user)
	restricted := redis.NewClient(&redis.Options{Addr: f.cfg.RedisAddr, Username: user, Password: password})
	defer restricted.Close()
	f.router = fixtureRouter(app.NewContainer(f.cfg, f.db, restricted).NewRouter())
	f.json(t, "GET", "/api/files", f.alice, nil, 200)
	id := immutableInit(t, f, 4, 4)
	sendChunk(t, f, id, 0, []byte("AAAA"), "", 503)
	if chunkFiles(t, f) != 0 {
		t.Fatal("failed admission wrote bytes")
	}
}
func TestImmutableChunkLateWriterCannotUndoCancelOrCompletion(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(strconv.FormatBool(complete), func(t *testing.T) {
			f := newFixture(t)
			id := fixturePendingUpload(t, f, f.alice, "late-writer.bin", []byte("AAAA"))
			body, ct := stagedBody(t, id, "BBBB")
			defer body.Close()
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				req := httptest.NewRequest("POST", "/api/files/upload/chunk", body)
				req.Header.Set("Content-Type", ct)
				req.Header.Set("Authorization", "Bearer "+f.alice)
				w := httptest.NewRecorder()
				f.router.ServeHTTP(w, req)
				done <- w
			}()
			select {
			case <-body.blocked:
			case <-time.After(5 * time.Second):
				t.Fatal("late writer not in streaming window")
			}
			var winner *httptest.ResponseRecorder
			if complete {
				winner = completeUploadRecorder(f, f.alice, id)
			} else {
				f.json(t, "POST", "/api/files/upload/cancel", f.alice, map[string]any{"upload_id": id}, 200)
			}
			body.Close()
			if w := <-done; w.Code != 409 {
				t.Fatal("late writer published", w.Code, w.Body.String())
			}
			if complete {
				file := completedBlob(t, f, winner)
				if w := f.json(t, "GET", fmt.Sprintf("/api/files/%d/download", file.ID), f.alice, nil, 200); w.Body.String() != "AAAA" {
					t.Fatal("late writer changed published bytes")
				}
				if chunkFiles(t, f) != 0 {
					t.Fatal("late candidate leaked")
				}
			} else {
				if chunkFiles(t, f) != 1 {
					t.Fatal("cancel removed old chunk before reconciliation or leaked a candidate")
				}
			}
		})
	}
}

func TestUploadBodyRejectsIncompleteMalformedTCPWithoutBackgroundDrain(t *testing.T) {
	f := newFixture(t)
	server := httptest.NewServer(deadlineChunkRouter(f, 2*time.Second))
	defer server.Close()
	pipe, writer := io.Pipe()
	defer writer.Close()
	defer pipe.Close()
	var prefix bytes.Buffer
	mw := multipart.NewWriter(&prefix)
	_, _ = mw.CreateFormField("surprise")
	req, err := http.NewRequest("POST", server.URL+"/chunk", pipe)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	results := make(chan int, 1)
	failures := make(chan error, 1)
	start := time.Now()
	go func() {
		response, err := server.Client().Do(req)
		if err != nil {
			failures <- err
			return
		}
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		results <- response.StatusCode
	}()
	if _, err := writer.Write(prefix.Bytes()); err != nil {
		t.Fatal(err)
	}
	select {
	case status := <-results:
		if status != 400 {
			t.Fatal("wrong malformed body response", status)
		}
	case err := <-failures:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("server drained an unfinished rejected body")
	}
	if time.Since(start) > time.Second {
		t.Fatal("malformed body waited for overall deadline")
	}
	waitUploadSlots(t, f, f.aliceID, 0)
}

func TestImmutableChunkActualTCPContentLengthAndChunkedRequests(t *testing.T) {
	f := newFixture(t)
	id := immutableInit(t, f, 8, 4)
	server := httptest.NewServer(f.router)
	defer server.Close()
	transport := &http.Transport{MaxConnsPerHost: 1}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	for i, payload := range []string{"AAAA", "BBBB"} {
		body, ct := chunkBody(t, id, i, []byte(payload), "")
		var reader io.Reader = body
		if i == 1 {
			reader = io.NopCloser(bytes.NewReader(body.Bytes()))
		}
		request, err := http.NewRequest("POST", server.URL+"/api/files/upload/chunk", reader)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+f.alice)
		request.Header.Set("Content-Type", ct)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal("valid TCP upload lost its response", err)
		}
		data, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 {
			t.Fatal("valid TCP body rejected", response.StatusCode, string(data), err)
		}
	}
	waitUploadSlots(t, f, f.aliceID, 0)
	file := completedBlob(t, f, completeUploadRecorder(f, f.alice, id))
	if w := f.json(t, "GET", fmt.Sprintf("/api/files/%d/download", file.ID), f.alice, nil, 200); w.Body.String() != "AAAABBBB" {
		t.Fatal("TCP manifest bytes changed")
	}
}
