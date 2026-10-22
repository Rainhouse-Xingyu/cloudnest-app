//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"

	"cloudenest_go/internal/store"
	"cloudenest_go/model"
	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// These helpers operate only after newFixture has verified loopback, the exact
// disposable database/user and the disposable marker. No product rollback drops
// constraints or modifies historical rows.
func dropFileNameObjectsForTest(t *testing.T, f *fixture) {
	t.Helper()
	var count int64
	if err := f.db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='file' AND column_name='cn_active_scope_v1'").Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count > 0 {
		if err := f.db.Exec("ALTER TABLE file DROP INDEX uq_file_active_name_v1, DROP CHECK cn_file_scalar_v1, DROP COLUMN cn_active_name_v1, DROP COLUMN cn_active_scope_v1").Error; err != nil {
			t.Fatal(err)
		}
	}
	if f.db.Migrator().HasTable("cloudnest_schema_migration") {
		if err := f.db.Exec("DELETE FROM cloudnest_schema_migration WHERE version=?", store.FileNameMigrationVersion).Error; err != nil {
			t.Fatal(err)
		}
	}
}
func legacyFileNamesForTest(t *testing.T, f *fixture) {
	t.Helper()
	dropFileNameObjectsForTest(t, f)
	t.Cleanup(func() {
		if err := store.MigrateFileNames(context.Background(), f.db); err != nil {
			t.Errorf("restore disposable constraint: %v", err)
		}
	})
}
func expectMySQLError(t *testing.T, err error, number uint16) {
	t.Helper()
	var mysqlErr *mysqldriver.MySQLError
	if !errors.As(err, &mysqlErr) || mysqlErr.Number != number {
		t.Fatalf("SQL error: %v, want MySQL %d", err, number)
	}
}

func TestFileNameConstraintRejectsDirectWrites(t *testing.T) {
	f := newFixture(t)
	if err := store.CheckFileSchema(f.db); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 12)
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			row := model.File{UserID: f.aliceID, Filename: "direct-concurrent"}
			results <- f.db.Create(&row).Error
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else {
			expectMySQLError(t, err, 1062)
		}
	}
	if successes != 1 {
		t.Fatalf("unlocked direct inserts: %d successful", successes)
	}
	if err := f.db.Create(&model.File{UserID: f.bobID, Filename: "direct-concurrent"}).Error; err != nil {
		t.Fatal("different owner root collided", err)
	}
	parent := fixtureFolder(t, f, f.alice, "direct-shared", 0)
	a := model.File{UserID: f.aliceID, Filename: "shared-name", ParentID: parent.ID}
	if err := f.db.Create(&a).Error; err != nil {
		t.Fatal(err)
	}
	expectMySQLError(t, f.db.Create(&model.File{UserID: f.bobID, Filename: a.Filename, ParentID: parent.ID}).Error, 1062)
	if err := f.db.Model(&a).Update("is_deleted", 1).Error; err != nil {
		t.Fatal(err)
	}
	active := model.File{UserID: f.bobID, Filename: a.Filename, ParentID: parent.ID}
	if err := f.db.Create(&active).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&model.File{UserID: f.aliceID, Filename: a.Filename, ParentID: parent.ID, IsDeleted: 1}).Error; err != nil {
		t.Fatal(err)
	}
	expectMySQLError(t, f.db.Model(&a).Update("is_deleted", 0).Error, 1062)
	for _, update := range []map[string]any{{"parent_id": nil}, {"parent_id": -1}, {"user_id": 0}, {"is_deleted": nil}, {"is_deleted": 2}, {"is_folder": nil}, {"is_folder": 2}, {"filename": ""}} {
		expectMySQLError(t, f.db.Model(&model.File{}).Where("id=?", active.ID).Updates(update).Error, 3819)
	}
	var unchanged model.File
	if err := f.db.First(&unchanged, active.ID).Error; err != nil || unchanged.UserID != f.bobID || unchanged.ParentID != parent.ID || unchanged.IsDeleted != 0 || unchanged.Filename != a.Filename {
		t.Fatalf("invalid write changed row: %#v %v", unchanged, err)
	}
}

func TestFileNameDatabaseConflictMapsToAPI409AndRollsBack(t *testing.T) {
	f := newFixture(t)
	const hook = "test:competing_namespace_write"
	if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		row, ok := tx.Statement.Dest.(*model.File)
		if ok && row.Filename == "database-conflict" {
			tx.AddError(tx.Exec("INSERT INTO file (user_id,filename,parent_id,is_folder,is_deleted) VALUES (?,?,0,1,0)", row.UserID, row.Filename).Error)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Callback().Create().Remove(hook) })
	f.json(t, "POST", "/api/files/folders", f.alice, map[string]any{"name": "database-conflict"}, http.StatusConflict)
	var count int64
	if err := f.db.Model(&model.File{}).Where("user_id=? AND filename=?", f.aliceID, "database-conflict").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("conflict left partial writes: %d %v", count, err)
	}
}

