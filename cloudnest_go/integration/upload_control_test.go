//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"cloudenest_go/internal/app"
	"cloudenest_go/internal/service"
	"cloudenest_go/internal/storage/mysql"
	"cloudenest_go/internal/store"
	"cloudenest_go/model"
	"gorm.io/gorm"
)

func control(t *testing.T, f *fixture, id string, epoch int64, paused bool, want int) {
	t.Helper()
	action := "resume"
	if paused {
		action = "pause"
	}
	f.json(t, "POST", "/api/files/upload/"+action, f.alice, map[string]any{"upload_id": id, "expected_epoch": epoch}, want)
}
func controlRouterRequest(router http.Handler, f *fixture, id string, epoch int64, paused bool) *httptest.ResponseRecorder {
	action := "resume"
	if paused {
		action = "pause"
	}
	raw := fmt.Sprintf(`{"upload_id":%q,"expected_epoch":%d}`, id, epoch)
	req := httptest.NewRequest("POST", "/api/files/upload/"+action, bytes.NewBufferString(raw))
	req.Header.Set("Authorization", "Bearer "+f.alice)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = f.remoteAddr
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}
func controlComplete(t *testing.T, f *fixture, id string, epoch int64) model.File {
	t.Helper()
	raw := fmt.Sprintf(`{"upload_id":%q}`, id)
	req := httptest.NewRequest("POST", "/api/files/upload/complete", bytes.NewBufferString(raw))
	req.Header.Set("Authorization", "Bearer "+f.alice)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Upload-Epoch", fmt.Sprint(epoch))
	req.RemoteAddr = f.remoteAddr
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal("control complete", w.Code, w.Body.String())
	}
	var out struct {
		Item model.File `json:"item"`
	}
	decode(t, w, &out)
	return out.Item
}
func controlChunk(t *testing.T, f *fixture, id string, payload []byte, epoch int64, want int) {
	t.Helper()
	body, ct := chunkBody(t, id, 0, payload, "")
	req := httptest.NewRequest("POST", "/api/files/upload/chunk", body)
	req.Header.Set("Authorization", "Bearer "+f.alice)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("X-Upload-Epoch", fmt.Sprint(epoch))
	req.RemoteAddr = f.remoteAddr
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	if w.Code != want {
		t.Fatal("control chunk", w.Code, w.Body.String())
	}
}
func TestUploadPauseResumePreservesConfirmedChunks(t *testing.T) {
	f := newFixture(t)
	id := fixturePendingUpload(t, f, f.alice, "paused.bin", []byte("keep"))
	original := receiptTask(t, f, id)
	row := chunkRow(t, f, id)
	control(t, f, id, 0, true, 200)
	task := receiptTask(t, f, id)
	if task.Status != 2 || task.TransferEpoch != 1 || task.ReservedBytes != 4 || !task.ExpiresAt.Equal(*original.ExpiresAt) {
		t.Fatal("pause changed contract", task)
	}
	f.json(t, "POST", "/api/files/upload/complete", f.alice, map[string]any{"upload_id": id}, 409)
	sendChunk(t, f, id, 0, []byte("keep"), "", 409)
	var progress struct {
		Status int   `json:"status"`
		Epoch  int64 `json:"transfer_epoch"`
		Count  int   `json:"uploaded_count"`
	}
	decode(t, f.json(t, "GET", "/api/files/upload/progress?upload_id="+id, f.alice, nil, 200), &progress)
	if progress.Status != 2 || progress.Epoch != 1 || progress.Count != 1 {
		t.Fatal("paused progress", progress)
	}
	if got := quotaOverview(t, f, f.alice); got.ReservedBytes != 4 {
		t.Fatal("pause released reservation", got)
	}
	if r := transferAudit(t, f); r.Usage.Charged != 4 {
		t.Fatal("pause freed confirmed bytes", r)
	}
	control(t, f, id, 1, false, 200)
	controlChunk(t, f, id, []byte("keep"), 2, 200)
	if got := chunkRow(t, f, id); got.ChunkPath != row.ChunkPath {
		t.Fatal("resume replaced original chunk")
	}
	file := controlComplete(t, f, id, 2)
	if w := f.json(t, "GET", fmt.Sprintf("/api/files/%d/download", file.ID), f.alice, nil, 200); w.Body.String() != "keep" {
		t.Fatal("resumed bytes changed")
	}
}
func TestUploadControlEpochRejectsDelayedCommandsAndOverflow(t *testing.T) {
	f := newFixture(t)
	id := immutableInit(t, f, 4, 4)
	control(t, f, id, 0, true, 200)
	revision := quotaLedger(t, f, f.aliceID).Revision
	control(t, f, id, 0, true, 200)
	if task := receiptTask(t, f, id); task.TransferEpoch != 1 || task.Status != 2 || quotaLedger(t, f, f.aliceID).Revision != revision {
		t.Fatal("duplicate pause mutated state", task)
	}
	control(t, f, id, 0, false, 409)
	control(t, f, id, 1, false, 200)
	control(t, f, id, 1, false, 200)
	control(t, f, id, 0, true, 409)
	control(t, f, id, 2, true, 200)
	if task := receiptTask(t, f, id); task.TransferEpoch != 3 || task.Status != 2 {
		t.Fatal("late intent reversed state", task)
	}
	if err := f.db.Model(&model.UploadTask{}).Where("upload_id=?", id).Update("transfer_epoch", int64(math.MaxInt64)).Error; err != nil {
		t.Fatal(err)
	}
	control(t, f, id, math.MaxInt64, false, 409)
	for _, body := range []map[string]any{{"upload_id": id}, {"upload_id": id, "expected_epoch": nil}, {"upload_id": id, "expected_epoch": -1}, {"upload_id": id, "expected_epoch": uint64(math.MaxUint64)}} {
		f.json(t, "POST", "/api/files/upload/pause", f.alice, body, 400)
	}
	f.json(t, "POST", "/api/files/upload/pause", f.bob, map[string]any{"upload_id": id, "expected_epoch": 0}, 403)
	f.json(t, "POST", "/api/files/upload/pause", "", map[string]any{"upload_id": id, "expected_epoch": 0}, 401)
}
func TestUploadPauseResumeFencesOldWriterAcrossContainers(t *testing.T) {
	f := newFixture(t)
	id := immutableInit(t, f, 4, 4)
	body, ct := stagedBody(t, id, "OLD!")
	defer body.Close()
	req := httptest.NewRequest("POST", "/api/files/upload/chunk", body)
	req.Header.Set("Authorization", "Bearer "+f.alice)
	req.Header.Set("Content-Type", ct)
	req.RemoteAddr = f.remoteAddr
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { w := httptest.NewRecorder(); f.router.ServeHTTP(w, req); done <- w }()
	select {
	case <-body.blocked:
	case <-time.After(3 * time.Second):
		t.Fatal("old body did not reach writer")
	}
	other := fixtureRouter(app.NewContainer(f.cfg, f.db, f.rdb).NewRouter())
	if w := controlRouterRequest(other, f, id, 0, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := controlRouterRequest(other, f, id, 1, false); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	body.Close()
	select {
	case w := <-done:
		if w.Code != 409 {
			t.Fatal("old generation committed", w.Code, w.Body.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("old writer did not stop")
	}
	if receiptTask(t, f, id).UploadedChunks != 0 {
		t.Fatal("pause/resume accepted stale chunk")
	}
	controlChunk(t, f, id, []byte("NEW!"), 2, 200)
	file := controlComplete(t, f, id, 2)
	if w := f.json(t, "GET", fmt.Sprintf("/api/files/%d/download", file.ID), f.alice, nil, 200); w.Body.String() != "NEW!" {
		t.Fatal("stale bytes published")
	}
}
func TestUploadPauseResumeFencesReadyMergeAndPreflight(t *testing.T) {
	f := newFixture(t)
	id := fixturePendingUpload(t, f, f.alice, "ready-old.bin", []byte("keep"))
	svc := service.NewFileService(mysql.NewFileRepo(f.db))
	snapshot, err := svc.PrepareCompleteUpload(context.Background(), f.aliceID, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	m := transferManager(f)
	h, err := m.Create(context.Background(), f.aliceID, snapshot.Task.ID, "merge", 4, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close(true)
	_, _ = h.Write([]byte("keep"))
	if err := h.Seal(4, fmt.Sprintf("%x", sha256.Sum256([]byte("keep")))); err != nil {
		t.Fatal(err)
	}
	control(t, f, id, 0, true, 200)
	control(t, f, id, 1, false, 200)
	if h.Context().Err() != nil {
		t.Fatal("independent manager unexpectedly locally canceled")
	}
	if old, err := m.Create(context.Background(), f.aliceID, snapshot.Task.ID, "chunk", 5, 0); err == nil {
		old.Close(true)
		t.Fatal("old preflight allocated new generation")
	}
	commit, err := svc.CompleteUpload(h.Context(), f.aliceID, 0, id, snapshot, h.Path(), h.Root(), 4, fmt.Sprintf("%x", sha256.Sum256([]byte("keep"))), h.Token(), f.remoteAddr)
	if err == nil || !commit.CandidateDisposable {
		t.Fatal("old merge published or lost disposal proof", commit, err)
	}
	if err := h.Close(true); err != nil {
		t.Fatal(err)
	}
	file := controlComplete(t, f, id, 2)
	if w := f.json(t, "GET", fmt.Sprintf("/api/files/%d/download", file.ID), f.alice, nil, 200); w.Body.String() != "keep" {
		t.Fatal("winner bytes lost")
	}
}
func TestUploadControlAuditFailureRollsBackStateAndEpoch(t *testing.T) {
	f := newFixture(t)
	id := fixturePendingUpload(t, f, f.alice, "control-audit.bin", []byte("keep"))
	const hook = "test:control_audit"
	var reject atomic.Bool
	reject.Store(true)
	if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if row, ok := tx.Statement.Dest.(*model.OperationLog); ok && (row.Action == "upload_pause" || row.Action == "upload_resume") && reject.Load() {
			tx.AddError(errors.New("injected control audit failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	removed := false
	defer func() {
		if !removed {
			_ = f.db.Callback().Create().Remove(hook)
		}
	}()
	control(t, f, id, 0, true, 500)
	if task := receiptTask(t, f, id); task.Status != 0 || task.TransferEpoch != 0 {
		t.Fatal("failed pause partially committed", task)
	}
	reject.Store(false)
	control(t, f, id, 0, true, 200)
	reject.Store(true)
	control(t, f, id, 1, false, 500)
	if task := receiptTask(t, f, id); task.Status != 2 || task.TransferEpoch != 1 {
		t.Fatal("failed resume partially committed", task)
	}
	reject.Store(false)
	control(t, f, id, 1, false, 200)
	_ = f.db.Callback().Create().Remove(hook)
	removed = true
	controlComplete(t, f, id, 2)
}
func TestUploadPausedQuotaExpiryAndZeroByteContract(t *testing.T) {
	f := newFixture(t)
	quotaSetting(t, f, "4")
	id := immutableInit(t, f, 4, 4)
	control(t, f, id, 0, true, 200)
	f.json(t, "POST", "/api/files/upload/init", f.alice, map[string]any{"filename": "over-paused.bin", "total_size": 1, "chunk_size": 1, "total_chunks": 1}, 507)
	quotaSetting(t, f, "3")
	control(t, f, id, 1, false, 507)
	if task := receiptTask(t, f, id); task.Status != 2 || task.TransferEpoch != 1 {
		t.Fatal("over-quota resume escaped")
	}
	if err := f.db.Model(&model.UploadTask{}).Where("upload_id=?", id).Update("expires_at", gorm.Expr("DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 1 SECOND)")).Error; err != nil {
		t.Fatal(err)
	}
	control(t, f, id, 1, false, 409)
	f.json(t, "GET", "/api/files/upload/progress?upload_id="+id, f.alice, nil, 409)
	f.json(t, "POST", "/api/files/upload/cancel", f.alice, map[string]any{"upload_id": id}, 200)
	control(t, f, id, 1, false, 409)
	zero := emptyInit(t, f, f.alice, "paused-empty.bin")
	control(t, f, zero, 0, true, 200)
	control(t, f, zero, 1, false, 200)
	controlComplete(t, f, zero, 2)
	control(t, f, zero, 2, true, 409)
}
func TestUploadResumeRechecksDirectoryAndBusinessPermission(t *testing.T) {
	f := newFixture(t)
	folder := fixtureFolder(t, f, f.bob, "control-shared", 0)
	grant := model.FilePermission{FileID: folder.ID, UserID: f.aliceID, Permission: "write"}
	if err := f.db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	var init struct {
		ID string `json:"upload_id"`
	}
	decode(t, f.json(t, "POST", "/api/files/upload/init", f.alice, map[string]any{"filename": "shared.bin", "parent_id": folder.ID, "total_size": 4, "chunk_size": 4, "total_chunks": 1}, 200), &init)
	control(t, f, init.ID, 0, true, 200)
	if err := f.db.Where("file_id=? AND user_id=?", folder.ID, f.aliceID).Delete(&model.FilePermission{}).Error; err != nil {
		t.Fatal(err)
	}
	control(t, f, init.ID, 1, false, 403)
	permission := model.UserPathPermission{UserID: f.aliceID, Method: "GET", Path: "/api/files"}
	if err := f.db.Create(&permission).Error; err != nil {
		t.Fatal(err)
	}
	control(t, f, init.ID, 1, true, 403)
	permission = model.UserPathPermission{UserID: f.aliceID, Method: "POST", Path: "/api/files/upload/pause"}
	if err := f.db.Create(&permission).Error; err != nil {
		t.Fatal(err)
	}
	control(t, f, init.ID, 1, true, 200)
	control(t, f, init.ID, 1, false, 403)
}
func TestUploadReconcilePreservesPausedChunksAndReclaimsFencedMerge(t *testing.T) {
	f := newFixture(t)
	id := fixturePendingUpload(t, f, f.alice, "paused-reconcile.bin", []byte("keep"))
	task := receiptTask(t, f, id)
	m := transferManager(f)
	h, err := m.Create(context.Background(), f.aliceID, task.ID, "merge", 4, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(h, "keep")
	if err := h.Seal(4, fmt.Sprintf("%x", sha256.Sum256([]byte("keep")))); err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			h.Close(true)
		}
	}()
	control(t, f, id, 0, true, 200)
	r, err := m.Reconcile(context.Background())
	if r.Busy != 1 || r.Usage.Charged != 8 {
		t.Fatal("pause stole live FD/reservation", r, err)
	}
	if err := h.Close(false); err != nil {
		t.Fatal(err)
	}
	closed = true
	r, err = m.Reconcile(context.Background())
	if err != nil || r.Removed != 1 || r.Usage.Charged != 4 {
		t.Fatal("fenced merge not reclaimed/confirmed chunk lost", r, err)
	}
	control(t, f, id, 1, false, 200)
	controlComplete(t, f, id, 2)
}
func TestUploadControlSchemaRequiresExplicitEpochMigration(t *testing.T) {
	f := newFixture(t)
	id := fixturePendingUpload(t, f, f.alice, "epoch-upgrade.bin", []byte("keep"))
	row := chunkRow(t, f, id)
	if err := f.db.Migrator().DropColumn(&model.UploadTask{}, "transfer_epoch"); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Migrator().DropColumn(&model.TransferArtifact{}, "transfer_epoch"); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckTransferSchema(f.db); err == nil {
		t.Fatal("missing epoch schema accepted")
	}
	if err := store.Migrate(f.db); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckTransferSchema(f.db); err != nil {
		t.Fatal(err)
	}
	if task := receiptTask(t, f, id); task.TransferEpoch != 0 {
		t.Fatal("migration fabricated epoch")
	}
	control(t, f, id, 0, true, 200)
	control(t, f, id, 1, false, 200)
	if now := chunkRow(t, f, id); now.ChunkPath != row.ChunkPath {
		t.Fatal("migration rewrote confirmed chunk")
	}
	if err := f.db.Exec("ALTER TABLE transfer_artifact MODIFY transfer_epoch BIGINT UNSIGNED NOT NULL DEFAULT 0").Error; err != nil {
		t.Fatal(err)
	}
	if err := store.CheckTransferSchema(f.db); err == nil {
		t.Fatal("epoch signedness drift accepted")
	}
	if err := f.db.Exec("ALTER TABLE transfer_artifact MODIFY transfer_epoch BIGINT NOT NULL DEFAULT 0").Error; err != nil {
		t.Fatal(err)
	}
	controlComplete(t, f, id, 2)
}

func TestUploadPauseInterruptsActualTCPBodyLocallyAndAcrossContainers(t *testing.T) {
	for _, remote := range []bool{false, true} {
		t.Run(fmt.Sprintf("remote_%t", remote), func(t *testing.T) {
			f := newFixture(t)
			id := immutableInit(t, f, 4, 4)
			task := receiptTask(t, f, id)
			server := httptest.NewServer(f.router)
			defer server.Close()
			raw, ct := chunkBody(t, id, 0, []byte("keep"), "")
			data := raw.Bytes()
			cut := bytes.LastIndex(data, []byte("\r\n--"))
			if cut < 0 {
				t.Fatal("missing MIME boundary")
			}
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			req, _ := http.NewRequest("POST", server.URL+"/api/files/upload/chunk", reader)
			req.Header.Set("Authorization", "Bearer "+f.alice)
			req.Header.Set("Content-Type", ct)
			type result struct {
				code int
				err  error
			}
			done := make(chan result, 1)
			go func() {
				resp, err := server.Client().Do(req)
				if err != nil {
					done <- result{err: err}
					return
				}
				_, err = io.ReadAll(resp.Body)
				resp.Body.Close()
				done <- result{resp.StatusCode, err}
			}()
			if _, err := writer.Write(data[:cut]); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				var count int64
				if err := f.db.Model(&model.TransferArtifact{}).Where("task_id=? AND state='writing'", task.ID).Count(&count).Error; err != nil {
					t.Fatal(err)
				}
				if count == 1 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("TCP body never acquired writer")
				}
				time.Sleep(10 * time.Millisecond)
			}
			controller := f.router
			if remote {
				controller = fixtureRouter(app.NewContainer(f.cfg, f.db, f.rdb).NewRouter())
			}
			start := time.Now()
			if w := controlRouterRequest(controller, f, id, 0, true); w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			wait := 3 * time.Second
			if remote {
				wait = 27 * time.Second
			}
			select {
			case got := <-done:
				if got.err != nil || got.code != 409 {
					t.Fatal("paused TCP body not rejected", got)
				}
			case <-time.After(wait):
				t.Fatal("paused TCP read did not stop")
			}
			if remote && time.Since(start) < 10*time.Second {
				t.Fatal("test did not exercise cross-process heartbeat")
			}
			if task := receiptTask(t, f, id); task.Status != 2 || task.UploadedChunks != 0 {
				t.Fatal("paused body committed", task)
			}
			if r := transferAudit(t, f); r.Usage.Charged != 0 {
				t.Fatal("stopped body retained reservation", r)
			}
		})
	}
}
func TestUploadPauseFencesReadyHTTPCompletionWithout500(t *testing.T) {
	f := newFixture(t)
	id := fixturePendingUpload(t, f, f.alice, "pause-ready-http.bin", []byte("keep"))
	task := receiptTask(t, f, id)
	blocked, release := make(chan struct{}), make(chan struct{})
	var once atomic.Bool
	const hook = "test:pause_http_ready"
	if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if row, ok := tx.Statement.Dest.(*model.UserStorageLedger); ok && row.UserID == f.aliceID {
			var ready int64
			if err := f.db.Model(&model.TransferArtifact{}).Where("task_id=? AND kind='merge' AND state='ready'", task.ID).Count(&ready).Error; err != nil {
				tx.AddError(err)
				return
			}
			if ready > 0 && once.CompareAndSwap(false, true) {
				close(blocked)
				<-release
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- completeUploadRecorder(f, f.alice, id) }()
	released, removed := false, false
	defer func() {
		if !released {
			close(release)
		}
		if !removed {
			_ = f.db.Callback().Create().Remove(hook)
		}
	}()
	select {
	case <-blocked:
	case <-time.After(3 * time.Second):
		t.Fatal("completion not ready")
	}
	control(t, f, id, 0, true, 200)
	close(release)
	released = true
	select {
	case w := <-done:
		if w.Code != 409 {
			t.Fatal("paused completion", w.Code, w.Body.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("completion did not stop")
	}
	_ = f.db.Callback().Create().Remove(hook)
	removed = true
	control(t, f, id, 1, false, 200)
	controlComplete(t, f, id, 2)
}

func TestUploadControlledTaskRequiresExplicitEpochBeforeCandidateAllocation(t *testing.T) {
	f := newFixture(t)
	id := immutableInit(t, f, 4, 4)
	// No metadata has been admitted: a queued legacy/old request must not silently
	// receive the latest generation when it finally reaches the server.
	control(t, f, id, 0, true, 200)
	control(t, f, id, 1, false, 200)
	sendChunk(t, f, id, 0, []byte("OLD!"), "", 409)
	controlChunk(t, f, id, []byte("OLD!"), 0, 409)
	var count int64
	if err := f.db.Model(&model.TransferArtifact{}).Where("task_id=?", receiptTask(t, f, id).ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("old request acquired new generation", count, err)
	}
	controlChunk(t, f, id, []byte("NEW!"), 2, 200)
	f.json(t, "POST", "/api/files/upload/complete", f.alice, map[string]any{"upload_id": id}, 409)
	file := controlComplete(t, f, id, 2)
	if replay := emptyComplete(t, f, f.alice, id); replay.ID != file.ID {
		t.Fatal("old client cannot acknowledge completed controlled task")
	}
	for _, headers := range [][]string{{""}, {"-1"}, {"9223372036854775808"}, {"0", "2"}} {
		// Use a new running task; completed receipts intentionally bypass epoch headers.
		fresh := immutableInit(t, f, 4, 4)
		body, ct := chunkBody(t, fresh, 0, []byte("data"), "")
		req := httptest.NewRequest("POST", "/api/files/upload/chunk", body)
		req.Header.Set("Authorization", "Bearer "+f.alice)
		req.Header.Set("Content-Type", ct)
		req.RemoteAddr = f.remoteAddr
		for _, value := range headers {
			req.Header.Add("X-Upload-Epoch", value)
		}
		w := httptest.NewRecorder()
		f.router.ServeHTTP(w, req)
		if w.Code != 400 {
			t.Fatal("ambiguous epoch accepted", headers, w.Code)
		}
	}
}
