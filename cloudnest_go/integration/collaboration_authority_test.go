//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"cloudenest_go/internal/service/contract"
	storageMysql "cloudenest_go/internal/storage/mysql"
	"cloudenest_go/model"
)

func TestWorkspaceMemberConditionalEditsAndCreateOnly(t *testing.T) {
	f := newFixture(t)
	var created struct{ Item model.Workspace }
	decode(t, f.json(t, "POST", "/api/workspaces", f.alice, map[string]string{"name": "Conditional members"}, 200), &created)
	base := fmt.Sprintf("/api/workspaces/%d/members", created.Item.ID)
	target := fmt.Sprintf("%s/%d", base, f.bobID)
	f.json(t, "POST", base, f.alice, map[string]any{"user_id": f.bobID, "role": "viewer", "create_only": true}, 200)
	f.json(t, "POST", base, f.alice, map[string]any{"user_id": f.bobID, "role": "admin", "create_only": true}, 409)
	f.json(t, "PUT", target, f.bob, map[string]any{"role": "editor", "expected_role": "viewer"}, 403)
	// Legacy calls retain their original behavior; new callers opt into CAS.
	f.json(t, "PUT", target, f.alice, map[string]string{"role": "editor"}, 200)
	f.json(t, "PUT", target, f.alice, map[string]string{"role": "admin", "expected_role": "viewer"}, 409)
	f.json(t, "DELETE", target, f.alice, map[string]string{"expected_role": "viewer"}, 409)
	f.json(t, "PUT", target, f.alice, map[string]string{"role": "viewer", "expected_role": "invalid"}, 400)
	f.json(t, "DELETE", fmt.Sprintf("%s/%d", base, f.aliceID), f.alice, map[string]string{"expected_role": "viewer"}, 403)
	count := func() int64 {
		t.Helper()
		var n int64
		if err := f.db.Model(&model.OperationLog{}).Where("action LIKE 'workspace_member_%' AND detail LIKE ?", fmt.Sprintf("workspace_id=%d %%", created.Item.ID)).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	repo := storageMysql.NewCollaborationRepo(f.db)
	start := make(chan struct{})
	results := make(chan error, 2)
	var group sync.WaitGroup
	for _, role := range []string{"viewer", "member"} {
		group.Go(func() {
			<-start
			expected := "editor"
			results <- repo.ChangeWorkspaceMember(context.Background(), contract.WorkspaceMemberChange{
				WorkspaceID: created.Item.ID, ActorID: f.aliceID, TargetID: f.bobID, Role: role, ExpectedRole: &expected, UpdateOnly: true,
			})
		})
	}
	close(start)
	group.Wait()
	close(results)
	writes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			writes++
		} else if errors.Is(err, contract.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if writes != 1 || conflicts != 1 || count() != before+1 {
		t.Fatal("membership CAS was not atomic", writes, conflicts)
	}
	var saved model.WorkspaceUser
	if err := f.db.First(&saved, "workspace_id=? AND user_id=?", created.Item.ID, f.bobID).Error; err != nil || saved.Role != "viewer" && saved.Role != "member" {
		t.Fatal("wrong winning membership", err)
	}
	f.json(t, "DELETE", target, f.alice, map[string]string{"expected_role": saved.Role}, 200)
	f.json(t, "DELETE", target, f.alice, map[string]string{"expected_role": saved.Role}, 409)
	f.json(t, "POST", base, f.alice, map[string]any{"user_id": f.bobID, "role": "viewer"}, 200)
	f.json(t, "POST", base, f.alice, map[string]any{"user_id": f.bobID, "role": "editor"}, 200)
	f.json(t, "DELETE", target, f.alice, nil, 200)
}

func TestWorkspaceMembershipAuthorityAndAudit(t *testing.T) {
	f := newFixture(t)
	third := model.User{Username: fmt.Sprintf("workspace-third-%d", f.aliceID), Password: "unused fixture", Status: 1}
	if err := f.db.Create(&third).Error; err != nil {
		t.Fatal(err)
	}
	var created struct{ Item model.Workspace }
	decode(t, f.json(t, "POST", "/api/workspaces", f.alice, map[string]string{"name": "Membership fixture"}, 200), &created)
	if created.Item.ID == 0 {
		t.Fatal("workspace creation did not return an ID")
	}
	members := fmt.Sprintf("/api/workspaces/%d/members", created.Item.ID)
	member := func(id int64) string { return fmt.Sprintf("%s/%d", members, id) }
	invite := func(id int64, role string) map[string]any { return map[string]any{"user_id": id, "role": role} }
	role := func(value string) map[string]string { return map[string]string{"role": value} }

	f.json(t, "POST", members, f.bob, invite(third.ID, "editor"), 403)
	f.json(t, "POST", members, f.alice, invite(f.bobID, "admin"), 200)
	f.json(t, "POST", members, f.bob, invite(third.ID, "admin"), 403)
	f.json(t, "PUT", member(f.aliceID), f.bob, role("viewer"), 403)
	f.json(t, "DELETE", member(f.bobID), f.bob, nil, 403)
	f.json(t, "POST", members, f.bob, invite(third.ID, "editor"), 200)

	const drop = "DROP TRIGGER IF EXISTS test_workspace_member_audit"
	if err := f.db.Exec("CREATE TRIGGER test_workspace_member_audit BEFORE INSERT ON operation_log FOR EACH ROW BEGIN IF NEW.action='workspace_member_role_update' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='audit unavailable'; END IF; END").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Exec(drop).Error })
	f.json(t, "PUT", member(third.ID), f.bob, role("viewer"), 500)
	var saved model.WorkspaceUser
	if err := f.db.First(&saved, "workspace_id=? AND user_id=?", created.Item.ID, third.ID).Error; err != nil || saved.Role != "editor" {
		t.Fatal("failed audit changed member role", saved.Role, err)
	}
	if err := f.db.Exec(drop).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "PUT", member(third.ID), f.bob, role("viewer"), 200)
	f.json(t, "PUT", member(f.bobID), f.alice, role("viewer"), 200)
	f.json(t, "GET", members, f.bob, nil, 200)
	f.json(t, "DELETE", member(third.ID), f.bob, nil, 403)
	f.json(t, "DELETE", member(f.bobID), f.alice, nil, 200)
	f.json(t, "GET", members, f.bob, nil, 403)

	var count int64
	if err := f.db.Model(&model.OperationLog{}).Where("user_id IN ? AND action LIKE 'workspace_%'", []int64{f.aliceID, f.bobID}).Count(&count).Error; err != nil || count != 6 {
		t.Fatal("expected only committed workspace mutations in audit", count, err)
	}
}

