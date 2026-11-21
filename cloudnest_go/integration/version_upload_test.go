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
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/internal/store"
	"cloudenest_go/model"
	"gorm.io/gorm"
)

func initVersionUpload(f *fixture, token string, fileID int64, key string, input map[string]any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(input)
	request := httptest.NewRequest("POST", fmt.Sprintf("/api/files/%d/versions/upload", fileID), bytes.NewReader(raw))
	request.RemoteAddr = f.remoteAddr
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	f.router.ServeHTTP(response, request)
	return response
}

func TestVersionUploadPublishesBoundTargetAndReplaysOriginalReceipt(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "version-target.txt", []byte("original"))
	parent := fixtureFolder(t, f, f.alice, "version-target-folder", 0)
	f.json(t, "POST", "/api/files/move", f.alice, map[string]any{"file_ids": []int64{file.ID}, "target_parent_id": parent.ID}, 200)
	file.ParentID = parent.ID
	payload := []byte("replacement")
	quotaSetting(t, f, fmt.Sprint(len("original")+len(payload)))
	input := map[string]any{"filename": "local-source.txt", "total_size": len(payload), "chunk_size": len(payload), "total_chunks": 1, "base_version": 1, "comment": "Native uploaded version"}
	result := initializationResult(t, initVersionUpload(f, f.alice, file.ID, "version-upload-one", input))
	controlChunk(t, f, result.UploadID, payload, 0, 200)
	response := completeUploadRecorder(f, f.alice, result.UploadID)
	updated := versionUploadReceipt(t, f, response)
	if updated.ID != file.ID || updated.CurrentVersion != 2 || updated.Filename != file.Filename || updated.UserID != f.aliceID || updated.ParentID != parent.ID {
		t.Fatalf("version upload created or changed the wrong file: %+v", updated)
	}
	replay := completeUploadRecorder(f, f.alice, result.UploadID)
	if replay.Code != 200 || replay.Body.String() != response.Body.String() {
		t.Fatal("version receipt did not replay", replay.Code, replay.Body.String())
	}
	download := f.json(t, "GET", fmt.Sprintf("/api/files/%d/download", file.ID), f.alice, nil, 200)
	if !bytes.Equal(download.Body.Bytes(), payload) {
		t.Fatal("version bytes differ")
	}
	historical := f.json(t, "GET", fmt.Sprintf("/api/files/%d/versions/1/download", file.ID), f.alice, nil, 200)
	if string(historical.Body.Bytes()) != "original" {
		t.Fatal("original history was lost")
	}
	var count int64
	if err := f.db.Table("file").Where("user_id=? AND is_folder=0", f.aliceID).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("version upload left a staging file", count, err)
	}
}

func versionUploadReceipt(t *testing.T, f *fixture, response *httptest.ResponseRecorder) model.File {
	t.Helper()
	if response.Code != 200 {
		t.Fatal("version completion", response.Code, response.Body.String())
	}
	var result struct {
		Item model.File `json:"item"`
	}
	decode(t, response, &result)
	var job model.AIIndexJob
	if err := f.db.First(&job, "file_id=?", result.Item.ID).Error; err != nil || job.Status != "queued" || job.Generation != 2 {
		t.Fatal("version did not atomically advance index", job, err)
	}
	var storage model.Storage
	if result.Item.StorageID == nil {
		t.Fatal("version receipt missing source")
	}
	if err := f.db.First(&storage, *result.Item.StorageID).Error; err != nil || job.SourceHash != storage.FileHash {
		t.Fatal("index queued the wrong source", err)
	}
	return result.Item
}

func versionChunk(t *testing.T, f *fixture, token, id string, payload []byte, epoch int64, want int) {
	t.Helper()
	body, ct := chunkBody(t, id, 0, payload, "")
	request := httptest.NewRequest("POST", "/api/files/upload/chunk", body)
	request.RemoteAddr = f.remoteAddr
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", ct)
	request.Header.Set("X-Upload-Epoch", fmt.Sprint(epoch))
	response := httptest.NewRecorder()
	f.router.ServeHTTP(response, request)
	if response.Code != want {
		t.Fatal("version chunk", response.Code, response.Body.String())
	}
}

