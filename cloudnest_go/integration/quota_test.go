//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"cloudenest_go/internal/quota"
	"cloudenest_go/internal/service"
	storageMysql "cloudenest_go/internal/storage/mysql"
	"cloudenest_go/internal/store"
	"cloudenest_go/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func quotaSetting(t *testing.T, f *fixture, value string) {
	t.Helper()
	var before []model.SystemSetting
	if err := f.db.Where("setting_key IN ?", []string{"storage_quota_bytes", "storage_default_quota_bytes"}).Find(&before).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.db.Where("setting_key IN ?", []string{"storage_quota_bytes", "storage_default_quota_bytes"}).Delete(&model.SystemSetting{}).Error; err != nil {
			t.Error(err)
		}
		if len(before) > 0 {
			if err := f.db.Create(&before).Error; err != nil {
				t.Error(err)
			}
		}
	})
	if err := storageMysql.NewSettingsRepo(f.db).UpsertSystemSetting(context.Background(), "storage_quota_bytes", value, "disposable quota fixture"); err != nil {
		t.Fatal(err)
	}
}
func quotaOverview(t *testing.T, f *fixture, token string) service.StorageOverview {
	t.Helper()
	var response struct {
		Item service.StorageOverview `json:"item"`
	}
	decode(t, f.json(t, "GET", "/api/storage/overview", token, nil, 200), &response)
	return response.Item
}
func quotaJSON(f *fixture, method, path, token string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.RemoteAddr = f.remoteAddr
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}
func quotaInit(t *testing.T, f *fixture, token, name string, size int64) string {
	t.Helper()
	var response struct {
		UploadID string `json:"upload_id"`
	}
	decode(t, f.json(t, "POST", "/api/files/upload/init", token, map[string]any{"filename": name, "total_size": size, "chunk_size": size, "total_chunks": 1}, 200), &response)
	return response.UploadID
}
func quotaLedger(t *testing.T, f *fixture, uid int64) model.UserStorageLedger {
	t.Helper()
	var l model.UserStorageLedger
	if err := f.db.First(&l, "user_id=?", uid).Error; err != nil {
		t.Fatal(err)
	}
	return l
}

func TestQuotaConcurrentReservationsCancelAndResume(t *testing.T) {
	f := newFixture(t)
	quotaSetting(t, f, "10")
	type result struct {
		name string
		w    *httptest.ResponseRecorder
	}
	results := make(chan result, 12)
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("reserve-%d.txt", i)
			results <- result{name, quotaJSON(f, "POST", "/api/files/upload/init", f.alice, map[string]any{"filename": name, "total_size": 6, "chunk_size": 6, "total_chunks": 1})}
		}(i)
	}
	wg.Wait()
	close(results)
	var id, name string
	success := 0
	for result := range results {
		switch result.w.Code {
		case 200:
			success++
			name = result.name
			var r struct {
				UploadID string `json:"upload_id"`
			}
			decode(t, result.w, &r)
			id = r.UploadID
		case 507:
		default:
			t.Fatalf("concurrent reservation: %d %s", result.w.Code, result.w.Body.String())
		}
	}
	if success != 1 {
		t.Fatalf("accepted %d reservations", success)
	}
	overview := quotaOverview(t, f, f.alice)
	if overview.UsedBytes != 0 || overview.ReservedBytes != 6 || overview.AvailableForUploadBytes != 4 {
		t.Fatalf("reservation overview: %+v", overview)
	}
	// Cached counters are not an authority: zeroing one must not create room
	// beyond the still-live task reservation.
	if err := f.db.Model(&model.UserStorageLedger{}).Where("user_id=?", f.aliceID).Update("reserved_bytes", 0).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", "/api/files/upload/init", f.alice, map[string]any{"filename": "cannot-trust-cached-zero.txt", "total_size": 6, "chunk_size": 6, "total_chunks": 1}, 507)
	same := map[string]any{"upload_id": id, "filename": name, "total_size": 6, "chunk_size": 6, "total_chunks": 1}
	f.json(t, "POST", "/api/files/upload/init", f.alice, same, 200)
	same["filename"] = "changed.txt"
	f.json(t, "POST", "/api/files/upload/init", f.alice, same, 409)
	f.json(t, "POST", "/api/files/upload/cancel", f.bob, map[string]any{"upload_id": id, "user_id": f.aliceID}, 403)
	for range 2 {
		f.json(t, "POST", "/api/files/upload/cancel", f.alice, map[string]any{"upload_id": id}, 200)
	}
	if l := quotaLedger(t, f, f.aliceID); l.ReservedBytes != 0 || l.UsedBytes != 0 {
		t.Fatal("reservation not released:", l)
	}
	quotaInit(t, f, f.alice, "after-cancel.txt", 10)
	if l := quotaLedger(t, f, f.aliceID); l.ReservedBytes != 10 {
		t.Fatal("cancellation lost reusable capacity")
	}
	var count int64
	if err := f.db.Model(&model.OperationLog{}).Where("user_id=? AND action='upload_cancel'", f.aliceID).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("cancel not idempotent:", err, count)
	}
}

