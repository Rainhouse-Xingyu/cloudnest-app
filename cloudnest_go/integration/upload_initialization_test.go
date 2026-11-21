//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"cloudenest_go/internal/app"
	"cloudenest_go/internal/service"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	"gorm.io/gorm"
)

func keyedUploadInit(f *fixture, token, key string, body map[string]any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/files/upload/init", bytes.NewReader(raw))
	req.RemoteAddr = f.remoteAddr
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}

func initializationResult(t *testing.T, w *httptest.ResponseRecorder) service.FileUploadInitResult {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("initialization failed: %d %s", w.Code, w.Body.String())
	}
	var result struct {
		UploadID       string `json:"upload_id"`
		TransferEpoch  int64  `json:"transfer_epoch"`
		Status         int8   `json:"status"`
		UploadedChunks []int  `json:"uploaded_chunks"`
		UploadedCount  int    `json:"uploaded_count"`
	}
	decode(t, w, &result)
	return service.FileUploadInitResult{UploadID: result.UploadID, TransferEpoch: result.TransferEpoch, Status: result.Status, UploadedChunks: result.UploadedChunks, UploadedCount: result.UploadedCount}
}

func TestUploadInitializationRecoversActualTCPResponseLoss(t *testing.T) {
	f := newFixture(t)
	body := map[string]any{"filename": "init-lost.bin", "total_size": 7, "chunk_size": 7, "total_chunks": 1}
	const key = "init-response-loss-test"
	var disconnected atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.router.ServeHTTP(disconnectReceiptWriter{w, &disconnected}, r)
	}))
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", server.URL+"/api/files/upload/init", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+f.alice)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	response, requestErr := server.Client().Do(req)
	if requestErr == nil {
		data, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr == nil && len(data) > 0 {
			t.Fatalf("response was not lost: %s", data)
		}
	}
	server.Close()
	if !disconnected.Load() {
		t.Fatal("request never reached the committed response")
	}
	id, err := contract.UploadInitializationID(f.aliceID, key)
	if err != nil {
		t.Fatal(err)
	}
	task := receiptTask(t, f, id)
	before := quotaLedger(t, f, f.aliceID)
	f.router = fixtureRouter(app.NewContainer(f.cfg, f.db, f.rdb).NewRouter())
	for range 3 {
		result := initializationResult(t, keyedUploadInit(f, f.alice, key, body))
		if result.UploadID != id || result.Status != 0 || result.TransferEpoch != 0 {
			t.Fatalf("lost initialization not recovered: %+v", result)
		}
	}
	if after := quotaLedger(t, f, f.aliceID); after.Revision != before.Revision || after.ReservedBytes != 7 {
		t.Fatal("retry allocated quota again", before, after)
	}
	if after := receiptTask(t, f, id); task.ExpiresAt == nil || after.ExpiresAt == nil || !task.ExpiresAt.Equal(*after.ExpiresAt) {
		t.Fatal("retry renewed reservation lifetime")
	}
	var audits int64
	if err := f.db.Model(&model.OperationLog{}).Where("user_id=? AND action='upload_reserve' AND detail LIKE ?", f.aliceID, "%"+id+"%").Count(&audits).Error; err != nil || audits != 1 {
		t.Fatal("retry repeated allocation audit", audits, err)
	}
}