func versionUploadInput(base int, size int) map[string]any {
	return map[string]any{"filename": "version-source.txt", "total_size": size, "chunk_size": max(1, size), "total_chunks": min(1, size), "base_version": base, "comment": "Version upload fixture"}
}

func TestVersionUploadCollaboratorUsesTargetQuotaAndCannotReuseStorageCapability(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "owner-version.txt", []byte("original"))
	other := f.upload(t, f.bob, "other-target.txt", []byte("B"))
	payload := []byte("replacement")
	input := versionUploadInput(1, len(payload))
	if response := initVersionUpload(f, f.bob, file.ID, "not-authorized", input); response.Code != 403 {
		t.Fatal("cross-user initialization", response.Code)
	}
	path := fmt.Sprintf("/api/files/%d/collaborators", file.ID)
	f.json(t, "POST", path, f.alice, map[string]any{"user_id": f.bobID, "permission": "read"}, 200)
	if response := initVersionUpload(f, f.bob, file.ID, "read-only", input); response.Code != 403 {
		t.Fatal("reader initialized a version", response.Code)
	}
	f.json(t, "POST", path, f.alice, map[string]any{"user_id": f.bobID, "permission": "write"}, 200)
	quotaSetting(t, f, "19")
	id := initializationResult(t, initVersionUpload(f, f.bob, file.ID, "collaborator-version", input)).UploadID
	owner := quotaOverview(t, f, f.alice)
	actor := quotaOverview(t, f, f.bob)
	if owner.UsedBytes != 8 || owner.ReservedBytes != 11 || actor.UsedBytes != 1 || actor.ReservedBytes != 0 {
		t.Fatal("reservation charged the wrong identity", owner, actor)
	}
	versionChunk(t, f, f.alice, id, payload, 0, 403)
	f.json(t, "GET", "/api/files/upload/progress?upload_id="+id, f.alice, nil, 404)
	versionChunk(t, f, f.bob, id, payload, 0, 200)
	updated := versionUploadReceipt(t, f, completeUploadRecorder(f, f.bob, id))
	if updated.ID != file.ID || updated.UserID != f.aliceID || updated.CurrentVersion != 2 {
		t.Fatal("wrong version publication", updated)
	}
	owner = quotaOverview(t, f, f.alice)
	actor = quotaOverview(t, f, f.bob)
	if owner.UsedBytes != 19 || owner.ReservedBytes != 0 || actor.UsedBytes != 1 || actor.ReservedBytes != 0 {
		t.Fatal("publication charged the wrong identity", owner, actor)
	}
	// Knowing a completion's storage ID is not authority over its retained object.
	f.json(t, "POST", fmt.Sprintf("/api/files/%d/versions", other.ID), f.bob, map[string]any{"storage_id": *updated.StorageID, "base_version": 1}, 403)
	f.json(t, "DELETE", path+fmt.Sprintf("/%d", f.bobID), f.alice, nil, 200)
	if replay := completeUploadRecorder(f, f.bob, id); replay.Code != 200 {
		t.Fatal("owned acknowledgement became an ACL-dependent publication", replay.Code)
	}
	f.json(t, "GET", fmt.Sprintf("/api/files/%d/download", file.ID), f.bob, nil, 403)
}