func TestQuotaPublicationCopyRecycleAndPermanentRelease(t *testing.T) {
	f := newFixture(t)
	quotaSetting(t, f, "8")
	payload := []byte("1234")
	id := fixturePendingUpload(t, f, f.alice, "quota-publish.txt", payload)
	if r := quotaOverview(t, f, f.alice); r.UsedBytes != 0 || r.ReservedBytes != 4 {
		t.Fatalf("pending: %+v", r)
	}
	file := completedBlob(t, f, completeUploadRecorder(f, f.alice, id))
	if r := quotaOverview(t, f, f.alice); r.UsedBytes != 4 || r.ReservedBytes != 0 || r.ActiveBytes != 4 {
		t.Fatalf("published: %+v", r)
	}
	var copied struct {
		FileIDs []int64 `json:"file_ids"`
	}
	w := f.json(t, "POST", "/api/files/copy", f.alice, map[string]any{"file_ids": []int64{file.ID}, "target_parent_id": 0}, 200)
	// API uses new_file_ids; inspect the authoritative rows for the copied ID.
	decode(t, w, &copied)
	var rows []model.File
	if err := f.db.Where("user_id=? AND is_folder=0", f.aliceID).Find(&rows).Error; err != nil || len(rows) != 2 {
		t.Fatal("copy not created:", err)
	}
	f.json(t, "POST", "/api/files/copy", f.alice, map[string]any{"file_ids": []int64{file.ID}, "target_parent_id": 0}, 507)
	if r := quotaOverview(t, f, f.alice); r.UsedBytes != 8 {
		t.Fatal("physical dedup bypassed logical copy billing:", r)
	}
	f.json(t, "DELETE", fmt.Sprintf("/api/files/%d", file.ID), f.alice, nil, 200)
	if r := quotaOverview(t, f, f.alice); r.UsedBytes != 8 || r.RecycleBytes != 4 || r.ActiveBytes != 4 {
		t.Fatalf("recycle: %+v", r)
	}
	f.json(t, "POST", fmt.Sprintf("/api/files/%d/restore", file.ID), f.alice, nil, 200)
	if r := quotaOverview(t, f, f.alice); r.UsedBytes != 8 {
		t.Fatal("restore double charged")
	}
	f.json(t, "DELETE", fmt.Sprintf("/api/files/%d", file.ID), f.alice, nil, 200)
	f.json(t, "DELETE", fmt.Sprintf("/api/files/%d/permanent", file.ID), f.alice, nil, 200)
	if r := quotaOverview(t, f, f.alice); r.UsedBytes != 4 || r.RecycleBytes != 0 {
		t.Fatal("permanent delete did not release:", r)
	}
	if l := quotaLedger(t, f, f.aliceID); l.UsedBytes != 4 || l.ReservedBytes != 0 {
		t.Fatal("ledger differs from retained graph")
	}
}

