//go:build integration

package integration

import (
	"fmt"
	"testing"

	"cloudenest_go/model"
)

func TestFileMutationAuditFailureRollsBackMetadata(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "audit-mutation.txt", []byte(t.Name()))
	folder := fixtureFolder(t, f, f.alice, "audit-target", 0)
	path := fmt.Sprintf("/api/files/%d", file.ID)
	move := map[string]any{"file_ids": []int64{file.ID}, "target_parent_id": folder.ID}
	const drop = "DROP TRIGGER IF EXISTS test_file_mutation_audit"
	t.Cleanup(func() { _ = f.db.Exec(drop).Error })
	fail := func(actions string) {
		t.Helper()
		if err := f.db.Exec(drop).Error; err != nil {
			t.Fatal(err)
		}
		sql := "CREATE TRIGGER test_file_mutation_audit BEFORE INSERT ON operation_log FOR EACH ROW BEGIN IF NEW.action IN (" +
			actions + ") THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='audit unavailable'; END IF; END"
		if err := f.db.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	assertFile := func(deleted int8) {
		t.Helper()
		var actual model.File
		if err := f.db.First(&actual, file.ID).Error; err != nil {
			t.Fatal(err)
		}
		if actual.Filename != file.Filename || actual.ParentID != file.ParentID || actual.IsDeleted != deleted {
			t.Fatal("metadata changed despite audit failure", actual)
		}
	}

	fail("'create_folder','rename','move','copy','delete'")
	f.json(t, "POST", "/api/files/folders", f.alice, map[string]any{"name": "blocked folder", "parent_id": folder.ID}, 500)
	f.json(t, "PUT", path+"/rename", f.alice, map[string]string{"name": "changed.pdf"}, 500)
	f.json(t, "POST", "/api/files/move", f.alice, move, 500)
	f.json(t, "POST", "/api/files/copy", f.alice, move, 500)
	f.json(t, "DELETE", path, f.alice, nil, 500)
	assertFile(0)
	var children int64
	if err := f.db.Model(&model.File{}).Where("parent_id=?", folder.ID).Count(&children).Error; err != nil || children != 0 {
		t.Fatal("failed mutations left entries", children, err)
	}
	fail("'restore'")
	f.json(t, "DELETE", path, f.alice, nil, 200)
	f.json(t, "POST", path+"/restore", f.alice, nil, 500)
	assertFile(1)
	if err := f.db.Exec(drop).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", path+"/restore", f.alice, nil, 200)
	assertFile(0)
}
