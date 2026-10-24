//go:build integration

package integration

import (
	"archive/zip"
	"bytes"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/internal/transfer"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"cloudenest_go/model"
)

func archiveFolder(t *testing.T, f *fixture, payload []byte) (model.File, model.File) {
	t.Helper()
	folder := fixtureFolder(t, f, f.alice, "archive-source", 0)
	file := f.upload(t, f.alice, "source.bin", payload)
	f.json(t, "POST", "/api/files/move", f.alice, map[string]any{"file_ids": []int64{file.ID}, "target_parent_id": folder.ID}, 200)
	return folder, file
}
func archiveGet(f *fixture, folderID int64) *httptest.ResponseRecorder {
	return identityRequest(f, f.alice, fmt.Sprintf("/api/files/%d/download-zip", folderID), nil)
}
func TestArchiveRejectsCorruptSourceBeforeSendingZIP(t *testing.T) {
	f := newFixture(t)
	folder, file := archiveFolder(t, f, []byte("expected-original"))
	var storage model.Storage
	if err := f.db.First(&storage, *file.StorageID).Error; err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storage.FilePath, bytes.Repeat([]byte("!"), len("expected-original")), 0600); err != nil {
		t.Fatal(err)
	}
	w := archiveGet(f, folder.ID)
	if w.Code != 409 || w.Header().Get("ETag") != "" || bytes.HasPrefix(w.Body.Bytes(), []byte("PK")) {
		t.Fatal("corrupt object republished as valid archive", w.Code, w.Header())
	}
}
func TestArchiveHonorsSharedTemporaryVolumeBudget(t *testing.T) {
	f := newFixture(t)
	folder, _ := archiveFolder(t, f, []byte("archive bytes"))
	if err := transferManager(f).SetLimit(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	w := archiveGet(f, folder.ID)
	if w.Code != 507 || bytes.HasPrefix(w.Body.Bytes(), []byte("PK")) {
		t.Fatal("archive bypassed temporary volume", w.Code, w.Header())
	}
}

func TestArchiveSuccessfulPublicAndNestedBatchUseRegisteredOutput(t *testing.T) {
	f := newFixture(t)
	resetRateSettings(t, f)
	payload := []byte("nested archive payload")
	folder, file := archiveFolder(t, f, payload)
	var tasksBefore int64
	f.db.Model(&model.UploadTask{}).Count(&tasksBefore)
	zipped := archiveGet(f, folder.ID)
	if zipped.Code != 200 {
		t.Fatal(zipped.Code, zipped.Body.String())
	}
	assertIdentity(t, zipped, zipped.Body.Bytes())
	batch := f.json(t, "POST", "/api/files/batch-download", f.alice, map[string]any{"file_ids": []int64{folder.ID, file.ID}}, 200)
	outer, err := zip.NewReader(bytes.NewReader(batch.Body.Bytes()), int64(batch.Body.Len()))
	if err != nil || len(outer.File) != 2 {
		t.Fatal("bad outer ZIP", err)
	}
	for _, entry := range outer.File {
		in, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(in)
		in.Close()
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(entry.Name, ".zip") {
			inner, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, part := range inner.File {
				if part.FileInfo().IsDir() {
					continue
				}
				r, e := part.Open()
				if e != nil {
					t.Fatal(e)
				}
				b, e := io.ReadAll(r)
				r.Close()
				if e != nil || !bytes.Equal(b, payload) {
					t.Fatal("nested source changed", e)
				}
				found = true
			}
			if !found {
				t.Fatal("nested ZIP empty")
			}
		} else if !bytes.Equal(data, payload) {
			t.Fatal("batch source changed")
		}
	}
	var share struct {
		Code string `json:"share_code"`
	}
	decode(t, f.json(t, "POST", fmt.Sprintf("/api/files/%d/share", folder.ID), f.alice, map[string]any{}, 200), &share)
	public := identityRequest(f, "", "/api/public/shares/"+share.Code+"/download", nil)
	if public.Code != 200 {
		t.Fatal(public.Code, public.Body.String())
	}
	assertIdentity(t, public, public.Body.Bytes())
	var rows []model.TransferArtifact
	if err := f.db.Where("kind='archive' AND user_id=?", f.aliceID).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatal("nested ZIP created extra temp payload", len(rows))
	}
	for _, row := range rows {
		if row.State != "done" || row.TaskID != 0 || row.UserID != f.aliceID {
			t.Fatal("archive not released/owned", row.State, row.TaskID, row.UserID)
		}
	}
	var operations int64
	if err := f.db.Model(&model.OperationLog{}).Where("user_id=? AND action='batch_download' AND status='success'", f.aliceID).Count(&operations).Error; err != nil {
		t.Fatal(err)
	}
	if operations != 1 {
		t.Fatal("batch download business audit lost", operations)
	}
	var after int64
	f.db.Model(&model.UploadTask{}).Count(&after)
	if after != tasksBefore || transferAudit(t, f).Usage.Charged != 0 {
		t.Fatal("synthetic upload or leaked charge")
	}
}

