//go:build integration

package integration

import (
	"fmt"
	"strings"
	"testing"

	"cloudenest_go/internal/store"
	"cloudenest_go/model"
)

func TestWorkspaceFileRolesAndRevocation(t *testing.T) {
	f := newFixture(t)
	var response struct{ Item model.Workspace }
	decode(t, f.json(t, "POST", "/api/workspaces", f.alice, map[string]string{"name": "Scoped workspace"}, 200), &response)
	workspace := response.Item
	if workspace.RootFileID == nil {
		t.Fatal("missing workspace root")
	}
	root := *workspace.RootFileID
	member := fmt.Sprintf("/api/workspaces/%d/members/%d", workspace.ID, f.bobID)
	list := func(id int64) string { return fmt.Sprintf("/api/files?parent_id=%d", id) }
	var folders struct{ Item model.File }
	folderInput := map[string]any{"name": "Workspace folder", "parent_id": root}
	decode(t, f.json(t, "POST", "/api/files/folders", f.alice, folderInput, 200), &folders)
	folder := folders.Item.ID
	file := f.uploadTo(t, f.alice, "workspace-document.txt", []byte(t.Name()), folder)
	if file.WorkspaceID == nil || *file.WorkspaceID != workspace.ID {
		t.Fatal("upload did not inherit destination workspace")
	}
	path := fmt.Sprintf("/api/files/%d", file.ID)
	f.json(t, "GET", list(root), f.bob, nil, 403)
	f.json(t, "GET", path+"/download", f.bob, nil, 403)

	f.json(t, "POST", fmt.Sprintf("/api/workspaces/%d/members", workspace.ID), f.alice, map[string]any{"user_id": f.bobID, "role": "viewer"}, 200)
	f.json(t, "GET", list(root), f.bob, nil, 200)
	w := f.json(t, "GET", list(folder), f.bob, nil, 200)
	if !strings.Contains(w.Body.String(), file.Filename) {
		t.Fatal("workspace file is missing from viewer listing")
	}
	f.json(t, "GET", path+"/download", f.bob, nil, 200)
	search := "/api/files/search?name=workspace-document"
	if w := f.json(t, "GET", search, f.bob, nil, 200); !strings.Contains(w.Body.String(), file.Filename) {
		t.Fatal("workspace search omitted a readable file")
	}
	archive := f.json(t, "GET", fmt.Sprintf("/api/files/%d/download-zip", folder), f.bob, nil, 200)
	if readArchive(t, archive.Body.Bytes())["Workspace folder/"+file.Filename] != t.Name() {
		t.Fatal("archive did not contain workspace file")
	}
	f.json(t, "GET", fmt.Sprintf("/api/ai/index/%d", file.ID), f.bob, nil, 200)
	f.json(t, "POST", "/api/files/folders", f.bob, folderInput, 403)
	f.json(t, "PUT", path+"/rename", f.bob, map[string]string{"name": "viewer-denied.txt"}, 403)
	f.json(t, "DELETE", path, f.bob, nil, 403)

	f.json(t, "PUT", member, f.alice, map[string]string{"role": "editor"}, 200)
	f.json(t, "PUT", path+"/rename", f.bob, map[string]string{"name": "workspace-renamed.txt"}, 200)
	own := f.uploadTo(t, f.bob, "editor-created.txt", []byte("editor-created workspace fixture"), folder)
	ownPath := fmt.Sprintf("/api/files/%d", own.ID)
	f.json(t, "GET", ownPath+"/download", f.alice, nil, 200)
	f.json(t, "POST", "/api/files/move", f.bob, map[string]any{"file_ids": []int64{own.ID}, "target_parent_id": root}, 200)
	f.json(t, "POST", "/api/files/copy", f.bob, map[string]any{"file_ids": []int64{own.ID}, "target_parent_id": folder}, 200)
	f.json(t, "POST", "/api/files/move", f.bob, map[string]any{"file_ids": []int64{own.ID}}, 403)
	f.json(t, "POST", "/api/files/copy", f.bob, map[string]any{"file_ids": []int64{own.ID}}, 403)
	f.json(t, "POST", ownPath+"/share", f.bob, map[string]any{}, 403)
	f.json(t, "POST", ownPath+"/collaborators", f.bob, map[string]any{"user_id": f.aliceID, "permission": "write"}, 403)
	f.json(t, "DELETE", path, f.bob, nil, 200)
	f.json(t, "POST", path+"/restore", f.bob, nil, 200)
	private := f.upload(t, f.bob, "private-version-target.txt", []byte("private target"))
	version := fmt.Sprintf("/api/files/%d/versions", private.ID)
	f.json(t, "POST", version, f.bob, map[string]any{"storage_id": own.StorageID, "base_version": 1}, 403)
	f.json(t, "PUT", member, f.alice, map[string]string{"role": "viewer"}, 200)
	f.json(t, "GET", ownPath+"/download", f.bob, nil, 200)
	f.json(t, "PUT", ownPath+"/rename", f.bob, map[string]string{"name": "downgraded.txt"}, 403)
	f.json(t, "DELETE", member, f.alice, nil, 200)
	if w := f.json(t, "GET", "/api/files/search?name=editor-created", f.bob, nil, 200); strings.Contains(w.Body.String(), own.Filename) {
		t.Fatal("workspace search retained a revoked file")
	}

	f.json(t, "GET", ownPath+"/download", f.bob, nil, 403)
	f.json(t, "PUT", ownPath+"/rename", f.bob, map[string]string{"name": "revoked.txt"}, 403)
	f.json(t, "GET", list(root), f.bob, nil, 403)
	f.json(t, "GET", fmt.Sprintf("/api/ai/index/%d", own.ID), f.bob, nil, 404)
	f.json(t, "GET", ownPath+"/download", f.alice, nil, 200)
}

func TestWorkspaceRootMigrationDoesNotGrantLegacyFileAccess(t *testing.T) {
	f := newFixture(t)
	legacy := model.Workspace{Name: "Legacy workspace", OwnerID: f.aliceID}
	if err := f.db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&model.WorkspaceUser{WorkspaceID: legacy.ID, UserID: f.bobID, Role: "member"}).Error; err != nil {
		t.Fatal(err)
	}
	file := f.upload(t, f.alice, "legacy-workspace.txt", []byte(t.Name()))
	if err := f.db.Model(&file).Update("workspace_id", legacy.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec("ALTER TABLE workspace DROP INDEX uq_workspace_root_v1, DROP COLUMN root_file_id").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Migrate(f.db); err != nil {
			t.Error(err)
		}
	})
	if store.CheckWorkspaceRootSchema(f.db) == nil || f.db.Migrator().HasColumn(&model.Workspace{}, "root_file_id") {
		t.Fatal("schema verifier accepted or changed legacy schema")
	}
	if err := store.Migrate(f.db); err != nil {
		t.Fatal(err)
	}
	if err := f.db.First(&legacy, legacy.ID).Error; err != nil || legacy.RootFileID != nil {
		t.Fatal("legacy workspace was automatically activated", legacy.RootFileID, err)
	}
	path := fmt.Sprintf("/api/files/%d/download", file.ID)
	f.json(t, "GET", path, f.bob, nil, 403)
	f.json(t, "GET", path, f.alice, nil, 200)
}
