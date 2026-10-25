//go:build integration

package integration

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"cloudenest_go/internal/blob"
	"cloudenest_go/internal/store"
	"cloudenest_go/internal/transfer"
	"cloudenest_go/model"
	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

func transferManager(f *fixture) *transfer.Manager { return transfer.NewManager(f.db, f.cfg) }
func transferArtifact(t *testing.T, f *fixture, token string) model.TransferArtifact {
	t.Helper()
	var row model.TransferArtifact
	if err := f.db.First(&row, "token=?", token).Error; err != nil {
		t.Fatal(err)
	}
	return row
}
func transferExpire(t *testing.T, f *fixture, token string) {
	t.Helper()
	if err := f.db.Model(&model.TransferArtifact{}).Where("token=?", token).Update("expires_at", gorm.Expr("DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 1 SECOND)")).Error; err != nil {
		t.Fatal(err)
	}
}
func transferAudit(t *testing.T, f *fixture) transfer.Report {
	t.Helper()
	r, err := transferManager(f).Audit(context.Background())
	if err != nil && !errors.Is(err, transfer.ErrPending) {
		t.Fatal(err)
	}
	return r
}
func newTransferHandle(t *testing.T, f *fixture, kind string, cap int64) (*transfer.Handle, string) {
	t.Helper()
	id := immutableInit(t, f, 4, 4)
	task := receiptTask(t, f, id)
	h, err := transferManager(f).Create(context.Background(), f.aliceID, task.ID, kind, cap, 0)
	if err != nil {
		t.Fatal(err)
	}
	return h, id
}
func sealedTransfer(t *testing.T, f *fixture) (*transfer.Handle, string) {
	t.Helper()
	h, id := newTransferHandle(t, f, "chunk", 5)
	if _, err := io.WriteString(h, "AAAA"); err != nil {
		t.Fatal(err)
	}
	if err := h.Seal(4, fmt.Sprintf("%x", sha256.Sum256([]byte("AAAA")))); err != nil {
		t.Fatal(err)
	}
	return h, id
}