func TestFileNameMigrationRefusesHistoricalConflictsWithoutChangingRows(t *testing.T) {
	f := newFixture(t)
	legacyFileNamesForTest(t, f)
	rows := []model.File{{UserID: f.aliceID, Filename: "historical-dupe"}, {UserID: f.aliceID, Filename: "historical-dupe"}, {UserID: f.aliceID, Filename: "historical-null"}}
	if err := f.db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	ids := []int64{rows[0].ID, rows[1].ID, rows[2].ID}
	t.Cleanup(func() {
		if err := f.db.Where("id IN ?", ids).Delete(&model.File{}).Error; err != nil {
			t.Error(err)
		}
	})
	if err := f.db.Model(&rows[2]).Update("parent_id", nil).Error; err != nil {
		t.Fatal(err)
	}
	var before, after []model.File
	if err := f.db.Order("id").Find(&before).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.MigrateFileNames(context.Background(), f.db); !errors.Is(err, store.ErrFileNameAuditFindings) {
		t.Fatalf("legacy audit bypassed: %v", err)
	}
	if err := f.db.Order("id").Find(&after).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rejected migration altered historical rows")
	}
	var columns, versions int64
	if err := f.db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='file' AND column_name IN ('cn_active_scope_v1','cn_active_name_v1')").Scan(&columns).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Raw("SELECT COUNT(*) FROM cloudnest_schema_migration WHERE version=?", store.FileNameMigrationVersion).Scan(&versions).Error; err != nil {
		t.Fatal(err)
	}
	if columns != 0 || versions != 0 {
		t.Fatal("failed preflight installed partial DDL or a success marker")
	}
	if err := store.CheckFileSchema(f.db); err == nil {
		t.Fatal("startup accepted unmigrated file schema")
	}
}

func TestFileNameMigrationRepeatsAndRecoversLedgerGap(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "migration-preserves.bin", []byte("historical bytes must survive"))
	var before, after []model.File
	var accountBefore, accountAfter model.User
	if err := f.db.Order("id").Find(&before).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.First(&accountBefore, f.aliceID).Error; err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := store.MigrateFileNames(context.Background(), f.db); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.db.Exec("DELETE FROM cloudnest_schema_migration WHERE version=?", store.FileNameMigrationVersion).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.CheckFileSchema(f.db); err == nil {
		t.Fatal("missing ledger ignored")
	}
	if err := store.MigrateFileNames(context.Background(), f.db); err != nil {
		t.Fatal("exact DDL could not recover ledger gap", err)
	}
	if err := store.CheckFileSchema(f.db); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Order("id").Find(&after).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.First(&accountAfter, f.aliceID).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(accountBefore, accountAfter) {
		t.Fatal("repeat migration changed historical files or account")
	}
	w := f.json(t, "GET", fmt.Sprintf("/api/files/%d/download", file.ID), f.alice, nil, http.StatusOK)
	if w.Body.String() != "historical bytes must survive" {
		t.Fatal("file bytes changed")
	}
	var count int64
	if err := f.db.Raw("SELECT COUNT(*) FROM cloudnest_schema_migration WHERE version=?", store.FileNameMigrationVersion).Scan(&count).Error; err != nil || count != 1 {
		t.Fatalf("ledger count %d %v", count, err)
	}
}

func TestFileNameMigrationRejectsDrift(t *testing.T) {
	f := newFixture(t)
	var checksum string
	if err := f.db.Raw("SELECT checksum FROM cloudnest_schema_migration WHERE version=?", store.FileNameMigrationVersion).Scan(&checksum).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec("UPDATE cloudnest_schema_migration SET checksum=? WHERE version=?", strings.Repeat("0", 64), store.FileNameMigrationVersion).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.db.Exec("UPDATE cloudnest_schema_migration SET checksum=? WHERE version=?", checksum, store.FileNameMigrationVersion).Error; err != nil {
			t.Error(err)
		}
	})
	if store.CheckFileSchema(f.db) == nil || store.MigrateFileNames(context.Background(), f.db) == nil {
		t.Fatal("checksum drift was accepted")
	}
	var unchanged string
	if err := f.db.Raw("SELECT checksum FROM cloudnest_schema_migration WHERE version=?", store.FileNameMigrationVersion).Scan(&unchanged).Error; err != nil {
		t.Fatal(err)
	}
	if unchanged != strings.Repeat("0", 64) {
		t.Fatal("migration overwrote altered history")
	}
	if err := f.db.Exec("UPDATE cloudnest_schema_migration SET checksum=? WHERE version=?", checksum, store.FileNameMigrationVersion).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec("ALTER TABLE file DROP INDEX uq_file_active_name_v1, ADD INDEX uq_file_active_name_v1 (cn_active_scope_v1,cn_active_name_v1)").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.db.Exec("ALTER TABLE file DROP INDEX uq_file_active_name_v1, ADD UNIQUE INDEX uq_file_active_name_v1 (cn_active_scope_v1,cn_active_name_v1)").Error; err != nil {
			t.Error(err)
		}
	})
	if store.CheckFileSchema(f.db) == nil || store.MigrateFileNames(context.Background(), f.db) == nil {
		t.Fatal("non-unique replacement passed validation")
	}
	if err := f.db.Exec("ALTER TABLE file DROP INDEX uq_file_active_name_v1, ADD UNIQUE INDEX uq_file_active_name_v1 (cn_active_scope_v1,cn_active_name_v1)").Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec("ALTER TABLE file ALTER CHECK cn_file_scalar_v1 NOT ENFORCED").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.db.Exec("ALTER TABLE file ALTER CHECK cn_file_scalar_v1 ENFORCED").Error; err != nil {
			t.Error(err)
		}
	})
	if store.CheckFileSchema(f.db) == nil || store.MigrateFileNames(context.Background(), f.db) == nil {
		t.Fatal("disabled CHECK passed validation")
	}
}