func TestQuotaRetainedVersionsCountOncePerFileObject(t *testing.T) {
	f := newFixture(t)
	quotaSetting(t, f, "16")
	a := f.upload(t, f.alice, "quota-history-a.txt", []byte("1234"))
	b := f.upload(t, f.alice, "quota-history-b.txt", []byte("123456"))
	path := fmt.Sprintf("/api/files/%d/versions", a.ID)
	f.json(t, "POST", path, f.alice, map[string]any{"storage_id": b.StorageID, "base_version": 1}, 200)
	if r := quotaOverview(t, f, f.alice); r.UsedBytes != 16 || r.ActiveBytes != 12 || r.HistoryBytes != 4 {
		t.Fatalf("history: %+v", r)
	}
	f.json(t, "POST", path+"/1/restore", f.alice, map[string]any{"base_version": 2}, 200)
	if r := quotaOverview(t, f, f.alice); r.UsedBytes != 16 || r.ActiveBytes != 10 || r.HistoryBytes != 6 {
		t.Fatalf("restore billing: %+v", r)
	}
	f.json(t, "POST", path+"/2/restore", f.alice, map[string]any{"base_version": 3}, 200)
	f.json(t, "POST", path, f.alice, map[string]any{"storage_id": b.StorageID, "base_version": 4}, 200)
	if r := quotaOverview(t, f, f.alice); r.UsedBytes != 16 {
		t.Fatal("repeated versions charged repeatedly")
	}
	f.json(t, "POST", "/api/files/upload/init", f.alice, map[string]any{"filename": "over-history.txt", "total_size": 1, "chunk_size": 1, "total_chunks": 1}, 507)
}

func TestQuotaSharedWriterChargesFileOwner(t *testing.T) {
	f := newFixture(t)
	target := f.upload(t, f.alice, "owner-billing.txt", []byte("1234"))
	source := f.upload(t, f.bob, "writer-source.txt", []byte("123456"))
	quotaSetting(t, f, "8")
	if err := f.db.Create(&model.FilePermission{FileID: target.ID, UserID: f.bobID, Permission: "write"}).Error; err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/files/%d/versions", target.ID)
	f.json(t, "POST", path, f.bob, map[string]any{"storage_id": source.StorageID, "base_version": 1, "user_id": f.bobID}, 507)
	if quotaOverview(t, f, f.alice).UsedBytes != 4 || quotaOverview(t, f, f.bob).UsedBytes != 6 {
		t.Fatal("failed shared publication changed balances")
	}
	var current model.File
	if err := f.db.First(&current, target.ID).Error; err != nil || current.CurrentVersion != 1 {
		t.Fatal("over-quota version changed target")
	}
	if err := storageMysql.NewSettingsRepo(f.db).UpsertSystemSetting(context.Background(), "storage_quota_bytes", "10", ""); err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", path, f.bob, map[string]any{"storage_id": source.StorageID, "base_version": 1}, 200)
	if quotaOverview(t, f, f.alice).UsedBytes != 10 || quotaOverview(t, f, f.bob).UsedBytes != 6 {
		t.Fatal("writer billed instead of target owner")
	}
}

func TestQuotaReducedLimitRechecksPublicationAndAllowsCancellation(t *testing.T) {
	f := newFixture(t)
	quotaSetting(t, f, "20")
	id := fixturePendingUpload(t, f, f.alice, "reduced-quota.txt", []byte("1234567890"))
	if err := storageMysql.NewSettingsRepo(f.db).UpsertSystemSetting(context.Background(), "storage_quota_bytes", "5", ""); err != nil {
		t.Fatal(err)
	}
	if w := completeUploadRecorder(f, f.alice, id); w.Code != 507 {
		t.Fatal("reduced quota ignored:", w.Code)
	}
	if r := quotaOverview(t, f, f.alice); r.UsedBytes != 0 || r.ReservedBytes != 10 || r.AvailableForUploadBytes != 0 {
		t.Fatal("failed completion lost reservation:", r)
	}
	f.json(t, "POST", "/api/files/upload/cancel", f.alice, map[string]any{"upload_id": id}, 200)
	quotaInit(t, f, f.alice, "fits-current-limit.txt", 5)
	var files int64
	if err := f.db.Model(&model.File{}).Where("user_id=? AND is_folder=0", f.aliceID).Count(&files).Error; err != nil || files != 0 {
		t.Fatal("failed publication persisted file")
	}
}

