//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"cloudenest_go/internal/app"
	"cloudenest_go/model"
)

func fixtureFolder(t *testing.T, f *fixture, token, name string, parentID int64) model.File {
	t.Helper()
	w := f.json(t, "POST", "/api/files/folders", token, map[string]any{"name": name, "parent_id": parentID}, http.StatusOK)
	var result struct {
		Item model.File `json:"item"`
	}
	decode(t, w, &result)
	return result.Item
}

func TestRootNamespacesArePerOwner(t *testing.T) {
	f := newFixture(t)
	alice := fixtureFolder(t, f, f.alice, "documents", 0)
	bob := fixtureFolder(t, f, f.bob, "documents", 0)
	if alice.ID == bob.ID || alice.UserID == bob.UserID {
		t.Fatal("distinct roots reused an object")
	}
	other := fixtureFolder(t, f, f.bob, "temporary", 0)
	fixtureFolder(t, f, f.alice, "alice-only-name", 0)
	f.json(t, "PUT", fmt.Sprintf("/api/files/%d/rename", other.ID), f.bob, map[string]string{"name": "alice-only-name"}, http.StatusOK)
	// Shared write access does not move a root file into the editor's namespace.
	grant := model.FilePermission{FileID: alice.ID, UserID: f.bobID, Permission: "write"}
	if err := f.db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "PUT", fmt.Sprintf("/api/files/%d/rename", alice.ID), f.bob, map[string]string{"name": "alice-only-name"}, http.StatusConflict)
	child := fixtureFolder(t, f, f.alice, "shared-parent", 0)
	grant = model.FilePermission{FileID: child.ID, UserID: f.bobID, Permission: "write"}
	if err := f.db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	fixtureFolder(t, f, f.alice, "child", child.ID)
	f.json(t, "POST", "/api/files/folders", f.bob, map[string]any{"name": "child", "parent_id": child.ID}, http.StatusConflict)
}

func TestConcurrentFolderNames(t *testing.T) {
	f := newFixture(t)
	start := make(chan struct{})
	results := make(chan int, 12)
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			req := httptest.NewRequest("POST", "/api/files/folders", bytes.NewBufferString(`{"name":"concurrent-folder"}`))
			req.RemoteAddr = f.remoteAddr
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+f.alice)
			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)
			results <- w.Code
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	counts := map[int]int{}
	for code := range results {
		counts[code]++
	}
	if counts[200] != 1 || counts[409] != 11 {
		t.Fatalf("concurrent folder results: %v, want one success and eleven conflicts", counts)
	}
	var rows int64
	if err := f.db.Model(&model.File{}).Where("user_id = ? AND parent_id = 0 AND filename = ? AND is_deleted = 0", f.aliceID, "concurrent-folder").Count(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("persisted %d duplicate folders", rows)
	}
}

func TestKeepBothNamesAcrossFileOperations(t *testing.T) {
	f := newFixture(t)
	first := f.upload(t, f.alice, "report.bin", []byte("first report"))
	second := f.upload(t, f.alice, "report.bin", []byte("second report"))
	if first.Filename != "report.bin" || second.Filename != "report (1).bin" {
		t.Fatal("upload did not preserve both names")
	}
	// An unrelated user's root does not reserve or rename this filename.
	other := f.upload(t, f.bob, "report.bin", []byte("bob report"))
	if other.Filename != "report.bin" {
		t.Fatal("other user's root affected naming")
	}
	for _, want := range []string{"report (2).bin", "report (3).bin"} {
		f.json(t, "POST", "/api/files/copy", f.alice, map[string]any{"file_ids": []int64{first.ID}, "target_parent_id": 0}, http.StatusOK)
		var count int64
		if err := f.db.Model(&model.File{}).Where("user_id = ? AND parent_id = 0 AND filename = ? AND is_deleted = 0", f.aliceID, want).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("copy did not allocate %s", want)
		}
	}
	f.json(t, "POST", "/api/files/move", f.alice, map[string]any{"file_ids": []int64{first.ID}, "target_parent_id": 0}, http.StatusOK)
	var moved model.File
	if err := f.db.First(&moved, first.ID).Error; err != nil {
		t.Fatal(err)
	}
	if moved.Filename != first.Filename {
		t.Fatal("move to current directory renamed the file")
	}
	f.json(t, "DELETE", fmt.Sprintf("/api/files/%d", first.ID), f.alice, nil, http.StatusOK)
	replacement := f.upload(t, f.alice, "report.bin", []byte("replacement report"))
	if replacement.Filename != first.Filename {
		t.Fatal("deleted name was not available")
	}
	w := f.json(t, "POST", fmt.Sprintf("/api/files/%d/restore", first.ID), f.alice, nil, http.StatusOK)
	var restored struct {
		Item model.File `json:"item"`
	}
	decode(t, w, &restored)
	if restored.Item.Filename != "report (4).bin" {
		t.Fatal("restore did not keep both versions")
	}
	for id, want := range map[int64]string{first.ID: "first report", second.ID: "second report", replacement.ID: "replacement report", other.ID: "bob report"} {
		token := f.alice
		if id == other.ID {
			token = f.bob
		}
		w := f.json(t, "GET", fmt.Sprintf("/api/files/%d/download", id), token, nil, http.StatusOK)
		if w.Body.String() != want {
			t.Fatal("name allocation changed file bytes")
		}
	}
}

