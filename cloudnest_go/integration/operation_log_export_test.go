//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"cloudenest_go/internal/transfer"
	"cloudenest_go/model"
	"gorm.io/gorm"
)

func TestOperationLogCSVAdmissionPrecedesHistoricalRowReads(t *testing.T) {
	f := newFixture(t)
	var held []*transfer.Handle
	t.Cleanup(func() {
		for _, handle := range held {
			_ = handle.Close(true)
		}
	})
	for i := 0; i < 2; i++ {
		handle, err := transferManager(f).CreateArchive(context.Background(), f.aliceID)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, handle)
	}
	var queries atomic.Int32
	const callback = "native_audit_admission_query"
	if err := f.db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "operation_log" {
			queries.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Query().Remove(callback) })
	f.json(t, "GET", "/api/operation-logs/export", f.alice, nil, 409)
	if queries.Load() != 0 {
		t.Fatal("rejected audit export loaded historical rows", queries.Load())
	}
	for _, handle := range held {
		if err := handle.Close(true); err != nil {
			t.Fatal(err)
		}
	}
	held = nil
	if transferAudit(t, f).Usage.Charged != 0 {
		t.Fatal("admission test retained its temporary holds")
	}
}

func TestOperationLogCSVSnapshotBoundsDigestPrivacyAndTCPRelease(t *testing.T) {
	f := newFixture(t)
	preserveSettings(t, f, []string{"download_speed_limit_kbps"})
	rateSetting(t, f, "download_speed_limit_kbps", "10240")
	action := fmt.Sprintf("audit_csv_%d", f.aliceID)
	now := time.Now().UTC().Truncate(time.Second)
	rows := make([]model.OperationLog, 5002)
	for i := range rows {
		rows[i] = model.OperationLog{UserID: &f.aliceID, Action: action, Status: "success", Detail: fmt.Sprintf("synthetic export row %d with a bounded comma, value", i), ClientIP: "2001:db8::1", CreateTime: now}
	}
	rows[len(rows)-1].Detail = "=SUM(1,2)"
	rows[len(rows)-2].Detail = "quoted \"text\" and\nnewline"
	if err := f.db.CreateInBatches(&rows, 128).Error; err != nil {
		t.Fatal(err)
	}
	peer := model.OperationLog{UserID: &f.bobID, Action: action, Status: "success", Detail: "synthetic-peer-private", CreateTime: now}
	if err := f.db.Create(&peer).Error; err != nil {
		t.Fatal(err)
	}
	path := "/api/operation-logs/export?action=" + action
	read := func(token, path string, records int) *httptest.ResponseRecorder {
		t.Helper()
		w := f.json(t, "GET", path, token, nil, 200)
		assertIdentity(t, w, w.Body.Bytes())
		all, err := csv.NewReader(bytes.NewReader(w.Body.Bytes())).ReadAll()
		if err != nil || len(all) != records+1 {
			t.Fatal("CSV complete record count", len(all), err)
		}
		for _, row := range all {
			if len(row) != 8 {
				t.Fatal("CSV columns", len(row))
			}
		}
		if w.Header().Get("Content-Type") != "text/csv; charset=utf-8" || w.Header().Get("X-CloudNest-Audit-Rows") != strconv.Itoa(records) || w.Header().Get("X-CloudNest-Audit-Timezone") != "UTC" {
			t.Fatal("CSV receipt", w.Header())
		}
		return w
	}
	ordinary := read(f.alice, path, 5000)
	if ordinary.Header().Get("X-CloudNest-Audit-Truncated") != "true" || ordinary.Header().Get("X-CloudNest-Audit-Scope") != "self" || ordinary.Header().Get("X-CloudNest-Audit-User") != strconv.FormatInt(f.aliceID, 10) || bytes.Contains(ordinary.Body.Bytes(), []byte(peer.Detail)) {
		t.Fatal("CSV cap or privacy receipt")
	}
	parsed, err := csv.NewReader(bytes.NewReader(ordinary.Body.Bytes())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if parsed[1][5] != "'=SUM(1,2)" || parsed[2][5] != rows[len(rows)-2].Detail {
		t.Fatal("CSV formula or quoting boundary")
	}
	snapshot := read(f.alice, fmt.Sprintf("%s&snapshot_id=%d", path, rows[9].ID), 10)
	if snapshot.Header().Get("X-CloudNest-Audit-Snapshot") != strconv.FormatInt(rows[9].ID, 10) || snapshot.Header().Get("X-CloudNest-Audit-Truncated") != "false" {
		t.Fatal("CSV snapshot mismatch")
	}
	peerExport := read(f.bob, path+"&all=1", 1)
	if peerExport.Header().Get("X-CloudNest-Audit-Scope") != "self" || !bytes.Contains(peerExport.Body.Bytes(), []byte(peer.Detail)) {
		t.Fatal("legacy CSV all scope changed")
	}
	f.json(t, "GET", path+"&all=1&cursor_mode=1", f.bob, nil, 403)
	// A real slow TCP body keeps the registered artifact charged through IO and
	// releases it on disconnect. The production pacer and response writer run.
	server := httptest.NewServer(f.router)
	defer server.Close()
	req, _ := http.NewRequest("GET", server.URL+path+"&rate_kbps=16", nil)
	req.Header.Set("Authorization", "Bearer "+f.alice)
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 {
		response.Body.Close()
		t.Fatal(response.StatusCode)
	}
	if _, err := io.ReadFull(response.Body, make([]byte, 16<<10)); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	var artifact model.TransferArtifact
	if err := f.db.Where("kind='archive' AND user_id=? AND state='ready'", f.aliceID).First(&artifact).Error; err != nil || artifact.Bytes <= 0 || artifact.Bytes != artifact.Size {
		response.Body.Close()
		t.Fatal("CSV artifact not held through HTTP", err)
	}
	response.Body.Close()
	deadline := time.Now().Add(3 * time.Second)
	for transferArtifact(t, f, artifact.Token).State != "done" {
		if time.Now().After(deadline) {
			t.Fatal("CSV disconnect retained artifact")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if transferAudit(t, f).Usage.Charged != 0 {
		t.Fatal("CSV export leaked temporary budget")
	}
	// Exhausted temporary storage fails before any CSV representation is sent.
	if err := transferManager(f).SetLimit(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	refused := f.json(t, "GET", path, f.alice, nil, 507)
	if bytes.HasPrefix(refused.Body.Bytes(), []byte("id,user_id")) || transferAudit(t, f).Usage.Charged != 0 {
		t.Fatal("CSV bypassed temporary budget")
	}
	var retained int64
	if err := f.db.Model(&model.OperationLog{}).Where("action=?", action).Count(&retained).Error; err != nil || retained != 5003 {
		t.Fatal("audit export modified history", retained, err)
	}
}