func TestFileWriterCannotDelegateAndFailedAuditPreservesGrant(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "collaboration-authority.txt", []byte(t.Name()))
	third := model.User{Username: fmt.Sprintf("acl-third-%d", f.aliceID), Password: "unused fixture", Status: 1}
	if err := f.db.Create(&third).Error; err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/files/%d/collaborators", file.ID)
	grant := func(id int64) map[string]any { return map[string]any{"user_id": id, "permission": "write"} }
	revoke := fmt.Sprintf("%s/%d", path, f.bobID)

	f.json(t, "POST", path, f.alice, grant(f.bobID), 200)
	f.json(t, "POST", path, f.bob, grant(third.ID), 403)
	f.json(t, "DELETE", revoke, f.bob, nil, 403)
	if err := f.db.Exec("CREATE TRIGGER test_file_grant_audit BEFORE INSERT ON operation_log FOR EACH ROW BEGIN IF NEW.action='file_permission_revoke' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='audit unavailable'; END IF; END").Error; err != nil {
		t.Fatal(err)
	}
	const drop = "DROP TRIGGER IF EXISTS test_file_grant_audit"
	t.Cleanup(func() { _ = f.db.Exec(drop).Error })
	f.json(t, "DELETE", revoke, f.alice, nil, 500)
	download := fmt.Sprintf("/api/files/%d/download", file.ID)
	f.json(t, "GET", download, f.bob, nil, http.StatusOK)
	if err := f.db.Exec(drop).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "DELETE", revoke, f.alice, nil, 200)
	f.json(t, "GET", download, f.bob, nil, http.StatusForbidden)
}

