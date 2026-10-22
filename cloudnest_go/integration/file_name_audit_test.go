//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"cloudenest_go/internal/store"
	"cloudenest_go/model"
)

func TestFileNameAuditCLIIsReadOnlyAndUsesDatabaseCollation(t *testing.T) {
	f := newFixture(t)
	legacyFileNamesForTest(t, f)
	baseline, err := store.AuditFileNames(context.Background(), f.db)
	if err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	binary := filepath.Join(temp, "cloudnest")
	if output, err := exec.Command("go", "build", "-o", binary, "../cmd/server").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v %s", err, output)
	}
	run := func(wantFindings bool) store.FileNameAudit {
		t.Helper()
		cmd := exec.Command(binary, "audit-file-names")
		cmd.Dir = temp
		// No JWT secret, Redis, data directory, or project environment is needed.
		cmd.Env = []string{"MYSQL_DSN=" + f.cfg.MySQLDSN}
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		if (err != nil) != wantFindings {
			t.Fatalf("audit exit: %v stderr=%s", err, stderr.String())
		}
		var report store.FileNameAudit
		if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
			t.Fatalf("audit JSON: %v", err)
		}
		if report.HasFindings() != wantFindings {
			t.Fatal("exit and report disagree")
		}
		return report
	}
	run(baseline.HasFindings())
	root := model.File{UserID: f.aliceID, Filename: "audit-parent", IsFolder: 1}
	if err := f.db.Create(&root).Error; err != nil {
		t.Fatal(err)
	}
	rows := []model.File{
		{UserID: f.aliceID, Filename: "audit-dupe.txt"},
		{UserID: f.aliceID, Filename: "audit-dupe.txt"},
		{UserID: f.aliceID, Filename: "AUDIT-DUPE.txt"},
		{UserID: f.bobID, Filename: "audit-dupe.txt"}, // Other owner's root is independent.
		{UserID: f.aliceID, Filename: "audit-dupe.txt", IsDeleted: 1},
		{UserID: f.aliceID, ParentID: root.ID, Filename: "shared-dupe.txt"},
		{UserID: f.bobID, ParentID: root.ID, Filename: "shared-dupe.txt"},
		{UserID: f.aliceID, Filename: "invalid-parent", ParentID: -1},
		{UserID: f.aliceID, Filename: "invalid-folder", IsFolder: 2},
		{UserID: f.aliceID, Filename: "invalid-folder", IsFolder: 2},
	}
	if err := f.db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ids := []int64{root.ID}
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		if err := f.db.Where("id IN ?", ids).Delete(&model.File{}).Error; err != nil {
			t.Error(err)
		}
	})

	// Exercise nullable legacy scalar metadata; it must be reported, not silently
	// grouped as a valid per-user root or hidden by a failed integer scan.
	if err := f.db.Model(&model.File{}).Where("id = ?", rows[7].ID).Update("parent_id", nil).Error; err != nil {
		t.Fatal(err)
	}
	var before, after []model.File
	if err := f.db.Order("id").Find(&before).Error; err != nil {
		t.Fatal(err)
	}
	var schemaBefore, schemaAfter int64
	if err := f.db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE()").Scan(&schemaBefore).Error; err != nil {
		t.Fatal(err)
	}
	report := run(true)
	if report.ConflictGroups != baseline.ConflictGroups+2 || report.InvalidScalarRows != baseline.InvalidScalarRows+3 {
		t.Fatalf("audit counts: %#v", report)
	}
	foundRoot, foundShared := false, false
	for _, group := range report.Conflicts {
		if group.ParentID == 0 && group.RootOwnerID == f.aliceID {
			foundRoot = true
			want := int64(2)
			if strings.HasSuffix(report.FilenameCollation, "_ci") {
				want = 3
			}
			if group.EntryCount != want || group.FirstFileID != rows[0].ID {
				t.Fatalf("root scope or collation lost: %#v", group)
			}
		}
		if group.ParentID == root.ID {
			foundShared = true
			if group.RootOwnerID != 0 || group.EntryCount != 2 {
				t.Fatalf("shared namespace split by owner: %#v", group)
			}
		}
	}
	if !foundRoot || !foundShared {
		t.Fatal("expected conflicts not reported")
	}
	if err := f.db.Order("id").Find(&after).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE()").Scan(&schemaAfter).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || schemaBefore != schemaAfter {
		t.Fatal("audit changed file records or schema")
	}
	// Test-only cleanup of exactly the legacy corrupt fixture inserted above.
	ids := []int64{root.ID}
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	if err := f.db.Where("id IN ?", ids).Delete(&model.File{}).Error; err != nil {
		t.Fatal(err)
	}
	final := run(baseline.HasFindings())
	if final.ConflictGroups != baseline.ConflictGroups || final.InvalidScalarRows != baseline.InvalidScalarRows {
		t.Fatal("audit baseline did not recover after fixture cleanup")
	}
}

func TestFileNameAuditReportsTruncatedSamplesWithoutLosingTotals(t *testing.T) {
	f := newFixture(t)
	legacyFileNamesForTest(t, f)
	before, err := store.AuditFileNames(context.Background(), f.db)
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]model.File, 0, 202)
	for n := range 101 {
		name := fmt.Sprintf("audit-group-%03d", n)
		rows = append(rows, model.File{UserID: f.aliceID, Filename: name}, model.File{UserID: f.aliceID, Filename: name})
	}
	if err := f.db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ids := make([]int64, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		if err := f.db.Where("id IN ?", ids).Delete(&model.File{}).Error; err != nil {
			t.Error(err)
		}
	})
	report, err := store.AuditFileNames(context.Background(), f.db)
	if err != nil {
		t.Fatal(err)
	}
	if report.ConflictGroups != before.ConflictGroups+101 || report.ConflictingFiles != before.ConflictingFiles+202 || !report.Truncated || len(report.Conflicts) != report.SampleLimit || report.SampleLimit != 100 {
		t.Fatalf("truncated report lost totals: %#v", report)
	}
}