func TestArchiveGrowthCannotOverbookAcrossManagers(t *testing.T) {
	f := newFixture(t)
	m := transferManager(f)
	if err := m.SetLimit(context.Background(), 1<<20); err != nil {
		t.Fatal(err)
	}
	a, err := m.CreateArchive(context.Background(), f.aliceID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := transferManager(f).CreateArchive(context.Background(), f.aliceID)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(true)
	defer b.Close(true)
	results := make(chan error, 2)
	for _, h := range []*transfer.Handle{a, b} {
		go func(h *transfer.Handle) { _, err := h.Write(make([]byte, 800<<10)); results <- err }(h)
	}
	failures := 0
	for range 2 {
		err := <-results
		if errors.Is(err, contract.ErrTransferSpace) {
			failures++
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if failures != 1 {
		t.Fatal("concurrent growth overbooked", failures)
	}
	if r := transferAudit(t, f); r.Usage.Charged > 1<<20 || r.Usage.Charged < 800<<10 {
		t.Fatal("incorrect durable charge", r)
	}
}

func TestArchiveAdmissionLimitsAreSharedAndKeepUploadBudget(t *testing.T) {
	f := newFixture(t)
	m := transferManager(f)
	var handles []*transfer.Handle
	defer func() {
		for _, h := range handles {
			h.Close(true)
		}
	}()
	for _, uid := range []int64{f.aliceID, f.aliceID, f.bobID, f.bobID} {
		h, err := m.CreateArchive(context.Background(), uid)
		if err != nil {
			t.Fatal(err)
		}
		handles = append(handles, h)
	}
	if h, err := transferManager(f).CreateArchive(context.Background(), f.aliceID); !errors.Is(err, contract.ErrTransferBusy) {
		if h != nil {
			h.Close(true)
		}
		t.Fatal("owner cap ignored", err)
	}
	// Independent identity reaches the volume cap; no synthetic upload is made.
	third := model.User{Username: fmt.Sprintf("archive-third-%d", f.aliceID), Password: "test-only-disabled-login"}
	if err := f.db.Create(&third).Error; err != nil {
		t.Fatal(err)
	}
	if h, err := transferManager(f).CreateArchive(context.Background(), third.ID); !errors.Is(err, contract.ErrTransferBusy) {
		if h != nil {
			h.Close(true)
		}
		t.Fatal("volume cap ignored", err)
	}
	for _, h := range handles {
		if err := h.Close(true); err != nil {
			t.Fatal(err)
		}
	}
	handles = nil
	if err := m.SetLimit(context.Background(), 1<<20); err != nil {
		t.Fatal(err)
	}
	h, err := m.CreateArchive(context.Background(), f.aliceID)
	if err != nil {
		t.Fatal(err)
	}
	handles = append(handles, h)
	if _, err := h.Write(make([]byte, 1<<20)); err != nil {
		t.Fatal(err)
	}
	id := immutableInit(t, f, 4, 4)
	task := receiptTask(t, f, id)
	if candidate, err := m.Create(context.Background(), f.aliceID, task.ID, "chunk", 5, 0); !errors.Is(err, contract.ErrTransferSpace) {
		if candidate != nil {
			candidate.Close(true)
		}
		t.Fatal("upload ignored archive reservation", err)
	}
	if err := h.Close(true); err != nil {
		t.Fatal(err)
	}
	handles = nil
	candidate, err := m.Create(context.Background(), f.aliceID, task.ID, "chunk", 5, 0)
	if err != nil {
		t.Fatal("capacity not returned", err)
	}
	candidate.Close(true)
}

func TestArchiveExpiredLeaseCannotDeleteOpenDescriptor(t *testing.T) {
	f := newFixture(t)
	m := transferManager(f)
	h, err := m.CreateArchive(context.Background(), f.aliceID)
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(h, "held bytes")
	if err := h.SealArchive(); err != nil {
		t.Fatal(err)
	}
	token, path := h.Token(), h.Path()
	transferExpire(t, f, token)
	r, err := transferManager(f).Reconcile(context.Background())
	if !errors.Is(err, transfer.ErrPending) || r.Busy != 1 || r.Usage.Charged != 10 {
		t.Fatal("open archive stolen", r, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(false); err != nil {
		t.Fatal(err)
	}
	r, err = m.Reconcile(context.Background())
	if err != nil || r.Removed != 1 || r.Usage.Charged != 0 {
		t.Fatal("archive orphan not reclaimed", r, err)
	}
	if transferArtifact(t, f, token).State != "done" {
		t.Fatal("missing deletion receipt")
	}
}

func TestArchiveRenewsWithoutAnUploadTask(t *testing.T) {
	f := newFixture(t)
	m := transferManager(f)
	h, err := m.CreateArchive(context.Background(), f.aliceID)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close(true)
	first := transferArtifact(t, f, h.Token()).ExpiresAt
	deadline := time.Now().Add(25 * time.Second)
	for !transferArtifact(t, f, h.Token()).ExpiresAt.After(first) {
		if time.Now().After(deadline) {
			t.Fatal("archive lease did not renew")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if h.Context().Err() != nil {
		t.Fatal("archive renewal required a fake upload task")
	}
	if _, err := h.Write([]byte("renewed")); err != nil {
		t.Fatal(err)
	}
	if err := h.SealArchive(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.ArchiveReader(); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveReaderUsesSealedFDAndReplacedPathIsRetained(t *testing.T) {
	f := newFixture(t)
	m := transferManager(f)
	h, err := m.CreateArchive(context.Background(), f.aliceID)
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(h, "original")
	if err := h.SealArchive(); err != nil {
		t.Fatal(err)
	}
	path := h.Path()
	if err := os.Rename(path, path+".external"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replaced"), 0600); err != nil {
		t.Fatal(err)
	}
	reader, _, err := h.ArchiveReader()
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	if err != nil || string(got) != "original" {
		t.Fatal("reopened archive path", string(got), err)
	}
	if err := h.Close(false); err != nil {
		t.Fatal(err)
	}
	transferExpire(t, f, h.Token())
	r, err := m.Reconcile(context.Background())
	if !errors.Is(err, transfer.ErrPending) || r.Blocked != 1 || r.Usage.Charged != 8 {
		t.Fatal("replacement incorrectly adopted", r, err)
	}
	got, err = os.ReadFile(path)
	if err != nil || string(got) != "replaced" {
		t.Fatal("replacement deleted", err)
	}
}

func TestArchiveUnlinkBeforeBudgetCommitIsRecoverable(t *testing.T) {
	f := newFixture(t)
	m := transferManager(f)
	h, err := m.CreateArchive(context.Background(), f.aliceID)
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(h, "journal")
	if err := h.SealArchive(); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(false); err != nil {
		t.Fatal(err)
	}
	transferExpire(t, f, h.Token())
	const hook = "test:archive_done_failure"
	if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if row, ok := tx.Statement.Dest.(*model.OperationLog); ok && row.Action == "transfer_delete_done" {
			tx.AddError(errors.New("injected archive done failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	_, err = m.Reconcile(context.Background())
	f.db.Callback().Create().Remove(hook)
	if err == nil {
		t.Fatal("done audit failure ignored")
	}
	if _, err := os.Stat(h.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("payload not unlinked", err)
	}
	if transferArtifact(t, f, h.Token()).State != "deleting" || transferAudit(t, f).Usage.Charged != 7 {
		t.Fatal("unknown commit freed capacity")
	}
	r, err := m.Reconcile(context.Background())
	if err != nil || r.Removed != 1 || r.Usage.Charged != 0 {
		t.Fatal("missing payload did not complete journal", r, err)
	}
}

func TestArchiveCorruptSourceFailsForNestedBatchAndPublicShare(t *testing.T) {
	f := newFixture(t)
	folder, file := archiveFolder(t, f, []byte("source-snapshot"))
	var share struct {
		Code string `json:"share_code"`
	}
	decode(t, f.json(t, "POST", fmt.Sprintf("/api/files/%d/share", folder.ID), f.alice, map[string]any{}, 200), &share)
	var storage model.Storage
	f.db.First(&storage, *file.StorageID)
	if err := os.WriteFile(storage.FilePath, bytes.Repeat([]byte("!"), len("source-snapshot")), 0600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{file.ID, folder.ID} {
		w := f.json(t, "POST", "/api/files/batch-download", f.alice, map[string]any{"file_ids": []int64{id}}, 409)
		if w.Header().Get("ETag") != "" || bytes.HasPrefix(w.Body.Bytes(), []byte("PK")) {
			t.Fatal("batch published corrupted source")
		}
	}
	public := identityRequest(f, "", "/api/public/shares/"+share.Code+"/download", map[string]string{"If-None-Match": "*"})
	if public.Code != 409 || public.Header().Get("Repr-Digest") != "" {
		t.Fatal("public corruption conditional success", public.Code)
	}
	if leaseCount(t, f, *file.StorageID) != 0 || transferAudit(t, f).Usage.Charged != 0 {
		t.Fatal("failed ZIP leaked reservations")
	}
}

func TestArchiveRealTCPDisconnectReleasesPayloadAndReadLeases(t *testing.T) {
	f := newFixture(t)
	resetRateSettings(t, f)
	payload := make([]byte, 512<<10)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	folder, file := archiveFolder(t, f, payload)
	rateSetting(t, f, "download_speed_limit_kbps", "64")
	server := httptest.NewServer(f.router)
	defer server.Close()
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/files/%d/download-zip", server.URL, folder.ID), nil)
	req.Header.Set("Authorization", "Bearer "+f.alice)
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	if _, err := io.ReadFull(response.Body, make([]byte, 16<<10)); err != nil {
		t.Fatal(err)
	}
	var archive model.TransferArtifact
	if err := f.db.Where("kind='archive' AND state='ready' AND user_id=?", f.aliceID).First(&archive).Error; err != nil {
		t.Fatal("payload not held through HTTP", err)
	}
	if archive.Bytes != archive.Size || archive.Bytes <= 0 {
		t.Fatal("HTTP payload not charged")
	}
	response.Body.Close()
	deadline := time.Now().Add(5 * time.Second)
	for transferArtifact(t, f, archive.Token).State != "done" {
		if time.Now().After(deadline) {
			t.Fatal("disconnect left active payload")
		}
		time.Sleep(20 * time.Millisecond)
	}
	waitNoReadLeases(t, f, *file.StorageID)
	if transferAudit(t, f).Usage.Charged != 0 {
		t.Fatal("disconnect leaked disk budget")
	}
}

func TestArchiveGrowthFailurePrecedesIOAndBatchBodyIsBounded(t *testing.T) {
	f := newFixture(t)
	m := transferManager(f)
	h, err := m.CreateArchive(context.Background(), f.aliceID)
	if err != nil {
		t.Fatal(err)
	}
	const hook = "test:archive_growth_failure"
	if err := f.db.Callback().Update().Before("gorm:update").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "transfer_artifact" {
			if changes, ok := tx.Statement.Dest.(map[string]any); ok && changes["bytes"] != nil {
				tx.AddError(errors.New("injected archive growth failure"))
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	n, err := h.Write(make([]byte, 8192))
	f.db.Callback().Update().Remove(hook)
	if err == nil || n != 0 {
		t.Fatal("unreserved write performed", n, err)
	}
	info, err := os.Stat(h.Path())
	if err != nil || info.Size() != 0 || transferAudit(t, f).Usage.Charged != 1 {
		t.Fatal("failed growth changed payload/capacity", err)
	}
	if err := h.Close(true); err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", "/api/files/batch-download", f.alice, map[string]any{"file_ids": []int64{1}, "padding": strings.Repeat("x", 17<<10)}, 413)
	for _, test := range []struct {
		tail   string
		status int
	}{{strings.Repeat(" ", 17<<10), 413}, {" {}", 400}} {
		req := httptest.NewRequest("POST", "/api/files/batch-download", strings.NewReader(`{"file_ids":[1]}`+test.tail))
		req.Header.Set("Authorization", "Bearer "+f.alice)
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = f.remoteAddr
		w := httptest.NewRecorder()
		f.router.ServeHTTP(w, req)
		if w.Code != test.status {
			t.Fatal("trailing JSON escaped input bound", w.Code, test.status)
		}
	}
}

func TestArchiveExpiredCannotSealOrGrowAndCorruptFDIsRejected(t *testing.T) {
	f := newFixture(t)
	m := transferManager(f)
	h, err := m.CreateArchive(context.Background(), f.aliceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	transferExpire(t, f, h.Token())
	if _, err := h.Write(make([]byte, 8192)); !errors.Is(err, contract.ErrTransferUnavailable) {
		t.Fatal("expired archive grew", err)
	}
	if err := h.SealArchive(); !errors.Is(err, contract.ErrTransferUnavailable) {
		t.Fatal("expired archive sealed", err)
	}
	if err := h.Close(true); err != nil {
		t.Fatal(err)
	}
	other, err := m.CreateArchive(context.Background(), f.aliceID)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close(true)
	io.WriteString(other, "original")
	if err := other.SealArchive(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other.Path(), []byte("corrupt!"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := other.ArchiveReader(); !errors.Is(err, contract.ErrStorageIntegrity) {
		t.Fatal("sealed FD corruption accepted", err)
	}
}
