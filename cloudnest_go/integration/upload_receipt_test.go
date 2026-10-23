//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cloudenest_go/internal/app"
	"cloudenest_go/internal/service"
	"cloudenest_go/internal/store"
	"cloudenest_go/model"
	"gorm.io/gorm"
)

func receiptTask(t *testing.T, f *fixture, id string) model.UploadTask {
	t.Helper()
	var task model.UploadTask
	if err := f.db.First(&task, "upload_id=?", id).Error; err != nil {
		t.Fatal(err)
	}
	return task
}
func assertReceiptReply(t *testing.T, f *fixture, id string, original *httptest.ResponseRecorder) {
	t.Helper()
	w := completeUploadRecorder(f, f.alice, id)
	if w.Code != 200 || w.Body.String() != original.Body.String() {
		t.Fatalf("receipt replay changed result: %d %s", w.Code, w.Body.String())
	}
}
func receiptSideEffects(t *testing.T, f *fixture, fileID int64) []int64 {
	t.Helper()
	var values []int64
	for _, table := range []any{&model.File{}, &model.FileVersion{}, &model.AiTask{}} {
		var n int64
		q := f.db.Model(table)
		switch table.(type) {
		case *model.File:
			q = q.Where("id=?", fileID)
		default:
			q = q.Where("file_id=?", fileID)
		}
		if err := q.Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		values = append(values, n)
	}
	var audits int64
	if err := f.db.Model(&model.OperationLog{}).Where("file_id=?", fileID).Count(&audits).Error; err != nil {
		t.Fatal(err)
	}
	values = append(values, audits)
	var ledger model.UserStorageLedger
	if err := f.db.First(&ledger, "user_id=?", f.aliceID).Error; err != nil {
		t.Fatal(err)
	}
	return append(values, int64(ledger.Revision), ledger.UsedBytes, ledger.ReservedBytes)
}

func TestUploadReceiptReplaysAcrossContainerReconstruction(t *testing.T) {
	f := newFixture(t)
	id := fixturePendingUpload(t, f, f.alice, "receipt-restart.bin", []byte("committed before retry"))
	original := completeUploadRecorder(f, f.alice, id)
	file := completedBlob(t, f, original)
	waitNoReadLeases(t, f, *file.StorageID)
	before := fmt.Sprint(receiptSideEffects(t, f, file.ID))
	// These changes must not turn acknowledgement into a second allocation.
	quotaSetting(t, f, "broken-policy")
	if err := f.db.Model(&model.UploadTask{}).Where("upload_id=?", id).Update("expires_at", gorm.Expr("DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 1 DAY)")).Error; err != nil {
		t.Fatal(err)
	}
	f.router = app.NewContainer(f.cfg, f.db, f.rdb).NewRouter()
	for range 3 {
		assertReceiptReply(t, f, id, original)
	}
	if got := fmt.Sprint(receiptSideEffects(t, f, file.ID)); got != before {
		t.Fatalf("replay repeated side effects: %s => %s", before, got)
	}
	f.json(t, "POST", "/api/files/upload/cancel", f.alice, map[string]any{"upload_id": id}, 409)
	var progress service.FileUploadProgressResult
	decode(t, f.json(t, "GET", "/api/files/upload/progress?upload_id="+id, f.alice, nil, 200), &progress)
	if progress.Status != 1 || progress.Progress != 100 || progress.UploadedCount != progress.TotalChunks || len(progress.MissingChunks) != 0 {
		t.Fatalf("completed task regressed: %+v", progress)
	}
}

type disconnectReceiptWriter struct {
	http.ResponseWriter
	disconnected *atomic.Bool
}