func TestFileNameMigrationSerializesConcurrentOperators(t *testing.T) {
	f := newFixture(t)
	legacyFileNamesForTest(t, f)
	if store.CheckFileSchema(f.db) == nil {
		t.Fatal("startup accepted legacy schema")
	}
	start := make(chan struct{})
	results := make(chan error, 6)
	for range 6 {
		go func() { <-start; results <- store.MigrateFileNames(context.Background(), f.db) }()
	}
	close(start)
	for range 6 {
		if err := <-results; err != nil {
			t.Fatal("concurrent migration failed", err)
		}
	}
	if err := store.CheckFileSchema(f.db); err != nil {
		t.Fatal(err)
	}
}

func TestFileNameMigrationRejectsPartialObjects(t *testing.T) {
	f := newFixture(t)
	legacyFileNamesForTest(t, f)
	if err := f.db.Exec("ALTER TABLE file ADD COLUMN cn_active_scope_v1 BIGINT GENERATED ALWAYS AS (parent_id) VIRTUAL").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.db.Exec("ALTER TABLE file DROP COLUMN cn_active_scope_v1").Error; err != nil {
			t.Error(err)
		}
	})
	if store.CheckFileSchema(f.db) == nil || store.MigrateFileNames(context.Background(), f.db) == nil {
		t.Fatal("partial schema was automatically accepted or repaired")
	}
	var count int64
	if err := f.db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='file' AND column_name='cn_active_name_v1'").Scan(&count).Error; err != nil || count != 0 {
		t.Fatal("partial schema was changed")
	}
}

func TestFileNameMigrationCLIPreflightAndUpgrade(t *testing.T) {
	f := newFixture(t)
	legacyFileNamesForTest(t, f)
	temp := t.TempDir()
	binary := filepath.Join(temp, "cloudnest")
	if output, err := exec.Command("go", "build", "-o", binary, "../cmd/server").CombinedOutput(); err != nil {
		t.Fatalf("build CLI %v %s", err, output)
	}
	dataDir := filepath.Join(temp, "must-not-be-created")
	start := exec.Command(binary, "start", "8080", dataDir)
	start.Dir = temp
	start.Env = []string{"MYSQL_DSN=" + f.cfg.MySQLDSN, "JWT_SECRET=" + f.cfg.JWTSecret, "REDIS_ADDR=127.0.0.1:1"}
	output, err := start.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "migrate-file-names") {
		t.Fatalf("startup did not fail at file schema: %v %s", err, output)
	}
	if _, err := os.Stat(dataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unmigrated startup touched the data directory")
	}
	rows := []model.File{{UserID: f.aliceID, Filename: "private-do-not-log"}, {UserID: f.aliceID, Filename: "private-do-not-log"}}
	if err := f.db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	ids := []int64{rows[0].ID, rows[1].ID}
	t.Cleanup(func() {
		if err := f.db.Where("id IN ?", ids).Delete(&model.File{}).Error; err != nil {
			t.Error(err)
		}
	})
	run := func(wantSuccess bool) {
		t.Helper()
		cmd := exec.Command(binary, "migrate-file-names")
		cmd.Dir = temp
		cmd.Env = []string{"MYSQL_DSN=" + f.cfg.MySQLDSN}
		output, err := cmd.CombinedOutput()
		if (err == nil) != wantSuccess {
			t.Fatalf("CLI success=%v want %v: %s", err == nil, wantSuccess, output)
		}
		if strings.Contains(string(output), "private-do-not-log") {
			t.Fatal("migration printed a private conflict name")
		}
	}
	run(false)
	if err := f.db.Where("id IN ?", ids).Delete(&model.File{}).Error; err != nil {
		t.Fatal(err)
	}
	run(true)
	run(true)
	if err := store.CheckFileSchema(f.db); err != nil {
		t.Fatal(err)
	}
}