func TestVersionUploadRechecksRevocationAndAllowsCancellation(t *testing.T) {
	for _, afterChunk := range []bool{false, true} {
		t.Run(fmt.Sprint(afterChunk), func(t *testing.T) {
			f := newFixture(t)
			file := f.upload(t, f.alice, "revoked-version.txt", []byte("original"))
			path := fmt.Sprintf("/api/files/%d/collaborators", file.ID)
			f.json(t, "POST", path, f.alice, map[string]any{"user_id": f.bobID, "permission": "write"}, 200)
			payload := []byte("replacement")
			id := initializationResult(t, initVersionUpload(f, f.bob, file.ID, "revocation-version", versionUploadInput(1, len(payload)))).UploadID
			if afterChunk {
				versionChunk(t, f, f.bob, id, payload, 0, 200)
			}
			f.json(t, "DELETE", path+fmt.Sprintf("/%d", f.bobID), f.alice, nil, 200)
			versionChunk(t, f, f.bob, id, payload, 0, 403)
			if response := completeUploadRecorder(f, f.bob, id); response.Code != 403 {
				t.Fatal("revoked version completion", response.Code, response.Body.String())
			}
			f.json(t, "POST", "/api/files/upload/pause", f.bob, map[string]any{"upload_id": id, "expected_epoch": 0}, 200)
			f.json(t, "POST", "/api/files/upload/resume", f.bob, map[string]any{"upload_id": id, "expected_epoch": 1}, 403)
			f.json(t, "POST", "/api/files/upload/cancel", f.bob, map[string]any{"upload_id": id}, 200)
			if usage := quotaOverview(t, f, f.alice); usage.UsedBytes != 8 || usage.ReservedBytes != 0 {
				t.Fatal("revoked reservation was not released", usage)
			}
			var current model.File
			if err := f.db.First(&current, file.ID).Error; err != nil || current.CurrentVersion != 1 || *current.StorageID != *file.StorageID {
				t.Fatal("revoked upload changed target", current, err)
			}
		})
	}
}

func TestVersionUploadFreezesBindingAndInitializationReplay(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "frozen-version.txt", []byte("original"))
	other := f.upload(t, f.alice, "other-version.txt", []byte("other"))
	input := versionUploadInput(1, 7)
	id := initializationResult(t, initVersionUpload(f, f.alice, file.ID, "frozen-version", input)).UploadID
	before := quotaLedger(t, f, f.aliceID)
	for range 3 {
		if replay := initializationResult(t, initVersionUpload(f, f.alice, file.ID, "frozen-version", input)); replay.UploadID != id {
			t.Fatal("initialization allocated again")
		}
	}
	for _, change := range []map[string]any{{"base_version": 2}, {"comment": "different"}, {"filename": "other.txt"}, {"total_size": 8}, {"chunk_size": 4}, {"total_chunks": 2}} {
		changed := map[string]any{}
		for k, v := range input {
			changed[k] = v
		}
		for k, v := range change {
			changed[k] = v
		}
		if response := initVersionUpload(f, f.alice, file.ID, "frozen-version", changed); response.Code != 409 {
			t.Fatal("changed bound contract", change, response.Code, response.Body.String())
		}
	}
	if response := initVersionUpload(f, f.alice, other.ID, "frozen-version", input); response.Code != 409 {
		t.Fatal("changed target was accepted", response.Code)
	}
	ordinary := map[string]any{"filename": "version-source.txt", "total_size": 7, "chunk_size": 7, "total_chunks": 1}
	if response := keyedUploadInit(f, f.alice, "frozen-version", ordinary); response.Code != 409 {
		t.Fatal("ordinary initializer adopted version ticket", response.Code)
	}
	for _, field := range []string{"storage_id", "quota_user_id", "parent_id", "upload_id"} {
		changed := map[string]any{}
		for k, v := range input {
			changed[k] = v
		}
		changed[field] = int64(123)
		if response := initVersionUpload(f, f.alice, file.ID, "forged-"+field, changed); response.Code != 400 {
			t.Fatal("authority field was accepted", field, response.Code)
		}
	}
	if after := quotaLedger(t, f, f.aliceID); after.Revision != before.Revision || after.ReservedBytes != 7 {
		t.Fatal("replays changed reservation", before, after)
	}
}