func (w disconnectReceiptWriter) Write(p []byte) (int, error) {
	if w.disconnected.CompareAndSwap(false, true) {
		conn, _, err := w.ResponseWriter.(http.Hijacker).Hijack()
		if err != nil {
			return 0, err
		}
		_ = conn.Close()
	}
	return 0, io.ErrClosedPipe
}
func TestUploadReceiptRecoversActualTCPResponseLoss(t *testing.T) {
	f := newFixture(t)
	id := fixturePendingUpload(t, f, f.alice, "receipt-network.bin", []byte("published despite lost HTTP body"))
	var disconnected atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.router.ServeHTTP(disconnectReceiptWriter{w, &disconnected}, r)
	}))
	defer server.Close()
	req, err := http.NewRequest("POST", server.URL+"/api/files/upload/complete", strings.NewReader(`{"upload_id":"`+id+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+f.alice)
	req.Header.Set("Content-Type", "application/json")
	response, requestErr := server.Client().Do(req)
	if requestErr == nil {
		body, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr == nil && len(body) > 0 {
			t.Fatalf("test did not lose response: %s", body)
		}
	}
	if !disconnected.Load() {
		t.Fatal("request never reached response write")
	}
	task := receiptTask(t, f, id)
	if task.Status != 1 || task.ResultFileID == nil || task.ResultJSON == nil {
		t.Fatal("lost response did not commit a receipt")
	}
	f.waitAnalysis(t, *task.ResultFileID)
	var file model.File
	if err := json.Unmarshal([]byte(*task.ResultJSON), &file); err != nil {
		t.Fatal(err)
	}
	waitNoReadLeases(t, f, *file.StorageID)
	before := fmt.Sprint(receiptSideEffects(t, f, *task.ResultFileID))
	f.router = app.NewContainer(f.cfg, f.db, f.rdb).NewRouter()
	replay := completeUploadRecorder(f, f.alice, id)
	if replay.Code != 200 || strings.TrimSpace(replay.Body.String()) != `{"item":`+*task.ResultJSON+`}` {
		t.Fatalf("lost response not recoverable: %d %s", replay.Code, replay.Body.String())
	}
	if after := fmt.Sprint(receiptSideEffects(t, f, *task.ResultFileID)); after != before {
		t.Fatal("network retry repeated side effects", before, after)
	}
}

func TestUploadReceiptOwnershipFrozenParentAndCurrentAuthentication(t *testing.T) {
	f := newFixture(t)
	parent := fixtureFolder(t, f, f.bob, "receipt-shared-parent", 0)
	if err := f.db.Create(&model.FilePermission{FileID: parent.ID, UserID: f.aliceID, Permission: "write"}).Error; err != nil {
		t.Fatal(err)
	}
	id := fixturePendingUploadAt(t, f, f.alice, "receipt-shared.bin", []byte("owned by uploader"), parent.ID)
	original := quotaJSON(f, "POST", "/api/files/upload/complete", f.alice, map[string]any{"upload_id": id, "parent_id": parent.ID})
	file := completedBlob(t, f, original)
	waitNoReadLeases(t, f, *file.StorageID)
	f.json(t, "POST", "/api/files/upload/complete", f.bob, map[string]any{"upload_id": id, "parent_id": parent.ID}, 403)
	f.json(t, "POST", "/api/files/upload/complete", f.alice, map[string]any{"upload_id": id}, 409)
	if err := f.db.Where("file_id=? AND user_id=?", parent.ID, f.aliceID).Delete(&model.FilePermission{}).Error; err != nil {
		t.Fatal(err)
	}
	replay := quotaJSON(f, "POST", "/api/files/upload/complete", f.alice, map[string]any{"upload_id": id, "parent_id": parent.ID})
	if replay.Code != 200 || replay.Body.String() != original.Body.String() {
		t.Fatal("own acknowledgement depended on current parent write grant")
	}
	if err := f.db.Model(&model.User{}).Where("id=?", f.aliceID).Update("status", 0).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", "/api/files/upload/complete", f.alice, map[string]any{"upload_id": id, "parent_id": parent.ID}, 401)
	if err := f.db.Model(&model.User{}).Where("id=?", f.aliceID).Update("status", 1).Error; err != nil {
		t.Fatal(err)
	}
	if file.UserID != f.aliceID {
		t.Fatal("fixture owner wrong")
	}
}