func TestFileNameMigrationCancellationReleasesMaintenanceLock(t *testing.T) {
	f := newFixture(t)
	legacyFileNamesForTest(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const hook = "test:cancel_file_name_migration"
	if err := f.db.Callback().Row().Before("gorm:row").Register(hook, func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "SELECT VERSION()") {
			cancel()
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Callback().Row().Remove(hook) })
	if err := store.MigrateFileNames(ctx, f.db); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
	if err := f.db.Callback().Row().Remove(hook); err != nil {
		t.Fatal(err)
	}
	if err := store.MigrateFileNames(context.Background(), f.db); err != nil {
		t.Fatal("retry could not acquire released lock", err)
	}
	if err := store.CheckFileSchema(f.db); err != nil {
		t.Fatal(err)
	}
}

func TestFileNameMigrationAtomicDDLRejectsNewConflictAfterPreflight(t *testing.T) {
	f := newFixture(t)
	legacyFileNamesForTest(t, f)
	first := model.File{UserID: f.aliceID, Filename: "late-private-conflict"}
	if err := f.db.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	second := model.File{UserID: f.aliceID, Filename: first.Filename}
	const hook = "test:late_file_name_conflict"
	if err := f.db.Callback().Raw().Before("gorm:raw").Register(hook, func(tx *gorm.DB) {
		if strings.HasPrefix(tx.Statement.SQL.String(), "ALTER TABLE `file` ADD COLUMN") {
			if err := f.db.Create(&second).Error; err != nil {
				tx.AddError(err)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		f.db.Callback().Raw().Remove(hook)
		if err := f.db.Where("id IN ?", []int64{first.ID, second.ID}).Delete(&model.File{}).Error; err != nil {
			t.Error(err)
		}
	})
	err := store.MigrateFileNames(context.Background(), f.db)
	if err == nil || strings.Contains(err.Error(), first.Filename) {
		t.Fatalf("late DDL conflict leaked or passed: %v", err)
	}
	if err := f.db.Callback().Raw().Remove(hook); err != nil {
		t.Fatal(err)
	}
	var objects, markers, rows int64
	if err := f.db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='file' AND column_name IN ('cn_active_scope_v1','cn_active_name_v1')").Scan(&objects).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Raw("SELECT COUNT(*) FROM cloudnest_schema_migration WHERE version=?", store.FileNameMigrationVersion).Scan(&markers).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.File{}).Where("id IN ?", []int64{first.ID, second.ID}).Count(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if objects != 0 || markers != 0 || rows != 2 {
		t.Fatalf("failed atomic ALTER left objects=%d markers=%d rows=%d", objects, markers, rows)
	}
}

func TestFileNameMigrationPreservesBinaryCollation(t *testing.T) {
	f := newFixture(t)
	legacyFileNamesForTest(t, f)
	var original string
	if err := f.db.Raw("SELECT collation_name FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='file' AND column_name='filename'").Scan(&original).Error; err != nil {
		t.Fatal(err)
	}
	rows := []model.File{{UserID: f.aliceID, Filename: "binary-case"}, {UserID: f.aliceID, Filename: "BINARY-CASE"}}
	t.Cleanup(func() {
		if err := f.db.Where("id IN ?", []int64{rows[0].ID, rows[1].ID}).Delete(&model.File{}).Error; err != nil {
			t.Error(err)
		}
		dropFileNameObjectsForTest(t, f)
		// original is a server-returned collation, never a user supplied SQL token.
		if !regexp.MustCompile(`^[a-zA-Z0-9_]+$`).MatchString(original) {
			t.Fatal("invalid test collation")
		}
		if err := f.db.Exec("ALTER TABLE file MODIFY filename VARCHAR(255) CHARACTER SET utf8mb4 COLLATE " + original + " NOT NULL").Error; err != nil {
			t.Error(err)
		}
	})
	if err := f.db.Exec("ALTER TABLE file MODIFY filename VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL").Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.MigrateFileNames(context.Background(), f.db); err != nil {
		t.Fatal("binary collation migration changed comparison rules", err)
	}
	if err := store.CheckFileSchema(f.db); err != nil {
		t.Fatal(err)
	}
	expectMySQLError(t, f.db.Create(&model.File{UserID: f.aliceID, Filename: rows[0].Filename}).Error, 1062)
	var generated string
	if err := f.db.Raw("SELECT collation_name FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='file' AND column_name='cn_active_name_v1'").Scan(&generated).Error; err != nil || generated != "utf8mb4_bin" {
		t.Fatalf("generated collation %s: %v", generated, err)
	}
}