func TestVersionUploadConcurrentPublicationPreservesUnseenCurrentVersion(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "racing-version.txt", []byte("original"))
	ids := []string{}
	for i := range 2 {
		id := initializationResult(t, initVersionUpload(f, f.alice, file.ID, fmt.Sprintf("racing-version-%d", i), versionUploadInput(1, 7))).UploadID
		versionChunk(t, f, f.alice, id, bytes.Repeat([]byte{byte('A' + i)}, 7), 0, 200)
		ids = append(ids, id)
	}
	results := make(chan *httptest.ResponseRecorder, 2)
	var group sync.WaitGroup
	for _, id := range ids {
		group.Add(1)
		go func() { defer group.Done(); results <- completeUploadRecorder(f, f.alice, id) }()
	}
	group.Wait()
	close(results)
	accepted, conflicted := 0, 0
	for response := range results {
		switch response.Code {
		case 200:
			accepted++
			versionUploadReceipt(t, f, response)
		case 409:
			conflicted++
		default:
			t.Fatal("concurrent version publication", response.Code, response.Body.String())
		}
	}
	if accepted != 1 || conflicted != 1 {
		t.Fatal("unseen update overwritten", accepted, conflicted)
	}
	var count int64
	if err := f.db.Table("file_version").Where("file_id=?", file.ID).Count(&count).Error; err != nil || count != 2 {
		t.Fatal("history advanced twice", count, err)
	}
	for _, id := range ids {
		task := receiptTask(t, f, id)
		if task.Status == 0 {
			f.json(t, "POST", "/api/files/upload/cancel", f.alice, map[string]any{"upload_id": id}, 200)
		}
	}
	if usage := quotaOverview(t, f, f.alice); usage.UsedBytes != 15 || usage.ReservedBytes != 0 {
		t.Fatal("conflict reservation accounting", usage)
	}
}

func TestVersionUploadAuditFailureRollsBackTargetIndexReceiptAndQuota(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "atomic-version.txt", []byte("original"))
	payload := []byte("replacement")
	id := initializationResult(t, initVersionUpload(f, f.alice, file.ID, "atomic-version", versionUploadInput(1, len(payload)))).UploadID
	versionChunk(t, f, f.alice, id, payload, 0, 200)
	for _, stage := range []string{"file_version_upload", "quota_accounting"} {
		const hook = "test:version-upload-atomicity"
		if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
			if row, ok := tx.Statement.Dest.(*model.OperationLog); ok && row.Action == stage {
				tx.AddError(errors.New("injected version audit failure"))
			}
		}); err != nil {
			t.Fatal(err)
		}
		response := completeUploadRecorder(f, f.alice, id)
		f.db.Callback().Create().Remove(hook)
		if response.Code != 500 {
			t.Fatal("injected audit failure", stage, response.Code, response.Body.String())
		}
		var current model.File
		var job model.AIIndexJob
		if err := f.db.First(&current, file.ID).Error; err != nil || current.CurrentVersion != 1 || *current.StorageID != *file.StorageID {
			t.Fatal("target escaped rollback", current, err)
		}
		if err := f.db.First(&job, "file_id=?", file.ID).Error; err != nil || job.Generation != 1 {
			t.Fatal("index escaped rollback", job, err)
		}
		task := receiptTask(t, f, id)
		if task.Status != 0 || task.ResultJSON != nil || task.ReservedBytes != int64(len(payload)) {
			t.Fatal("receipt escaped rollback", task)
		}
		if usage := quotaOverview(t, f, f.alice); usage.UsedBytes != 8 || usage.ReservedBytes != int64(len(payload)) {
			t.Fatal("quota escaped rollback", usage)
		}
	}
	versionUploadReceipt(t, f, completeUploadRecorder(f, f.alice, id))
}

func TestVersionUploadSchemaRequiresExplicitAdditiveUpgrade(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "upgrade-version.txt", []byte("original"))
	ordinary := quotaInit(t, f, f.alice, "legacy-pending.txt", 4)
	before := receiptTask(t, f, ordinary)
	if err := f.db.Exec("ALTER TABLE upload_task DROP INDEX idx_version_upload_quota, DROP INDEX idx_version_upload_target, DROP COLUMN version_file_id, DROP COLUMN version_base, DROP COLUMN version_comment, DROP COLUMN quota_user_id").Error; err != nil {
		t.Fatal(err)
	}
	if err := store.CheckFileSchema(f.db); err == nil {
		t.Fatal("old schema accepted without explicit upgrade")
	}
	if err := store.Migrate(f.db); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckFileSchema(f.db); err != nil {
		t.Fatal(err)
	}
	after := receiptTask(t, f, ordinary)
	if after.ID != before.ID || after.ContractVersion != 1 || after.VersionFileID != nil || after.QuotaUserID != nil || after.VersionBase != 0 || after.VersionComment != "" || after.Status != before.Status || after.ReservedBytes != before.ReservedBytes {
		t.Fatal("upgrade rewrote an ordinary task", before, after)
	}
	initializationResult(t, initVersionUpload(f, f.alice, file.ID, "after-upgrade", versionUploadInput(1, 7)))
}