func TestQuotaExpirationReleasesEffectiveReservation(t *testing.T) {
	f := newFixture(t)
	quotaSetting(t, f, "5")
	id := fixturePendingUpload(t, f, f.alice, "expired-reservation.txt", []byte("12345"))
	if err := f.db.Model(&model.UploadTask{}).Where("upload_id=?", id).Update("expires_at", gorm.Expr("DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 1 MINUTE)")).Error; err != nil {
		t.Fatal(err)
	}
	if r := quotaOverview(t, f, f.alice); r.ReservedBytes != 0 {
		t.Fatal("expired reservation held capacity")
	}
	if w := completeUploadRecorder(f, f.alice, id); w.Code != 409 {
		t.Fatal("expired upload published:", w.Code)
	}
	quotaInit(t, f, f.alice, "after-expiry.txt", 5)
	var task model.UploadTask
	if err := f.db.Where("upload_id=?", id).First(&task).Error; err != nil || task.Status != 4 || task.ReservedBytes != 0 {
		t.Fatal("new admission did not record expiration")
	}
	f.json(t, "POST", "/api/files/upload/cancel", f.alice, map[string]any{"upload_id": id}, 200)
	if r := quotaOverview(t, f, f.alice); r.ReservedBytes != 5 {
		t.Fatal("expired cancel released another task")
	}
}

func TestQuotaCancelAndCompleteSerialize(t *testing.T) {
	f := newFixture(t)
	quotaSetting(t, f, "20")
	published := int64(0)
	for i := range 6 {
		payload := []byte{byte(i + 1)}
		id := fixturePendingUpload(t, f, f.alice, fmt.Sprintf("cancel-race-%d.bin", i), payload)
		done := make(chan *httptest.ResponseRecorder, 1)
		cancel := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- completeUploadRecorder(f, f.alice, id) }()
		go func() {
			cancel <- quotaJSON(f, "POST", "/api/files/upload/cancel", f.alice, map[string]any{"upload_id": id})
		}()
		completion, cancellation := <-done, <-cancel
		if completion.Code == 200 {
			if cancellation.Code != 409 {
				t.Fatal("completed upload canceled:", cancellation.Code)
			}
			file := completedBlob(t, f, completion)
			published++
			if w := f.json(t, "GET", fmt.Sprintf("/api/files/%d/download", file.ID), f.alice, nil, 200); !bytes.Equal(w.Body.Bytes(), payload) {
				t.Fatal("race changed published bytes")
			}
		} else if completion.Code != 409 || cancellation.Code != 200 {
			t.Fatalf("cancel race: complete=%d cancel=%d", completion.Code, cancellation.Code)
		}
	}
	if r := quotaOverview(t, f, f.alice); r.UsedBytes != published || r.ReservedBytes != 0 {
		t.Fatalf("race accounting: %+v published=%d", r, published)
	}
}

func TestQuotaAuditFailuresRollbackReservationPublicationAndRelease(t *testing.T) {
	f := newFixture(t)
	quotaSetting(t, f, "10")
	const hook = "test:quota_audit_failure"
	fail := func() {
		t.Helper()
		if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
			if log, ok := tx.Statement.Dest.(*model.OperationLog); ok && log.Action == "quota_accounting" {
				tx.AddError(errors.New("injected quota audit failure"))
			}
		}); err != nil {
			t.Fatal(err)
		}
	}
	clear := func() { f.db.Callback().Create().Remove(hook) }
	t.Cleanup(clear)
	fail()
	f.json(t, "POST", "/api/files/upload/init", f.alice, map[string]any{"filename": "rollback-reservation.txt", "total_size": 5, "chunk_size": 5, "total_chunks": 1}, 500)
	clear()
	if r := quotaOverview(t, f, f.alice); r.ReservedBytes != 0 {
		t.Fatal("failed reservation left balance")
	}
	id := fixturePendingUpload(t, f, f.alice, "rollback-publication.txt", []byte("12345"))
	fail()
	if w := completeUploadRecorder(f, f.alice, id); w.Code != 500 {
		t.Fatal("publication ignored audit failure")
	}
	clear()
	if r := quotaOverview(t, f, f.alice); r.UsedBytes != 0 || r.ReservedBytes != 5 {
		t.Fatal("publication rollback lost balance")
	}
	fail()
	f.json(t, "POST", "/api/files/upload/cancel", f.alice, map[string]any{"upload_id": id}, 500)
	clear()
	if r := quotaOverview(t, f, f.alice); r.ReservedBytes != 5 {
		t.Fatal("failed cancel released balance")
	}
	completedBlob(t, f, completeUploadRecorder(f, f.alice, id))
}

