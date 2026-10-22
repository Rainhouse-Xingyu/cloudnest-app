//go:build integration

package integration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"cloudenest_go/internal/store"
	"cloudenest_go/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Only callers that already passed newFixture's disposable database guards may
// remove constraints. This is not a product downgrade or repair operation.
func dropStorageReferencesForTest(t *testing.T, f *fixture) {
	t.Helper()
	for _, step := range []struct{ table, constraint, index string }{
		{"file", "cn_file_storage_v1", "idx_cn_file_storage_v1"},
		{"file_version", "cn_version_storage_v1", "idx_cn_version_storage_v1"},
	} {
		var count int64
		if err := f.db.Raw("SELECT COUNT(*) FROM information_schema.referential_constraints WHERE constraint_schema=DATABASE() AND table_name=? AND constraint_name=?", step.table, step.constraint).Scan(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			if err := f.db.Exec("ALTER TABLE `" + step.table + "` DROP FOREIGN KEY `" + step.constraint + "`").Error; err != nil {
				t.Fatal(err)
			}
		}
		if err := f.db.Raw("SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name=? AND index_name=?", step.table, step.index).Scan(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			if err := f.db.Exec("ALTER TABLE `" + step.table + "` DROP INDEX `" + step.index + "`").Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := f.db.Exec("DELETE FROM cloudnest_schema_migration WHERE version IN (?,?)", store.FileStorageMigrationVersion, store.VersionStorageMigrationVersion).Error; err != nil {
		t.Fatal(err)
	}
}

func legacyStorageReferencesForTest(t *testing.T, f *fixture) {
	t.Helper()
	dropStorageReferencesForTest(t, f)
	t.Cleanup(func() {
		if err := store.MigrateStorageReferences(context.Background(), f.db); err != nil {
			t.Error("restore disposable storage references:", err)
		}
	})
}

func storageMetadataForTest(t *testing.T, f *fixture, suffix string) model.Storage {
	t.Helper()
	size := int64(1)
	// Deliberately missing physical bytes: audit/migration must never access disk.
	row := model.Storage{FileHash: fmt.Sprintf("ref-%d-%s", f.aliceID, suffix), FilePath: "/nonexistent/private/never-read-by-reference-audit", FileSize: &size}
	if err := f.db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

func TestStorageReferencesRestrictAllRetainedMetadata(t *testing.T) {
	f := newFixture(t)
	s := storageMetadataForTest(t, f, "retained")
	file := model.File{UserID: f.aliceID, Filename: "retained-in-recycle", StorageID: &s.ID, IsDeleted: 1}
	if err := f.db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	version := model.FileVersion{FileID: file.ID, Version: 1, StorageID: s.ID, SourceVerified: false}
	if err := f.db.Create(&version).Error; err != nil {
		t.Fatal(err)
	}
	expectMySQLError(t, f.db.Delete(&s).Error, 1451)
	expectMySQLError(t, f.db.Model(&model.Storage{}).Where("id=?", s.ID).Update("id", s.ID+1000000000).Error, 1451)
	missing := s.ID + 1000000000
	expectMySQLError(t, f.db.Create(&model.File{UserID: f.aliceID, Filename: "dangling-direct", StorageID: &missing}).Error, 1452)
	expectMySQLError(t, f.db.Model(&model.File{}).Where("id=?", file.ID).Update("storage_id", missing).Error, 1452)
	expectMySQLError(t, f.db.Create(&model.FileVersion{FileID: file.ID, Version: 2, StorageID: missing}).Error, 1452)
	expectMySQLError(t, f.db.Model(&model.FileVersion{}).Where("id=?", version.ID).Update("storage_id", missing).Error, 1452)
	// Even an unknown-provenance historical row without a live file retains bytes.
	if err := f.db.Delete(&file).Error; err != nil {
		t.Fatal(err)
	}
	expectMySQLError(t, f.db.Delete(&s).Error, 1451)
	if err := f.db.Delete(&version).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Delete(&s).Error; err != nil {
		t.Fatal(err)
	}
}

func TestStorageReferenceAuditCountsEveryReferenceWithoutDiskAccess(t *testing.T) {
	f := newFixture(t)
	before, err := store.AuditStorageReferences(context.Background(), f.db)
	if err != nil {
		t.Fatal(err)
	}
	a := storageMetadataForTest(t, f, "current")
	b := storageMetadataForTest(t, f, "history-only")
	_ = storageMetadataForTest(t, f, "unreferenced")
	files := []model.File{
		{UserID: f.aliceID, Filename: "ref-active", StorageID: &a.ID},
		{UserID: f.aliceID, Filename: "ref-recycled", StorageID: &a.ID, IsDeleted: 1},
		{UserID: f.aliceID, Filename: "ref-folder-pointer", StorageID: &a.ID, IsFolder: 1},
		{UserID: f.aliceID, Filename: "ref-null-pointer", IsFolder: 1},
	}
	if err := f.db.Create(&files).Error; err != nil {
		t.Fatal(err)
	}
	v := model.FileVersion{FileID: files[0].ID, Version: 1, StorageID: b.ID}
	if err := f.db.Create(&v).Error; err != nil {
		t.Fatal(err)
	}
	after, err := store.AuditStorageReferences(context.Background(), f.db)
	if err != nil {
		t.Fatal(err)
	}
	if after.StorageObjects-before.StorageObjects != 3 || after.FileReferences-before.FileReferences != 3 || after.VersionReferences-before.VersionReferences != 1 || after.UnreferencedObjects-before.UnreferencedObjects != 1 || after.HasFindings() {
		t.Fatalf("reference summary: %+v", after)
	}
	again, err := store.AuditStorageReferences(context.Background(), f.db)
	if err != nil {
		t.Fatal(err)
	}
	again.GeneratedAt = after.GeneratedAt
	if !reflect.DeepEqual(after, again) {
		t.Fatal("read-only audit changed its input")
	}
}

func TestStorageReferenceAuditCLIReportsBoundedFindingsAndMigrationPreservesRows(t *testing.T) {
	f := newFixture(t)
	legacyStorageReferencesForTest(t, f)
	files := make([]model.File, 101)
	missing := int64(-7654321)
	for i := range files {
		files[i] = model.File{UserID: f.aliceID, Filename: fmt.Sprintf("old-dangling-%d", i), StorageID: &missing, IsDeleted: 1}
	}
	if err := f.db.Create(&files).Error; err != nil {
		t.Fatal(err)
	}
	version := model.FileVersion{FileID: files[0].ID, Version: 1, StorageID: missing}
	if err := f.db.Create(&version).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.db.Delete(&version).Error; err != nil {
			t.Error(err)
		}
		ids := make([]int64, len(files))
		for i, row := range files {
			ids[i] = row.ID
		}
		if err := f.db.Where("id IN ?", ids).Delete(&model.File{}).Error; err != nil {
			t.Error(err)
		}
	})
	report, err := store.AuditStorageReferences(context.Background(), f.db)
	if err != nil {
		t.Fatal(err)
	}
	if report.DanglingFiles != 101 || report.DanglingVersions != 1 || !report.HasFindings() || !report.Truncated || len(report.DanglingReferences) != 100 {
		t.Fatalf("bad bounded audit: %+v", report)
	}
	if err := store.MigrateStorageReferences(context.Background(), f.db); !errors.Is(err, store.ErrStorageReferenceFindings) {
		t.Fatalf("bad preflight: %v", err)
	}
	var constraints, markers int64
	if err := f.db.Raw("SELECT COUNT(*) FROM information_schema.referential_constraints WHERE constraint_schema=DATABASE() AND constraint_name IN ('cn_file_storage_v1','cn_version_storage_v1')").Scan(&constraints).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Raw("SELECT COUNT(*) FROM cloudnest_schema_migration WHERE version IN (?,?)", store.FileStorageMigrationVersion, store.VersionStorageMigrationVersion).Scan(&markers).Error; err != nil {
		t.Fatal(err)
	}
	if constraints != 0 || markers != 0 {
		t.Fatal("failed preflight changed schema")
	}
	var unchanged []model.File
	ids := make([]int64, len(files))
	for i, row := range files {
		ids[i] = row.ID
	}
	if err := f.db.Where("id IN ?", ids).Order("id").Find(&unchanged).Error; err != nil {
		t.Fatal(err)
	}
	if len(unchanged) != len(files) {
		t.Fatal("audit/migration deleted historical rows")
	}
	for i, row := range unchanged {
		if row.ID != files[i].ID || row.StorageID == nil || *row.StorageID != missing || row.Filename != files[i].Filename || row.IsDeleted != 1 {
			t.Fatal("audit/migration rewrote history")
		}
	}
	binary := filepath.Join(t.TempDir(), "cloudnest")
	if output, err := exec.Command("go", "build", "-o", binary, "../cmd/server").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v %s", err, output)
	}
	cmd := exec.Command(binary, "audit-storage-references")
	cmd.Dir = t.TempDir()
	cmd.Env = []string{"MYSQL_DSN=" + f.cfg.MySQLDSN}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatal("CLI findings exited successfully")
	}
	if !strings.Contains(stdout.String(), `"dangling_files": 101`) || strings.Contains(stdout.String(), "old-dangling-") || strings.Contains(stdout.String(), "/private/") {
		t.Fatalf("audit output leaked metadata or lacked findings: %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "dangling references") {
		t.Fatal("CLI failed for unexpected reason:", stderr.String())
	}
}