func TestUploadInitializationConcurrentReplayAndChangedContract(t *testing.T) {
	f := newFixture(t)
	quotaSetting(t, f, "7") // The first request consumes all available quota.
	body := map[string]any{"filename": "init-concurrent.bin", "total_size": 7, "chunk_size": 7, "total_chunks": 1}
	const key = "parallel-initialization"
	results := make(chan *httptest.ResponseRecorder, 12)
	var group sync.WaitGroup
	for range 12 {
		group.Add(1)
		go func() {
			defer group.Done()
			results <- keyedUploadInit(f, f.alice, key, body)
		}()
	}
	group.Wait()
	close(results)
	id := ""
	for w := range results {
		result := initializationResult(t, w)
		if id != "" && id != result.UploadID {
			t.Fatal("concurrent initialization allocated different tasks")
		}
		id = result.UploadID
	}
	before := quotaLedger(t, f, f.aliceID)
	for _, change := range []map[string]any{
		{"filename": "different.bin"}, {"total_size": 8}, {"parent_id": int64(987654321)}, {"chunk_size": 1}, {"total_chunks": 2},
	} {
		changed := make(map[string]any)
		for k, v := range body {
			changed[k] = v
		}
		for k, v := range change {
			changed[k] = v
		}
		if w := keyedUploadInit(f, f.alice, key, changed); w.Code != 409 {
			t.Fatalf("changed contract accepted: %v: %d %s", change, w.Code, w.Body.String())
		}
	}
	var count int64
	if err := f.db.Model(&model.UploadTask{}).Where("user_id=?", f.aliceID).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("duplicate task allocation", count, err)
	}
	if after := quotaLedger(t, f, f.aliceID); after.Revision != before.Revision || after.ReservedBytes != 7 {
		t.Fatal("conflict mutated the ledger", before, after)
	}
	other := initializationResult(t, keyedUploadInit(f, f.bob, key, body))
	if other.UploadID == id {
		t.Fatal("initialization key leaked across owners")
	}
}

func TestUploadInitializationReplaysCurrentStateAndOriginalDefaults(t *testing.T) {
	f := newFixture(t)
	parent := fixtureFolder(t, f, f.bob, "initialization-parent", 0)
	if err := f.db.Create(&model.FilePermission{FileID: parent.ID, UserID: f.aliceID, Permission: "write"}).Error; err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"filename": "init-default.bin", "total_size": 7, "parent_id": parent.ID}
	const key = "default-initialization"
	result := initializationResult(t, keyedUploadInit(f, f.alice, key, body))
	f.json(t, "POST", "/api/files/upload/pause", f.alice, map[string]any{"upload_id": result.UploadID, "expected_epoch": 0}, 200)
	if err := f.db.Where("file_id=? AND user_id=?", parent.ID, f.aliceID).Delete(&model.FilePermission{}).Error; err != nil {
		t.Fatal(err)
	}
	// An acknowledgement cannot allocate or write in the now-inaccessible parent.
	quotaSetting(t, f, "broken-policy")
	var defaults []model.SystemSetting
	if err := f.db.Where("setting_key=?", "upload_default_chunk_size_mb").Find(&defaults).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.db.Where("setting_key=?", "upload_default_chunk_size_mb").Delete(&model.SystemSetting{}).Error; err != nil {
			t.Error(err)
		}
		if len(defaults) > 0 {
			if err := f.db.Create(&defaults).Error; err != nil {
				t.Error(err)
			}
		}
	})
	if err := f.db.Where("setting_key=?", "upload_default_chunk_size_mb").Delete(&model.SystemSetting{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&model.SystemSetting{SettingKey: "upload_default_chunk_size_mb", SettingValue: "999999999"}).Error; err != nil {
		t.Fatal(err)
	}
	paused := initializationResult(t, keyedUploadInit(f, f.alice, key, body))
	if paused.UploadID != result.UploadID || paused.Status != 2 || paused.TransferEpoch != 1 {
		t.Fatalf("stale init state: %+v", paused)
	}
	f.json(t, "POST", "/api/files/upload/cancel", f.alice, map[string]any{"upload_id": result.UploadID}, 200)
	cancelled := initializationResult(t, keyedUploadInit(f, f.alice, key, body))
	if cancelled.Status != 3 {
		t.Fatalf("cancelled task resurrected: %+v", cancelled)
	}
	newBody := map[string]any{"filename": "init-default.bin", "total_size": 7, "parent_id": parent.ID, "chunk_size": 7, "total_chunks": 1}
	if w := keyedUploadInit(f, f.alice, "different-key", newBody); w.Code != 403 {
		t.Fatalf("new allocation bypassed parent permission: %d %s", w.Code, w.Body.String())
	}
	if err := f.db.Model(&model.User{}).Where("id=?", f.aliceID).Update("status", 0).Error; err != nil {
		t.Fatal(err)
	}
	if w := keyedUploadInit(f, f.alice, key, body); w.Code != 401 {
		t.Fatal("replay bypassed current authentication", w.Code)
	}
}

