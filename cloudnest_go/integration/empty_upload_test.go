//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"cloudenest_go/model"
)

func emptyInit(t *testing.T, f *fixture, token, name string) string {
	t.Helper()
	var r struct {
		ID string `json:"upload_id"`
	}
	decode(t, f.json(t, "POST", "/api/files/upload/init", token, map[string]any{"filename": name, "total_size": 0, "chunk_size": 4096, "total_chunks": 0}, 200), &r)
	if r.ID == "" {
		t.Fatal("empty upload missing ID")
	}
	return r.ID
}
func emptyComplete(t *testing.T, f *fixture, token, id string) model.File {
	t.Helper()
	var r struct {
		Item model.File `json:"item"`
	}
	decode(t, f.json(t, "POST", "/api/files/upload/complete", token, map[string]any{"upload_id": id}, 200), &r)
	return r.Item
}
func TestEmptyUploadPublishesZeroBytesAndReplaysOriginalReceipt(t *testing.T) {
	f := newFixture(t)
	id := emptyInit(t, f, f.alice, "empty.txt")
	before := quotaOverview(t, f, f.alice)
	if before.UsedBytes != 0 || before.ReservedBytes != 0 || !quotaLedger(t, f, f.aliceID).AccountingValid {
		t.Fatal("zero reservation not counted correctly", before)
	}
	file := emptyComplete(t, f, f.alice, id)
	if file.Size != 0 || file.StorageID == nil || file.CurrentVersion != 1 {
		t.Fatal("invalid empty file", file)
	}
	response := f.json(t, "GET", fmt.Sprintf("/api/files/%d/download", file.ID), f.alice, nil, http.StatusOK)
	if response.Body.Len() != 0 || response.Header().Get("Content-Length") != "0" {
		t.Fatal("not an actual empty download", response.Header(), response.Body.String())
	}
	replay := emptyComplete(t, f, f.alice, id)
	if replay.ID != file.ID || replay.Size != 0 {
		t.Fatal("empty retry published again")
	}
}

