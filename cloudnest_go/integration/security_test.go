//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"cloudenest_go/internal/store"
	"cloudenest_go/model"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func TestVersionRejectsOtherUsersStorage(t *testing.T) {
	f := newFixture(t)
	target := f.upload(t, f.alice, "alice-version.bin", []byte("alice original"))
	source := f.upload(t, f.bob, "bob-secret.bin", []byte("bob confidential data"))
	path := fmt.Sprintf("/api/files/%d", target.ID)
	f.json(t, "POST", path+"/versions", f.alice, map[string]any{"storage_id": source.StorageID, "base_version": 1}, http.StatusForbidden)
	w := f.json(t, "GET", path+"/download", f.alice, nil, http.StatusOK)
	if w.Body.String() != "alice original" {
		t.Fatal("rejected version changed file contents")
	}
}

func TestVersionAuthorizationAndConcurrency(t *testing.T) {
	f := newFixture(t)
	target := f.upload(t, f.alice, "version-target.bin", []byte("original"))
	source := f.upload(t, f.alice, "version-source.bin", []byte("new content"))
	path := fmt.Sprintf("/api/files/%d/versions", target.ID)
	f.json(t, "POST", path, f.alice, map[string]any{"storage_id": source.StorageID, "base_version": 1, "size": -1}, http.StatusBadRequest)
	f.json(t, "POST", path, f.bob, map[string]any{"storage_id": source.StorageID, "base_version": 1}, http.StatusForbidden)
	// Concurrent requests with one base version must create exactly one version.
	raw, err := json.Marshal(map[string]any{"storage_id": source.StorageID, "base_version": 1})
	if err != nil {
		t.Fatal(err)
	}
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("POST", path, bytes.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+f.alice)
			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)
			statuses <- w.Code
		}()
	}
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatalf("concurrent version results: %v", counts)
	}
	var count int64
	if err := f.db.Model(&model.FileVersion{}).Where("file_id = ?", target.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("created %d versions, want 1", count)
	}
	got := f.json(t, "GET", fmt.Sprintf("/api/files/%d/download", target.ID), f.alice, nil, http.StatusOK)
	if got.Body.String() != "new content" {
		t.Fatal("new version bytes mismatch")
	}
	// An authorized writer may publish their own uploaded bytes, but a reader cannot.
	bobSource := f.upload(t, f.bob, "writer-source.bin", []byte("collaborator content"))
	permission := model.FilePermission{FileID: target.ID, UserID: f.bobID, Permission: "read"}
	if err := f.db.Create(&permission).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", path, f.bob, map[string]any{"storage_id": bobSource.StorageID, "base_version": 2}, http.StatusForbidden)
	if err := f.db.Model(&permission).Update("permission", "write").Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", path, f.bob, map[string]any{"storage_id": bobSource.StorageID, "base_version": 2}, http.StatusOK)
	// Deleted/unreferenced storage does not confer a capability.
	unlinked := f.upload(t, f.alice, "deleted-source.bin", []byte("deleted bytes"))
	if err := f.db.Model(&unlinked).Update("is_deleted", 1).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", path, f.alice, map[string]any{"storage_id": unlinked.StorageID, "base_version": 3}, http.StatusForbidden)
}

func TestAccountChangesTakeEffectForExistingSessions(t *testing.T) {
	f := newFixture(t)
	role := model.Role{RoleName: fmt.Sprintf("unused-%d", f.aliceID)}
	if err := f.db.Where("role_name = ?", "admin").First(&role).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		role = model.Role{RoleName: "admin"}
		if err := f.db.Create(&role).Error; err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	userRole := model.UserRole{UserID: f.aliceID, RoleID: role.ID}
	if err := f.db.Create(&userRole).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "GET", "/api/settings/permissions", f.alice, nil, http.StatusOK)
	if err := f.db.Delete(&userRole).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "GET", "/api/settings/permissions", f.alice, nil, http.StatusForbidden)
	if err := f.db.Model(&model.User{}).Where("id = ?", f.aliceID).Update("status", 0).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "GET", "/api/files", f.alice, nil, http.StatusUnauthorized)
	legacy := model.User{Username: fmt.Sprintf("legacy-%d", f.bobID), Password: "legacy-plaintext-password", Status: 1}
	if err := f.db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", "/api/auth/login", "", map[string]string{"username": legacy.Username, "password": legacy.Password}, http.StatusUnauthorized)
}

func TestBootstrapPreservesExistingAccounts(t *testing.T) {
	f := newFixture(t)
	password := "test-only-unique-bootstrap-password"
	if err := store.BootstrapAdminUser(f.db, password); err == nil {
		t.Fatal("bootstrap accepted populated user table")
	}
	// Only within this disposable database, roll back an empty-table scenario.
	rollback := errors.New("rollback disposable bootstrap scenario")
	err := f.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id > 0").Delete(&model.UserRole{}).Error; err != nil {
			return err
		}
		if err := tx.Where("id > 0").Delete(&model.User{}).Error; err != nil {
			return err
		}
		if err := store.BootstrapAdminUser(tx, password); err != nil {
			return err
		}
		var admin model.User
		if err := tx.Where("username = ?", "admin").First(&admin).Error; err != nil {
			return err
		}
		if err := bcrypt.CompareHashAndPassword([]byte(admin.Password), []byte(password)); err != nil {
			return err
		}
		if err := store.BootstrapAdminUser(tx, "another-test-only-bootstrap-password"); err == nil {
			return errors.New("second bootstrap modified existing accounts")
		}
		var roles int64
		if err := tx.Model(&model.UserRole{}).Where("user_id = ?", admin.ID).Count(&roles).Error; err != nil {
			return err
		}
		if roles != 1 {
			return fmt.Errorf("bootstrap created %d role bindings", roles)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
}

func TestPathPermissionRevocationIsImmediate(t *testing.T) {
	f := newFixture(t)
	read := model.UserPathPermission{UserID: f.aliceID, Method: "GET", Path: "/api/files"}
	write := model.UserPathPermission{UserID: f.aliceID, Method: "POST", Path: "/api/files/folders"}
	if err := f.db.Create(&read).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", "/api/files/folders", f.alice, map[string]string{"name": "revocation-test"}, http.StatusForbidden)
	if err := f.db.Create(&write).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", "/api/files/folders", f.alice, map[string]string{"name": "revocation-test"}, http.StatusOK)
	if err := f.db.Delete(&write).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", "/api/files/folders", f.alice, map[string]string{"name": "revocation-test-2"}, http.StatusForbidden)
}
