//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"cloudenest_go/internal/blob"
	"cloudenest_go/internal/config"
	"cloudenest_go/internal/maintenance"
	"cloudenest_go/internal/store"
	"cloudenest_go/model"
	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

func gcStorage(t *testing.T, f *fixture, id int64) model.Storage {
	t.Helper()
	var s model.Storage
	if err := f.db.First(&s, id).Error; err != nil {
		t.Fatal(err)
	}
	return s
}
func gcRun(t *testing.T, f *fixture) maintenance.GCReport {
	t.Helper()
	r, err := maintenance.RunStorageGC(context.Background(), f.db, filepath.Join(f.cfg.DataDir, "storage"))
	if err != nil {
		t.Fatalf("GC failed: %+v %v", r, err)
	}
	return r
}
func gcAge(t *testing.T, f *fixture, id int64, column string) {
	t.Helper()
	if column != "orphaned_at" && column != "quarantined_at" {
		t.Fatal("unsafe test age column")
	}
	// Only guarded disposable fixture timestamps are moved; production grace
	// periods have no configuration/CLI bypass and are never slept through here.
	if err := f.db.Model(&model.Storage{}).Where("id=?", id).Update(column, gorm.Expr("DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 25 HOUR)")).Error; err != nil {
		t.Fatal(err)
	}
}
func gcOrphan(t *testing.T, f *fixture, name string) model.Storage {
	t.Helper()
	file := f.upload(t, f.alice, name, []byte("managed disposable payload "+name))
	waitNoReadLeases(t, f, *file.StorageID)
	f.json(t, "DELETE", fmt.Sprintf("/api/files/%d", file.ID), f.alice, nil, 200)
	f.json(t, "DELETE", fmt.Sprintf("/api/files/%d/permanent", file.ID), f.alice, nil, 200)
	return gcStorage(t, f, *file.StorageID)
}
func gcQuarantine(t *testing.T, f *fixture, s model.Storage) model.Storage {
	t.Helper()
	if r := gcRun(t, f); r.Marked != 1 {
		t.Fatalf("first observation: %+v", r)
	}
	gcAge(t, f, s.ID, "orphaned_at")
	if r := gcRun(t, f); r.Quarantined != 1 {
		t.Fatalf("quarantine: %+v", r)
	}
	return gcStorage(t, f, s.ID)
}
func gcBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("retained bytes changed: %v", err)
	}
}

func TestStorageGCTwoGracePeriodsAndDurableTombstone(t *testing.T) {
	f := newFixture(t)
	s := gcOrphan(t, f, "two-graces.txt")
	payload, err := os.ReadFile(s.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Managed || s.ManagedRoot == "" || s.ManagedPath == "" {
		t.Fatal("publisher omitted explicit ownership")
	}
	if r := gcRun(t, f); r.Marked != 1 || r.Quarantined != 0 {
		t.Fatalf("first pass: %+v", r)
	}
	if r := gcRun(t, f); r.Quarantined != 0 {
		t.Fatal("first grace bypassed")
	}
	gcBytes(t, s.FilePath, payload)
	gcAge(t, f, s.ID, "orphaned_at")
	if r := gcRun(t, f); r.Quarantined != 1 || r.Tombstoned != 0 {
		t.Fatalf("quarantine pass: %+v", r)
	}
	s = gcStorage(t, f, s.ID)
	trash := filepath.Join(s.ManagedRoot, s.GCPath)
	gcBytes(t, trash, payload)
	if _, err := os.Stat(s.FilePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("original still present:", err)
	}
	if r := gcRun(t, f); r.Tombstoned != 0 || r.Unlinked != 0 {
		t.Fatal("second grace bypassed")
	}
	gcAge(t, f, s.ID, "quarantined_at")
	expiredToken, err := blob.RandomToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec("INSERT INTO storage_read_lease(token,storage_id,expires_at) VALUES (?,?,DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 1 HOUR))", expiredToken, s.ID).Error; err != nil {
		t.Fatal(err)
	}
	if r := gcRun(t, f); r.Tombstoned != 1 || r.Unlinked != 1 || r.ExpiredLeasesPruned != 1 {
		t.Fatalf("final pass: %+v", r)
	}
	var count int64
	if err := f.db.Model(&model.Storage{}).Where("id=?", s.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("parent not tombstoned:", err)
	}
	if _, err := os.Stat(trash); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("quarantined bytes not removed:", err)
	}
	var journal model.StorageGarbage
	if err := f.db.First(&journal, "token=?", s.GCToken).Error; err != nil || journal.Status != "done" || journal.CompletedAt == nil {
		t.Fatal("durable completion missing:", err)
	}
	if err := f.db.Model(&model.OperationLog{}).Where("action='storage_gc_tombstone' AND detail=?", fmt.Sprintf("storage_id=%d", s.ID)).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("tombstone audit missing:", err)
	}
	if r := gcRun(t, f); r.Tombstoned != 0 || r.Unlinked != 0 {
		t.Fatal("repeat was not idempotent")
	}
}