func TestEmptyUploadRequiresExplicitSizeAndCanonicalZeroChunks(t *testing.T) {
	f := newFixture(t)
	for _, body := range []map[string]any{
		{"filename": "missing.txt"}, {"filename": "null.txt", "total_size": nil},
		{"filename": "negative.txt", "total_size": -1},
		{"filename": "one-empty-chunk.txt", "total_size": 0, "chunk_size": 4096, "total_chunks": 1},
		{"filename": "bad-chunk.txt", "total_size": 0, "chunk_size": -1},
		{"filename": "bad-count.txt", "total_size": 0, "total_chunks": -1},
		{"filename": "huge-chunk.txt", "total_size": 0, "chunk_size": int64(65 << 20)},
	} {
		f.json(t, "POST", "/api/files/upload/init", f.alice, body, 400)
	}
	var count int64
	if err := f.db.Model(&model.UploadTask{}).Where("user_id=?", f.aliceID).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("invalid init persisted", count, err)
	}
	var automatic struct {
		ID string `json:"upload_id"`
	}
	decode(t, f.json(t, "POST", "/api/files/upload/init", f.alice, map[string]any{"filename": "automatic.txt", "total_size": 0}, 200), &automatic)
	task := receiptTask(t, f, automatic.ID)
	if task.TotalChunks == nil || *task.TotalChunks != 0 || task.ChunkSize == nil || *task.ChunkSize <= 0 {
		t.Fatal("empty shape not canonical", task)
	}
	sendChunk(t, f, automatic.ID, 0, nil, "", 400)
	if chunkFiles(t, f) != 0 {
		t.Fatal("empty task accepted chunk body")
	}
	emptyComplete(t, f, f.alice, automatic.ID)
}
func TestEmptyUploadReservesNoLogicalBytesAtExactQuota(t *testing.T) {
	f := newFixture(t)
	quotaSetting(t, f, "4")
	f.upload(t, f.alice, "full-quota.bin", []byte("data"))
	id := emptyInit(t, f, f.alice, "zero-at-quota.bin")
	if got := quotaOverview(t, f, f.alice); got.UsedBytes != 4 || got.ReservedBytes != 0 || got.AvailableForUploadBytes != 0 {
		t.Fatal("zero changed reservation", got)
	}
	emptyComplete(t, f, f.alice, id)
	if got := quotaOverview(t, f, f.alice); got.UsedBytes != 4 || got.ReservedBytes != 0 || !quotaLedger(t, f, f.aliceID).AccountingValid {
		t.Fatal("zero changed billable bytes", got)
	}
	f.json(t, "POST", "/api/files/upload/init", f.alice, map[string]any{"filename": "one-more.bin", "total_size": 1, "chunk_size": 1, "total_chunks": 1}, 507)
}
func TestEmptyUploadConcurrentCompletionOnlyPublishesOnce(t *testing.T) {
	f := newFixture(t)
	id := emptyInit(t, f, f.alice, "concurrent-empty.bin")
	var group sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 8)
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			results <- quotaJSON(f, "POST", "/api/files/upload/complete", f.alice, map[string]any{"upload_id": id})
		}()
	}
	group.Wait()
	close(results)
	for response := range results {
		if response.Code != 200 && response.Code != 409 {
			t.Fatal("unexpected competing completion", response.Code, response.Body.String())
		}
	}
	file := emptyComplete(t, f, f.alice, id)
	var count int64
	if err := f.db.Model(&model.File{}).Where("user_id=?", f.aliceID).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("duplicate empty file", count, err)
	}
	if err := f.db.Model(&model.FileVersion{}).Where("file_id=?", file.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("duplicate initial version", count, err)
	}
	if got := transferAudit(t, f); got.Usage.Charged != 0 {
		t.Fatal("completion leaked reservation", got)
	}
}
func TestEmptyUploadPreservesOwnerFrozenShapeAndProgress(t *testing.T) {
	f := newFixture(t)
	id := emptyInit(t, f, f.alice, "frozen-empty.bin")
	f.json(t, "POST", "/api/files/upload/complete", f.bob, map[string]any{"upload_id": id}, 403)
	f.json(t, "POST", "/api/files/upload/complete", f.alice, map[string]any{"upload_id": id, "parent_id": 999999}, 409)
	f.json(t, "POST", "/api/files/upload/init", f.alice, map[string]any{"upload_id": id, "filename": "frozen-empty.bin", "total_size": 0, "chunk_size": 8192, "total_chunks": 0}, 409)
	f.json(t, "POST", "/api/files/upload/init", f.alice, map[string]any{"upload_id": id, "filename": "frozen-empty.bin", "total_size": 0, "chunk_size": 4096, "total_chunks": 0}, 200)
	var progress struct {
		Status, Uploaded, Total int
		Progress                float64
		Missing, Chunks         []int
	}
	readProgress := func() {
		var response struct {
			Status   int     `json:"status"`
			Uploaded int     `json:"uploaded_count"`
			Total    int     `json:"total_chunks"`
			Progress float64 `json:"progress"`
			Missing  []int   `json:"missing_chunks"`
			Chunks   []int   `json:"uploaded_chunks"`
		}
		decode(t, f.json(t, "GET", "/api/files/upload/progress?upload_id="+id, f.alice, nil, 200), &response)
		progress.Status, progress.Uploaded, progress.Total, progress.Progress, progress.Missing, progress.Chunks = response.Status, response.Uploaded, response.Total, response.Progress, response.Missing, response.Chunks
	}
	readProgress()
	if progress.Status != 0 || progress.Progress != 0 || progress.Total != 0 || progress.Missing == nil || progress.Chunks == nil {
		t.Fatal("pending zero progress", progress)
	}
	file := emptyComplete(t, f, f.alice, id)
	readProgress()
	if progress.Status != 1 || progress.Progress != 100 || progress.Total != 0 || progress.Uploaded != 0 || len(progress.Missing) != 0 || len(progress.Chunks) != 0 {
		t.Fatal("completed zero progress", progress)
	}
	f.json(t, "PUT", fmt.Sprintf("/api/files/%d/rename", file.ID), f.alice, map[string]any{"name": "renamed-empty.bin"}, 200)
	replay := emptyComplete(t, f, f.alice, id)
	if replay.ID != file.ID || replay.Filename != "frozen-empty.bin" {
		t.Fatal("zero receipt was reconstructed", replay)
	}
}
func TestEmptyUploadStillChargesMinimumTemporaryReservation(t *testing.T) {
	f := newFixture(t)
	m := transferManager(f)
	if err := m.SetLimit(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	first := emptyInit(t, f, f.alice, "held-empty.bin")
	task := receiptTask(t, f, first)
	handle, err := m.Create(context.Background(), f.aliceID, task.ID, "merge", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = handle.Close(true)
		}
	}()
	if err := handle.Seal(0, fmt.Sprintf("%x", sha256.Sum256(nil))); err != nil {
		t.Fatal(err)
	}
	if got := transferAudit(t, f); got.Usage.Charged != 1 {
		t.Fatal("zero bypassed temp budget", got)
	}
	other := emptyInit(t, f, f.alice, "waiting-empty.bin")
	f.json(t, "POST", "/api/files/upload/complete", f.alice, map[string]any{"upload_id": other}, 507)
	if err := handle.Close(true); err != nil {
		t.Fatal(err)
	}
	closed = true
	if got := transferAudit(t, f); got.Usage.Charged != 0 {
		t.Fatal("zero candidate not reclaimed", got)
	}
	emptyComplete(t, f, f.alice, other)
}
func TestEmptyUploadPublicationRollbackKeepsRetryableTask(t *testing.T) {
	f := newFixture(t)
	id := emptyInit(t, f, f.alice, "rollback-empty.bin")
	const hook = "test:empty_publish_audit"
	if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if row, ok := tx.Statement.Dest.(*model.OperationLog); ok && row.Action == "upload_complete" && row.UserID != nil && *row.UserID == f.aliceID {
			tx.AddError(errors.New("injected empty publication audit failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", "/api/files/upload/complete", f.alice, map[string]any{"upload_id": id}, 500)
	_ = f.db.Callback().Create().Remove(hook)
	if task := receiptTask(t, f, id); task.Status != 0 || task.ResultJSON != nil || task.MergeArtifact != "" {
		t.Fatal("partial empty publication", task)
	}
	if got := transferAudit(t, f); got.Usage.Charged != 0 {
		t.Fatal("failed zero publication retained candidate", got)
	}
	file := emptyComplete(t, f, f.alice, id)
	f.json(t, "GET", fmt.Sprintf("/api/files/%d/download", file.ID), f.alice, nil, 200)
}
func TestEmptyUploadDeduplicatesAcrossOwnersWithoutGrantingAccess(t *testing.T) {
	f := newFixture(t)
	alice := emptyComplete(t, f, f.alice, emptyInit(t, f, f.alice, "alice-empty.bin"))
	bob := emptyComplete(t, f, f.bob, emptyInit(t, f, f.bob, "bob-empty.bin"))
	if alice.StorageID == nil || bob.StorageID == nil || *alice.StorageID != *bob.StorageID {
		t.Fatal("empty object not deduplicated")
	}
	f.json(t, "GET", fmt.Sprintf("/api/files/%d/download", alice.ID), f.bob, nil, 403)
	f.json(t, "DELETE", fmt.Sprintf("/api/files/%d", alice.ID), f.alice, nil, 200)
	f.json(t, "DELETE", fmt.Sprintf("/api/files/%d/permanent", alice.ID), f.alice, nil, 200)
	response := f.json(t, "GET", fmt.Sprintf("/api/files/%d/download", bob.ID), f.bob, nil, 200)
	if response.Body.Len() != 0 {
		t.Fatal("other owner's empty bytes changed")
	}
	if replay := emptyComplete(t, f, f.alice, receiptUploadID(t, f, alice.ID)); replay.ID != alice.ID {
		t.Fatal("deleted zero receipt lost")
	}
	if got := quotaOverview(t, f, f.bob); got.UsedBytes != 0 || got.UnmeteredReferences != 0 {
		t.Fatal("verified zero reference not measured", got)
	}
}
func receiptUploadID(t *testing.T, f *fixture, fileID int64) string {
	t.Helper()
	var task model.UploadTask
	if err := f.db.Where("result_file_id=?", fileID).First(&task).Error; err != nil {
		t.Fatal(err)
	}
	return task.UploadID
}
func TestEmptyUploadSupportsVersionsCopyAndPublicDownload(t *testing.T) {
	f := newFixture(t)
	file := emptyComplete(t, f, f.alice, emptyInit(t, f, f.alice, "version-empty.txt"))
	f.json(t, "POST", fmt.Sprintf("/api/files/%d/versions/1/restore", file.ID), f.alice, map[string]any{"base_version": 1}, 200)
	response := f.json(t, "GET", fmt.Sprintf("/api/files/%d/versions/1/download", file.ID), f.alice, nil, 200)
	if response.Body.Len() != 0 {
		t.Fatal("empty history not empty")
	}
	f.json(t, "POST", "/api/files/copy", f.alice, map[string]any{"file_ids": []int64{file.ID}, "target_parent_id": 0}, 200)
	var share struct {
		Code string `json:"share_code"`
	}
	decode(t, f.json(t, "POST", fmt.Sprintf("/api/files/%d/share", file.ID), f.alice, map[string]any{}, 200), &share)
	if response := f.json(t, "GET", "/api/public/shares/"+share.Code+"/download", "", nil, 200); response.Body.Len() != 0 {
		t.Fatal("public zero download not empty")
	}
	if got := quotaOverview(t, f, f.alice); got.UsedBytes != 0 || got.HistoryBytes != 0 || got.UnmeteredReferences != 0 {
		t.Fatal("zero history/copy usage", got)
	}
	var storage model.Storage
	if err := f.db.First(&storage, *file.StorageID).Error; err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(storage.FilePath)
	if err != nil || stat.Size() != 0 || storage.FileHash != fmt.Sprintf("%x", sha256.Sum256(nil)) {
		t.Fatal("not a real verified empty object", storage.FileHash, err)
	}
}
func TestEmptyUploadCancelledExpiredAndInvalidZeroTasksStayRejected(t *testing.T) {
	f := newFixture(t)
	canceled := emptyInit(t, f, f.alice, "cancel-empty.bin")
	f.json(t, "POST", "/api/files/upload/cancel", f.alice, map[string]any{"upload_id": canceled}, 200)
	f.json(t, "POST", "/api/files/upload/complete", f.alice, map[string]any{"upload_id": canceled}, 409)
	expired := emptyInit(t, f, f.alice, "expired-empty.bin")
	if err := f.db.Model(&model.UploadTask{}).Where("upload_id=?", expired).Update("expires_at", gorm.Expr("DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 1 SECOND)")).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", "/api/files/upload/complete", f.alice, map[string]any{"upload_id": expired}, 409)
	for key, value := range map[string]any{"transfer_protocol": 0, "chunk_protocol": 0, "chunk_size": nil, "total_chunks": 1, "uploaded_chunks": 1} {
		id := emptyInit(t, f, f.alice, "invalid-"+key+".bin")
		if err := f.db.Model(&model.UploadTask{}).Where("upload_id=?", id).Update(key, value).Error; err != nil {
			t.Fatal(err)
		}
		f.json(t, "POST", "/api/files/upload/complete", f.alice, map[string]any{"upload_id": id}, 409)
		f.json(t, "GET", "/api/storage/overview", f.alice, nil, 503)
		f.json(t, "POST", "/api/files/upload/cancel", f.alice, map[string]any{"upload_id": id}, 200)
	}
	if got := quotaOverview(t, f, f.alice); got.UsedBytes != 0 || got.ReservedBytes != 0 {
		t.Fatal("canceled/expired zero usage", got)
	}
}
func TestEmptyUploadDoesNotIgnoreUnexpectedChunkMetadata(t *testing.T) {
	f := newFixture(t)
	id := emptyInit(t, f, f.alice, "unknown-chunk-empty.bin")
	path := filepath.Join(t.TempDir(), "legacy.part")
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	size := int64(4)
	row := model.UploadChunk{UploadID: id, ChunkIndex: 0, ChunkSize: &size, ChunkPath: path, Status: 0}
	if err := f.db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", "/api/files/upload/complete", f.alice, map[string]any{"upload_id": id}, 409)
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "keep" {
		t.Fatal("adopted/removed unexpected bytes", err)
	}
	var count int64
	if err := f.db.Model(&model.File{}).Where("user_id=?", f.aliceID).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("published despite unexpected metadata", count, err)
	}
}

func TestEmptyUploadDoesNotBypassBrokenQuotaOrTemporaryPolicy(t *testing.T) {
	f := newFixture(t)
	quotaSetting(t, f, "broken")
	f.json(t, "POST", "/api/files/upload/init", f.alice, map[string]any{"filename": "bad-quota-empty.bin", "total_size": 0, "chunk_size": 4096, "total_chunks": 0}, 503)
	quotaSetting(t, f, "4")
	id := emptyInit(t, f, f.alice, "bad-temp-empty.bin")
	m := transferManager(f)
	if err := m.SetLimit(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	audit := transferAudit(t, f)
	if err := f.db.Model(&model.TransferVolume{}).Where("id=?", audit.VolumeID).Update("limit_bytes", 0).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", "/api/files/upload/complete", f.alice, map[string]any{"upload_id": id}, 503)
	if task := receiptTask(t, f, id); task.Status != 0 || task.MergeArtifact != "" {
		t.Fatal("bad policy partially published zero", task)
	}
	if err := m.SetLimit(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	emptyComplete(t, f, f.alice, id)
}