func TestUploadInitializationCompletedReceiptAndHeaderValidation(t *testing.T) {
	f := newFixture(t)
	body := map[string]any{"filename": "init-empty.bin", "total_size": 0}
	const key = "empty-initialization"
	result := initializationResult(t, keyedUploadInit(f, f.alice, key, body))
	complete := completeUploadRecorder(f, f.alice, result.UploadID)
	file := completedBlob(t, f, complete)
	waitNoReadLeases(t, f, *file.StorageID)
	before := fmt.Sprint(receiptSideEffects(t, f, file.ID))
	completed := initializationResult(t, keyedUploadInit(f, f.alice, key, body))
	if completed.Status != 1 || completed.UploadID != result.UploadID {
		t.Fatalf("completed init recreated task: %+v", completed)
	}
	assertReceiptReply(t, f, result.UploadID, complete)
	if after := fmt.Sprint(receiptSideEffects(t, f, file.ID)); after != before {
		t.Fatal("init replay repeated publication side effects", before, after)
	}
	for _, badKey := range []string{"", "contains space", "comma,key", "non-ascii-中", strings.Repeat("a", 129)} {
		if w := keyedUploadInit(f, f.alice, badKey, body); w.Code != 400 {
			t.Fatal("invalid key accepted", badKey, w.Code)
		}
	}
	raw, _ := json.Marshal(body)
	duplicateHeader := httptest.NewRequest("POST", "/api/files/upload/init", bytes.NewReader(raw))
	duplicateHeader.RemoteAddr = f.remoteAddr
	duplicateHeader.Header.Set("Authorization", "Bearer "+f.alice)
	duplicateHeader.Header.Set("Content-Type", "application/json")
	duplicateHeader.Header.Add("Idempotency-Key", key)
	duplicateHeader.Header.Add("Idempotency-Key", key)
	duplicateReply := httptest.NewRecorder()
	f.router.ServeHTTP(duplicateReply, duplicateHeader)
	if duplicateReply.Code != 400 {
		t.Fatal("multiple initialization keys accepted", duplicateReply.Code)
	}
	withID := map[string]any{"filename": "init-empty.bin", "total_size": 0, "upload_id": result.UploadID}
	if w := keyedUploadInit(f, f.alice, key, withID); w.Code != 400 {
		t.Fatal("ambiguous key and upload_id accepted", w.Code)
	}
	// Legacy requests keep their original allocation and explicit-ID semantics.
	legacy := initializationResult(t, quotaJSON(f, "POST", "/api/files/upload/init", f.alice, body))
	legacyAgain := initializationResult(t, quotaJSON(f, "POST", "/api/files/upload/init", f.alice, body))
	if legacy.UploadID == legacyAgain.UploadID {
		t.Fatal("legacy independent initializations were deduplicated")
	}
	f.json(t, "POST", "/api/files/upload/init", f.alice, map[string]any{"filename": "init-empty.bin", "total_size": 0, "upload_id": "unknown-upload-id"}, 404)
	// Expired terminal records preserve their identity rather than reserving again.
	const expiredKey = "expired-initialization"
	expiredTask := initializationResult(t, keyedUploadInit(f, f.alice, expiredKey, body))
	if err := f.db.Model(&model.UploadTask{}).Where("upload_id=?", expiredTask.UploadID).Updates(map[string]any{"status": 4, "expires_at": gorm.Expr("DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 1 DAY)")}).Error; err != nil {
		t.Fatal(err)
	}
	if expired := initializationResult(t, keyedUploadInit(f, f.alice, expiredKey, body)); expired.Status != 4 || expired.UploadID != expiredTask.UploadID {
		t.Fatal("expired initialization was renewed", expired)
	}
}