func TestStorageReferenceMigrationResumesInterTableAndLedgerGaps(t *testing.T) {
	f := newFixture(t)
	legacyStorageReferencesForTest(t, f)
	const hook = "test:storage_reference_second_ddl"
	if err := f.db.Callback().Raw().Before("gorm:raw").Register(hook, func(tx *gorm.DB) {
		if strings.HasPrefix(tx.Statement.SQL.String(), "ALTER TABLE `file_version` ADD INDEX") {
			tx.AddError(errors.New("injected second-table interruption"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Callback().Raw().Remove(hook) })
	if err := store.MigrateStorageReferences(context.Background(), f.db); err == nil {
		t.Fatal("second-table failure ignored")
	}
	if err := f.db.Callback().Raw().Remove(hook); err != nil {
		t.Fatal(err)
	}
	var markers []string
	if err := f.db.Raw("SELECT version FROM cloudnest_schema_migration WHERE version IN (?,?) ORDER BY version", store.FileStorageMigrationVersion, store.VersionStorageMigrationVersion).Scan(&markers).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(markers, []string{store.FileStorageMigrationVersion}) {
		t.Fatalf("cross-table progress falsely atomic: %v", markers)
	}
	if store.CheckFileSchema(f.db) == nil {
		t.Fatal("startup accepted partial migration")
	}
	if err := store.MigrateStorageReferences(context.Background(), f.db); err != nil {
		t.Fatal(err)
	}
	// Crash after a completed atomic ALTER and before its ledger INSERT.
	if err := f.db.Exec("DELETE FROM cloudnest_schema_migration WHERE version IN (?,?)", store.FileStorageMigrationVersion, store.VersionStorageMigrationVersion).Error; err != nil {
		t.Fatal(err)
	}
	if store.CheckFileSchema(f.db) == nil {
		t.Fatal("startup accepted missing migration stamps")
	}
	if err := store.MigrateStorageReferences(context.Background(), f.db); err != nil {
		t.Fatal("exact DDL gap not recoverable:", err)
	}
	if err := store.MigrateStorageReferences(context.Background(), f.db); err != nil {
		t.Fatal("idempotent retry:", err)
	}
	if err := store.CheckFileSchema(f.db); err != nil {
		t.Fatal(err)
	}
}

func TestStorageReferenceMigrationRejectsDrift(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct {
		name, ddl    string
		removeLedger bool
	}{
		{"recorded_missing_fk", "ALTER TABLE file DROP FOREIGN KEY cn_file_storage_v1", false},
		{"cascade", "ALTER TABLE file DROP FOREIGN KEY cn_file_storage_v1, ADD CONSTRAINT cn_file_storage_v1 FOREIGN KEY(storage_id) REFERENCES storage(id) ON DELETE CASCADE ON UPDATE RESTRICT", true},
		{"wrong_parent_column", "ALTER TABLE file DROP FOREIGN KEY cn_file_storage_v1, ADD CONSTRAINT cn_file_storage_v1 FOREIGN KEY(storage_id) REFERENCES file_version(id) ON DELETE RESTRICT ON UPDATE RESTRICT", true},
		{"partial_index_only", "ALTER TABLE file DROP FOREIGN KEY cn_file_storage_v1", true},
		{"checksum", "UPDATE cloudnest_schema_migration SET checksum=REPEAT('0',64) WHERE version='20261022_02_file_storage_refs'", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				dropStorageReferencesForTest(t, f)
				if err := store.MigrateStorageReferences(context.Background(), f.db); err != nil {
					t.Fatal(err)
				}
			}()
			// Existing reference rows make changing targets unsafe even in a fixture;
			// the wrong-parent test uses an empty child table's version constraint.
			ddl := tc.ddl
			if tc.name == "wrong_parent_column" {
				// Use a separate empty table as the incorrect parent to avoid relying on
				// unrelated test data. Foreign-key checks remain enabled throughout.
				if err := f.db.Exec("CREATE TABLE ref_wrong_parent (id BIGINT PRIMARY KEY) ENGINE=InnoDB").Error; err != nil {
					t.Fatal(err)
				}
				if err := f.db.Exec("INSERT INTO ref_wrong_parent SELECT DISTINCT storage_id FROM file WHERE storage_id IS NOT NULL").Error; err != nil {
					t.Fatal(err)
				}
				ddl = strings.ReplaceAll(ddl, "file_version(id)", "ref_wrong_parent(id)")
				defer func() {
					if err := f.db.Exec("ALTER TABLE file DROP FOREIGN KEY cn_file_storage_v1").Error; err != nil {
						t.Error(err)
					}
					if err := f.db.Exec("DROP TABLE ref_wrong_parent").Error; err != nil {
						t.Error(err)
					}
				}()
			}
			for _, statement := range strings.Split(ddl, ", ADD CONSTRAINT") {
				if strings.HasPrefix(statement, " cn_file_storage_v1") {
					statement = "ALTER TABLE file ADD CONSTRAINT" + statement
				}
				if err := f.db.Exec(statement).Error; err != nil {
					t.Fatal(err)
				}
			}
			if tc.removeLedger {
				if err := f.db.Exec("DELETE FROM cloudnest_schema_migration WHERE version=?", store.FileStorageMigrationVersion).Error; err != nil {
					t.Fatal(err)
				}
			}
			if store.CheckFileSchema(f.db) == nil || store.MigrateStorageReferences(context.Background(), f.db) == nil {
				t.Fatal("drift accepted or repaired")
			}
		})
	}
}

func TestStorageReferenceMigrationConcurrentOperators(t *testing.T) {
	f := newFixture(t)
	legacyStorageReferencesForTest(t, f)
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for range 6 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- store.MigrateStorageReferences(context.Background(), f.db) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CheckFileSchema(f.db); err != nil {
		t.Fatal(err)
	}
}

func TestStorageReferenceMigrationLateDanglingRowIsNotRepaired(t *testing.T) {
	f := newFixture(t)
	legacyStorageReferencesForTest(t, f)
	missing := int64(-9876543)
	row := model.FileVersion{FileID: 9876543, Version: 1, StorageID: missing}
	const hook = "test:storage_reference_late_row"
	if err := f.db.Callback().Raw().Before("gorm:raw").Register(hook, func(tx *gorm.DB) {
		if strings.HasPrefix(tx.Statement.SQL.String(), "ALTER TABLE `file_version` ADD INDEX") {
			if err := f.db.Create(&row).Error; err != nil {
				tx.AddError(err)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		f.db.Callback().Raw().Remove(hook)
		if row.ID != 0 {
			if err := f.db.Delete(&row).Error; err != nil {
				t.Error(err)
			}
		}
	})
	if err := store.MigrateStorageReferences(context.Background(), f.db); err == nil {
		t.Fatal("late dangling reference passed checked ALTER")
	}
	if err := f.db.Callback().Raw().Remove(hook); err != nil {
		t.Fatal(err)
	}
	var indexes, markers, rows int64
	if err := f.db.Raw("SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='file_version' AND index_name='idx_cn_version_storage_v1'").Scan(&indexes).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Raw("SELECT COUNT(*) FROM cloudnest_schema_migration WHERE version=?", store.VersionStorageMigrationVersion).Scan(&markers).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.FileVersion{}).Where("id=?", row.ID).Count(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if indexes != 0 || markers != 0 || rows != 1 {
		t.Fatalf("atomic step repaired history or left partial objects: %d %d %d", indexes, markers, rows)
	}
	if err := f.db.Delete(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.MigrateStorageReferences(context.Background(), f.db); err != nil {
		t.Fatal("retry:", err)
	}
}

func TestStorageReferencesSerializeConcurrentPublicationAndRemoval(t *testing.T) {
	f := newFixture(t)
	// Delete wins while holding the parent X lock. The waiting/new reference must
	// fail; an application-side reference-count query alone cannot guarantee this.
	s := storageMetadataForTest(t, f, "delete-first")
	tx := f.db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&model.Storage{}, s.ID).Error; err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- f.db.Create(&model.File{UserID: f.aliceID, Filename: "late-reference", StorageID: &s.ID}).Error
	}()
	if err := tx.Delete(&s).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	expectMySQLError(t, <-result, 1452)
	// Reference wins and commits after the removal request starts. The object is
	// retained even when the only new child is a historical version.
	s = storageMetadataForTest(t, f, "reference-first")
	tx = f.db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	v := model.FileVersion{FileID: 987654321, Version: 1, StorageID: s.ID}
	if err := tx.Create(&v).Error; err != nil {
		t.Fatal(err)
	}
	go func() { result <- f.db.Delete(&s).Error }()
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	expectMySQLError(t, <-result, 1451)
}

func TestStorageReferenceMigrationRejectsDisabledChecksAndReleasesCanceledLock(t *testing.T) {
	f := newFixture(t)
	// Change only one disposable connection and restore it before returning it to
	// the pool. Product code must fail instead of enabling checks and trusting rows
	// that might have been inserted while checks were disabled.
	if err := f.db.Connection(func(conn *gorm.DB) error {
		if err := conn.Exec("SET SESSION foreign_key_checks=0").Error; err != nil {
			return err
		}
		defer func() {
			if err := conn.Exec("SET SESSION foreign_key_checks=1").Error; err != nil {
				t.Error(err)
			}
		}()
		if store.CheckStorageReferenceSchema(conn) == nil || store.MigrateStorageReferences(context.Background(), conn) == nil {
			return errors.New("disabled foreign-key enforcement accepted")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	legacyStorageReferencesForTest(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const hook = "test:storage_reference_cancel"
	if err := f.db.Callback().Row().After("gorm:row").Register(hook, func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "GET_LOCK") {
			cancel()
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Callback().Row().Remove(hook) })
	if err := store.MigrateStorageReferences(ctx, f.db); err == nil {
		t.Fatal("canceled migration succeeded")
	}
	if err := f.db.Callback().Row().Remove(hook); err != nil {
		t.Fatal(err)
	}
	if err := store.MigrateStorageReferences(context.Background(), f.db); err != nil {
		t.Fatal("cancellation retained advisory lock:", err)
	}
}

func TestStorageReferenceCLIUpgradeAndStartupFailClosed(t *testing.T) {
	f := newFixture(t)
	legacyStorageReferencesForTest(t, f)
	binary := filepath.Join(t.TempDir(), "cloudnest")
	if output, err := exec.Command("go", "build", "-o", binary, "../cmd/server").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v %s", err, output)
	}
	temp := t.TempDir()
	cmd := exec.Command(binary)
	cmd.Dir = temp
	cmd.Env = []string{"MYSQL_DSN=" + f.cfg.MySQLDSN, "JWT_SECRET=test-only-signing-secret-with-at-least-32-bytes"}
	if output, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(output), "migrate-storage-references") {
		t.Fatalf("startup did not refuse missing references: %v %s", err, output)
	}
	for _, command := range []string{"audit-storage-references", "migrate-storage-references", "migrate-storage-references", "audit-storage-references"} {
		cmd = exec.Command(binary, command)
		cmd.Dir = temp
		cmd.Env = []string{"MYSQL_DSN=" + f.cfg.MySQLDSN}
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("database-only %s: %v %s", command, err, output)
		}
	}
	if err := store.CheckFileSchema(f.db); err != nil {
		t.Fatal(err)
	}
	// A query failure is nonzero and cannot print a successful empty report.
	cmd = exec.Command(binary, "audit-storage-references")
	cmd.Dir = temp
	cmd.Env = []string{"MYSQL_DSN=cloudnest_test:disposable@tcp(127.0.0.1:1)/cloudnest_test?timeout=1s"}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err == nil || stdout.Len() != 0 {
		t.Fatal("failed audit reported clean output")
	}
}

func TestStorageReferenceSchemaRejectsAdditionalForeignKeyOnStorageID(t *testing.T) {
	f := newFixture(t)
	if err := f.db.Exec("ALTER TABLE file ADD CONSTRAINT unexpected_storage_cascade FOREIGN KEY(storage_id) REFERENCES storage(id) ON DELETE CASCADE").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.db.Exec("ALTER TABLE file DROP FOREIGN KEY unexpected_storage_cascade").Error; err != nil {
			t.Error(err)
		}
	})
	if store.CheckFileSchema(f.db) == nil || store.MigrateStorageReferences(context.Background(), f.db) == nil {
		t.Fatal("additional foreign key bypassed exact-definition check")
	}
}
