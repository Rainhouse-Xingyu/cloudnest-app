//go:build integration

package integration

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"cloudenest_go/model"
)

func fixtureShare(t *testing.T, f *fixture, token string, id int64) string {
	t.Helper()
	w := f.json(t, "POST", fmt.Sprintf("/api/files/%d/share", id), token, map[string]any{"expire_hours": 1}, http.StatusOK)
	var result struct {
		ShareCode string `json:"share_code"`
	}
	decode(t, w, &result)
	if result.ShareCode == "" {
		t.Fatal("missing share code")
	}
	return result.ShareCode
}

func TestRecursiveArchivesRequireEveryChildPermission(t *testing.T) {
	f := newFixture(t)
	folder := fixtureFolder(t, f, f.alice, "shared-archive", 0)
	secret := f.upload(t, f.bob, "private.bin", []byte("private descendant bytes"))
	if err := f.db.Model(&secret).Update("parent_id", folder.ID).Error; err != nil {
		t.Fatal(err)
	}
	archivePath := fmt.Sprintf("/api/files/%d/download-zip", folder.ID)
	share := fixtureShare(t, f, f.alice, folder.ID)
	f.json(t, "GET", archivePath, f.alice, nil, http.StatusForbidden)
	f.json(t, "POST", "/api/files/batch-download", f.alice, map[string]any{"file_ids": []int64{folder.ID}}, http.StatusForbidden)
	f.json(t, "GET", "/api/public/shares/"+share+"/download", "", nil, http.StatusForbidden)
	grant := model.FilePermission{FileID: secret.ID, UserID: f.aliceID, Permission: "read"}
	if err := f.db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	w := f.json(t, "GET", archivePath, f.alice, nil, http.StatusOK)
	z, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range z.File {
		if entry.Name == "shared-archive/private.bin" {
			r, err := entry.Open()
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(r)
			r.Close()
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != "private descendant bytes" {
				t.Fatal("archive bytes mismatch")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("authorized child omitted from archive")
	}
	if err := f.db.Delete(&grant).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "GET", "/api/public/shares/"+share+"/download", "", nil, http.StatusForbidden)
}

func TestPermanentDeleteRequiresOwnershipOfEveryDescendant(t *testing.T) {
	f := newFixture(t)
	root := fixtureFolder(t, f, f.alice, "legacy-deleted-tree", 0)
	secret := f.upload(t, f.bob, "foreign-deleted-child.bin", []byte("foreign child"))
	// Model a historical mixed-owner recycle tree; only disposable fixture rows.
	if err := f.db.Model(&secret).Updates(map[string]any{"parent_id": root.ID, "is_deleted": 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&root).Update("is_deleted", 1).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "DELETE", fmt.Sprintf("/api/files/%d/permanent", root.ID), f.alice, nil, http.StatusForbidden)
	for _, id := range []int64{root.ID, secret.ID} {
		var count int64
		if err := f.db.Model(&model.File{}).Where("id = ?", id).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatal("rejected deletion removed a descendant")
		}
	}
}

func TestMoveRequiresWriteOnEveryDescendant(t *testing.T) {
	f := newFixture(t)
	source := fixtureFolder(t, f, f.alice, "move-tree", 0)
	target := fixtureFolder(t, f, f.alice, "move-target", 0)
	secret := f.upload(t, f.bob, "foreign-child.bin", []byte("foreign child"))
	if err := f.db.Model(&secret).Update("parent_id", source.ID).Error; err != nil {
		t.Fatal(err)
	}
	request := map[string]any{"file_ids": []int64{source.ID}, "target_parent_id": target.ID}
	f.json(t, "POST", "/api/files/move", f.alice, request, http.StatusForbidden)
	grant := model.FilePermission{FileID: secret.ID, UserID: f.aliceID, Permission: "read"}
	if err := f.db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", "/api/files/move", f.alice, request, http.StatusForbidden)
	if err := f.db.Model(&grant).Update("permission", "write").Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", "/api/files/move", f.alice, request, http.StatusOK)
}

func TestBatchDownloadFailsOnUnauthorizedSelection(t *testing.T) {
	f := newFixture(t)
	mine := f.upload(t, f.alice, "mine.bin", []byte("mine"))
	other := f.upload(t, f.bob, "other.bin", []byte("other"))
	f.json(t, "POST", "/api/files/batch-download", f.alice, map[string]any{"file_ids": []int64{mine.ID, other.ID}}, http.StatusForbidden)
}

func TestShareCannotOutliveCreatorsPermission(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "reshared.bin", []byte("reshared bytes"))
	grant := model.FilePermission{FileID: file.ID, UserID: f.bobID, Permission: "read"}
	if err := f.db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	code := fixtureShare(t, f, f.bob, file.ID)
	f.json(t, "GET", "/api/public/shares/"+code+"/download", "", nil, http.StatusOK)
	if err := f.db.Delete(&grant).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "GET", "/api/public/shares/"+code, "", nil, http.StatusNotFound)
	f.json(t, "GET", "/api/public/shares/"+code+"/download", "", nil, http.StatusNotFound)
}