func TestQuotaUnknownHistoryDoesNotRevealOrBillForeignSize(t *testing.T) {
	f := newFixture(t)
	target := f.upload(t, f.alice, "unmetered-owner.txt", []byte("1234"))
	private := f.upload(t, f.bob, "unmetered-private.txt", []byte("bob private content length must stay private"))
	declared := int64(1)
	legacy := model.FileVersion{FileID: target.ID, Version: 99, StorageID: *private.StorageID, Size: &declared, SourceVerified: false}
	if err := f.db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	if r := quotaOverview(t, f, f.alice); r.UsedBytes != 4 || r.UnmeteredReferences != 1 {
		t.Fatal("unknown history size disclosed or billed:", r)
	}
	proof := f.upload(t, f.alice, "unmetered-new-upload.txt", []byte("new"))
	if proof.ID == 0 {
		t.Fatal("unknown legacy metadata blocked independent recovery upload")
	}
	f.json(t, "GET", fmt.Sprintf("/api/files/%d/versions/99/download", target.ID), f.alice, nil, 403)
	legacyFolder := model.File{UserID: f.aliceID, Filename: "legacy-folder-pointer", IsFolder: 1, StorageID: private.StorageID}
	if err := f.db.Create(&legacyFolder).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", "/api/files/copy", f.alice, map[string]any{"file_ids": []int64{legacyFolder.ID}, "target_parent_id": 0}, 409)
}

func TestQuotaMalformedPolicyAndInputFailClosed(t *testing.T) {
	f := newFixture(t)
	quotaSetting(t, f, "not-a-byte-count")
	f.json(t, "POST", "/api/files/upload/init", f.alice, map[string]any{"filename": "invalid-policy.txt", "total_size": 1, "chunk_size": 1, "total_chunks": 1}, 503)
	f.json(t, "GET", "/api/storage/overview", f.alice, nil, 503)
	if err := storageMysql.NewSettingsRepo(f.db).UpsertSystemSetting(context.Background(), "storage_quota_bytes", "100", ""); err != nil {
		t.Fatal(err)
	}
	for _, body := range []map[string]any{
		{"filename": "mismatch.txt", "total_size": 10, "chunk_size": 5, "total_chunks": 1},
		{"filename": "overflow.txt", "total_size": int64(math.MaxInt64), "chunk_size": 1, "total_chunks": 1},
		{"filename": "negative-chunk.txt", "total_size": 10, "chunk_size": -1},
		{"filename": "negative-count.txt", "total_size": 10, "chunk_size": 5, "total_chunks": -2},
	} {
		f.json(t, "POST", "/api/files/upload/init", f.alice, body, 400)
	}
	var count int64
	if err := f.db.Model(&model.UploadTask{}).Where("user_id=?", f.aliceID).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("invalid input reserved bytes")
	}
	id := quotaInit(t, f, f.alice, "recover-policy-cancel.txt", 1)
	if err := storageMysql.NewSettingsRepo(f.db).UpsertSystemSetting(context.Background(), "storage_quota_bytes", "broken-again", ""); err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", "/api/files/upload/cancel", f.alice, map[string]any{"upload_id": id}, 200)
	if quotaLedger(t, f, f.aliceID).ReservedBytes != 0 {
		t.Fatal("malformed policy blocked cancellation")
	}

}