func TestVersionUploadStaleBaseBeforeBodyAndCompletionCannotOverwrite(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "stale-version.txt", []byte("original"))
	payload := []byte("unseen replacement")
	id := initializationResult(t, initVersionUpload(f, f.alice, file.ID, "stale-version", versionUploadInput(1, len(payload)))).UploadID
	versionChunk(t, f, f.alice, id, payload, 0, 200)
	f.json(t, "POST", fmt.Sprintf("/api/files/%d/versions/1/restore", file.ID), f.alice, map[string]int{"base_version": 1}, 200)
	versionChunk(t, f, f.alice, id, payload, 0, 409)
	if response := completeUploadRecorder(f, f.alice, id); response.Code != 409 {
		t.Fatal("stale preflight must conflict", response.Code, response.Body.String())
	}
	f.json(t, "POST", "/api/files/upload/cancel", f.alice, map[string]any{"upload_id": id}, 200)
	var current model.File
	if err := f.db.First(&current, file.ID).Error; err != nil || current.CurrentVersion != 2 || *current.StorageID != *file.StorageID {
		t.Fatal("stale upload replaced unseen version", current, err)
	}
}

func TestVersionUploadPauseResumeEmptyAndOriginalReceiptAfterLaterChanges(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "pause-empty-version.txt", []byte("original"))
	input := versionUploadInput(1, 0)
	id := initializationResult(t, initVersionUpload(f, f.alice, file.ID, "empty-version", input)).UploadID
	control(t, f, id, 0, true, 200)
	if result := initializationResult(t, initVersionUpload(f, f.alice, file.ID, "empty-version", input)); result.Status != 2 || result.TransferEpoch != 1 {
		t.Fatal("initialization lost paused state", result)
	}
	control(t, f, id, 1, false, 200)
	request := httptest.NewRequest("POST", "/api/files/upload/complete", bytes.NewBufferString(fmt.Sprintf(`{"upload_id":%q}`, id)))
	request.RemoteAddr = f.remoteAddr
	request.Header.Set("Authorization", "Bearer "+f.alice)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Upload-Epoch", "2")
	response := httptest.NewRecorder()
	f.router.ServeHTTP(response, request)
	updated := versionUploadReceipt(t, f, response)
	if updated.Size != 0 || updated.ID != file.ID || updated.CurrentVersion != 2 {
		t.Fatal("empty version invalid", updated)
	}
	f.json(t, "POST", fmt.Sprintf("/api/files/%d/versions/1/restore", file.ID), f.alice, map[string]int{"base_version": 2}, 200)
	replay := completeUploadRecorder(f, f.alice, id)
	if replay.Code != 200 || replay.Body.String() != response.Body.String() {
		t.Fatal("later versions changed the old receipt", replay.Code, replay.Body.String())
	}
	if result := initializationResult(t, initVersionUpload(f, f.alice, file.ID, "empty-version", input)); result.Status != 1 {
		t.Fatal("completed initialization was reallocated", result)
	}
}