func TestConcurrentCopiesAndRenames(t *testing.T) {
	f := newFixture(t)
	source := fixtureFolder(t, f, f.alice, "parallel-copy", 0)
	second := fixtureRouter(app.NewContainer(f.cfg, f.db, f.rdb).NewRouter())
	run := func(method string, paths []string, bodies []any) map[int]int {
		t.Helper()
		start := make(chan struct{})
		results := make(chan int, len(paths))
		var wg sync.WaitGroup
		for i, path := range paths {
			raw, err := json.Marshal(bodies[i])
			if err != nil {
				t.Fatal(err)
			}
			wg.Add(1)
			go func(i int, path string, raw []byte) {
				defer wg.Done()
				<-start
				req := httptest.NewRequest(method, path, bytes.NewReader(raw))
				req.Header.Set("Authorization", "Bearer "+f.alice)
				req.Header.Set("Content-Type", "application/json")
				req.RemoteAddr = f.remoteAddr
				w := httptest.NewRecorder()
				if i%2 == 0 {
					f.router.ServeHTTP(w, req)
				} else {
					second.ServeHTTP(w, req)
				}
				results <- w.Code
			}(i, path, raw)
		}
		close(start)
		wg.Wait()
		close(results)
		counts := map[int]int{}
		for code := range results {
			counts[code]++
		}
		return counts
	}
	paths, bodies := make([]string, 12), make([]any, 12)
	for i := range paths {
		paths[i] = "/api/files/copy"
		bodies[i] = map[string]any{"file_ids": []int64{source.ID}, "target_parent_id": 0}
	}
	if counts := run("POST", paths, bodies); counts[200] != 12 {
		t.Fatalf("parallel copies: %v", counts)
	}
	var rows []model.File
	if err := f.db.Where("user_id = ? AND parent_id = 0 AND filename LIKE ?", f.aliceID, "parallel-copy%").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, row := range rows {
		if names[row.Filename] {
			t.Fatal("duplicate copy name")
		}
		names[row.Filename] = true
	}
	if len(names) != 13 {
		t.Fatalf("got %d copy names", len(names))
	}
	for i := range paths {
		paths[i] = fmt.Sprintf("/api/files/%d/rename", rows[i].ID)
		bodies[i] = map[string]string{"name": "parallel-rename"}
	}
	if counts := run("PUT", paths, bodies); counts[200] != 1 || counts[409] != 11 {
		t.Fatalf("parallel renames: %v", counts)
	}
}

func TestFolderDeletionSerializesWithChildCreation(t *testing.T) {
	f := newFixture(t)
	for i := range 8 {
		parent := fixtureFolder(t, f, f.alice, fmt.Sprintf("delete-create-race-%d", i), 0)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for _, method := range []string{"POST", "DELETE"} {
			wg.Add(1)
			go func(method string) {
				defer wg.Done()
				<-start
				path := fmt.Sprintf("/api/files/%d", parent.ID)
				raw := []byte("null")
				if method == "POST" {
					path = "/api/files/folders"
					raw = []byte(fmt.Sprintf(`{"name":"child","parent_id":%d}`, parent.ID))
				}
				req := httptest.NewRequest(method, path, bytes.NewReader(raw))
				req.Header.Set("Authorization", "Bearer "+f.alice)
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				f.router.ServeHTTP(w, req)
				if w.Code != 200 && w.Code != 403 && w.Code != 404 && w.Code != 409 {
					t.Errorf("unexpected delete/create race status %d", w.Code)
				}
			}(method)
		}
		close(start)
		wg.Wait()
		var orphans int64
		if err := f.db.Table("file c").Joins("JOIN file p ON p.id = c.parent_id").Where("p.id = ? AND p.is_deleted = 1 AND c.is_deleted = 0", parent.ID).Count(&orphans).Error; err != nil {
			t.Fatal(err)
		}
		if orphans != 0 {
			t.Fatal("active child persisted under deleted folder")
		}
	}
}

