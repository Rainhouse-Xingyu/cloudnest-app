//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cloudenest_go/internal/app"
	"cloudenest_go/internal/config"
	"cloudenest_go/internal/store"
	"cloudenest_go/model"

	"github.com/gin-gonic/gin"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

var fixtureIPSequence atomic.Uint64

type fixture struct {
	cfg            config.Config
	rdb            *redis.Client
	remoteAddr     string
	db             *gorm.DB
	router         http.Handler
	alice, bob     string
	aliceID, bobID int64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dsn := os.Getenv("CLOUDNEST_TEST_MYSQL_DSN")
	parsed, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid disposable test DSN")
	}
	host, _, err := net.SplitHostPort(parsed.Addr)
	if err != nil || parsed.Net != "tcp" || host != "127.0.0.1" || parsed.DBName != "cloudnest_test" || parsed.User != "cloudnest_test" || os.Getenv("CLOUDNEST_TEST_DISPOSABLE") != "1" {
		t.Fatal("refusing database access: run scripts/test-integration.sh with disposable containers")
	}
	redisAddr := os.Getenv("CLOUDNEST_TEST_REDIS_ADDR")
	host, _, err = net.SplitHostPort(redisAddr)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("test Redis must be on loopback")
	}
	cfg := config.Config{MySQLDSN: dsn, RedisAddr: redisAddr, JWTSecret: "test-only-signing-secret-with-at-least-32-bytes", JWTExpireHours: 1, DataDir: t.TempDir(), AIOllamaBaseURL: "http://127.0.0.1:1", AITimeoutSec: 1}
	// Observe the schema before opening the application connection. In the first
	// test this database is empty; NewMySQL must not create any tables.
	probe, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	var before int64
	err = probe.QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = 'cloudnest_test'").Scan(&before)
	_ = probe.Close()
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.NewMySQL(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	var after int64
	if err := sqlDB.QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = 'cloudnest_test'").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("opening the application connection modified the schema")
	}
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	rdb, err := store.NewRedis(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	gin.SetMode(gin.TestMode)
	f := &fixture{db: db, cfg: cfg, rdb: rdb, router: app.NewContainer(cfg, db, rdb).NewRouter(), remoteAddr: fmt.Sprintf("[2001:db8:1::%x]:12345", fixtureIPSequence.Add(1))}
	password := "disposable-fixture-password"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	for i, dest := range []*string{&f.alice, &f.bob} {
		user := model.User{Username: fmt.Sprintf("test-%d-%d", time.Now().UnixNano(), i), Password: string(hash), Status: 1}
		if err := db.Create(&user).Error; err != nil {
			t.Fatal(err)
		}
		resp := f.json(t, "POST", "/api/auth/login", "", map[string]any{"username": user.Username, "password": password}, http.StatusOK)
		var login struct {
			Token string `json:"token"`
		}
		decode(t, resp, &login)
		if login.Token == "" {
			t.Fatal("missing session token")
		}
		*dest = login.Token
		if i == 0 {
			f.aliceID = user.ID
		} else {
			f.bobID = user.ID
		}
	}
	return f
}

func (f *fixture) request(t *testing.T, method, path, token, contentType string, body io.Reader, want int) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	req.RemoteAddr = f.remoteAddr
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	if w.Code != want {
		t.Fatalf("%s %s: status %d, want %d", method, path, w.Code, want)
	}
	return w
}

func (f *fixture) json(t *testing.T, method, path, token string, body any, want int) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return f.request(t, method, path, token, "application/json", bytes.NewReader(raw), want)
}