func TestTransferBudgetChargesChunkAndMergeUntilHandoff(t *testing.T) {
	f := newFixture(t)
	m := transferManager(f)
	if err := m.SetLimit(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	id := immutableInit(t, f, 4, 4)
	sendChunk(t, f, id, 0, []byte("AAAA"), "", 200)
	if r := transferAudit(t, f); r.Usage.Charged != 4 {
		t.Fatalf("sealed chunk charge %+v", r)
	}
	if w := completeUploadRecorder(f, f.alice, id); w.Code != 507 {
		t.Fatal("merge did not pre-reserve full output", w.Code, w.Body.String())
	}
	if receiptTask(t, f, id).MergeArtifact != "" {
		t.Fatal("failed reservation left a merge claim")
	}
	if err := m.SetLimit(context.Background(), 8); err != nil {
		t.Fatal(err)
	}
	file := completedBlob(t, f, completeUploadRecorder(f, f.alice, id))
	if r := transferAudit(t, f); r.Usage.Charged != 0 {
		t.Fatal("confirmed handoff did not release temporary capacity", r)
	}
	var records []model.TransferArtifact
	if err := f.db.Where("task_id=?", receiptTask(t, f, id).ID).Find(&records).Error; err != nil {
		t.Fatal(err)
	}
	published, done := 0, 0
	for _, a := range records {
		if a.State == "published" {
			published++
		}
		if a.State == "done" {
			done++
		}
	}
	if published != 1 || done != 1 {
		t.Fatal("missing publication/cleanup journal", published, done)
	}
	if w := f.json(t, "GET", fmt.Sprintf("/api/files/%d/download", file.ID), f.alice, nil, 200); w.Body.String() != "AAAA" {
		t.Fatal("handoff removed published bytes")
	}
}
func TestTransferConcurrentReservationsCannotOverbook(t *testing.T) {
	f := newFixture(t)
	m := transferManager(f)
	if err := m.SetLimit(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	id := immutableInit(t, f, 4, 4)
	task := receiptTask(t, f, id)
	type result struct {
		h   *transfer.Handle
		err error
	}
	results := make(chan result, 8)
	start := make(chan struct{})
	for range 8 {
		go func() {
			<-start
			h, e := m.Create(context.Background(), f.aliceID, task.ID, "chunk", 5, 0)
			results <- result{h, e}
		}()
	}
	close(start)
	var handles []*transfer.Handle
	for range 8 {
		r := <-results
		if r.err == nil {
			handles = append(handles, r.h)
		} else if !strings.Contains(r.err.Error(), "temporary storage capacity") {
			t.Fatal(r.err)
		}
	}
	defer func() {
		for _, h := range handles {
			h.Close(true)
		}
	}()
	if len(handles) != 2 || transferAudit(t, f).Usage.Charged != 10 {
		t.Fatal("oversubscribed durable budget", len(handles))
	}
	for _, h := range handles {
		if err := h.Close(true); err != nil {
			t.Fatal(err)
		}
	}
	if r := transferAudit(t, f); r.Usage.Charged != 0 {
		t.Fatal("unused reservations not released", r)
	}
}
func TestTransferExpiredLeaseCannotDeleteOpenFile(t *testing.T) {
	f := newFixture(t)
	h, _ := newTransferHandle(t, f, "chunk", 5)
	defer h.Close(false)
	transferExpire(t, f, h.Token())
	r, err := transferManager(f).Reconcile(context.Background())
	if !errors.Is(err, transfer.ErrPending) || r.Busy != 1 || r.Usage.Charged != 5 {
		t.Fatal("expired lease bypassed FD fence", r, err)
	}
	if _, err := os.Stat(h.Path()); err != nil {
		t.Fatal("live writer unlinked", err)
	}
	if err := h.Close(false); err != nil {
		t.Fatal(err)
	}
	r, err = transferManager(f).Reconcile(context.Background())
	if err != nil || r.Removed != 1 || r.Usage.Charged != 0 {
		t.Fatal("closed expired writer not reclaimed", r, err)
	}
}
func TestTransferRealProcessLockSurvivesLeaseUntilProcessDies(t *testing.T) {
	f := newFixture(t)
	h, _ := newTransferHandle(t, f, "chunk", 5)
	token, path := h.Token(), h.Path()
	if err := h.Close(false); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("python3", "-c", `import fcntl,os,sys,time
f=open(sys.argv[1],'r+b');fcntl.flock(f,fcntl.LOCK_EX);f.write(b'AAAA');f.flush();os.fsync(f.fileno());print('LOCKED',flush=True);time.sleep(60)`, path)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if command.ProcessState == nil {
			command.Process.Kill()
			command.Wait()
		}
	}()
	ready := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(stdout).ReadString('\n'); ready <- line }()
	select {
	case line := <-ready:
		if line != "LOCKED\n" {
			t.Fatal("writer not ready", stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("writer readiness timed out")
	}
	transferExpire(t, f, token)
	r, err := transferManager(f).Reconcile(context.Background())
	if !errors.Is(err, transfer.ErrPending) || r.Busy != 1 || r.Usage.Charged != 5 {
		t.Fatal("live process fence failed", r, err)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	r, err = transferManager(f).Reconcile(context.Background())
	if err != nil || r.Removed != 1 || r.Usage.Charged != 0 {
		t.Fatal("dead process recovery failed", r, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("dead writer path retained", err)
	}
}
func TestTransferUnlinkBeforeDoneRetainsChargeAndRecovers(t *testing.T) {
	f := newFixture(t)
	h, _ := sealedTransfer(t, f)
	token, path := h.Token(), h.Path()
	h.Close(false)
	transferExpire(t, f, token)
	const hook = "test:transfer_done_failure"
	if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if row, ok := tx.Statement.Dest.(*model.OperationLog); ok && row.Action == "transfer_delete_done" {
			tx.AddError(errors.New("injected done audit failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	_, err := transferManager(f).Reconcile(context.Background())
	f.db.Callback().Create().Remove(hook)
	if err == nil {
		t.Fatal("failed completion audit accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("unlink window not exercised", err)
	}
	if a := transferArtifact(t, f, token); a.State != "deleting" {
		t.Fatal("uncertain deletion lost durable intent", a.State)
	}
	if r := transferAudit(t, f); r.Usage.Charged != 4 {
		t.Fatal("unconfirmed deletion freed budget", r)
	}
	r, err := transferManager(f).Reconcile(context.Background())
	if err != nil || r.Removed != 1 || r.Usage.Charged != 0 {
		t.Fatal("missing-file retry failed", r, err)
	}
}
func TestTransferDeleteIntentAuditFailurePreservesBytes(t *testing.T) {
	f := newFixture(t)
	h, _ := sealedTransfer(t, f)
	token, path := h.Token(), h.Path()
	h.Close(false)
	transferExpire(t, f, token)
	const hook = "test:transfer_intent_failure"
	if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if row, ok := tx.Statement.Dest.(*model.OperationLog); ok && row.Action == "transfer_delete_intent" {
			tx.AddError(errors.New("injected deletion intent audit failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	_, err := transferManager(f).Reconcile(context.Background())
	f.db.Callback().Create().Remove(hook)
	if err == nil {
		t.Fatal("missing deletion intent audit ignored")
	}
	data, e := os.ReadFile(path)
	if e != nil || string(data) != "AAAA" || transferAudit(t, f).Usage.Charged != 4 {
		t.Fatal("uncommitted intent changed bytes/budget", e)
	}
	if a := transferArtifact(t, f, token); a.State != "ready" {
		t.Fatal("failed intent escaped transaction", a.State)
	}
}
func TestTransferUnprovenIntentAndReplacedInodeAreBlocked(t *testing.T) {
	f := newFixture(t)
	id := immutableInit(t, f, 4, 4)
	task := receiptTask(t, f, id)
	const hook = "test:transfer_inode_registration_failure"
	if err := f.db.Callback().Update().Before("gorm:update").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "transfer_artifact" {
			if changes, ok := tx.Statement.Dest.(map[string]interface{}); ok && changes["state"] == "writing" {
				tx.AddError(errors.New("injected inode registration failure"))
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	h, err := transferManager(f).Create(context.Background(), f.aliceID, task.ID, "chunk", 5, 0)
	f.db.Callback().Update().Remove(hook)
	if err == nil {
		h.Close(true)
		t.Fatal("partial registration did not fail")
	}
	var a model.TransferArtifact
	if err := f.db.First(&a, "task_id=?", task.ID).Error; err != nil {
		t.Fatal(err)
	}
	transferExpire(t, f, a.Token)
	r, err := transferManager(f).Reconcile(context.Background())
	if !errors.Is(err, transfer.ErrPending) || r.Blocked != 1 || r.Usage.Charged != 5 {
		t.Fatal("unproven file adopted", r, err)
	}
	path := filepath.Join(f.cfg.DataDir, "storage", a.RelativePath)
	if _, err := os.Stat(path); err != nil {
		t.Fatal("unproven intent path deleted")
	}
	second, _ := sealedTransfer(t, f)
	secondToken, secondPath := second.Token(), second.Path()
	second.Close(false)
	transferExpire(t, f, secondToken)
	if err := os.Rename(secondPath, secondPath+".held"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte("BBBB"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err = transferManager(f).Reconcile(context.Background())
	if !errors.Is(err, transfer.ErrPending) || r.Blocked != 2 {
		t.Fatal("substituted inode adopted", r, err)
	}
	for _, p := range []string{secondPath, secondPath + ".held"} {
		if _, err := os.Stat(p); err != nil {
			t.Fatal("replacement evidence removed", err)
		}
	}
	encoded, _ := json.Marshal(r)
	if bytes.Contains(encoded, []byte(f.cfg.DataDir)) {
		t.Fatal("report leaked private paths")
	}
}
func TestTransferUnexpectedStorageAliasIsRetainedAndReported(t *testing.T) {
	f := newFixture(t)
	h, _ := sealedTransfer(t, f)
	token, path := h.Token(), h.Path()
	h.Close(false)
	transferExpire(t, f, token)
	size := int64(4)
	row := model.Storage{FileHash: fmt.Sprintf("%x", sha256.Sum256([]byte("alias metadata"))), FilePath: path, FileSize: &size}
	if err := f.db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	r, err := transferManager(f).Reconcile(context.Background())
	if !errors.Is(err, transfer.ErrPending) || r.Blocked != 1 || r.Usage.Charged != 4 {
		t.Fatal("published alias not protected", r, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "AAAA" {
		t.Fatal("storage alias bytes removed", err)
	}
}
func TestTransferSingleMergeClaimAndGlobalMergeCeiling(t *testing.T) {
	f := newFixture(t)
	m := transferManager(f)
	var tasks []model.UploadTask
	for i := range 5 {
		id := fixturePendingUpload(t, f, f.alice, fmt.Sprintf("merge-claim-%d.bin", i), []byte("AAAA"))
		tasks = append(tasks, receiptTask(t, f, id))
	}
	var handles []*transfer.Handle
	defer func() {
		for _, h := range handles {
			h.Close(true)
		}
	}()
	for i := range 4 {
		h, err := m.Create(context.Background(), f.aliceID, tasks[i].ID, "merge", 4, 0)
		if err != nil {
			t.Fatal(err)
		}
		handles = append(handles, h)
	}
	if h, err := m.Create(context.Background(), f.aliceID, tasks[0].ID, "merge", 4, 0); err == nil {
		h.Close(true)
		t.Fatal("second merger admitted for same task")
	}
	if h, err := m.Create(context.Background(), f.aliceID, tasks[4].ID, "merge", 4, 0); err == nil {
		h.Close(true)
		t.Fatal("volume merge concurrency exceeded")
	}
	transferExpire(t, f, handles[0].Token())
	if h, err := m.Create(context.Background(), f.aliceID, tasks[0].ID, "merge", 4, 0); err == nil {
		h.Close(true)
		t.Fatal("expired but open merge claim stolen")
	}
	if err := handles[0].Close(true); err != nil {
		t.Fatal(err)
	}
	h, err := m.Create(context.Background(), f.aliceID, tasks[4].ID, "merge", 4, 0)
	if err != nil {
		t.Fatal("closed claim did not release merge slot", err)
	}
	handles = append(handles, h)
}
func TestTransferReconcileIsBoundedAndNeverScansUnknownFiles(t *testing.T) {
	f := newFixture(t)
	m := transferManager(f)
	if err := m.SetLimit(context.Background(), 1024); err != nil {
		t.Fatal(err)
	}
	volume := transferAudit(t, f).VolumeID
	id := immutableInit(t, f, 4, 4)
	task := receiptTask(t, f, id)
	if err := f.db.Model(&task).Update("transfer_volume", volume).Error; err != nil {
		t.Fatal(err)
	}
	var records []model.TransferArtifact
	for range 70 {
		token, err := blob.RandomToken()
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, model.TransferArtifact{Token: token, VolumeID: volume, RelativePath: filepath.Join(".chunks-v1", "chunk-"+token+".part"), UserID: f.aliceID, TaskID: task.ID, Kind: "chunk", State: "intent", Bytes: 1, ExpiresAt: time.Now().UTC().Add(-time.Hour)})
	}
	if err := f.db.Create(&records).Error; err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(f.cfg.DataDir, "storage", ".chunks-v1", "chunk-"+strings.Repeat("a", 64)+".part")
	if err := os.MkdirAll(filepath.Dir(unknown), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unknown, []byte("unregistered evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := m.Reconcile(context.Background())
	if err != nil || r.Checked != 64 || r.Removed != 64 || r.Usage.Charged != 6 {
		t.Fatal("unbounded/incorrect first pass", r, err)
	}
	r, err = m.Reconcile(context.Background())
	if err != nil || r.Removed != 6 || r.Usage.Charged != 0 {
		t.Fatal("bounded recovery incomplete", r, err)
	}
	data, err := os.ReadFile(unknown)
	if err != nil || string(data) != "unregistered evidence" {
		t.Fatal("prefix scan adopted unknown bytes", err)
	}
}
func TestTransferFilesystemHeadroomRejectsBeforeCandidateCreation(t *testing.T) {
	f := newFixture(t)
	m := transferManager(f)
	if err := m.SetLimit(context.Background(), 64); err != nil {
		t.Fatal(err)
	}
	volume := transferAudit(t, f).VolumeID
	if err := f.db.Model(&model.TransferVolume{}).Where("id=?", volume).Update("min_free_bytes", int64(math.MaxInt64)).Error; err != nil {
		t.Fatal(err)
	}
	id := immutableInit(t, f, 4, 4)
	sendChunk(t, f, id, 0, []byte("AAAA"), "", 507)
	if chunkFiles(t, f) != 0 || transferAudit(t, f).Usage.Charged != 0 {
		t.Fatal("headroom failure created a candidate/reservation")
	}
}

func TestTransferCancelAndExpiryCleanOnlyRegisteredUnpublishedFiles(t *testing.T) {
	f := newFixture(t)
	m := transferManager(f)
	published := f.upload(t, f.alice, "published-retained.bin", []byte("retained"))
	waitNoReadLeases(t, f, *published.StorageID)
	cancelID := fixturePendingUpload(t, f, f.alice, "cancel-artifacts.bin", []byte("AAAA"))
	cancelPath := chunkRow(t, f, cancelID).ChunkPath
	f.json(t, "POST", "/api/files/upload/cancel", f.alice, map[string]any{"upload_id": cancelID}, 200)
	r, err := m.Reconcile(context.Background())
	if err != nil || r.Removed != 1 {
		t.Fatal("canceled chunk not reclaimed", r, err)
	}
	if _, err := os.Stat(cancelPath); !os.IsNotExist(err) {
		t.Fatal("canceled path retained")
	}
	expiredID := fixturePendingUpload(t, f, f.alice, "expired-artifacts.bin", []byte("BBBB"))
	expiredPath := chunkRow(t, f, expiredID).ChunkPath
	if err := f.db.Model(&model.UploadTask{}).Where("upload_id=?", expiredID).Update("expires_at", gorm.Expr("DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 1 SECOND)")).Error; err != nil {
		t.Fatal(err)
	}
	r, err = m.Reconcile(context.Background())
	if err != nil || r.Removed != 1 || r.Usage.Charged != 0 {
		t.Fatal("expired chunk not reclaimed", r, err)
	}
	if _, err := os.Stat(expiredPath); !os.IsNotExist(err) {
		t.Fatal("expired path retained")
	}
	if w := f.json(t, "GET", fmt.Sprintf("/api/files/%d/download", published.ID), f.alice, nil, 200); w.Body.String() != "retained" {
		t.Fatal("reconciler touched published content")
	}
}
func TestTransferHeartbeatRenewsBeforeOldDeadline(t *testing.T) {
	f := newFixture(t)
	h, _ := newTransferHandle(t, f, "chunk", 5)
	defer h.Close(true)
	if err := f.db.Model(&model.TransferArtifact{}).Where("token=?", h.Token()).Update("expires_at", gorm.Expr("DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 30 SECOND)")).Error; err != nil {
		t.Fatal(err)
	}
	original := transferArtifact(t, f, h.Token()).ExpiresAt
	deadline := time.Now().Add(28 * time.Second)
	renewed := false
	for time.Now().Before(deadline) {
		if transferArtifact(t, f, h.Token()).ExpiresAt.After(original.Add(30 * time.Second)) {
			renewed = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !renewed {
		t.Fatal("real lease heartbeat never advanced database deadline")
	}
	wait := time.Until(original.Add(time.Second))
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		<-timer.C
	}
	if _, err := io.WriteString(h, "AAAA"); err != nil {
		t.Fatal(err)
	}
	if err := h.Seal(4, fmt.Sprintf("%x", sha256.Sum256([]byte("AAAA")))); err != nil {
		t.Fatal("renewed writer rejected after old deadline", err)
	}
}
func TestTransferSealRejectsForgedDigestAndOverrun(t *testing.T) {
	f := newFixture(t)
	h, _ := newTransferHandle(t, f, "chunk", 5)
	defer h.Close(true)
	if _, err := io.WriteString(h, "123456"); err == nil {
		t.Fatal("writer exceeded reserved capacity")
	}
	if _, err := io.WriteString(h, "AAAA"); err != nil {
		t.Fatal(err)
	}
	if err := h.Seal(4, strings.Repeat("0", 64)); err == nil {
		t.Fatal("caller-supplied false digest accepted")
	}
	if err := h.Seal(4, fmt.Sprintf("%x", sha256.Sum256([]byte("AAAA")))); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(h, "x"); err == nil {
		t.Fatal("sealed writer accepted further bytes")
	}
}
func TestTransferPublicationAuditFailureRollsBackHandoff(t *testing.T) {
	f := newFixture(t)
	id := fixturePendingUpload(t, f, f.alice, "handoff-rollback.bin", []byte("AAAA"))
	const hook = "test:transfer_publication_audit_failure"
	if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if row, ok := tx.Statement.Dest.(*model.OperationLog); ok && row.Action == "transfer_published" {
			tx.AddError(errors.New("injected transfer publication audit failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	response := completeUploadRecorder(f, f.alice, id)
	f.db.Callback().Create().Remove(hook)
	if response.Code != 500 {
		t.Fatal("failed handoff audit not returned", response.Code, response.Body.String())
	}
	task := receiptTask(t, f, id)
	if task.Status != 0 || task.MergeArtifact != "" || task.ResultFileID != nil {
		t.Fatal("failed handoff escaped transaction")
	}
	if r := transferAudit(t, f); r.Usage.Charged != 4 {
		t.Fatal("failed output not cleaned or input charge lost", r)
	}
	completedBlob(t, f, completeUploadRecorder(f, f.alice, id))
}
func TestTransferVolumeAffinityAndInvalidPolicyFailClosed(t *testing.T) {
	f := newFixture(t)
	id := fixturePendingUpload(t, f, f.alice, "volume-affinity.bin", []byte("AAAA"))
	task := receiptTask(t, f, id)
	otherCfg := f.cfg
	otherCfg.DataDir = t.TempDir()
	other := transfer.NewManager(f.db, otherCfg)
	if h, err := other.Create(context.Background(), f.aliceID, task.ID, "chunk", 5, 0); err == nil {
		h.Close(true)
		t.Fatal("task silently moved to another physical volume")
	}
	m := transferManager(f)
	volume := transferAudit(t, f).VolumeID
	if err := f.db.Model(&model.TransferVolume{}).Where("id=?", volume).Update("limit_bytes", 0).Error; err != nil {
		t.Fatal(err)
	}
	sendChunk(t, f, id, 0, []byte("AAAA"), "", 503)
	f.json(t, "POST", "/api/files/upload/cancel", f.alice, map[string]any{"upload_id": id}, 200)
	report, err := m.Reconcile(context.Background())
	if !errors.Is(err, transfer.ErrPending) || report.Usage.Charged != 0 || report.Usage.Broken == 0 {
		t.Fatal("bad policy prevented cleanup or appeared healthy", report, err)
	}
	if err := m.SetLimit(context.Background(), 16); err != nil {
		t.Fatal(err)
	}
	if r := transferAudit(t, f); r.Usage.Broken != 0 || r.LimitBytes != 16 {
		t.Fatal("policy repair not visible", r)
	}
}
func TestTransferExplicitUpgradePreservesUnknownOldChunks(t *testing.T) {
	f := newFixture(t)
	id := fixturePendingUpload(t, f, f.alice, "pre-registry.bin", []byte("AAAA"))
	path := chunkRow(t, f, id).ChunkPath
	for _, sql := range []string{"DROP TABLE transfer_artifact", "DROP TABLE transfer_volume", "ALTER TABLE upload_task DROP COLUMN transfer_protocol, DROP COLUMN transfer_volume, DROP COLUMN merge_artifact", "ALTER TABLE upload_chunk DROP COLUMN artifact_token"} {
		if err := f.db.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CheckFileSchema(f.db); err == nil {
		t.Fatal("startup accepted missing registry schema")
	}
	if err := store.Migrate(f.db); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckFileSchema(f.db); err != nil {
		t.Fatal(err)
	}
	task := receiptTask(t, f, id)
	if task.TransferProtocol != 0 || task.TransferVolume != "" {
		t.Fatal("migration fabricated transfer ownership")
	}
	sendChunk(t, f, id, 0, []byte("AAAA"), "", 409)
	f.json(t, "POST", "/api/files/upload/cancel", f.alice, map[string]any{"upload_id": id}, 200)
	m := transferManager(f)
	if err := m.SetLimit(context.Background(), 32); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "AAAA" {
		t.Fatal("old unregistered bytes adopted or removed", err)
	}
	if err := f.db.Exec("ALTER TABLE transfer_artifact DROP INDEX idx_transfer_path").Error; err != nil {
		t.Fatal(err)
	}
	if err := store.CheckFileSchema(f.db); err == nil {
		t.Fatal("changed registry uniqueness accepted")
	}
	if err := store.Migrate(f.db); err != nil {
		t.Fatal(err)
	}
}
func TestTransferMaintenanceCLIUsesOnlyExplicitGuardedRoot(t *testing.T) {
	f := newFixture(t)
	m := transferManager(f)
	if err := m.SetLimit(context.Background(), 64); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "cloudnest-transfer")
	if output, err := exec.Command("go", "build", "-o", binary, "../cmd/server").CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	run := func(root bool, args ...string) ([]byte, error) {
		cmd := exec.Command(binary, args...)
		cmd.Dir = t.TempDir()
		cmd.Env = []string{"MYSQL_DSN=" + f.cfg.MySQLDSN}
		if root {
			cmd.Env = append(cmd.Env, "DATA_DIR="+f.cfg.DataDir)
		}
		return cmd.CombinedOutput()
	}
	if output, err := run(false, "reconcile-transfers"); err == nil || !bytes.Contains(output, []byte("explicit DATA_DIR")) {
		t.Fatal("implicit root accepted", err, string(output))
	}
	var before int64
	_ = f.db.Model(&model.OperationLog{}).Count(&before).Error
	output, err := run(true, "audit-transfers")
	if err != nil || !bytes.Contains(output, []byte(`"limit_bytes":64`)) || bytes.Contains(output, []byte(f.cfg.DataDir)) {
		t.Fatal("CLI audit failed/leaked path", err, string(output))
	}
	var after int64
	_ = f.db.Model(&model.OperationLog{}).Count(&after).Error
	if after != before {
		t.Fatal("audit wrote metadata")
	}
	if output, err := run(true, "set-transfer-limit", "32"); err != nil {
		t.Fatal(err, string(output))
	}
	if transferAudit(t, f).LimitBytes != 32 {
		t.Fatal("CLI policy change not persisted")
	}
	h, _ := sealedTransfer(t, f)
	h.Close(false)
	transferExpire(t, f, h.Token())
	if output, err := run(true, "reconcile-transfers"); err != nil || !bytes.Contains(output, []byte(`"removed":1`)) {
		t.Fatal("CLI reconciliation failed", err, string(output))
	}
}

func TestTransferServerSIGTERMInterruptsPartialBodyAndClosesWriter(t *testing.T) {
	f := newFixture(t)
	m := transferManager(f)
	if err := m.SetLimit(context.Background(), 64); err != nil {
		t.Fatal(err)
	}
	id := immutableInit(t, f, 4, 4)
	task := receiptTask(t, f, id)
	binary := filepath.Join(t.TempDir(), "cloudnest-server")
	if output, err := exec.Command("go", "build", "-o", binary, "../cmd/server").CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	command := exec.Command(binary)
	command.Dir = t.TempDir()
	command.Env = []string{"MYSQL_DSN=" + f.cfg.MySQLDSN, "REDIS_ADDR=" + f.cfg.RedisAddr, "JWT_SECRET=" + f.cfg.JWTSecret, "JWT_EXPIRE_HOURS=1", "DATA_DIR=" + f.cfg.DataDir, "APP_BIND_ADDR=127.0.0.1", "TRANSFER_MAINTENANCE_DSN=" + f.cfg.MySQLDSN, fmt.Sprintf("APP_PORT=%d", port), "AI_OLLAMA_BASE_URL=http://127.0.0.1:1"}
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- command.Wait() }()
	stopped := false
	defer func() {
		if !stopped {
			command.Process.Kill()
			<-finished
		}
	}()
	address := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: time.Second}
	ready := false
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		req, _ := http.NewRequest("GET", address+"/api/auth/me", nil)
		req.Header.Set("Authorization", "Bearer "+f.alice)
		response, e := client.Do(req)
		if e == nil {
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode == 200 {
				ready = true
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatal("isolated server did not start")
	}
	body, ct := chunkBody(t, id, 0, []byte("AAAA"), "")
	raw := body.Bytes()
	split := bytes.Index(raw, []byte("AAAA"))
	if split < 0 {
		t.Fatal("missing test payload")
	}
	reader, writer := io.Pipe()
	defer writer.Close()
	defer reader.Close()
	req, _ := http.NewRequest("POST", address+"/api/files/upload/chunk", reader)
	req.Header.Set("Authorization", "Bearer "+f.alice)
	req.Header.Set("Content-Type", ct)
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		response, _ := (&http.Client{}).Do(req)
		if response != nil {
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
		}
	}()
	if _, err := writer.Write(raw[:split]); err != nil {
		t.Fatal(err)
	}
	writerReady := false
	until = time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		var n int64
		if err := f.db.Model(&model.TransferArtifact{}).Where("task_id=? AND state='writing'", task.ID).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n == 1 {
			writerReady = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !writerReady {
		t.Fatal("server never opened registered writer")
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		stopped = true
		if err != nil {
			t.Fatal("graceful server exit failed", err, output.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not interrupt partial body")
	}
	writer.Close()
	select {
	case <-requestDone:
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP request outlived stopped server")
	}
	report, err := m.Reconcile(context.Background())
	if err != nil && !errors.Is(err, transfer.ErrPending) {
		t.Fatal(err)
	}
	if report.Usage.Charged != 0 {
		t.Fatal("graceful shutdown left registered writer charged", report)
	}
	if receiptTask(t, f, id).Status != 0 {
		t.Fatal("partial body published or canceled task during shutdown")
	}
}

func TestTransferCleanupRefusesRealAndHiddenTriggers(t *testing.T) {
	f := newFixture(t)
	h, _ := sealedTransfer(t, f)
	token, path := h.Token(), h.Path()
	h.Close(false)
	transferExpire(t, f, token)
	trigger := fmt.Sprintf("cn_transfer_visibility_%d", f.aliceID)
	if err := f.db.Exec("CREATE TRIGGER " + trigger + " BEFORE UPDATE ON transfer_artifact FOR EACH ROW SET @cn_transfer_trigger=1").Error; err != nil {
		t.Fatal(err)
	}
	defer f.db.Exec("DROP TRIGGER IF EXISTS " + trigger)
	if _, err := transferManager(f).Reconcile(context.Background()); err == nil {
		t.Fatal("visible trigger accepted")
	}
	cfg := f.cfg
	parsed, err := mysqldriver.ParseDSN(cfg.MySQLDSN)
	if err != nil {
		t.Fatal(err)
	}
	parsed.User = "cloudnest_gc_limited"
	parsed.Passwd = "cloudnest-disposable-test"
	cfg.MySQLDSN = parsed.FormatDSN()
	limited, err := store.NewMySQL(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if sqlDB, err := limited.DB(); err == nil {
		defer sqlDB.Close()
	}
	if _, err := transfer.NewManager(limited, cfg).Reconcile(context.Background()); err == nil {
		t.Fatal("hidden trigger absence accepted")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "AAAA" {
		t.Fatal("trigger preflight touched registered bytes", err)
	}
	if err := f.db.Exec("DROP TRIGGER " + trigger).Error; err != nil {
		t.Fatal(err)
	}
	if r, err := transferManager(f).Reconcile(context.Background()); err != nil || r.Removed != 1 || r.Usage.Charged != 0 {
		t.Fatal("reviewed trigger removal did not restore cleanup", r, err)
	}
}
func TestTransferVolumeIsBoundToItsActualDatabase(t *testing.T) {
	f := newFixture(t)
	m := transferManager(f)
	if err := m.SetLimit(context.Background(), 64); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(f.cfg.DataDir, "storage", ".transfer-volume-v1")
	before, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	// The original fixture verified loopback, disposable marker and server/user.
	// information_schema is the same disposable server's read-only metadata DB.
	parsed, err := mysqldriver.ParseDSN(f.cfg.MySQLDSN)
	if err != nil {
		t.Fatal(err)
	}
	parsed.DBName = "information_schema"
	cfg := f.cfg
	cfg.MySQLDSN = parsed.FormatDSN()
	other, err := store.NewMySQL(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if sqlDB, err := other.DB(); err == nil {
		defer sqlDB.Close()
	}
	var writes int
	const hook = "test:foreign_database_must_not_write"
	if err := other.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) { writes++ }); err != nil {
		t.Fatal(err)
	}
	defer other.Callback().Create().Remove(hook)
	original, err := transfer.DatabaseIdentity(context.Background(), f.db)
	if err != nil {
		t.Fatal(err)
	}
	different, err := transfer.DatabaseIdentity(context.Background(), other)
	if err != nil || original == different {
		t.Fatal("database identities not distinct", err)
	}
	if err := transfer.NewManager(other, cfg).SetLimit(context.Background(), 128); err == nil || writes != 0 {
		t.Fatal("wrong database reached a write", err, writes)
	}
	after, err := os.ReadFile(marker)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("wrong connection rewrote volume binding", err)
	}
	if transferAudit(t, f).LimitBytes != 64 {
		t.Fatal("wrong connection changed actual policy")
	}
}
func TestTransferKnownPreFileFailureDoesNotLeaveMergeStuck(t *testing.T) {
	f := newFixture(t)
	id := fixturePendingUpload(t, f, f.alice, "publication-retry.bin", []byte("AAAA"))
	month := filepath.Join(f.cfg.DataDir, "storage", ".managed-v1", time.Now().Format("200601"))
	if err := os.MkdirAll(month, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(month, 0777); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(month, 0700)
	if w := completeUploadRecorder(f, f.alice, id); w.Code != 503 {
		t.Fatal("unsafe directory not rejected", w.Code)
	}
	if task := receiptTask(t, f, id); task.MergeArtifact != "" {
		t.Fatal("known no-file failure stranded a merge claim")
	}
	if err := os.Chmod(month, 0700); err != nil {
		t.Fatal(err)
	}
	completedBlob(t, f, completeUploadRecorder(f, f.alice, id))
}
