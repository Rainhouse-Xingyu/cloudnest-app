//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"cloudenest_go/internal/store"
	"cloudenest_go/model"
)

func TestLivePhotoConcurrentBindingAndBothSourcePermissions(t *testing.T) {
	f := newFixture(t)
	image := f.upload(t, f.alice, "manual-still.heif", []byte("live photo still fixture"))
	first := f.upload(t, f.alice, "manual-motion-a.mov", []byte("live photo motion A fixture"))
	second := f.upload(t, f.alice, "manual-motion-b.mov", []byte("live photo motion B fixture"))
	body := func(videoID int64) map[string]int64 {
		return map[string]int64{"image_file_id": image.ID, "video_file_id": videoID}
	}
	const bind = "/api/live-photos/bind"
	imageGrant := model.FilePermission{FileID: image.ID, UserID: f.bobID, Permission: "write"}
	if err := f.db.Create(&imageGrant).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, http.MethodPost, bind, f.bob, body(first.ID), http.StatusForbidden)

	results := make(chan int, 8)
	start := make(chan struct{})
	for i := range cap(results) {
		video := []int64{first.ID, second.ID}[i%2]
		go func() {
			<-start
			raw, _ := json.Marshal(body(video))
			req := httptest.NewRequest(http.MethodPost, bind, bytes.NewReader(raw))
			req.RemoteAddr = f.remoteAddr
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+f.alice)
			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)
			results <- w.Code
		}()
	}
	close(start)
	statuses := map[int]int{}
	for range cap(results) {
		statuses[<-results]++
	}
	if statuses[200] != 1 || statuses[409] != 7 {
		t.Fatal("expected one binding and seven conflicts", statuses)
	}
	var row model.LivePhoto
	if err := f.db.First(&row, "image_file_id=?", image.ID).Error; err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/live-photos/%d", row.ID)
	f.json(t, http.MethodGet, path, f.bob, nil, http.StatusForbidden)
	videoGrant := model.FilePermission{FileID: row.VideoFileID, UserID: f.bobID, Permission: "read"}
	if err := f.db.Create(&videoGrant).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, http.MethodGet, path, f.bob, nil, http.StatusOK)
	f.json(t, http.MethodDelete, path, f.bob, nil, http.StatusForbidden)
	f.json(t, http.MethodDelete, path, f.alice, nil, http.StatusOK)
	f.json(t, http.MethodDelete, path, f.alice, nil, http.StatusNotFound)
	var logs int64
	if err := f.db.Model(&model.OperationLog{}).Where("file_id=? AND action IN ?", image.ID, []string{"live_photo_bind", "live_photo_unbind"}).Count(&logs).Error; err != nil || logs != 2 {
		t.Fatal("expected only committed bind and unbind audit rows", logs, err)
	}
}

func TestLivePhotoAutoBindUploadAndAuditRollback(t *testing.T) {
	f := newFixture(t)
	image := f.upload(t, f.alice, "upload-pair.heif", []byte("upload auto still"))
	video := f.upload(t, f.alice, "upload-pair.mov", []byte("upload auto motion"))
	var pair model.LivePhoto
	if err := f.db.First(&pair, "image_file_id=? AND video_file_id=?", image.ID, video.ID).Error; err != nil {
		t.Fatal("upload did not automatically bind supported sources", err)
	}
	f.json(t, http.MethodDelete, fmt.Sprintf("/api/live-photos/%d", pair.ID), f.alice, nil, http.StatusOK)
	const trigger = "live_photo_test_audit"
	if err := f.db.Exec("CREATE TRIGGER " + trigger + " BEFORE INSERT ON operation_log FOR EACH ROW BEGIN IF NEW.action='live_photo_auto_bind' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test audit failure'; END IF; END").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Exec("DROP TRIGGER IF EXISTS " + trigger).Error })
	request := map[string]any{"file_ids": []int64{image.ID, video.ID}}
	f.json(t, http.MethodPost, "/api/live-photos/auto-bind", f.alice, request, http.StatusInternalServerError)
	var count int64
	if err := f.db.Model(&model.LivePhoto{}).Where("image_file_id=?", image.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("failed audit left an automatic binding", count, err)
	}
	if err := f.db.Exec("DROP TRIGGER " + trigger).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, http.MethodPost, "/api/live-photos/auto-bind", f.alice, request, http.StatusOK)
	pair = model.LivePhoto{}
	if err := f.db.First(&pair, "image_file_id=? AND video_file_id=?", image.ID, video.ID).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, http.MethodDelete, fmt.Sprintf("/api/files/%d", video.ID), f.alice, nil, http.StatusOK)
	f.json(t, http.MethodGet, fmt.Sprintf("/api/live-photos/%d", pair.ID), f.alice, nil, http.StatusNotFound)
}

func TestLivePhotoMigrationPreservesHistoryAndRejectsDuplicates(t *testing.T) {
	f := newFixture(t)
	if err := f.db.Exec("ALTER TABLE live_photo DROP INDEX uq_live_photo_image_v1, DROP INDEX uq_live_photo_video_v1").Error; err != nil {
		t.Fatal(err)
	}
	a := model.LivePhoto{ImageFileID: 880001, VideoFileID: 880002}
	b := model.LivePhoto{ImageFileID: 880003, VideoFileID: a.VideoFileID}
	t.Cleanup(func() {
		if err := f.db.Delete(&model.LivePhoto{}, "id IN ?", []int64{a.ID, b.ID}).Error; err != nil {
			t.Error(err)
		}
		if err := store.Migrate(f.db); err != nil {
			t.Error(err)
		}
	})
	if store.CheckLivePhotoSchema(f.db) == nil {
		t.Fatal("startup accepted legacy indexes")
	}
	if err := f.db.Create(&a).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&b).Error; err != nil {
		t.Fatal(err)
	}
	if store.Migrate(f.db) == nil {
		t.Fatal("migration accepted ambiguous historic bindings")
	}
	var count int64
	if err := f.db.Model(&model.LivePhoto{}).Where("id IN ?", []int64{a.ID, b.ID}).Count(&count).Error; err != nil || count != 2 {
		t.Fatal("migration modified historical data", count, err)
	}
	if err := f.db.Delete(&b).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(f.db); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckLivePhotoSchema(f.db); err != nil {
		t.Fatal(err)
	}
	var saved model.LivePhoto
	if err := f.db.First(&saved, a.ID).Error; err != nil || saved.VideoFileID != a.VideoFileID {
		t.Fatal("upgrade did not preserve the existing binding", saved, err)
	}
}