func TestArchivesPreserveNamesAndRejectMissingBytes(t *testing.T) {
	f := newFixture(t)
	root := fixtureFolder(t, f, f.alice, "space folder", 0)
	a := f.upload(t, f.alice, "a b.txt", []byte("spaces"))
	b := f.upload(t, f.alice, "a_b.txt", []byte("underscore"))
	for _, file := range []model.File{a, b} {
		if err := f.db.Model(&file).Update("parent_id", root.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
	endpoint := fmt.Sprintf("/api/files/%d/download-zip", root.ID)
	w := f.json(t, "GET", endpoint, f.alice, nil, http.StatusOK)
	entries := readArchive(t, w.Body.Bytes())
	if entries["space folder/a b.txt"] != "spaces" || entries["space folder/a_b.txt"] != "underscore" {
		t.Fatalf("names or bytes changed: %v", entries)
	}
	legacyFileNamesForTest(t, f)
	originalName := b.Filename
	t.Cleanup(func() {
		if err := f.db.Model(&model.File{}).Where("id=?", b.ID).Update("filename", originalName).Error; err != nil {
			t.Error(err)
		}
	})
	// Historical duplicate metadata must fail explicitly instead of ambiguous ZIP names.
	if err := f.db.Model(&b).Update("filename", a.Filename).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "GET", endpoint, f.alice, nil, http.StatusConflict)
	if err := f.db.Model(&b).Update("filename", originalName).Error; err != nil {
		t.Fatal(err)
	}
	var storage model.Storage
	if err := f.db.First(&storage, *a.StorageID).Error; err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(storage.FilePath); err != nil {
		t.Fatal(err)
	}
	f.json(t, "GET", endpoint, f.alice, nil, http.StatusNotFound)
	f.json(t, "POST", "/api/files/batch-download", f.alice, map[string]any{"file_ids": []int64{b.ID, a.ID}}, http.StatusNotFound)
}

func readArchive(t *testing.T, data []byte) map[string]string {
	t.Helper()
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	entries := make(map[string]string)
	for _, entry := range z.File {
		if _, exists := entries[entry.Name]; exists {
			t.Fatalf("duplicate ZIP path %q", entry.Name)
		}
		r, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		entries[entry.Name] = string(data)
	}
	return entries
}

func TestBatchArchiveReservesCompleteNames(t *testing.T) {
	f := newFixture(t)
	root := fixtureFolder(t, f, f.alice, "report", 0)
	file := f.upload(t, f.alice, "report.zip", []byte("ordinary file"))
	suffix := f.upload(t, f.alice, "report (1).zip", []byte("literal suffix"))
	w := f.json(t, "POST", "/api/files/batch-download", f.alice, map[string]any{"file_ids": []int64{file.ID, suffix.ID, root.ID}}, http.StatusOK)
	entries := readArchive(t, w.Body.Bytes())
	if len(entries) != 3 || entries["report.zip"] != "ordinary file" || entries["report (1).zip"] != "literal suffix" {
		t.Fatalf("unexpected archive: %v", entries)
	}
	nested := readArchive(t, []byte(entries["report (2).zip"]))
	if _, ok := nested["report/"]; !ok {
		t.Fatal("empty folder lost")
	}
}

func TestPublicFolderUsesCreatorPermissionsAndAccount(t *testing.T) {
	f := newFixture(t)
	root := fixtureFolder(t, f, f.alice, "reshared", 0)
	child := f.upload(t, f.alice, "owned.txt", []byte("owner can read this"))
	if err := f.db.Model(&child).Update("parent_id", root.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&model.FilePermission{FileID: root.ID, UserID: f.bobID, Permission: "read"}).Error; err != nil {
		t.Fatal(err)
	}
	code := fixtureShare(t, f, f.bob, root.ID)
	endpoint := "/api/public/shares/" + code
	f.json(t, "GET", endpoint+"/download", "", nil, http.StatusForbidden)
	if err := f.db.Create(&model.FilePermission{FileID: child.ID, UserID: f.bobID, Permission: "read"}).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "GET", endpoint+"/download", "", nil, http.StatusOK)
	if err := f.db.Model(&model.User{}).Where("id = ?", f.bobID).Update("status", 0).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "GET", endpoint, "", nil, http.StatusNotFound)
	f.json(t, "GET", endpoint+"/download", "", nil, http.StatusNotFound)
}