func TestUploadReceiptDoesNotResurrectOrAuthorizeDeletedContent(t *testing.T) {
	f := newFixture(t)
	id := fixturePendingUpload(t, f, f.alice, "receipt-lifecycle.bin", []byte("original acknowledgement"))
	original := completeUploadRecorder(f, f.alice, id)
	file := completedBlob(t, f, original)
	waitNoReadLeases(t, f, *file.StorageID)
	f.json(t, "PUT", fmt.Sprintf("/api/files/%d/rename", file.ID), f.alice, map[string]string{"name": "renamed-after-upload.bin"}, 200)
	assertReceiptReply(t, f, id, original)
	var current model.File
	if err := f.db.First(&current, file.ID).Error; err != nil || current.Filename != "renamed-after-upload.bin" {
		t.Fatal("receipt rewrote current name", err)
	}
	f.json(t, "DELETE", fmt.Sprintf("/api/files/%d", file.ID), f.alice, nil, 200)
	f.json(t, "DELETE", fmt.Sprintf("/api/files/%d/permanent", file.ID), f.alice, nil, 200)
	assertReceiptReply(t, f, id, original)
	f.json(t, "GET", fmt.Sprintf("/api/files/%d/download", file.ID), f.alice, nil, 403)
	target := f.upload(t, f.bob, "receipt-not-a-capability.bin", []byte("bob content"))
	f.json(t, "POST", fmt.Sprintf("/api/files/%d/versions", target.ID), f.bob, map[string]any{"storage_id": *file.StorageID, "size": file.Size, "base_version": 1}, 403)
	var count int64
	if err := f.db.Model(&model.File{}).Where("id=?", file.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("receipt retained/resurrected file", err, count)
	}
}

func TestUploadReceiptAndPublicationRollbackTogether(t *testing.T) {
	f := newFixture(t)
	payload := []byte("receipt transaction rollback")
	id := fixturePendingUpload(t, f, f.alice, "receipt-rollback.bin", payload)
	for _, stage := range []string{"result-transition", "quota-audit"} {
		const hook = "test:receipt_atomicity"
		callback := func(tx *gorm.DB) {
			if stage == "result-transition" && tx.Statement.Table == "upload_task" {
				if updates, ok := tx.Statement.Dest.(map[string]interface{}); ok && updates["result_json"] != nil {
					tx.AddError(errors.New("injected receipt persistence failure"))
				}
			}
			if stage == "quota-audit" {
				if row, ok := tx.Statement.Dest.(*model.OperationLog); ok && row.Action == "quota_accounting" {
					tx.AddError(errors.New("injected post-receipt audit failure"))
				}
			}
		}
		var err error
		if stage == "result-transition" {
			err = f.db.Callback().Update().Before("gorm:update").Register(hook, callback)
		} else {
			err = f.db.Callback().Create().Before("gorm:create").Register(hook, callback)
		}
		if err != nil {
			t.Fatal(err)
		}
		reply := completeUploadRecorder(f, f.alice, id)
		f.db.Callback().Update().Remove(hook)
		f.db.Callback().Create().Remove(hook)
		if reply.Code != 500 {
			t.Fatalf("%s failure=%d %s", stage, reply.Code, reply.Body.String())
		}
		task := receiptTask(t, f, id)
		if task.Status != 0 || task.ResultVersion != 0 || task.ResultFileID != nil || task.ResultJSON != nil || task.ReservedBytes != int64(len(payload)) {
			t.Fatal("receipt escaped rolled back transaction")
		}
		var count int64
		if err := f.db.Model(&model.File{}).Where("user_id=? AND filename=?", f.aliceID, "receipt-rollback.bin").Count(&count).Error; err != nil || count != 0 {
			t.Fatal("publication escaped rollback", err, count)
		}
	}
	original := completeUploadRecorder(f, f.alice, id)
	completedBlob(t, f, original)
	assertReceiptReply(t, f, id, original)
}