func TestCopyTreeChecksDescendantsAndCycles(t *testing.T) {
	f := newFixture(t)
	parent := fixtureFolder(t, f, f.alice, "mixed-owner-folder", 0)
	secret := f.upload(t, f.bob, "private-child.bin", []byte("private child bytes"))
	if err := f.db.Model(&secret).Update("parent_id", parent.ID).Error; err != nil {
		t.Fatal(err)
	}
	request := map[string]any{"file_ids": []int64{parent.ID}, "target_parent_id": 0}
	f.json(t, "POST", "/api/files/copy", f.alice, request, http.StatusForbidden)
	var copied int64
	if err := f.db.Model(&model.File{}).Where("user_id = ? AND parent_id = 0 AND filename = ?", f.aliceID, "mixed-owner-folder (1)").Count(&copied).Error; err != nil {
		t.Fatal(err)
	}
	if copied != 0 {
		t.Fatal("failed recursive copy left partial output")
	}
	grant := model.FilePermission{FileID: secret.ID, UserID: f.aliceID, Permission: "read"}
	if err := f.db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", "/api/files/copy", f.alice, request, http.StatusOK)
	child := fixtureFolder(t, f, f.alice, "nested", parent.ID)
	for _, target := range []int64{parent.ID, child.ID} {
		f.json(t, "POST", "/api/files/copy", f.alice, map[string]any{"file_ids": []int64{parent.ID}, "target_parent_id": target}, http.StatusConflict)
	}
	// A corrupt historical two-node cycle must fail within bounded traversal.
	cycleA := fixtureFolder(t, f, f.alice, "cycle-a", 0)
	cycleB := fixtureFolder(t, f, f.alice, "cycle-b", cycleA.ID)
	if err := f.db.Model(&cycleA).Update("parent_id", cycleB.ID).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", "/api/files/copy", f.alice, map[string]any{"file_ids": []int64{cycleA.ID}, "target_parent_id": 0}, http.StatusConflict)
}

func TestNameValidationAndMissingRestoreParent(t *testing.T) {
	f := newFixture(t)
	for _, name := range []string{".", "..", "bad/path", "bad\\path", "bad\x00name", strings.Repeat("中", 256)} {
		f.json(t, "POST", "/api/files/folders", f.alice, map[string]string{"name": name}, http.StatusBadRequest)
		f.json(t, "POST", "/api/files/upload/init", f.alice, map[string]any{"filename": name, "total_size": 1, "chunk_size": 1, "total_chunks": 1}, http.StatusBadRequest)
	}
	folder := fixtureFolder(t, f, f.alice, strings.Repeat("中", 255), 0)
	f.json(t, "POST", "/api/files/copy", f.alice, map[string]any{"file_ids": []int64{folder.ID}, "target_parent_id": 0}, http.StatusOK)
	var longCopy model.File
	if err := f.db.Where("user_id = ? AND filename = ?", f.aliceID, strings.Repeat("中", 251)+" (1)").First(&longCopy).Error; err != nil {
		t.Fatal(err)
	}
	parent := fixtureFolder(t, f, f.alice, "restore-parent", 0)
	child := fixtureFolder(t, f, f.alice, "restore-child", parent.ID)
	f.json(t, "DELETE", fmt.Sprintf("/api/files/%d", child.ID), f.alice, nil, http.StatusOK)
	f.json(t, "DELETE", fmt.Sprintf("/api/files/%d", parent.ID), f.alice, nil, http.StatusOK)
	f.json(t, "POST", fmt.Sprintf("/api/files/%d/restore", child.ID), f.alice, nil, http.StatusNotFound)
	var stillDeleted model.File
	if err := f.db.First(&stillDeleted, child.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stillDeleted.IsDeleted != 1 {
		t.Fatal("restore into missing parent changed the item")
	}
}