func TestPermanentDeleteRejectsActiveChildrenAndRetainsAudit(t *testing.T) {
	f := newFixture(t)
	root := fixtureFolder(t, f, f.alice, "historical-tree", 0)
	child := fixtureFolder(t, f, f.alice, "child", root.ID)
	if err := f.db.Model(&root).Update("is_deleted", 1).Error; err != nil {
		t.Fatal(err)
	}
	endpoint := fmt.Sprintf("/api/files/%d/permanent", root.ID)
	f.json(t, "DELETE", endpoint, f.alice, nil, http.StatusConflict)
	if err := f.db.Model(&child).Update("is_deleted", 1).Error; err != nil {
		t.Fatal(err)
	}
	audit := model.OperationLog{FileID: &child.ID, UserID: &f.aliceID, Action: "test_history", Status: "success", Detail: "must remain"}
	if err := f.db.Create(&audit).Error; err != nil {
		t.Fatal(err)
	}
	// Losing audit persistence must roll back dependent and file metadata deletes.
	const hook = "test:reject_file_delete_audit"
	if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if row, ok := tx.Statement.Dest.(*model.OperationLog); ok && row.Action == "permanent_delete" {
			tx.AddError(errors.New("test audit unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Callback().Create().Remove(hook) })
	f.json(t, "DELETE", endpoint, f.alice, nil, http.StatusInternalServerError)
	var count int64
	if err := f.db.Model(&model.File{}).Where("id IN ?", []int64{root.ID, child.ID}).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("failed audit changed tree: %d %v", count, err)
	}
	if err := f.db.Callback().Create().Remove(hook); err != nil {
		t.Fatal(err)
	}
	f.json(t, "DELETE", endpoint, f.alice, nil, http.StatusOK)
	if err := f.db.Model(&model.File{}).Where("id IN ?", []int64{root.ID, child.ID}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("tree remains: %d %v", count, err)
	}
	var saved model.OperationLog
	if err := f.db.First(&saved, audit.ID).Error; err != nil || saved.Detail != audit.Detail {
		t.Fatalf("history lost: %v", err)
	}
	if err := f.db.Model(&model.OperationLog{}).Where("file_id = ? AND action = ?", root.ID, "permanent_delete").Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("delete audit: %d %v", count, err)
	}
	var deletion model.OperationLog
	if err := f.db.Where("file_id = ? AND action = ?", root.ID, "permanent_delete").First(&deletion).Error; err != nil || deletion.ClientIP == "" {
		t.Fatalf("delete audit missing client: %v", err)
	}

}

func TestTreeCyclesDepthAndOverlappingRootsFail(t *testing.T) {
	f := newFixture(t)
	a := fixtureFolder(t, f, f.alice, "cycle-a", 0)
	b := fixtureFolder(t, f, f.alice, "cycle-b", a.ID)
	if err := f.db.Model(&a).Update("parent_id", b.ID).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "GET", fmt.Sprintf("/api/files/%d/download-zip", a.ID), f.alice, nil, http.StatusConflict)
	f.json(t, "POST", "/api/files/move", f.alice, map[string]any{"file_ids": []int64{a.ID}, "target_parent_id": 0}, http.StatusConflict)
	if err := f.db.Model(&model.File{}).Where("id IN ?", []int64{a.ID, b.ID}).Update("is_deleted", 1).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "DELETE", fmt.Sprintf("/api/files/%d/permanent", a.ID), f.alice, nil, http.StatusConflict)
	root := fixtureFolder(t, f, f.alice, "deep", 0)
	child := fixtureFolder(t, f, f.alice, "child", root.ID)
	f.json(t, "POST", "/api/files/copy", f.alice, map[string]any{"file_ids": []int64{root.ID, child.ID}, "target_parent_id": 0}, http.StatusConflict)
	parent := child.ID
	for range 255 {
		node := model.File{UserID: f.aliceID, Filename: "deep", ParentID: parent, IsFolder: 1}
		if err := f.db.Create(&node).Error; err != nil {
			t.Fatal(err)
		}
		parent = node.ID
	}
	f.json(t, "GET", fmt.Sprintf("/api/files/%d/download-zip", root.ID), f.alice, nil, http.StatusConflict)
}

func TestConcurrentOpposingMovesCannotCreateCycle(t *testing.T) {
	f := newFixture(t)
	for n := range 6 {
		a := fixtureFolder(t, f, f.alice, fmt.Sprintf("opposing-a-%d", n), 0)
		b := fixtureFolder(t, f, f.alice, fmt.Sprintf("opposing-b-%d", n), 0)
		start := make(chan struct{})
		results := make(chan int, 2)
		for _, pair := range [][2]int64{{a.ID, b.ID}, {b.ID, a.ID}} {
			go func(pair [2]int64) {
				<-start
				body := fmt.Sprintf(`{"file_ids":[%d],"target_parent_id":%d}`, pair[0], pair[1])
				req := httptest.NewRequest("POST", "/api/files/move", strings.NewReader(body))
				req.RemoteAddr = f.remoteAddr
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+f.alice)
				w := httptest.NewRecorder()
				f.router.ServeHTTP(w, req)
				results <- w.Code
			}(pair)
		}
		close(start)
		codes := map[int]int{}
		for range 2 {
			codes[<-results]++
		}
		if codes[http.StatusOK] != 1 || codes[http.StatusConflict] != 1 {
			t.Fatalf("opposing moves: %v", codes)
		}
	}
}
