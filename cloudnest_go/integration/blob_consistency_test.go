//go:build integration

package integration

import (
	"bytes"
	"cloudenest_go/internal/store"
	"crypto/md5"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cloudenest_go/model"
	"gorm.io/gorm"
)

func fixturePendingUpload(t *testing.T, f *fixture, token, name string, payload []byte) string {
	t.Helper()
	w := f.json(t, "POST", "/api/files/upload/init", token, map[string]any{"filename": name, "total_size": len(payload), "chunk_size": len(payload), "total_chunks": 1}, http.StatusOK)
	var init struct {
		UploadID string `json:"upload_id"`
	}
	decode(t, w, &init)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for key, value := range map[string]string{"upload_id": init.UploadID, "chunk_index": "0", "chunk_md5": fmt.Sprintf("%x", md5.Sum(payload))} {
		if err := mw.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	part, err := mw.CreateFormFile("chunk", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	f.request(t, "POST", "/api/files/upload/chunk", token, mw.FormDataContentType(), &body, http.StatusOK)
	return init.UploadID
}
func completeUploadRecorder(f *fixture, token, id string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]any{"upload_id": id})
	req := httptest.NewRequest("POST", "/api/files/upload/complete", bytes.NewReader(body))
	req.RemoteAddr = f.remoteAddr
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}
func completedBlob(t *testing.T, f *fixture, w *httptest.ResponseRecorder) model.File {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("complete status %d body=%s", w.Code, w.Body.String())
	}
	var result struct {
		Item model.File `json:"item"`
	}
	decode(t, w, &result)
	f.waitAnalysis(t, result.Item.ID)
	return result.Item
}

func TestConcurrentUploadCompletionCannotDeletePublishedBytes(t *testing.T) {
	f := newFixture(t)
	payload := []byte("bytes that must outlive concurrent completion")
	id := fixturePendingUpload(t, f, f.alice, "concurrent-final.bin", payload)
	paused := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var seen atomic.Int32
	const hook = "test:pause_first_namespace_before_write"
	if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if row, ok := tx.Statement.Dest.(*model.FileNamespace); ok && row.Key == fmt.Sprintf("root:%d", f.aliceID) && seen.Add(1) == 1 {
			close(paused)
			<-release
		}
	}); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- completeUploadRecorder(f, f.alice, id) }()
	defer func() { once.Do(func() { close(release) }); f.db.Callback().Create().Remove(hook) }()
	select {
	case <-paused:
	case <-time.After(10 * time.Second):
		t.Fatal("first request did not reach namespace write")
	}
	second := completeUploadRecorder(f, f.alice, id)
	once.Do(func() { close(release) })
	first := <-done
	var successful []model.File
	for _, w := range []*httptest.ResponseRecorder{second, first} {
		if w.Code == http.StatusOK {
			successful = append(successful, completedBlob(t, f, w))
		}
	}
	if second.Code != http.StatusOK || first.Code != http.StatusConflict {
		t.Errorf("completion statuses second=%d first=%d, want 200/409", second.Code, first.Code)
	}
	if len(successful) == 0 {
		t.Fatal("neither completion published a file")
	}
	winner := successful[0]

	var count int64
	if err := f.db.Model(&model.File{}).Where("user_id=? AND filename LIKE ?", f.aliceID, "concurrent-final%").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("duplicate logical files: %d", count)
	}
	w := f.json(t, "GET", fmt.Sprintf("/api/files/%d/download", winner.ID), f.alice, nil, http.StatusOK)
	if !bytes.Equal(w.Body.Bytes(), payload) {
		t.Fatal("concurrent completion changed published bytes")
	}
}