func TestQuotaPolicyEditWaitsForAllocationGate(t *testing.T) {
	f := newFixture(t)
	quotaSetting(t, f, "10")
	tx := f.db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	if err := quota.Begin(tx, f.aliceID); err != nil {
		t.Fatal(err)
	}
	attempted := make(chan struct{})
	var once sync.Once
	const hook = "test:quota_policy_lock_attempt"
	if err := f.db.Callback().Query().Before("gorm:query").Register(hook, func(q *gorm.DB) {
		if lock, ok := q.Statement.Clauses["FOR"].Expression.(clause.Locking); ok && q.Statement.Table == "storage_quota_policy" && lock.Strength == "UPDATE" {
			once.Do(func() { close(attempted) })
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Callback().Query().Remove(hook) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- storageMysql.NewSettingsRepo(f.db).UpsertSystemSetting(ctx, "STORAGE_QUOTA_BYTES", "8", "")
	}()
	select {
	case <-attempted:
	case <-ctx.Done():
		t.Fatal("policy writer did not acquire its gate")
	}
	select {
	case err := <-done:
		t.Fatal("policy edit passed active allocation gate:", err)
	default:
	}
	before, err := quota.Snapshot(tx, f.aliceID)
	if err != nil || before.LimitBytes != 10 {
		t.Fatal("allocation lost its policy snapshot:", err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if quotaOverview(t, f, f.alice).TotalBytes != 8 {
		t.Fatal("policy update not applied")
	}
}

func TestQuotaFrozenParentAndLegacyTasksCannotPublish(t *testing.T) {
	f := newFixture(t)
	quotaSetting(t, f, "20")
	id := fixturePendingUpload(t, f, f.alice, "fixed-parent.txt", []byte("fixed"))
	folder := fixtureFolder(t, f, f.alice, "different-destination", 0)
	f.json(t, "POST", "/api/files/upload/complete", f.alice, map[string]any{"upload_id": id, "parent_id": folder.ID}, 409)
	if r := quotaOverview(t, f, f.alice); r.ReservedBytes != 5 || r.UsedBytes != 0 {
		t.Fatal("changed parent consumed reservation")
	}
	legacy := model.UploadTask{UploadID: fmt.Sprintf("legacy-no-reservation-%d", f.aliceID), UserID: &f.aliceID, Filename: "old-pending.txt", Status: 0}
	if err := f.db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", "/api/files/upload/complete", f.alice, map[string]any{"upload_id": legacy.UploadID}, 409)
	f.json(t, "POST", "/api/files/upload/complete", f.bob, map[string]any{"upload_id": legacy.UploadID}, 403)
	completedBlob(t, f, completeUploadRecorder(f, f.alice, id))
}

func TestQuotaSchemaExplicitUpgradeAndMissingPolicyRefusal(t *testing.T) {
	f := newFixture(t)
	old := model.UploadTask{UploadID: fmt.Sprintf("pre-quota-%d", f.aliceID), UserID: &f.aliceID, Filename: "old-task.txt"}
	if err := f.db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	// Only the already guarded disposable schema is downgraded. Old tasks are
	// retained and marked contract 0 by defaults, never silently grandfathered.
	for _, table := range []string{"user_storage_ledger", "storage_quota_policy"} {
		if err := f.db.Exec("DROP TABLE " + table).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := f.db.Exec("ALTER TABLE upload_task DROP INDEX idx_upload_quota,DROP INDEX idx_upload_task_expires_at,DROP COLUMN contract_version,DROP COLUMN reserved_bytes,DROP COLUMN parent_id,DROP COLUMN expires_at").Error; err != nil {
		t.Fatal(err)
	}
	if store.CheckFileSchema(f.db) == nil {
		t.Fatal("startup accepted absent quota schema")
	}
	t.Cleanup(func() {
		if err := store.Migrate(f.db); err != nil {
			t.Error(err)
		}
	})
	if err := store.Migrate(f.db); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckFileSchema(f.db); err != nil {
		t.Fatal(err)
	}
	var preserved model.UploadTask
	if err := f.db.First(&preserved, old.ID).Error; err != nil || preserved.ContractVersion != 0 || preserved.Filename != old.Filename {
		t.Fatal("upgrade adopted or rewrote old task")
	}
	if err := f.db.Delete(&model.StorageQuotaPolicy{}, 1).Error; err != nil {
		t.Fatal(err)
	}
	if store.CheckFileSchema(f.db) == nil {
		t.Fatal("startup accepted absent policy singleton")
	}
}

// The physical cleanup boundary is explicit: cancellation leaves the
// existing chunks for the transfer reconciler instead of deleting arbitrary paths.
func TestQuotaCancellationRetainsRecordedChunkUntilReconciliation(t *testing.T) {
	f := newFixture(t)
	id := fixturePendingUpload(t, f, f.alice, "cancel-retained-chunk.txt", []byte("retained pending cleanup"))
	var chunk model.UploadChunk
	if err := f.db.Where("upload_id=?", id).First(&chunk).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", "/api/files/upload/cancel", f.alice, map[string]any{"upload_id": id}, 200)
	raw, err := os.ReadFile(chunk.ChunkPath)
	if err != nil || string(raw) != "retained pending cleanup" {
		t.Fatal("cancel deleted pending reconciliation bytes:", err)
	}
	if !filepath.IsAbs(chunk.ChunkPath) {
		t.Fatal("fixture expected absolute temp chunk path")
	}
	if w := completeUploadRecorder(f, f.alice, id); w.Code != 409 {
		t.Fatal("canceled task published")
	}
}

func TestQuotaDefaultPolicyAndExplicitOverride(t *testing.T) {
	f := newFixture(t)
	quotaSetting(t, f, "10")
	if err := f.db.Where("setting_key='storage_quota_bytes'").Delete(&model.SystemSetting{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := storageMysql.NewSettingsRepo(f.db).UpsertSystemSetting(context.Background(), "storage_default_quota_bytes", "5", ""); err != nil {
		t.Fatal(err)
	}
	file := f.upload(t, f.alice, "default-policy.txt", []byte("1234"))
	if r := quotaOverview(t, f, f.alice); r.TotalBytes != 5 || r.QuotaSource != "storage_default_quota_bytes" {
		t.Fatal("default policy ignored:", r)
	}
	f.json(t, "POST", "/api/files/upload/init", f.alice, map[string]any{"filename": "default-over.txt", "total_size": 2, "chunk_size": 2, "total_chunks": 1}, 507)
	if err := storageMysql.NewSettingsRepo(f.db).UpsertSystemSetting(context.Background(), "storage_quota_bytes", "10", ""); err != nil {
		t.Fatal(err)
	}
	quotaInit(t, f, f.alice, "explicit-policy.txt", 6)
	if r := quotaOverview(t, f, f.alice); r.UsedBytes != file.Size || r.ReservedBytes != 6 || r.TotalBytes != 10 || r.QuotaSource != "storage_quota_bytes" {
		t.Fatal("override policy ignored:", r)
	}
}

func TestQuotaBrokenSizeCannotBecomeZeroUsageAndCancellationStillWorks(t *testing.T) {
	f := newFixture(t)
	quotaSetting(t, f, "20")
	file := f.upload(t, f.alice, "broken-metering.txt", []byte("1234"))
	pending := quotaInit(t, f, f.alice, "pending-before-broken-metadata.txt", 2)
	if err := f.db.Model(&model.Storage{}).Where("id=?", file.StorageID).Update("file_size", nil).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Model(&model.Storage{}).Where("id=?", file.StorageID).Update("file_size", file.Size) })
	f.json(t, "GET", "/api/storage/overview", f.alice, nil, 503)
	f.json(t, "POST", "/api/files/upload/init", f.alice, map[string]any{"filename": "must-not-undercharge.txt", "total_size": 1, "chunk_size": 1, "total_chunks": 1}, 503)
	f.json(t, "POST", "/api/files/upload/cancel", f.alice, map[string]any{"upload_id": pending}, 200)
	if l := quotaLedger(t, f, f.aliceID); l.AccountingValid || l.ReservedBytes != 0 {
		t.Fatal("invalid usage was reported trustworthy:", l)
	}
	if err := f.db.Model(&model.Storage{}).Where("id=?", file.StorageID).Update("file_size", file.Size).Error; err != nil {
		t.Fatal(err)
	}
	if r := quotaOverview(t, f, f.alice); r.UsedBytes != 4 || r.ReservedBytes != 0 {
		t.Fatal("repaired metadata not re-evaluated")
	}
}

func TestQuotaExpiryDuringPublicationCannotCommit(t *testing.T) {
	f := newFixture(t)
	quotaSetting(t, f, "10")
	id := fixturePendingUpload(t, f, f.alice, "expires-during-publish.txt", []byte("12345"))
	const hook = "test:expire_task_during_publication"
	if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*model.Storage); !ok {
			return
		}
		tx.AddError(tx.Session(&gorm.Session{NewDB: true}).Model(&model.UploadTask{}).Where("upload_id=?", id).Update("expires_at", gorm.Expr("DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 1 MINUTE)")).Error)
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Callback().Create().Remove(hook) })
	if w := completeUploadRecorder(f, f.alice, id); w.Code != 409 {
		t.Fatal("late expiry published:", w.Code, w.Body.String())
	}
	if err := f.db.Callback().Create().Remove(hook); err != nil {
		t.Fatal(err)
	}
	if r := quotaOverview(t, f, f.alice); r.UsedBytes != 0 || r.ReservedBytes != 5 {
		t.Fatal("late-expiry rollback lost reservation:", r)
	}
	// The injected timestamp change rolled back with the failed transaction; a
	// real elapsed deadline would remain expired and require a new task.
	completedBlob(t, f, completeUploadRecorder(f, f.alice, id))
}
