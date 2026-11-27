//go:build integration

package integration

import (
	"fmt"
	"net/url"
	"reflect"
	"testing"
	"time"

	"cloudenest_go/model"
)

type auditCursorResponse struct {
	Items      []model.OperationLog `json:"items"`
	Scope      string               `json:"scope"`
	UserID     int64                `json:"user_id"`
	Pagination struct {
		Limit int   `json:"limit"`
		Total int64 `json:"total"`
	} `json:"pagination"`
	Cursor struct {
		Snapshot int64 `json:"snapshot_id"`
		Before   int64 `json:"next_before_id"`
		After    int64 `json:"next_after_id"`
	} `json:"cursor"`
}

func TestOperationLogCursorRangePagingIsolationAndLegacy(t *testing.T) {
	f := newFixture(t)
	action := fmt.Sprintf("audit_cursor_%d", f.aliceID)
	now := time.Now().UTC().Truncate(time.Second)
	rows := make([]model.OperationLog, 0, 8)
	for i := 0; i < 8; i++ {
		owner := &f.aliceID
		if i >= 5 {
			owner = &f.bobID
		}
		if i == 7 {
			owner = nil
		}
		status := "success"
		if i == 3 || i == 4 {
			status = "failed"
		}
		rows = append(rows, model.OperationLog{UserID: owner, Action: action, Status: status, Detail: fmt.Sprintf("synthetic row %d", i), CreateTime: now})
	}
	if err := f.db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	base := "/api/operation-logs?cursor_mode=1&limit=2&action=" + url.QueryEscape(action)
	read := func(token, path string) auditCursorResponse {
		t.Helper()
		var out auditCursorResponse
		decode(t, f.json(t, "GET", path, token, nil, 200), &out)
		return out
	}
	ids := func(page auditCursorResponse) []int64 {
		out := []int64{}
		for _, row := range page.Items {
			out = append(out, row.ID)
		}
		return out
	}
	first := read(f.alice, base)
	if first.Scope != "self" || first.UserID != f.aliceID || first.Pagination.Total != 5 || first.Cursor.Snapshot != rows[4].ID || first.Cursor.After != 0 || !reflect.DeepEqual(ids(first), []int64{rows[4].ID, rows[3].ID}) {
		t.Fatal("first audit cursor", first)
	}
	newRow := model.OperationLog{UserID: &f.aliceID, Action: action, Status: "success", Detail: "inserted after first page", CreateTime: now}
	if err := f.db.Create(&newRow).Error; err != nil {
		t.Fatal(err)
	}
	next := read(f.alice, fmt.Sprintf("%s&snapshot_id=%d&before_id=%d", base, first.Cursor.Snapshot, first.Cursor.Before))
	if next.Pagination.Total != 5 || !reflect.DeepEqual(ids(next), []int64{rows[2].ID, rows[1].ID}) || next.Cursor.After != rows[2].ID {
		t.Fatal("insert shifted audit cursor", next)
	}
	last := read(f.alice, fmt.Sprintf("%s&snapshot_id=%d&before_id=%d", base, first.Cursor.Snapshot, next.Cursor.Before))
	if !reflect.DeepEqual(ids(last), []int64{rows[0].ID}) || last.Cursor.Before != 0 {
		t.Fatal("last audit page", last)
	}
	previous := read(f.alice, fmt.Sprintf("%s&snapshot_id=%d&after_id=%d", base, first.Cursor.Snapshot, next.Cursor.After))
	if !reflect.DeepEqual(ids(previous), ids(first)) || previous.Cursor.After != 0 {
		t.Fatal("previous audit page", previous)
	}
	refreshed := read(f.alice, base)
	if refreshed.Items[0].ID != newRow.ID || refreshed.Pagination.Total != 6 {
		t.Fatal("audit refresh did not establish a new range")
	}
	filtered := read(f.alice, base+"&status=failed")
	if filtered.Pagination.Total != 2 || filtered.Cursor.Before != 0 {
		t.Fatal("audit status filter", filtered)
	}
	dated := read(f.alice, base+"&start_time="+url.QueryEscape(now.Format(time.RFC3339))+"&end_time="+url.QueryEscape(now.Format(time.RFC3339)))
	if dated.Pagination.Total != 6 {
		t.Fatal("UTC audit date filter", dated.Pagination.Total)
	}
	f.json(t, "GET", base+"&all=1", f.bob, nil, 403)
	admin := model.Role{RoleName: "admin"}
	if err := f.db.Where("role_name=?", "admin").FirstOrCreate(&admin).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&model.UserRole{UserID: f.aliceID, RoleID: admin.ID}).Error; err != nil {
		t.Fatal(err)
	}
	all := read(f.alice, base+"&all=1")
	if all.Scope != "all" || all.UserID != f.aliceID || all.Pagination.Total != 9 || all.Cursor.Snapshot != newRow.ID {
		t.Fatal("admin audit scope", all)
	}
	var legacy struct {
		Items      []model.OperationLog
		Pagination struct {
			Page, Limit int
			Total       int64
		}
	}
	decode(t, f.json(t, "GET", "/api/operation-logs?all=1&action="+action, f.bob, nil, 200), &legacy)
	if legacy.Pagination.Total != 2 {
		t.Fatal("legacy ordinary all=1 changed visibility", legacy.Pagination.Total)
	}
	for _, row := range legacy.Items {
		if row.UserID == nil || *row.UserID != f.bobID {
			t.Fatal("legacy audit crossed accounts")
		}
	}
	for _, suffix := range []string{"&before_id=1", "&snapshot_id=-1", "&snapshot_id=5&before_id=2&after_id=1", "&start_time=bad", "&page=9223372036854775807"} {
		f.json(t, "GET", base+suffix, f.alice, nil, 400)
	}
	f.json(t, "GET", "/api/operation-logs?cursor_mode=2", f.alice, nil, 400)
	if err := f.db.Create(&model.UserPathPermission{UserID: f.bobID, Method: "GET", Path: "/api/files"}).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "GET", base, f.bob, nil, 403)
}