func TestConcurrentDedupAcrossUsersPublishesOneObject(t *testing.T) {
	f := newFixture(t)
	payload := []byte("concurrent cross-owner deduplicated object")
	hash := fmt.Sprintf("%x", sha256.Sum256(payload))
	a := fixturePendingUpload(t, f, f.alice, "dedup-a.bin", payload)
	b := fixturePendingUpload(t, f, f.bob, "dedup-b.bin", payload)
	gate := make(chan struct{})
	var seen atomic.Int32
	const hook = "test:concurrent_storage_insert"
	if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if row, ok := tx.Statement.Dest.(*model.Storage); ok && row.FileHash == hash {
			n := seen.Add(1)
			if n == 2 {
				close(gate)
			}
			if n <= 2 {
				select {
				case <-gate:
				case <-time.After(10 * time.Second):
					tx.AddError(errors.New("test storage insert barrier timed out"))
				}
			}

		}
	}); err != nil {
		t.Fatal(err)
	}
	defer f.db.Callback().Create().Remove(hook)
	results := make(chan *httptest.ResponseRecorder, 2)
	go func() { results <- completeUploadRecorder(f, f.alice, a) }()
	go func() { results <- completeUploadRecorder(f, f.bob, b) }()
	first, second := <-results, <-results
	var successful []model.File
	for _, w := range []*httptest.ResponseRecorder{first, second} {
		if w.Code == http.StatusOK {
			successful = append(successful, completedBlob(t, f, w))
		} else {
			t.Errorf("dedup completion status %d, want 200", w.Code)
		}
	}
	if len(successful) != 2 {
		t.FailNow()
	}
	one, two := successful[0], successful[1]

	if one.StorageID == nil || two.StorageID == nil || *one.StorageID != *two.StorageID {
		t.Fatal("dedup created two physical objects")
	}
	for _, file := range []model.File{one, two} {
		token := f.alice
		if file.UserID == f.bobID {
			token = f.bob
		}
		w := f.json(t, "GET", fmt.Sprintf("/api/files/%d/download", file.ID), token, nil, http.StatusOK)
		if !bytes.Equal(w.Body.Bytes(), payload) {
			t.Fatal("dedup bytes mismatch")
		}
	}
}

func TestUploadAndCopyPreserveIndependentVersionReferences(t *testing.T) {
	f := newFixture(t)
	target := f.upload(t, f.alice, "history-target.bin", []byte("original v1"))
	var initial []model.FileVersion
	if err := f.db.Where("file_id=?", target.ID).Find(&initial).Error; err != nil {
		t.Fatal(err)
	}
	if len(initial) != 1 || !initial[0].SourceVerified || initial[0].Version != 1 || initial[0].StorageID != *target.StorageID {
		t.Fatalf("initial version reference missing: %#v", initial)
	}
	source := f.upload(t, f.alice, "history-source.bin", []byte("replacement v2"))
	f.json(t, "POST", fmt.Sprintf("/api/files/%d/versions", target.ID), f.alice, map[string]any{"storage_id": source.StorageID, "base_version": 1}, http.StatusOK)
	var versions []model.FileVersion
	if err := f.db.Where("file_id=?", target.ID).Order("version").Find(&versions).Error; err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 || versions[0].StorageID != *target.StorageID || versions[1].StorageID != *source.StorageID {
		t.Fatal("version update lost original storage reference")
	}
	w := f.json(t, "POST", "/api/files/copy", f.alice, map[string]any{"file_ids": []int64{target.ID}}, http.StatusOK)
	var copied struct {
		IDs []int64 `json:"new_ids"`
	}
	decode(t, w, &copied)
	if len(copied.IDs) != 1 {
		t.Fatalf("copy response %s", w.Body.String())
	}
	var copy model.File
	if err := f.db.First(&copy, copied.IDs[0]).Error; err != nil {
		t.Fatal(err)
	}
	if copy.CurrentVersion != 1 {
		t.Fatalf("copy inherited unrelated version counter %d", copy.CurrentVersion)
	}
	var count int64
	if err := f.db.Model(&model.FileVersion{}).Where("file_id=? AND version=1 AND storage_id=?", copy.ID, source.StorageID).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("copy's own initial reference is missing")
	}
}