func TestVersionUploadActualLostInitializationAndCompletionRecoverAcrossContainers(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "lost-version.txt", []byte("original"))
	payload := []byte("response loss version")
	input := versionUploadInput(1, len(payload))
	const key = "lost-version-request"
	lost := func(path string, body any, key string) {
		t.Helper()
		var disconnected atomic.Bool
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			f.router.ServeHTTP(disconnectReceiptWriter{w, &disconnected}, r)
		}))
		raw, _ := json.Marshal(body)
		request, err := http.NewRequest("POST", server.URL+path, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+f.alice)
		request.Header.Set("Content-Type", "application/json")
		if key != "" {
			request.Header.Set("Idempotency-Key", key)
		}
		response, err := server.Client().Do(request)
		if err == nil {
			data, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr == nil && len(data) > 0 {
				t.Fatal("receipt was not lost")
			}
		}
		server.Close()
		if !disconnected.Load() {
			t.Fatal("request did not commit before losing response")
		}
	}
	lost(fmt.Sprintf("/api/files/%d/versions/upload", file.ID), input, key)
	id, err := contract.UploadInitializationID(f.aliceID, key)
	if err != nil {
		t.Fatal(err)
	}
	before := receiptTask(t, f, id)
	f.router = fixtureRouter(app.NewContainer(f.cfg, f.db, f.rdb).NewRouter())
	result := initializationResult(t, initVersionUpload(f, f.alice, file.ID, key, input))
	after := receiptTask(t, f, id)
	if result.UploadID != id || before.ExpiresAt == nil || after.ExpiresAt == nil || !before.ExpiresAt.Equal(*after.ExpiresAt) {
		t.Fatal("initialization loss created or renewed a task")
	}
	versionChunk(t, f, f.alice, id, payload, 0, 200)
	lost("/api/files/upload/complete", map[string]any{"upload_id": id}, "")
	f.router = fixtureRouter(app.NewContainer(f.cfg, f.db, f.rdb).NewRouter())
	updated := versionUploadReceipt(t, f, completeUploadRecorder(f, f.alice, id))
	if updated.ID != file.ID || updated.CurrentVersion != 2 {
		t.Fatal("completion loss appended twice", updated)
	}
	var count int64
	if err := f.db.Table("file_version").Where("file_id=?", file.ID).Count(&count).Error; err != nil || count != 2 {
		t.Fatal("response retry changed history", count, err)
	}
}

func cancelInitialization(f *fixture, token, key string) *httptest.ResponseRecorder {
	request := httptest.NewRequest("POST", "/api/files/upload/cancel-initialization", bytes.NewBufferString("{}"))
	request.RemoteAddr = f.remoteAddr
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	f.router.ServeHTTP(response, request)
	return response
}

