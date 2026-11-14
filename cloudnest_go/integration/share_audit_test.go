//go:build integration

package integration

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"cloudenest_go/internal/app"
	"cloudenest_go/model"
	"github.com/gin-gonic/gin"
)

func TestShareAuditIsAtomicAndRedactsBearerCodes(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "share-audit.txt", []byte("shared bytes"))
	var access bytes.Buffer
	old := gin.DefaultWriter
	gin.DefaultWriter = &access
	f.router = fixtureRouter(app.NewContainer(f.cfg, f.db, f.rdb).NewRouter())
	gin.DefaultWriter = old
	if err := f.db.Exec(`CREATE TRIGGER share_test_audit BEFORE INSERT ON operation_log FOR EACH ROW
		BEGIN IF NEW.action IN ('create_share','revoke_share') THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test audit unavailable'; END IF; END`).Error; err != nil {
		t.Fatal(err)
	}
	dropTrigger := func() {
		if err := f.db.Exec("DROP TRIGGER IF EXISTS share_test_audit").Error; err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(dropTrigger)
	create := fmt.Sprintf("/api/files/%d/share", file.ID)
	f.json(t, http.MethodPost, create, f.alice, map[string]any{}, http.StatusInternalServerError)
	var count int64
	if err := f.db.Model(&model.Share{}).Where("file_id=?", file.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("failed audit left a live share", count, err)
	}
	dropTrigger()
	code := fixtureShare(t, f, f.alice, file.ID)
	var share model.Share
	if err := f.db.First(&share, "share_code=?", code).Error; err != nil {
		t.Fatal(err)
	}
	public := "/api/public/shares/" + code
	revoke := "/api/shares/" + code
	if err := f.db.Exec(`CREATE TRIGGER share_test_audit BEFORE INSERT ON operation_log FOR EACH ROW
		BEGIN IF NEW.action='revoke_share' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test audit unavailable'; END IF; END`).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, http.MethodDelete, revoke, f.bob, nil, http.StatusNotFound)
	f.json(t, http.MethodDelete, revoke, f.alice, nil, http.StatusInternalServerError)
	f.json(t, http.MethodGet, public+"/download", "", nil, http.StatusOK)
	dropTrigger()
	f.json(t, http.MethodDelete, revoke, f.alice, nil, http.StatusOK)
	f.json(t, http.MethodGet, public, "", nil, http.StatusNotFound)
	f.json(t, http.MethodGet, public+"/unknown?private=test-query", "", nil, http.StatusNotFound)

	var logs []model.OperationLog
	if err := f.db.Where("file_id=? AND action IN ?", file.ID, []string{"create_share", "revoke_share"}).Order("id").Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 2 || logs[0].Action != "create_share" || logs[1].Action != "revoke_share" {
		t.Fatal("expected audit only for committed changes", len(logs))
	}
	for _, row := range logs {
		if row.Detail != fmt.Sprintf("share_id=%d", share.ID) || row.UserID == nil || *row.UserID != f.aliceID {
			t.Fatal("audit must identify actor and share without the bearer code")
		}
	}
	if out := access.String(); strings.Contains(out, code) || strings.Contains(out, "test-query") || !strings.Contains(out, "/api/public/shares/:code") || !strings.Contains(out, "/api/shares/:code") {
		t.Fatal("access log must keep route visibility without disclosing credentials")
	}
}