func TestStorageGCRetainsRecycleUnknownVersionsAndActiveReaders(t *testing.T) {
	f := newFixture(t)
	a := f.upload(t, f.alice, "retain-a.txt", []byte("shared bytes"))
	b := f.upload(t, f.alice, "retain-b.txt", []byte("shared bytes"))
	waitNoReadLeases(t, f, *a.StorageID)
	if *a.StorageID != *b.StorageID {
		t.Fatal("fixture not deduplicated")
	}
	for _, file := range []model.File{a, b} {
		f.json(t, "DELETE", fmt.Sprintf("/api/files/%d", file.ID), f.alice, nil, 200)
	}
	if r := gcRun(t, f); r.Scanned != 0 {
		t.Fatal("recycle references ignored")
	}
	s := gcStorage(t, f, *a.StorageID)
	ctx, reader := blob.NewManager(f.db).Begin(context.Background())
	defer reader.Close()
	if _, err := blob.Pin(ctx, s); err != nil {
		t.Fatal(err)
	}
	historical := model.FileVersion{FileID: 999999999, Version: 1, StorageID: s.ID, SourceVerified: false}
	if err := f.db.Create(&historical).Error; err != nil {
		t.Fatal(err)
	}
	for _, file := range []model.File{a, b} {
		f.json(t, "DELETE", fmt.Sprintf("/api/files/%d/permanent", file.ID), f.alice, nil, 200)
	}
	if r := gcRun(t, f); r.Scanned != 0 {
		t.Fatal("unknown history ignored")
	}
	if err := f.db.Delete(&historical).Error; err != nil {
		t.Fatal(err)
	}
	if r := gcRun(t, f); r.Scanned != 0 {
		t.Fatal("active reader ignored")
	}
	gcBytes(t, s.FilePath, []byte("shared bytes"))
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if r := gcRun(t, f); r.Marked != 1 {
		t.Fatal("unreferenced unleased object was not observed")
	}
}

func TestStorageGCRestoresQuarantineWhenNewReferenceAppears(t *testing.T) {
	f := newFixture(t)
	s := gcOrphan(t, f, "late-reference.txt")
	payload, _ := os.ReadFile(s.FilePath)
	s = gcQuarantine(t, f, s)
	// An outside writer still using legal FK inserts can race the maintenance
	// intent. GC must restore retained bytes rather than trusting its old count.
	file := model.File{UserID: f.aliceID, Filename: "late-ref.txt", StorageID: &s.ID, Size: *s.FileSize}
	if err := f.db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "GET", fmt.Sprintf("/api/files/%d/download", file.ID), f.alice, nil, http.StatusServiceUnavailable)
	if r := gcRun(t, f); r.Restored != 1 || r.Tombstoned != 0 {
		t.Fatalf("late reference: %+v", r)
	}
	current := gcStorage(t, f, s.ID)
	if current.State != blob.Active || current.GCPath != "" || current.OrphanedAt != nil {
		t.Fatal("restore state incomplete")
	}
	gcBytes(t, current.FilePath, payload)
	if _, err := os.Stat(filepath.Join(s.ManagedRoot, s.GCPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("restore left quarantine link")
	}
	w := f.json(t, "GET", fmt.Sprintf("/api/files/%d/download", file.ID), f.alice, nil, 200)
	if !bytes.Equal(w.Body.Bytes(), payload) {
		t.Fatal("restored download changed")
	}
}