func TestFilePermissionConditionalEditsAndLegacyDuplicates(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "conditional-grants.txt", []byte(t.Name()))
	base := fmt.Sprintf("/api/files/%d/collaborators", file.ID)
	target := fmt.Sprintf("%s/%d", base, f.bobID)
	f.json(t, "POST", base, f.alice, map[string]any{"user_id": f.bobID, "permission": "read", "create_only": true}, 200)
	f.json(t, "POST", base, f.alice, map[string]any{"user_id": f.bobID, "permission": "write", "create_only": true}, 409)
	f.json(t, "POST", base, f.alice, map[string]any{"user_id": f.bobID, "permission": "write", "expected_permission": "invalid"}, 400)
	f.json(t, "POST", base, f.alice, map[string]any{"user_id": f.bobID, "permission": "write", "create_only": true, "expected_permission": "read"}, 400)
	// Legacy upsert still works; stale conditional writers and removals do not.
	f.json(t, "POST", base, f.alice, map[string]any{"user_id": f.bobID, "permission": "write"}, 200)
	f.json(t, "DELETE", target, f.alice, map[string]string{"expected_permission": "read"}, 409)
	f.json(t, "POST", base, f.alice, map[string]any{"user_id": f.bobID, "permission": "read", "expected_permission": "read"}, 409)
	f.json(t, "DELETE", target, f.bob, map[string]string{"expected_permission": "read"}, 403)
	// Historical read+write duplicates grant effective write permission. One
	// conditional downgrade must update both rows, then revoke must remove both.
	if err := f.db.Create(&model.FilePermission{FileID: file.ID, UserID: f.bobID, Permission: "read"}).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", base, f.alice, map[string]any{"user_id": f.bobID, "permission": "read", "expected_permission": "write"}, 200)
	var rows []model.FilePermission
	if err := f.db.Where("file_id=? AND user_id=?", file.ID, f.bobID).Find(&rows).Error; err != nil || len(rows) != 2 {
		t.Fatal("duplicate fixture", err, len(rows))
	}
	for _, row := range rows {
		if row.Permission != "read" {
			t.Fatal("duplicate retained write access")
		}
	}
	count := func() int64 {
		t.Helper()
		var n int64
		if err := f.db.Model(&model.OperationLog{}).Where("file_id=? AND action LIKE 'file_permission_%'", file.ID).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	repo := storageMysql.NewCollaborationRepo(f.db)
	start, results := make(chan struct{}), make(chan error, 2)
	var group sync.WaitGroup
	for _, permission := range []string{"write", ""} {
		group.Go(func() {
			<-start
			expected := "read"
			results <- repo.ChangeFilePermission(context.Background(), contract.FilePermissionChange{
				ActorID: f.aliceID, FileID: file.ID, TargetID: f.bobID, Permission: permission, ExpectedPermission: &expected,
			})
		})
	}
	close(start)
	group.Wait()
	close(results)
	writes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			writes++
		} else if errors.Is(err, contract.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if writes != 1 || conflicts != 1 || count() != before+1 {
		t.Fatal("grant CAS was not atomic", writes, conflicts)
	}
	f.json(t, "DELETE", target, f.alice, nil, 200)
	f.json(t, "DELETE", target, f.alice, map[string]string{"expected_permission": "write"}, 409)
	rows = nil
	if err := f.db.Where("file_id=? AND user_id=?", file.ID, f.bobID).Find(&rows).Error; err != nil || len(rows) != 0 {
		t.Fatal("revoke retained a duplicate", err)
	}
	f.json(t, "POST", base, f.alice, map[string]any{"user_id": f.bobID, "permission": "read", "create_only": true}, 200)
	f.json(t, "DELETE", target, f.alice, map[string]string{"expected_permission": "read"}, 200)
	f.json(t, "GET", fmt.Sprintf("/api/files/%d/download", file.ID), f.bob, nil, 403)
}