func TestVersionUploadCancellationBeforeAllocationFencesLateInitializer(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "fenced-version.txt", []byte("original"))
	input := versionUploadInput(1, 7)
	const key = "late-version-initialization"
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	const hook = "test:late-version-initialization"
	if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if task, ok := tx.Statement.Dest.(*model.UploadTask); ok && task.ContractVersion == 2 {
			once.Do(func() { close(entered) })
			select {
			case <-release:
			case <-time.After(10 * time.Second):
				tx.AddError(errors.New("late initializer fixture timed out"))
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer f.db.Callback().Create().Remove(hook)
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	pending := make(chan *httptest.ResponseRecorder, 1)
	go func() { pending <- initVersionUpload(f, f.alice, file.ID, key, input) }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("initialization did not reach allocation")
	}
	if response := cancelInitialization(f, f.alice, key); response.Code != 200 {
		t.Fatal("pre-allocation cancellation failed", response.Code, response.Body.String())
	}
	unblock()
	select {
	case response := <-pending:
		if response.Code != 409 {
			t.Fatal("late initialization escaped cancellation", response.Code, response.Body.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("late initialization did not settle")
	}
	if response := initVersionUpload(f, f.alice, file.ID, key, input); response.Code != 409 {
		t.Fatal("canceled initialization key was reusable", response.Code)
	}
	ordinary := map[string]any{"filename": "fenced.txt", "total_size": 7, "chunk_size": 7, "total_chunks": 1}
	if response := keyedUploadInit(f, f.alice, key, ordinary); response.Code != 409 {
		t.Fatal("canceled key escaped through ordinary initializer", response.Code)
	}
	other := initializationResult(t, keyedUploadInit(f, f.bob, key, ordinary))
	f.json(t, "GET", "/api/files/upload/progress?upload_id="+other.UploadID, f.bob, nil, 200)
	if usage := quotaOverview(t, f, f.alice); usage.UsedBytes != 8 || usage.ReservedBytes != 0 {
		t.Fatal("canceled initializer charged quota", usage)
	}
}

func TestVersionUploadCancellationByKeyReleasesOwnerOrReturnsCommittedReceipt(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "cancel-key-version.txt", []byte("original"))
	f.json(t, "POST", fmt.Sprintf("/api/files/%d/collaborators", file.ID), f.alice, map[string]any{"user_id": f.bobID, "permission": "write"}, 200)
	input := versionUploadInput(1, 7)
	first := initializationResult(t, initVersionUpload(f, f.bob, file.ID, "key-before-completion", input))
	if response := cancelInitialization(f, f.bob, "key-before-completion"); response.Code != 200 {
		t.Fatal("key cancellation failed", response.Code)
	}
	if task := receiptTask(t, f, first.UploadID); task.Status != 3 || task.ReservedBytes != 0 {
		t.Fatal("key cancellation left reservation", task)
	}
	if usage := quotaOverview(t, f, f.alice); usage.ReservedBytes != 0 {
		t.Fatal("key cancellation charged actor instead of owner", usage)
	}
	second := initializationResult(t, initVersionUpload(f, f.bob, file.ID, "key-after-completion", input))
	versionChunk(t, f, f.bob, second.UploadID, []byte("payload"), 0, 200)
	expected := versionUploadReceipt(t, f, completeUploadRecorder(f, f.bob, second.UploadID))
	var result struct {
		Status int        `json:"status"`
		Item   model.File `json:"item"`
	}
	response := cancelInitialization(f, f.bob, "key-after-completion")
	if response.Code != 200 {
		t.Fatal("committed key cancellation", response.Code, response.Body.String())
	}
	decode(t, response, &result)
	if result.Status != 1 || result.Item.ID != expected.ID || result.Item.CurrentVersion != 2 {
		t.Fatal("completed key was misreported as canceled", result)
	}
}

func TestVersionUploadConcurrentActorsShareOneOwnerReservationBudget(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "shared-budget-version.txt", []byte("original"))
	f.json(t, "POST", fmt.Sprintf("/api/files/%d/collaborators", file.ID), f.alice, map[string]any{"user_id": f.bobID, "permission": "write"}, 200)
	quotaSetting(t, f, "15")
	type result struct {
		response *httptest.ResponseRecorder
		token    string
	}
	results := make(chan result, 12)
	var group sync.WaitGroup
	for index := range 12 {
		group.Add(1)
		go func() {
			defer group.Done()
			token := f.alice
			if index%2 == 1 {
				token = f.bob
			}
			results <- result{initVersionUpload(f, token, file.ID, fmt.Sprintf("shared-budget-%d", index), versionUploadInput(1, 7)), token}
		}()
	}
	group.Wait()
	close(results)
	accepted := 0
	var winningID, winningToken string
	for result := range results {
		switch result.response.Code {
		case 200:
			accepted++
			winningID = initializationResult(t, result.response).UploadID
			winningToken = result.token
		case 507:
		default:
			t.Fatal("concurrent cross-actor allocation", result.response.Code, result.response.Body.String())
		}
	}
	if accepted != 1 {
		t.Fatal("owner quota oversubscribed", accepted)
	}
	if usage := quotaOverview(t, f, f.alice); usage.UsedBytes != 8 || usage.ReservedBytes != 7 {
		t.Fatal("owner reservation missing", usage)
	}
	if usage := quotaOverview(t, f, f.bob); usage.UsedBytes != 0 || usage.ReservedBytes != 0 {
		t.Fatal("actor got charged target reservation", usage)
	}
	f.json(t, "POST", "/api/files/upload/cancel", winningToken, map[string]any{"upload_id": winningID}, 200)
	if usage := quotaOverview(t, f, f.alice); usage.ReservedBytes != 0 {
		t.Fatal("owner reservation not released", usage)
	}
}

func TestVersionUploadCommentFitsTheExistingHistoryColumn(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "comment-boundary.txt", []byte("original"))
	input := versionUploadInput(1, 7)
	input["comment"] = strings.Repeat("界", 256)
	if response := initVersionUpload(f, f.alice, file.ID, "too-long-version-comment", input); response.Code != 400 {
		t.Fatal("comment beyond existing history capacity was allocated", response.Code, response.Body.String())
	}
	input["comment"] = strings.Repeat("界", 255)
	id := initializationResult(t, initVersionUpload(f, f.alice, file.ID, "maximum-version-comment", input)).UploadID
	versionChunk(t, f, f.alice, id, []byte("payload"), 0, 200)
	versionUploadReceipt(t, f, completeUploadRecorder(f, f.alice, id))
	var version model.FileVersion
	if err := f.db.First(&version, "file_id=? AND version=2", file.ID).Error; err != nil || version.Comment != input["comment"] {
		t.Fatal("maximum comment was truncated", err)
	}
}
