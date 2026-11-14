//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cloudenest_go/internal/store"
	"cloudenest_go/model"
	"golang.org/x/crypto/bcrypt"
)

func TestSharePasscodeAndConcurrentDownloadLimit(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "protected.txt", []byte("private share fixture"))
	create := fmt.Sprintf("/api/files/%d/share", file.ID)
	const passcode = "fixture-passcode"
	createProtected := func(passcode string, limit int, want int) *httptest.ResponseRecorder {
		body, err := json.Marshal(map[string]any{"passcode": passcode, "max_downloads": limit})
		if err != nil {
			t.Fatal(err)
		}
		return f.request(t, http.MethodPost, "https://cloudnest.test"+create, f.alice, "application/json", bytes.NewReader(body), want)
	}
	createProtected("short", 0, 400)
	f.json(t, http.MethodPost, create, f.alice, map[string]any{"max_downloads": -1}, 400)
	var result struct {
		Code string `json:"share_code"`
	}
	f.json(t, http.MethodPost, create, f.alice, map[string]any{"passcode": passcode}, 426)
	decode(t, createProtected(passcode, 1, 200), &result)
	var row model.Share
	if err := f.db.First(&row, "share_code=?", result.Code).Error; err != nil {
		t.Fatal(err)
	}
	if bcrypt.CompareHashAndPassword([]byte(row.PasscodeHash), []byte(passcode)) != nil || row.PasscodeHash == passcode {
		t.Fatal("expected a salted bcrypt hash")
	}
	list := f.json(t, http.MethodGet, "/api/shares", f.alice, nil, 200)
	if strings.Contains(list.Body.String(), passcode) || strings.Contains(list.Body.String(), row.PasscodeHash) ||
		!strings.Contains(list.Body.String(), `"requires_passcode":true`) {
		t.Fatal("owner list did not redact credentials or report protection")
	}
	link := "/api/public/shares/" + result.Code
	send := func(path, code string, headers ...[2]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "https://cloudnest.test"+path, nil)
		req.RemoteAddr = f.remoteAddr
		if code != "" {
			req.Header.Set("X-Share-Passcode", code)
		}
		for _, header := range headers {
			req.Header.Set(header[0], header[1])
		}
		w := httptest.NewRecorder()
		f.router.ServeHTTP(w, req)
		return w
	}
	insecure := httptest.NewRequest(http.MethodGet, link, nil)
	insecure.RemoteAddr = f.remoteAddr
	insecure.Header.Set("X-Share-Passcode", passcode)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, insecure)
	if w.Code != 426 {
		t.Fatal("plaintext request with a passcode was accepted", w.Code)
	}
	for _, path := range []string{link, link + "/download"} {
		for _, code := range []string{"", "wrong-passcode"} {
			if w := send(path, code); w.Code != 403 || strings.Contains(w.Body.String(), file.Filename) {
				t.Fatal("protected share accepted invalid credentials", w.Code)
			}
		}
	}
	if w := send(link, passcode); w.Code != 200 || w.Header().Get("Cache-Control") == "" {
		t.Fatal("correct code did not reveal private metadata", w.Code)
	}

	if w := send(link+"/download", passcode, [2]string{"If-None-Match", "*"}); w.Code != 304 {
		t.Fatal("conditional response did not return 304", w.Code)
	}
	if w := send(link+"/download", passcode, [2]string{"Range", "bytes=1000-2000"}); w.Code != 416 {
		t.Fatal("unsatisfiable range did not return 416", w.Code)
	}
	start := make(chan struct{})
	replies := make(chan *httptest.ResponseRecorder, 8)
	for range cap(replies) {
		go func() {
			<-start
			replies <- send(link+"/download", passcode)
		}()
	}
	close(start)
	success := 0
	for range cap(replies) {
		w := <-replies
		switch w.Code {
		case 200:
			success++
			if w.Body.String() != "private share fixture" {
				t.Fatal("unexpected download")
			}
		case 410:
			if !strings.Contains(w.Body.String(), "share_download_limit") {
				t.Fatal("missing download limit diagnostic")
			}
		default:
			t.Fatal("unexpected concurrent response", w.Code)
		}
	}
	if err := f.db.First(&row, row.ID).Error; err != nil || success != 1 || row.DownloadCount != 1 {
		t.Fatal("concurrent downloads overspent or lost the slot", success, row.DownloadCount, err)
	}
	if err := f.db.Model(&row).Update("expire_time", time.Now().Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	if w := send(link, passcode); w.Code != 410 || !strings.Contains(w.Body.String(), "share expired") {
		t.Fatal("expired share accepted a valid code", w.Code)
	}
}

func TestShareProtectionMigrationKeepsLegacyLink(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "legacy-share.txt", []byte("legacy bytes"))
	code := fixtureShare(t, f, f.alice, file.ID)
	if err := f.db.Exec("ALTER TABLE share DROP COLUMN passcode_hash, DROP COLUMN max_downloads, DROP COLUMN download_count").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Migrate(f.db); err != nil {
			t.Error(err)
		}
	})
	if err := store.CheckFileSchema(f.db); err == nil || f.db.Migrator().HasColumn(&model.Share{}, "passcode_hash") {
		t.Fatal("startup must reject old schema without modifying it")
	}
	if err := store.Migrate(f.db); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckFileSchema(f.db); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		w := f.json(t, http.MethodGet, "/api/public/shares/"+code+"/download", "", nil, 200)
		if w.Body.String() != "legacy bytes" {
			t.Fatal("migration changed the legacy link")
		}
	}
}

func TestShareFolderLimitPreservesArchiveAndRevoke(t *testing.T) {
	f := newFixture(t)
	folder, _ := archiveFolder(t, f, []byte("folder share fixture"))
	var result struct {
		Code string `json:"share_code"`
	}
	create := fmt.Sprintf("/api/files/%d/share", folder.ID)
	decode(t, f.json(t, http.MethodPost, create, f.alice, map[string]any{"max_downloads": 1}, 200), &result)
	link := "/api/public/shares/" + result.Code
	w := f.json(t, http.MethodGet, link+"/download", "", nil, 200)
	entries := readArchive(t, w.Body.Bytes())
	if len(entries) != 2 {
		t.Fatal("limited link did not serve the folder archive", entries)
	}
	if entries["archive-source/source.bin"] != "folder share fixture" {
		t.Fatal("unexpected archive content", entries)
	}
	f.json(t, http.MethodGet, link+"/download", "", nil, 410)
	f.json(t, http.MethodDelete, "/api/shares/"+result.Code, f.alice, nil, 200)
	f.json(t, http.MethodGet, link, "", nil, 404)
}

func TestShareRateLimitCountsRequestsAcrossLinks(t *testing.T) {
	f := newFixture(t)
	for range 20 {
		f.json(t, http.MethodGet, "/api/public/shares/unavailable/download", "", nil, 404)
	}
	w := f.json(t, http.MethodGet, "/api/public/shares/other-link/download", "", nil, 429)
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("rate limit response omitted retry time")
	}
}