func TestStorageGCRecoversMovedFileBeforeQuarantineCommit(t *testing.T) {
	f := newFixture(t)
	s := gcOrphan(t, f, "quarantine-interruption.txt")
	payload, _ := os.ReadFile(s.FilePath)
	gcRun(t, f)
	gcAge(t, f, s.ID, "orphaned_at")
	const hook = "test:gc_quarantine_commit_failure"
	if err := f.db.Callback().Update().Before("gorm:update").Register(hook, func(tx *gorm.DB) {
		if values, ok := tx.Statement.Dest.(map[string]interface{}); ok && tx.Statement.Table == "storage" && values["state"] == "quarantined" {
			tx.AddError(errors.New("injected quarantine update failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Callback().Update().Remove(hook) })
	if _, err := maintenance.RunStorageGC(context.Background(), f.db, s.ManagedRoot); !errors.Is(err, maintenance.ErrGCPending) {
		t.Fatal("injected interruption ignored:", err)
	}
	if err := f.db.Callback().Update().Remove(hook); err != nil {
		t.Fatal(err)
	}
	s = gcStorage(t, f, s.ID)
	if s.State != "quarantining" {
		t.Fatal("lost durable intent")
	}
	gcBytes(t, filepath.Join(s.ManagedRoot, s.GCPath), payload)
	if r := gcRun(t, f); r.Quarantined != 1 {
		t.Fatalf("failed to resume already moved object: %+v", r)
	}
}

func TestStorageGCTombstoneAuditFailurePreservesParentAndBytes(t *testing.T) {
	f := newFixture(t)
	s := gcOrphan(t, f, "tombstone-audit.txt")
	payload, _ := os.ReadFile(s.FilePath)
	s = gcQuarantine(t, f, s)
	gcAge(t, f, s.ID, "quarantined_at")
	const hook = "test:gc_tombstone_audit_failure"
	if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if log, ok := tx.Statement.Dest.(*model.OperationLog); ok && log.Action == "storage_gc_tombstone" {
			tx.AddError(errors.New("injected audit failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Callback().Create().Remove(hook) })
	if _, err := maintenance.RunStorageGC(context.Background(), f.db, s.ManagedRoot); !errors.Is(err, maintenance.ErrGCPending) {
		t.Fatal("audit failure ignored:", err)
	}
	if err := f.db.Callback().Create().Remove(hook); err != nil {
		t.Fatal(err)
	}
	gcStorage(t, f, s.ID)
	gcBytes(t, filepath.Join(s.ManagedRoot, s.GCPath), payload)
	var count int64
	if err := f.db.Model(&model.StorageGarbage{}).Where("token=?", s.GCToken).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("failed tombstone left committed journal")
	}
	if r := gcRun(t, f); r.Tombstoned != 1 || r.Unlinked != 1 {
		t.Fatal("retry failed:", r)
	}
}

func TestStorageGCRecoversUnlinkBeforeJournalCompletion(t *testing.T) {
	f := newFixture(t)
	s := gcOrphan(t, f, "unlink-interruption.txt")
	s = gcQuarantine(t, f, s)
	gcAge(t, f, s.ID, "quarantined_at")
	const hook = "test:gc_unlink_completion_failure"
	if err := f.db.Callback().Update().Before("gorm:update").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "storage_garbage" {
			tx.AddError(errors.New("injected completion failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Callback().Update().Remove(hook) })
	report, err := maintenance.RunStorageGC(context.Background(), f.db, s.ManagedRoot)
	if !errors.Is(err, maintenance.ErrGCPending) || report.Tombstoned != 1 || report.Unlinked != 0 {
		t.Fatal("unlink completion failure ignored:", report, err)
	}
	if err := f.db.Callback().Update().Remove(hook); err != nil {
		t.Fatal(err)
	}
	var journal model.StorageGarbage
	if err := f.db.First(&journal, "token=?", s.GCToken).Error; err != nil || journal.Status != "pending" {
		t.Fatal("recovery marker lost")
	}
	if _, err := os.Stat(filepath.Join(s.ManagedRoot, s.GCPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("fixture did not reach post-unlink state")
	}
	republished := f.upload(t, f.alice, "republished-after-tombstone.txt", []byte("managed disposable payload unlink-interruption.txt"))
	if *republished.StorageID == s.ID {
		t.Fatal("removed storage ID was reused")
	}
	if r := gcRun(t, f); r.Unlinked != 1 || r.Tombstoned != 0 {
		t.Fatal("missing-file recovery failed:", r)
	}
	w := f.json(t, "GET", fmt.Sprintf("/api/files/%d/download", republished.ID), f.alice, nil, 200)
	if !bytes.Equal(w.Body.Bytes(), []byte("managed disposable payload unlink-interruption.txt")) {
		t.Fatal("old cleanup damaged new publication")
	}
}

func TestStorageGCRejectsCorruptionSymlinkAndPathEscape(t *testing.T) {
	for _, mode := range []string{"corrupt", "symlink", "escape"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			s := gcOrphan(t, f, "unsafe-"+mode+".txt")
			payload, _ := os.ReadFile(s.FilePath)
			outside := filepath.Join(t.TempDir(), "retained.txt")
			if err := os.WriteFile(outside, payload, 0600); err != nil {
				t.Fatal(err)
			}
			gcRun(t, f)
			gcAge(t, f, s.ID, "orphaned_at")
			switch mode {
			case "corrupt":
				if err := os.WriteFile(s.FilePath, []byte("corrupt evidence retained"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(s.FilePath); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, s.FilePath); err != nil {
					t.Fatal(err)
				}
			case "escape":
				if err := f.db.Model(&model.Storage{}).Where("id=?", s.ID).Updates(map[string]any{"managed_path": "../retained.txt", "file_path": outside}).Error; err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				r, err := maintenance.RunStorageGC(context.Background(), f.db, s.ManagedRoot)
				if !errors.Is(err, maintenance.ErrGCPending) || r.BlockedObjects != 1 {
					t.Fatalf("unsafe object not retained: %+v %v", r, err)
				}
			}
			gcBytes(t, outside, payload)
			current := gcStorage(t, f, s.ID)
			if current.State != "blocked" {
				t.Fatal("unsafe state did not block")
			}
		})
	}
}

func TestStorageGCNeverAdoptsLegacyPathsOrWrongRoots(t *testing.T) {
	f := newFixture(t)
	s := gcOrphan(t, f, "legacy-retention.txt")
	payload, _ := os.ReadFile(s.FilePath)
	// Even an object with a new-style filename and an old timestamp is not owned
	// by GC when the authoritative managed flag is false.
	if err := f.db.Model(&model.Storage{}).Where("id=?", s.ID).Updates(map[string]any{"managed": false, "orphaned_at": time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)}).Error; err != nil {
		t.Fatal(err)
	}
	if r := gcRun(t, f); r.Scanned != 0 {
		t.Fatal("legacy object adopted")
	}
	other := t.TempDir()
	if r, err := maintenance.RunStorageGC(context.Background(), f.db, other); err != nil || r.Scanned != 0 {
		t.Fatal("wrong root selected object:", r, err)
	}
	gcBytes(t, s.FilePath, payload)
	if err := os.Chmod(s.ManagedRoot, 0777); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(s.ManagedRoot, 0700)
	if _, err := maintenance.RunStorageGC(context.Background(), f.db, s.ManagedRoot); err == nil {
		t.Fatal("writable-by-others root accepted")
	}
	gcBytes(t, s.FilePath, payload)
	if err := os.Chmod(s.ManagedRoot, 0700); err != nil {
		t.Fatal(err)
	}
	month := filepath.Dir(s.FilePath)
	if err := os.Chmod(month, 0777); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(month, 0700)
	before, err := os.ReadDir(month)
	if err != nil {
		t.Fatal(err)
	}
	uploadID := fixturePendingUpload(t, f, f.alice, "unsafe-publication-root.txt", []byte("must-not-be-created"))
	if w := completeUploadRecorder(f, f.alice, uploadID); w.Code != 503 {
		t.Fatal("unsafe publication directory did not fail closed:", w.Code)
	}
	after, err := os.ReadDir(month)
	if err != nil || len(after) != len(before) {
		t.Fatal("publisher wrote into untrusted directory")
	}
}

func TestStorageGCConcurrentOperatorsSerializeOneIntent(t *testing.T) {
	f := newFixture(t)
	s := gcOrphan(t, f, "concurrent-gc.txt")
	var wg sync.WaitGroup
	reports := make(chan maintenance.GCReport, 6)
	errs := make(chan error, 6)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := maintenance.RunStorageGC(context.Background(), f.db, s.ManagedRoot)
			reports <- r
			errs <- err
		}()
	}
	wg.Wait()
	close(reports)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	marked := 0
	for r := range reports {
		marked += r.Marked
		if r.Quarantined != 0 || r.Tombstoned != 0 {
			t.Fatal("grace skipped by concurrent operator")
		}
	}
	if marked != 1 {
		t.Fatalf("observations=%d", marked)
	}
}

func TestStorageGCPrunesOnlyExpiredLeasesInBoundedBatches(t *testing.T) {
	f := newFixture(t)
	s := storageMetadataForTest(t, f, "prune-legacy")
	t.Cleanup(func() { f.db.Where("storage_id=?", s.ID).Delete(&model.StorageReadLease{}) })
	for i := range 261 {
		expiry := "DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 1 HOUR)"
		if i == 260 {
			expiry = "DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 1 HOUR)"
		}
		if err := f.db.Exec("INSERT INTO storage_read_lease(token,storage_id,expires_at) VALUES (?, ?, "+expiry+")", fmt.Sprintf("%032x%032x", s.ID, i), s.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []int64{256, 4, 0} {
		if got, err := maintenance.PruneExpiredReadLeases(context.Background(), f.db); err != nil || got != want {
			t.Fatalf("prune=%d want=%d err=%v", got, want, err)
		}
	}
	if leaseCount(t, f, s.ID) != 1 {
		t.Fatal("unexpired reader pruned")
	}
}

func TestStorageGCRefusesUnknownReferenceGraph(t *testing.T) {
	f := newFixture(t)
	s := gcOrphan(t, f, "unknown-reference.txt")
	payload, _ := os.ReadFile(s.FilePath)
	if err := f.db.Exec("CREATE TABLE extra_blob_ref (id BIGINT PRIMARY KEY,storage_id BIGINT,CONSTRAINT extra_blob_cascade FOREIGN KEY(storage_id) REFERENCES storage(id) ON DELETE CASCADE) ENGINE=InnoDB").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Exec("DROP TABLE IF EXISTS extra_blob_ref") })
	if _, err := maintenance.RunStorageGC(context.Background(), f.db, s.ManagedRoot); err == nil {
		t.Fatal("unknown incoming graph accepted")
	}
	gcBytes(t, s.FilePath, payload)
	if gcStorage(t, f, s.ID).OrphanedAt != nil {
		t.Fatal("unsafe schema pass mutated candidate")
	}
	if err := f.db.Exec("DROP TABLE extra_blob_ref").Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec("CREATE TABLE extra_blob_ref (id BIGINT PRIMARY KEY,storage_id BIGINT) ENGINE=InnoDB").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := maintenance.RunStorageGC(context.Background(), f.db, s.ManagedRoot); err == nil {
		t.Fatal("unconstrained reference column accepted")
	}
	if err := f.db.Exec("DROP TABLE extra_blob_ref").Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec("CREATE TRIGGER test_gc_unreviewed BEFORE DELETE ON storage FOR EACH ROW SET @gc_test_unreviewed=1").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Exec("DROP TRIGGER test_gc_unreviewed") })
	if _, err := maintenance.RunStorageGC(context.Background(), f.db, s.ManagedRoot); err == nil {
		t.Fatal("unreviewed trigger accepted")
	}
	limitedDSN, err := mysqldriver.ParseDSN(f.cfg.MySQLDSN)
	if err != nil {
		t.Fatal(err)
	}
	limitedDSN.User = "cloudnest_gc_limited" // Same already-guarded disposable endpoint.
	limited, err := store.NewMySQL(config.Config{MySQLDSN: limitedDSN.FormatDSN()})
	if err != nil {
		t.Fatal(err)
	}
	sqlLimited, err := limited.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlLimited.Close()
	if _, err := maintenance.RunStorageGC(context.Background(), limited, s.ManagedRoot); err == nil {
		t.Fatal("metadata-hidden trigger accepted by limited account")
	}

	if gcStorage(t, f, s.ID).OrphanedAt != nil {
		t.Fatal("preflight changed object")
	}

}

func TestStorageGCCLIRequiresRootAndExecutesOnlyGuardedFixture(t *testing.T) {
	f := newFixture(t)
	s := gcOrphan(t, f, "cli-gc.txt")
	binary := filepath.Join(t.TempDir(), "cloudnest")
	if output, err := exec.Command("go", "build", "-o", binary, "../cmd/server").CombinedOutput(); err != nil {
		t.Fatalf("build CLI %v %s", err, output)
	}
	run := func(command string, withRoot bool) ([]byte, error) {
		cmd := exec.Command(binary, command)
		cmd.Dir = t.TempDir()
		cmd.Env = []string{"MYSQL_DSN=" + f.cfg.MySQLDSN}
		if withRoot {
			cmd.Env = append(cmd.Env, "DATA_DIR="+f.cfg.DataDir)
		}
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		err := cmd.Run()
		return stdout.Bytes(), err
	}
	if out, err := run("gc-storage", false); err == nil || len(out) != 0 {
		t.Fatal("GC accepted implicit root")
	}
	check := func(want string) {
		t.Helper()
		out, err := run("gc-storage", true)
		if err != nil {
			t.Fatalf("GC CLI: %v %s", err, out)
		}
		var r map[string]any
		if err := json.Unmarshal(out, &r); err != nil || r[want] != float64(1) {
			t.Fatalf("GC CLI report: %s %v", out, err)
		}
	}
	check("marked")
	gcAge(t, f, s.ID, "orphaned_at")
	check("quarantined")
	gcAge(t, f, s.ID, "quarantined_at")
	check("unlinked")
	if out, err := run("prune-read-leases", false); err != nil || !bytes.Contains(out, []byte(`"pruned"`)) {
		t.Fatal("database-only prune CLI failed:", err)
	}
}

func TestStorageGCSchemaUpgradeDoesNotAdoptHistoricalObjects(t *testing.T) {
	f := newFixture(t)
	s := gcOrphan(t, f, "pre-gc-schema.txt")
	payload, _ := os.ReadFile(s.FilePath)
	if err := f.db.Exec("DROP TABLE storage_garbage").Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec("ALTER TABLE storage DROP INDEX idx_storage_gc,DROP COLUMN managed,DROP COLUMN managed_root,DROP COLUMN managed_path,DROP COLUMN orphaned_at,DROP COLUMN gc_token,DROP COLUMN gc_path,DROP COLUMN quarantined_at").Error; err != nil {
		t.Fatal(err)
	}
	if store.CheckFileSchema(f.db) == nil {
		t.Fatal("startup accepted absent GC metadata")
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
	current := gcStorage(t, f, s.ID)
	if current.Managed || current.ManagedRoot != "" || current.FilePath != s.FilePath || current.FileHash != s.FileHash {
		t.Fatal("legacy object was adopted or rewritten")
	}
	if r := gcRun(t, f); r.Scanned != 0 {
		t.Fatal("GC selected historical object")
	}
	gcBytes(t, s.FilePath, payload)
}

func TestStorageGCHardLinkInterruptionAndConflictingDestination(t *testing.T) {
	for _, same := range []bool{true, false} {
		t.Run(fmt.Sprintf("same-inode-%v", same), func(t *testing.T) {
			f := newFixture(t)
			s := gcOrphan(t, f, fmt.Sprintf("hardlink-%v.txt", same))
			payload, _ := os.ReadFile(s.FilePath)
			gcRun(t, f)
			gcAge(t, f, s.ID, "orphaned_at")
			const hook = "test:gc_hardlink_window"
			if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
				log, ok := tx.Statement.Dest.(*model.OperationLog)
				if !ok || log.Action != "storage_gc_mark" {
					return
				}
				var current model.Storage
				if err := tx.Session(&gorm.Session{NewDB: true}).First(&current, s.ID).Error; err != nil {
					tx.AddError(err)
					return
				}
				trash := filepath.Join(current.ManagedRoot, current.GCPath)
				if err := os.MkdirAll(filepath.Dir(trash), 0700); err != nil {
					tx.AddError(err)
					return
				}
				var err error
				if same {
					err = os.Link(s.FilePath, trash)
				} else {
					err = os.WriteFile(trash, []byte("independent destination retained"), 0600)
				}
				if err != nil {
					tx.AddError(err)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { f.db.Callback().Create().Remove(hook) })
			r, err := maintenance.RunStorageGC(context.Background(), f.db, s.ManagedRoot)
			if err := f.db.Callback().Create().Remove(hook); err != nil {
				t.Fatal(err)
			}
			current := gcStorage(t, f, s.ID)
			if same {
				if err != nil || r.Quarantined != 1 {
					t.Fatal("two-link recovery:", r, err)
				}
				gcBytes(t, filepath.Join(s.ManagedRoot, current.GCPath), payload)
				if _, err := os.Stat(s.FilePath); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("source link not finalized")
				}
			} else {
				if !errors.Is(err, maintenance.ErrGCPending) || current.State != "blocked" {
					t.Fatal("conflicting destination overwritten:", r, err)
				}
				gcBytes(t, s.FilePath, payload)
				gcBytes(t, filepath.Join(s.ManagedRoot, current.GCPath), []byte("independent destination retained"))
			}
		})
	}
}

func TestStorageGCLateForeignKeyFailureRollsBackTombstone(t *testing.T) {
	f := newFixture(t)
	s := gcOrphan(t, f, "final-fk.txt")
	payload, _ := os.ReadFile(s.FilePath)
	s = gcQuarantine(t, f, s)
	gcAge(t, f, s.ID, "quarantined_at")
	const hook = "test:gc_final_fk_guard"
	if err := f.db.Callback().Delete().Before("gorm:delete").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table != "storage" {
			return
		}
		tx.AddError(tx.Session(&gorm.Session{NewDB: true}).Create(&model.File{UserID: f.aliceID, Filename: "transaction-late-fk.txt", StorageID: &s.ID}).Error)
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Callback().Delete().Remove(hook) })
	r, err := maintenance.RunStorageGC(context.Background(), f.db, s.ManagedRoot)
	if !errors.Is(err, maintenance.ErrGCPending) || r.Tombstoned != 0 || r.Unlinked != 0 {
		t.Fatal("final FK failure ignored:", r, err)
	}
	if err := f.db.Callback().Delete().Remove(hook); err != nil {
		t.Fatal(err)
	}
	gcStorage(t, f, s.ID)
	gcBytes(t, filepath.Join(s.ManagedRoot, s.GCPath), payload)
	var count int64
	if err := f.db.Model(&model.StorageGarbage{}).Where("token=?", s.GCToken).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("failed final guard committed journal")
	}
	if r := gcRun(t, f); r.Tombstoned != 1 || r.Unlinked != 1 {
		t.Fatal("FK failure retry:", r)
	}
}

func TestStorageGCRetainsOldRepairPathsAfterCurrentObjectRemoval(t *testing.T) {
	f := newFixture(t)
	payload := []byte("verified repair with retained prior path")
	a := f.upload(t, f.alice, "repair-retain-a.txt", payload)
	waitNoReadLeases(t, f, *a.StorageID)
	old := gcStorage(t, f, *a.StorageID)
	evidence := []byte("corrupt previous object evidence")
	if err := os.WriteFile(old.FilePath, evidence, 0600); err != nil {
		t.Fatal(err)
	}
	b := f.upload(t, f.alice, "repair-retain-b.txt", payload)
	waitNoReadLeases(t, f, *b.StorageID)
	if *a.StorageID != *b.StorageID {
		t.Fatal("repair changed identity")
	}
	current := gcStorage(t, f, *a.StorageID)
	if current.FilePath == old.FilePath {
		t.Fatal("fixture did not repair")
	}
	for _, file := range []model.File{a, b} {
		f.json(t, "DELETE", fmt.Sprintf("/api/files/%d", file.ID), f.alice, nil, 200)
		f.json(t, "DELETE", fmt.Sprintf("/api/files/%d/permanent", file.ID), f.alice, nil, 200)
	}
	current = gcQuarantine(t, f, current)
	gcAge(t, f, current.ID, "quarantined_at")
	gcRun(t, f)
	gcBytes(t, old.FilePath, evidence)
	var count int64
	if err := f.db.Model(&model.StorageGarbage{}).Where("storage_id=? AND original_path=? AND status='retained'", old.ID, old.FilePath).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("old repair path record lost:", err)
	}
}