func TestUploadReceiptLegacyAndCorruptionNeverGuessAResult(t *testing.T) {
	f := newFixture(t)
	id := fixturePendingUpload(t, f, f.alice, "receipt-corruption.bin", []byte("validate stored acknowledgement"))
	original := completeUploadRecorder(f, f.alice, id)
	file := completedBlob(t, f, original)
	waitNoReadLeases(t, f, *file.StorageID)
	saved := receiptTask(t, f, id)
	restore := func() {
		if err := f.db.Model(&model.UploadTask{}).Where("id=?", saved.ID).Updates(map[string]any{"result_version": saved.ResultVersion, "result_file_id": saved.ResultFileID, "result_json": saved.ResultJSON, "reserved_bytes": 0}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := f.db.Model(&model.UploadTask{}).Where("id=?", saved.ID).Updates(map[string]any{"result_version": 0, "result_file_id": nil, "result_json": nil}).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", "/api/files/upload/complete", f.alice, map[string]any{"upload_id": id}, 409)
	restore()
	wrongOwner := file
	wrongOwner.UserID = f.bobID
	badOwner, _ := json.Marshal(wrongOwner)
	for _, changes := range []map[string]any{
		{"result_json": "{"}, {"result_json": *saved.ResultJSON + ` {}`}, {"result_json": string(badOwner)},
		{"result_file_id": file.ID + 99999}, {"result_file_id": nil}, {"result_json": nil}, {"result_version": 2}, {"reserved_bytes": 1},
	} {
		if err := f.db.Model(&model.UploadTask{}).Where("id=?", saved.ID).Updates(changes).Error; err != nil {
			t.Fatal(err)
		}
		f.json(t, "POST", "/api/files/upload/complete", f.alice, map[string]any{"upload_id": id}, 503)
		f.json(t, "POST", "/api/files/upload/complete", f.bob, map[string]any{"upload_id": id}, 403)
		restore()
	}
	assertReceiptReply(t, f, id, original)
	var count int64
	if err := f.db.Model(&model.OperationLog{}).Where("action='upload_complete' AND file_id=?", file.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("bad receipt republished", err, count)
	}
}

func TestUploadReceiptExplicitUpgradeKeepsOldCompletionUnknown(t *testing.T) {
	f := newFixture(t)
	id := fixturePendingUpload(t, f, f.alice, "receipt-upgrade.bin", []byte("old completed task"))
	original := completeUploadRecorder(f, f.alice, id)
	file := completedBlob(t, f, original)
	waitNoReadLeases(t, f, *file.StorageID)
	if err := f.db.Exec("ALTER TABLE upload_task DROP COLUMN result_version, DROP COLUMN result_file_id, DROP COLUMN result_json").Error; err != nil {
		t.Fatal(err)
	}
	if err := store.CheckFileSchema(f.db); err == nil {
		t.Fatal("startup accepted missing receipt schema")
	}
	if err := store.Migrate(f.db); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckFileSchema(f.db); err != nil {
		t.Fatal(err)
	}
	task := receiptTask(t, f, id)
	if task.Status != 1 || task.ResultVersion != 0 || task.ResultFileID != nil || task.ResultJSON != nil {
		t.Fatal("migration invented an old result")
	}
	f.json(t, "POST", "/api/files/upload/complete", f.alice, map[string]any{"upload_id": id}, 409)
	next := fixturePendingUpload(t, f, f.alice, "receipt-upgrade.bin", []byte("new receipt task"))
	completedBlob(t, f, completeUploadRecorder(f, f.alice, next))
	if err := f.db.Exec("ALTER TABLE upload_task MODIFY result_json VARCHAR(4096) NULL").Error; err != nil {
		t.Fatal(err)
	}
	if err := store.CheckFileSchema(f.db); err == nil {
		t.Fatal("startup accepted changed receipt type")
	}
	if err := store.Migrate(f.db); err != nil {
		t.Fatal(err)
	}
}

func TestUploadReceiptCoversChunkMetadataAndMergeRaces(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(fmt.Sprintf("after_count_%t", after), func(t *testing.T) {
			f := newFixture(t)
			id := fixturePendingUpload(t, f, f.alice, "receipt-preflight-race.bin", []byte("concurrent cleanup window"))
			paused, release := make(chan struct{}), make(chan struct{})
			var seen atomic.Int32
			var once sync.Once
			const hook = "test:receipt_chunk_snapshot"
			callback := func(tx *gorm.DB) {
				if tx.Statement.Table == "upload_chunk" && seen.Add(1) == 1 {
					close(paused)
					<-release
				}
			}
			var err error
			if after {
				err = f.db.Callback().Query().After("gorm:query").Register(hook, callback)
			} else {
				err = f.db.Callback().Query().Before("gorm:query").Register(hook, callback)
			}
			if err != nil {
				t.Fatal(err)
			}
			first := make(chan *httptest.ResponseRecorder, 1)
			go func() { first <- completeUploadRecorder(f, f.alice, id) }()
			defer func() { once.Do(func() { close(release) }); f.db.Callback().Query().Remove(hook) }()
			select {
			case <-paused:
			case <-time.After(10 * time.Second):
				t.Fatal("first completion did not reach chunk window")
			}
			second := completeUploadRecorder(f, f.alice, id)
			completedBlob(t, f, second)
			once.Do(func() { close(release) })
			replay := <-first
			if replay.Code != 200 || !bytes.Equal(replay.Body.Bytes(), second.Body.Bytes()) {
				t.Fatalf("race lost result: first=%d %s second=%d", replay.Code, replay.Body.String(), second.Code)
			}
		})
	}
}