func TestVersionDownloadAndRestoreUseCurrentFilePermissions(t *testing.T) {
	f := newFixture(t)
	target := f.upload(t, f.alice, "versioned.txt", []byte("original historical bytes"))
	source := f.upload(t, f.alice, "new.pdf", []byte("replacement historical bytes"))
	base := fmt.Sprintf("/api/files/%d", target.ID)
	f.json(t, "POST", base+"/versions", f.alice, map[string]any{"storage_id": source.StorageID, "base_version": 1, "file_type": "pdf"}, http.StatusOK)
	for version, want := range map[int]string{1: "original historical bytes", 2: "replacement historical bytes"} {
		w := f.json(t, "GET", fmt.Sprintf("%s/versions/%d/download", base, version), f.alice, nil, http.StatusOK)
		if w.Body.String() != want {
			t.Fatal("historical bytes changed")
		}
	}
	f.json(t, "GET", base+"/versions/1/download", f.bob, nil, http.StatusForbidden)
	f.json(t, "POST", base+"/versions/1/restore", f.bob, map[string]int{"base_version": 2}, http.StatusForbidden)
	grant := model.FilePermission{UserID: f.bobID, FileID: target.ID, Permission: "read"}
	if err := f.db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "GET", base+"/versions/1/download", f.bob, nil, http.StatusOK)
	f.json(t, "POST", base+"/versions/1/restore", f.bob, map[string]int{"base_version": 2}, http.StatusForbidden)
	if err := f.db.Model(&grant).Update("permission", "write").Error; err != nil {
		t.Fatal(err)
	}
	w := f.json(t, "POST", base+"/versions/1/restore", f.bob, map[string]any{"base_version": 2, "storage_id": source.StorageID}, http.StatusOK)
	var result struct {
		Item model.FileVersion `json:"item"`
	}
	decode(t, w, &result)
	if result.Item.Version != 3 || result.Item.StorageID != *target.StorageID || result.Item.FileType != "txt" {
		t.Fatalf("invalid restored version %#v", result.Item)
	}
	current := f.json(t, "GET", base+"/download", f.alice, nil, http.StatusOK)
	if current.Body.String() != "original historical bytes" {
		t.Fatal("restore did not publish historical bytes")
	}
	unchanged := f.json(t, "GET", base+"/versions/2/download", f.alice, nil, http.StatusOK)
	if unchanged.Body.String() != "replacement historical bytes" {
		t.Fatal("restore rewrote historical version")
	}
	f.json(t, "POST", base+"/versions/1/restore", f.alice, map[string]int{"base_version": 2}, http.StatusConflict)
	f.json(t, "GET", base+"/versions/99/download", f.alice, nil, http.StatusNotFound)
	if err := f.db.Delete(&grant).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "GET", base+"/versions/1/download", f.bob, nil, http.StatusForbidden)
	var log model.OperationLog
	if err := f.db.Where("file_id=? AND action=?", target.ID, "file_version_restore").First(&log).Error; err != nil || log.ClientIP == "" || !strings.Contains(log.Detail, "base=2") {
		t.Fatalf("restore audit missing %v %#v", err, log)
	}
	custom := model.UserPathPermission{UserID: f.aliceID, Method: "GET", Path: base + "/versions/1/download"}
	if err := f.db.Create(&custom).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "GET", base+"/versions/1/download", f.alice, nil, http.StatusOK)
	f.json(t, "POST", base+"/versions/1/restore", f.alice, map[string]int{"base_version": 3}, http.StatusForbidden)
	f.json(t, "GET", "/api/auth/me", f.alice, nil, http.StatusOK)

}

func TestConcurrentVersionRestoreIsAtomic(t *testing.T) {
	f := newFixture(t)
	target := f.upload(t, f.alice, "restore-target.bin", []byte("restore one"))
	source := f.upload(t, f.alice, "restore-source.bin", []byte("restore two"))
	path := fmt.Sprintf("/api/files/%d/versions", target.ID)
	f.json(t, "POST", path, f.alice, map[string]any{"storage_id": source.StorageID, "base_version": 1}, http.StatusOK)
	results := make(chan int, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			req := httptest.NewRequest("POST", path+"/1/restore", strings.NewReader(`{"base_version":2}`))
			req.RemoteAddr = f.remoteAddr
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+f.alice)
			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)
			results <- w.Code
		}()
	}
	close(start)
	counts := map[int]int{}
	for range 2 {
		counts[<-results]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatalf("concurrent restores %#v", counts)
	}
	var versions int64
	if err := f.db.Model(&model.FileVersion{}).Where("file_id=?", target.ID).Count(&versions).Error; err != nil || versions != 3 {
		t.Fatalf("restore history count %d %v", versions, err)
	}
}

