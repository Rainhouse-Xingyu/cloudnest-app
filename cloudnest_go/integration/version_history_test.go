//go:build integration

package integration

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"testing"

	"cloudenest_go/model"
)

func TestVersionHistoryReturnsItsAuthorizedCurrentVersion(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "native-history.txt", []byte("original version"))
	path := fmt.Sprintf("/api/files/%d/versions", file.ID)
	read := func() {
		t.Helper()
		var history struct {
			FileID         int64               `json:"file_id"`
			CurrentVersion int                 `json:"current_version"`
			Items          []model.FileVersion `json:"items"`
		}
		decode(t, f.json(t, "GET", path, f.alice, nil, 200), &history)
		if history.FileID != file.ID || history.CurrentVersion != 1 || len(history.Items) != 1 || history.Items[0].Version != history.CurrentVersion {
			t.Fatalf("history does not carry its current version: file=%d current=%d rows=%d", history.FileID, history.CurrentVersion, len(history.Items))
		}
	}
	read()
	f.json(t, "GET", path, f.bob, nil, 403)
	f.json(t, "POST", fmt.Sprintf("/api/files/%d/collaborators", file.ID), f.alice, map[string]any{"user_id": f.bobID, "permission": "read"}, 200)
	f.json(t, "GET", path, f.bob, nil, 200)
	f.json(t, "POST", path+"/1/restore", f.bob, map[string]int{"base_version": 1}, 403)
	f.json(t, "DELETE", fmt.Sprintf("/api/files/%d/collaborators/%d", file.ID, f.bobID), f.alice, nil, 200)
	f.json(t, "GET", path, f.bob, nil, 403)
	read()
}

func TestVersionHistoryRemainsCoherentWhileRestoresAppend(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "native-history-race.txt", []byte("version snapshot"))
	path := fmt.Sprintf("/api/files/%d/versions", file.ID)
	done := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		defer close(done)
		for base := 1; base <= 16; base++ {
			req := httptest.NewRequest("POST", path+"/1/restore", bytes.NewBufferString(fmt.Sprintf(`{"base_version":%d}`, base)))
			req.Header.Set("Authorization", "Bearer "+f.alice)
			req.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			f.router.ServeHTTP(response, req)
			if response.Code != 200 {
				result <- fmt.Errorf("restore %d returned %d", base, response.Code)
				return
			}
		}
		result <- nil
	}()
	defer func() { <-done }()
	for {
		var history struct {
			FileID         int64               `json:"file_id"`
			CurrentVersion int                 `json:"current_version"`
			Items          []model.FileVersion `json:"items"`
		}
		decode(t, f.json(t, "GET", path, f.alice, nil, 200), &history)
		if history.FileID != file.ID || len(history.Items) == 0 || len(history.Items) != history.CurrentVersion || history.Items[0].Version != history.CurrentVersion {
			t.Fatal("history and current version came from different snapshots")
		}
		select {
		case <-done:
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			return
		default:
		}
	}
}