func decode(t *testing.T, w *httptest.ResponseRecorder, dest any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), dest); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) upload(t *testing.T, token, filename string, payload []byte) model.File {
	t.Helper()
	resp := f.json(t, "POST", "/api/files/upload/init", token, map[string]any{"filename": filename, "total_size": len(payload), "chunk_size": len(payload), "total_chunks": 1}, http.StatusOK)
	var init struct {
		UploadID string `json:"upload_id"`
	}
	decode(t, resp, &init)
	if init.UploadID == "" {
		t.Fatal("missing upload ID")
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for key, value := range map[string]string{"upload_id": init.UploadID, "chunk_index": "0", "chunk_md5": fmt.Sprintf("%x", md5.Sum(payload))} {
		if err := w.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	part, err := w.CreateFormFile("chunk", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f.request(t, "POST", "/api/files/upload/chunk", token, w.FormDataContentType(), &body, http.StatusOK)
	resp = f.json(t, "POST", "/api/files/upload/complete", token, map[string]any{"upload_id": init.UploadID}, http.StatusOK)
	var completed struct {
		Item model.File `json:"item"`
	}
	decode(t, resp, &completed)
	if completed.Item.ID <= 0 || completed.Item.Size != int64(len(payload)) {
		t.Fatal("invalid completed file")
	}
	// Existing implementation launches an untracked goroutine. Wait for its terminal
	// audit entry before fixture cleanup; this is not an AI quality assertion.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var count int64
		err := f.db.Model(&model.OperationLog{}).Where("file_id = ? AND action = ?", completed.Item.ID, "ai_auto_analyze").Count(&count).Error
		if err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background analysis did not terminate")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return completed.Item
}

func TestAPIFileLifecycle(t *testing.T) {
	f := newFixture(t)
	f.json(t, "GET", "/api/files", "", nil, http.StatusUnauthorized)
	f.json(t, "POST", "/api/auth/login", "", map[string]string{"username": "missing", "password": "wrong"}, http.StatusUnauthorized)
	f.json(t, "POST", "/api/admin/users", f.alice, map[string]string{"username": "forbidden", "password": "forbidden"}, http.StatusForbidden)
	f.json(t, "POST", "/api/files/folders", f.alice, map[string]any{"name": "integration-folder"}, http.StatusOK)
	payload := []byte("CloudNest real upload/download fixture\x00\x01\xff")
	file := f.upload(t, f.alice, "integration.bin", payload)
	path := fmt.Sprintf("/api/files/%d", file.ID)
	download := f.json(t, "GET", path+"/download", f.alice, nil, http.StatusOK)
	if !bytes.Equal(download.Body.Bytes(), payload) {
		t.Fatal("download bytes differ from uploaded bytes")
	}
	f.json(t, "GET", path+"/download", f.bob, nil, http.StatusForbidden)
	f.json(t, "PUT", path+"/rename", f.bob, map[string]string{"name": "stolen.bin"}, http.StatusForbidden)
	listing := f.json(t, "GET", "/api/files", f.bob, nil, http.StatusOK)
	if strings.Contains(listing.Body.String(), "integration.bin") {
		t.Fatal("other user's file leaked in listing")
	}
	shareResp := f.json(t, "POST", path+"/share", f.alice, map[string]int{"expire_hours": 1}, http.StatusOK)
	var share struct {
		Code string `json:"share_code"`
	}
	decode(t, shareResp, &share)
	if share.Code == "" {
		t.Fatal("empty share code")
	}
	public := "/api/public/shares/" + share.Code
	publicDownload := f.json(t, "GET", public+"/download", "", nil, http.StatusOK)
	if !bytes.Equal(publicDownload.Body.Bytes(), payload) {
		t.Fatal("public download mismatch")
	}
	f.json(t, "DELETE", "/api/shares/"+share.Code, f.alice, nil, http.StatusOK)
	f.json(t, "GET", public, "", nil, http.StatusNotFound)
	f.json(t, "DELETE", path, f.alice, nil, http.StatusOK)
	f.json(t, "POST", path+"/restore", f.alice, nil, http.StatusOK)
	if got := f.json(t, "GET", path+"/download", f.alice, nil, http.StatusOK); !bytes.Equal(got.Body.Bytes(), payload) {
		t.Fatal("restored bytes differ")
	}
	f.json(t, "POST", "/api/auth/logout", f.alice, nil, http.StatusOK)
	f.json(t, "GET", "/api/auth/me", f.alice, nil, http.StatusUnauthorized)
}
