//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"testing"

	"cloudenest_go/model"
)

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
