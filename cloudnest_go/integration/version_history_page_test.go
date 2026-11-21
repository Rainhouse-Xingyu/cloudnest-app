//go:build integration

package integration

import (
	"fmt"
	"reflect"
	"testing"

	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
)

func TestVersionHistoryPagesTraverseGapsInBothDirectionsAndReauthorize(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "paged-history.txt", []byte("immutable historical bytes"))
	var first model.FileVersion
	if err := f.db.Where("file_id=?", file.ID).First(&first).Error; err != nil {
		t.Fatal(err)
	}
	var rows []model.FileVersion
	var expected []int
	for v := 127; v > 0; v-- {
		if v == 34 || v == 88 {
			continue
		} // Old files need not have contiguous history.
		expected = append(expected, v)
		if v == 1 {
			continue
		}
		row := first
		row.ID = 0
		row.Version = v
		rows = append(rows, row)
	}
	if err := f.db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.File{}).Where("id=?", file.ID).Update("current_version", 127).Error; err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/files/%d/versions", file.ID)
	read := func(query, token string, status int) contract.CollaborationFileVersionHistory {
		t.Helper()
		var h contract.CollaborationFileVersionHistory
		response := f.json(t, "GET", path+query, token, nil, status)
		if status == 200 {
			decode(t, response, &h)
		}
		return h
	}
	var legacy map[string]any
	decode(t, f.json(t, "GET", path, f.alice, nil, 200), &legacy)
	if legacy["page"] != nil || len(legacy["items"].([]any)) != len(expected) {
		t.Fatal("legacy history was silently truncated")
	}
	page := read("?page_size=50", f.alice, 200)
	var got []int
	for {
		if page.Page == nil || page.Page.Size != 50 || page.Page.Snapshot != 127 || len(page.Items) > 50 {
			t.Fatal("unbounded or unbound page")
		}
		for _, row := range page.Items {
			got = append(got, row.Version)
		}
		if !page.Page.HasOlder {
			break
		}
		page = read(fmt.Sprintf("?page_size=50&before_version=%d&snapshot_version=127", page.Items[len(page.Items)-1].Version), f.alice, 200)
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("page traversal skipped or repeated history: %v", got)
	}
	for page.Page.HasNewer {
		previousFirst := page.Items[0].Version
		page = read(fmt.Sprintf("?page_size=50&after_version=%d&snapshot_version=127", previousFirst), f.alice, 200)
		if len(page.Items) > 50 || page.Items[len(page.Items)-1].Version <= previousFirst {
			t.Fatal("newer page crossed cursor")
		}
	}
	if page.Items[0].Version != 127 {
		t.Fatal("newer traversal did not return to latest")
	}
	f.json(t, "POST", path[:len(path)-len("/versions")]+"/collaborators", f.alice, map[string]any{"user_id": f.bobID, "permission": "read"}, 200)
	read("?page_size=50", f.bob, 200)
	f.json(t, "DELETE", fmt.Sprintf("/api/files/%d/collaborators/%d", file.ID, f.bobID), f.alice, nil, 200)
	read("?page_size=50&before_version=78&snapshot_version=127", f.bob, 403)
	f.json(t, "POST", path+"/1/restore", f.alice, map[string]int{"base_version": 127}, 200)
	read("?page_size=50&before_version=78&snapshot_version=127", f.alice, 409)
	read("?page_size=50&after_version=78&snapshot_version=127", f.alice, 409)
	if current := read("?page_size=50", f.alice, 200); current.CurrentVersion != 128 || current.Items[0].Version != 128 {
		t.Fatal("refresh did not capture new generation")
	}
}

func TestVersionHistoryPageRejectsMalformedAndUnboundedQueries(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "history-page-input.txt", []byte("page"))
	path := fmt.Sprintf("/api/files/%d/versions", file.ID)
	for _, query := range []string{
		"page_size=0", "page_size=101", "page_size=-1", "page_size=01", "page_size=50&page_size=50",
		"page_size=50&before_version=1", "page_size=50&after_version=1", "page_size=50&snapshot_version=1",
		"page_size=50&before_version=1&after_version=1&snapshot_version=1",
		"page_size=50&before_version=2&snapshot_version=1", "page_size=50&before_version=0&snapshot_version=1",
		"page_size=50&snapshot_version=2147483648&after_version=1", "page_size=50&unknown=1", "page_size=%zz",
	} {
		f.json(t, "GET", path+"?"+query, f.alice, nil, 400)
	}
	f.json(t, "GET", path+"?page_size=50", f.bob, nil, 403)
}