func TestVersionAuditFailureRollsBackHistoryAndCurrentPointer(t *testing.T) {
	f := newFixture(t)
	target := f.upload(t, f.alice, "audit-v1.bin", []byte("audit one"))
	source := f.upload(t, f.alice, "audit-v2.bin", []byte("audit two"))
	path := fmt.Sprintf("/api/files/%d/versions", target.ID)
	f.json(t, "POST", path, f.alice, map[string]any{"storage_id": source.StorageID, "base_version": 1}, http.StatusOK)
	const hook = "test:reject_version_audit"
	if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if row, ok := tx.Statement.Dest.(*model.OperationLog); ok && (row.Action == "file_version_create" || row.Action == "file_version_restore") {
			tx.AddError(errors.New("test audit unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Callback().Create().Remove(hook) })
	f.json(t, "POST", path+"/1/restore", f.alice, map[string]int{"base_version": 2}, http.StatusInternalServerError)
	f.json(t, "POST", path, f.alice, map[string]any{"storage_id": source.StorageID, "base_version": 2}, http.StatusInternalServerError)
	var current model.File
	var count int64
	if err := f.db.First(&current, target.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.FileVersion{}).Where("file_id=?", target.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if current.CurrentVersion != 2 || *current.StorageID != *source.StorageID || count != 2 {
		t.Fatal("failed audit changed versions or pointer")
	}
}

func TestDedupRepairsUnavailableObjectsWithoutRemovingOldPaths(t *testing.T) {
	for _, mode := range []string{"missing", "corrupt"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			payload := []byte("verified original object " + mode)
			original := f.upload(t, f.alice, "repair-original.bin", payload)
			var before model.Storage
			if err := f.db.First(&before, *original.StorageID).Error; err != nil {
				t.Fatal(err)
			}
			if mode == "missing" {
				if err := os.Remove(before.FilePath); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(before.FilePath, bytes.Repeat([]byte("x"), len(payload)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			path := fmt.Sprintf("/api/files/%d/versions/1", original.ID)
			want := http.StatusConflict
			if mode == "missing" {
				want = http.StatusNotFound
			}
			f.json(t, "GET", path+"/download", f.alice, nil, want)
			f.json(t, "POST", path+"/restore", f.alice, map[string]int{"base_version": 1}, want)
			replacement := f.upload(t, f.bob, "repair-verified.bin", payload)
			if *replacement.StorageID != *original.StorageID {
				t.Fatal("repair orphaned existing references")
			}
			var after model.Storage
			if err := f.db.First(&after, *original.StorageID).Error; err != nil {
				t.Fatal(err)
			}
			if after.FilePath == before.FilePath {
				t.Fatal("repair rewrote an existing path")
			}
			w := f.json(t, "GET", path+"/download", f.alice, nil, http.StatusOK)
			if !bytes.Equal(w.Body.Bytes(), payload) {
				t.Fatal("repair did not restore verified bytes")
			}
			if mode == "corrupt" {
				still, err := os.ReadFile(before.FilePath)
				if err != nil || !bytes.Equal(still, bytes.Repeat([]byte("x"), len(payload))) {
					t.Fatal("repair deleted or changed old diagnostic bytes")
				}
			}
			f.json(t, "POST", path+"/restore", f.alice, map[string]int{"base_version": 1}, http.StatusOK)
		})
	}
}

func TestRepairAuditFailureKeepsObjectAndAllowsRetry(t *testing.T) {
	f := newFixture(t)
	payload := []byte("recover after repair audit fails")
	original := f.upload(t, f.alice, "repair-audit.bin", payload)
	var before model.Storage
	if err := f.db.First(&before, *original.StorageID).Error; err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(before.FilePath, bytes.Repeat([]byte("x"), len(payload)), 0o600); err != nil {
		t.Fatal(err)
	}
	pending := fixturePendingUpload(t, f, f.bob, "repair-audit-candidate.bin", payload)
	const hook = "test:repair_audit_failure"
	if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if row, ok := tx.Statement.Dest.(*model.OperationLog); ok && row.Action == "storage_repair" {
			tx.AddError(errors.New("test audit unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Callback().Create().Remove(hook) })
	failed := completeUploadRecorder(f, f.bob, pending)
	if failed.Code != 500 {
		t.Fatalf("failed repair status %d", failed.Code)
	}
	var after model.Storage
	var task model.UploadTask
	if err := f.db.First(&after, *original.StorageID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Where("upload_id=?", pending).First(&task).Error; err != nil {
		t.Fatal(err)
	}
	if after.FilePath != before.FilePath || task.Status != 0 {
		t.Fatal("failed repair committed metadata or task")
	}
	if err := f.db.Callback().Create().Remove(hook); err != nil {
		t.Fatal(err)
	}
	completedBlob(t, f, completeUploadRecorder(f, f.bob, pending))
	w := f.json(t, "GET", fmt.Sprintf("/api/files/%d/download", original.ID), f.alice, nil, http.StatusOK)
	if !bytes.Equal(w.Body.Bytes(), payload) {
		t.Fatal("retry did not recover object")
	}
}

func TestUploadSizeMismatchCannotPublish(t *testing.T) {
	f := newFixture(t)
	payload := []byte("validate final merged size")
	pending := fixturePendingUpload(t, f, f.alice, "size-mismatch.bin", payload)
	if err := f.db.Model(&model.UploadTask{}).Where("upload_id=?", pending).Update("total_size", len(payload)+1).Error; err != nil {
		t.Fatal(err)
	}
	w := completeUploadRecorder(f, f.alice, pending)
	if w.Code != 400 {
		t.Fatalf("mismatched bytes published: %d", w.Code)
	}
	var count int64
	if err := f.db.Model(&model.File{}).Where("user_id=? AND filename=?", f.aliceID, "size-mismatch.bin").Count(&count).Error; err != nil || count != 0 {
		t.Fatal("invalid size left file metadata")
	}
	if err := f.db.Model(&model.UploadTask{}).Where("upload_id=?", pending).Update("total_size", len(payload)).Error; err != nil {
		t.Fatal(err)
	}
	completedBlob(t, f, completeUploadRecorder(f, f.alice, pending))
}

func TestLegacyVersionPreservationDoesNotInventMissingHistory(t *testing.T) {
	f := newFixture(t)
	legacy := f.upload(t, f.alice, "legacy-current.bin", []byte("observed legacy version seven"))
	if err := f.db.Where("file_id=?", legacy.ID).Delete(&model.FileVersion{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&legacy).Update("current_version", 7).Error; err != nil {
		t.Fatal(err)
	}
	source := f.upload(t, f.alice, "legacy-replacement.bin", []byte("new version eight"))
	path := fmt.Sprintf("/api/files/%d/versions", legacy.ID)
	f.json(t, "POST", path, f.alice, map[string]any{"storage_id": source.StorageID, "base_version": 7}, http.StatusOK)
	var rows []model.FileVersion
	if err := f.db.Where("file_id=?", legacy.ID).Order("version").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Version != 7 || rows[0].CreateUser != nil || rows[1].Version != 8 || rows[0].StorageID != *legacy.StorageID {
		t.Fatalf("legacy history fabricated or lost: %#v", rows)
	}
	f.json(t, "GET", path+"/1/download", f.alice, nil, http.StatusNotFound)
	f.json(t, "POST", path+"/7/restore", f.alice, map[string]int{"base_version": 8}, http.StatusOK)
}

func TestVersionMetadataUpgradeRequiresExplicitMigration(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "legacy-metadata.bin", []byte("preserve refs across metadata migration"))
	// Simulate the old version schema only in the verified disposable database.
	for _, column := range []string{"file_type", "source_verified"} {
		if err := f.db.Migrator().DropColumn(&model.FileVersion{}, column); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CheckFileSchema(f.db); err == nil {
		t.Fatal("old version schema was accepted at startup")
	}
	var before []struct {
		ID, FileID, StorageID int64
		Version               int
	}
	if err := f.db.Table("file_version").Select("id,file_id,storage_id,version").Order("id").Scan(&before).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(f.db); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckFileSchema(f.db); err != nil {
		t.Fatal(err)
	}
	var after []struct {
		ID, FileID, StorageID int64
		Version               int
	}
	if err := f.db.Table("file_version").Select("id,file_id,storage_id,version").Order("id").Scan(&after).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("metadata migration changed version identities or references")
	}
	var legacy model.FileVersion
	if err := f.db.Where("file_id=? AND version=1", file.ID).First(&legacy).Error; err != nil || legacy.SourceVerified {
		t.Fatal("schema upgrade trusted old source without evidence")
	}

	w := f.json(t, "GET", fmt.Sprintf("/api/files/%d/versions/1/download", file.ID), f.alice, nil, http.StatusOK)
	if w.Body.String() != "preserve refs across metadata migration" {
		t.Fatal("migration lost historical bytes")
	}
}

func TestUnverifiedLegacyVersionCannotExposeForeignStorage(t *testing.T) {
	f := newFixture(t)
	target := f.upload(t, f.alice, "provenance-target.bin", []byte("alice current bytes"))
	private := f.upload(t, f.bob, "provenance-private.bin", []byte("bob private bytes"))
	size := private.Size
	old := model.FileVersion{FileID: target.ID, Version: 99, StorageID: *private.StorageID, Size: &size, SourceVerified: false}
	if err := f.db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/files/%d/versions/99", target.ID)
	f.json(t, "GET", path+"/download", f.alice, nil, http.StatusForbidden)
	f.json(t, "POST", path+"/restore", f.alice, map[string]any{"base_version": 1, "source_verified": true}, http.StatusForbidden)
	var unchanged model.File
	if err := f.db.First(&unchanged, target.ID).Error; err != nil || *unchanged.StorageID != *target.StorageID || unchanged.CurrentVersion != 1 {
		t.Fatal("legacy untrusted history changed current pointer")
	}
	grant := model.FilePermission{FileID: target.ID, UserID: f.bobID, Permission: "read"}
	if err := f.db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	// Bob already owns these bytes; reading them through a legacy row adds no
	// new capability. Target write permission is still separately required.
	f.json(t, "GET", path+"/download", f.bob, nil, http.StatusOK)
	f.json(t, "POST", path+"/restore", f.bob, map[string]int{"base_version": 1}, http.StatusForbidden)
	if err := f.db.Model(&grant).Update("permission", "write").Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", path+"/restore", f.bob, map[string]int{"base_version": 1}, http.StatusOK)
	published := f.json(t, "GET", fmt.Sprintf("/api/files/%d/versions/2/download", target.ID), f.alice, nil, http.StatusOK)
	if published.Body.String() != "bob private bytes" {
		t.Fatal("authorized writer did not publish owned bytes")
	}
}

func TestLegacyHistoryRequiresCurrentByteCapabilityUntilVerifiedPublication(t *testing.T) {
	f := newFixture(t)
	original := []byte("legacy history with unknown provenance")
	target := f.upload(t, f.alice, "unknown-v1.bin", original)
	source := f.upload(t, f.alice, "known-v2.bin", []byte("known second version"))
	base := fmt.Sprintf("/api/files/%d/versions", target.ID)
	f.json(t, "POST", base, f.alice, map[string]any{"storage_id": source.StorageID, "base_version": 1}, http.StatusOK)
	if err := f.db.Model(&model.FileVersion{}).Where("file_id=? AND version=1", target.ID).Update("source_verified", false).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "GET", base+"/1/download", f.alice, nil, http.StatusForbidden)
	f.json(t, "POST", base+"/1/restore", f.alice, map[string]int{"base_version": 2}, http.StatusForbidden)
	proof := f.upload(t, f.alice, "already-owned-original.bin", original)
	f.json(t, "GET", base+"/1/download", f.alice, nil, http.StatusOK)
	var legacy model.FileVersion
	if err := f.db.Where("file_id=? AND version=1", target.ID).First(&legacy).Error; err != nil || legacy.SourceVerified {
		t.Fatal("GET silently promoted old provenance")
	}
	f.json(t, "POST", base+"/1/restore", f.alice, map[string]int{"base_version": 2}, http.StatusOK)
	if err := f.db.Model(&proof).Update("is_deleted", 1).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", base, f.alice, map[string]any{"storage_id": source.StorageID, "base_version": 3}, http.StatusOK)
	f.json(t, "GET", base+"/1/download", f.alice, nil, http.StatusForbidden)
	f.json(t, "GET", base+"/3/download", f.alice, nil, http.StatusOK)
}
