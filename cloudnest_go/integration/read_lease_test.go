//go:build integration

package integration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"cloudenest_go/internal/blob"
	"cloudenest_go/internal/indexqueue"
	"cloudenest_go/internal/middleware"
	"cloudenest_go/internal/service"
	storageMysql "cloudenest_go/internal/storage/mysql"
	"cloudenest_go/internal/store"
	"cloudenest_go/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func shortLeaseManager(t *testing.T, f *fixture) *blob.Manager {
	t.Helper()
	m, err := blob.NewManagerWithTiming(f.db, 2*time.Second, 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func leaseCount(t *testing.T, f *fixture, id int64) int64 {
	t.Helper()
	var count int64
	if err := f.db.Model(&model.StorageReadLease{}).Where("storage_id=?", id).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	return count
}
func waitNoReadLeases(t *testing.T, f *fixture, id int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for leaseCount(t, f, id) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("read leases not released")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestReadLeasePinsPathsDeduplicatesAndRetainsParent(t *testing.T) {
	f := newFixture(t)
	s := storageMetadataForTest(t, f, "lease-parent")
	if _, err := blob.Pin(context.Background(), s); !errors.Is(err, blob.ErrReadUnavailable) {
		t.Fatal("unscoped read accepted:", err)
	}
	manager := blob.NewManager(f.db)
	ctx, a := manager.Begin(context.Background())
	defer a.Close()
	first, err := blob.Pin(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blob.Pin(ctx, s); err != nil {
		t.Fatal(err)
	}
	if leaseCount(t, f, s.ID) != 1 {
		t.Fatal("repeated object opened multiple leases")
	}
	if err := f.db.Model(&model.Storage{}).Where("id=?", s.ID).Update("file_path", "/nonexistent/repaired-path").Error; err != nil {
		t.Fatal(err)
	}
	same, err := blob.Pin(ctx, s)
	if err != nil || same.FilePath != first.FilePath {
		t.Fatal("scope changed its already pinned immutable path")
	}
	otherCtx, b := manager.Begin(context.Background())
	defer b.Close()
	changed, err := blob.Pin(otherCtx, s)
	if err != nil || changed.FilePath == first.FilePath {
		t.Fatal("new scope did not refresh path")
	}
	if leaseCount(t, f, s.ID) != 2 {
		t.Fatal("independent scopes shared token")
	}
	var tokens []string
	if err := f.db.Model(&model.StorageReadLease{}).Where("storage_id=?", s.ID).Pluck("token", &tokens).Error; err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 2 || len(tokens[0]) != 64 || tokens[0] == tokens[1] {
		t.Fatal("invalid independent cryptographic tokens")
	}
	expectMySQLError(t, f.db.Delete(&s).Error, 1451)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if leaseCount(t, f, s.ID) != 1 {
		t.Fatal("scope cleanup removed another reader")
	}
	if err := a.Check(); err == nil {
		t.Fatal("closed scope remained readable")
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if leaseCount(t, f, s.ID) != 0 {
		t.Fatal("lease cleanup failed")
	}
	if err := f.db.Delete(&s).Error; err != nil {
		t.Fatal(err)
	}
}

func TestReadLeaseRenewsUsingDatabaseExpiryAndReleasesCanceledScope(t *testing.T) {
	f := newFixture(t)
	s := storageMetadataForTest(t, f, "lease-renew")
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx, scope := shortLeaseManager(t, f).Begin(parent)
	defer scope.Close()
	if _, err := blob.Pin(ctx, s); err != nil {
		t.Fatal(err)
	}
	var initial time.Time
	if err := f.db.Model(&model.StorageReadLease{}).Select("expires_at").Where("storage_id=?", s.ID).Scan(&initial).Error; err != nil {
		t.Fatal(err)
	}
	// Wait beyond the original DB TTL; a merely one-time lease would be expired.
	timer := time.NewTimer(2300 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		t.Fatal("reader canceled before renewal:", ctx.Err())
	case <-timer.C:
	}
	var current time.Time
	if err := f.db.Model(&model.StorageReadLease{}).Select("expires_at").Where("storage_id=?", s.ID).Scan(&current).Error; err != nil {
		t.Fatal(err)
	}
	if !current.After(initial) || scope.Check() != nil {
		t.Fatal("long-running read did not renew")
	}
	cancel()
	if err := scope.Close(); err != nil {
		t.Fatal(err)
	}
	if leaseCount(t, f, s.ID) != 0 {
		t.Fatal("canceled scope kept its lease")
	}
}

func TestReadLeaseCannotResurrectExpiredOrInactiveObject(t *testing.T) {
	f := newFixture(t)
	s := storageMetadataForTest(t, f, "expired")
	ctx, scope := shortLeaseManager(t, f).Begin(context.Background())
	defer scope.Close()
	if _, err := blob.Pin(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.StorageReadLease{}).Where("storage_id=?", s.ID).Update("expires_at", time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)).Error; err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("expired lease was renewed")
	}
	if scope.Check() == nil {
		t.Fatal("expired lease still protected output")
	}
	var expired int64
	if err := f.db.Raw("SELECT COUNT(*) FROM storage_read_lease WHERE storage_id=? AND expires_at<=UTC_TIMESTAMP(6)", s.ID).Scan(&expired).Error; err != nil || expired != 1 {
		t.Fatal("expired lease resurrected:", err)
	}
	if err := scope.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&model.Storage{}).Where("id=?", s.ID).Update("state", "quarantining").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Model(&model.Storage{}).Where("id=?", s.ID).Update("state", blob.Active) })
	next, reader := blob.NewManager(f.db).Begin(context.Background())
	defer reader.Close()
	if _, err := blob.Pin(next, s); !errors.Is(err, blob.ErrReadUnavailable) {
		t.Fatal("inactive object accepted:", err)
	}
	if leaseCount(t, f, s.ID) != 0 {
		t.Fatal("inactive object gained a lease")
	}
}

func TestReadLeaseRenewFailureStopsBodyWithoutAppendingErrorJSON(t *testing.T) {
	f := newFixture(t)
	s := storageMetadataForTest(t, f, "failed-renew")
	r := gin.New()
	r.Use(middleware.ProtectBlobReads(shortLeaseManager(t, f)))
	var secondErr error
	r.GET("/stream", func(c *gin.Context) {
		if _, err := blob.Pin(c.Request.Context(), s); err != nil {
			t.Error(err)
			return
		}
		if _, err := c.Writer.Write([]byte("first")); err != nil {
			t.Error(err)
			return
		}
		// Losing a token simulates maintenance/database failure; no fake file or AI
		// success is returned. The next heartbeat must cancel the reader.
		if err := f.db.Where("storage_id=?", s.ID).Delete(&model.StorageReadLease{}).Error; err != nil {
			t.Error(err)
			return
		}
		select {
		case <-c.Request.Context().Done():
		case <-time.After(3 * time.Second):
			t.Error("lease loss did not cancel stream")
			return
		}
		_, secondErr = c.Writer.Write([]byte("must-not-be-sent"))
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/stream", nil))
	if secondErr == nil || w.Code != http.StatusOK || w.Body.String() != "first" {
		t.Fatalf("stream continued after loss: %v %d %q", secondErr, w.Code, w.Body.String())
	}
}

func TestReadLeaseAcquisitionFailureReturns503BeforePhysicalBytes(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "blocked-lease.txt", []byte("private-physical-body"))
	waitNoReadLeases(t, f, *file.StorageID)
	if err := f.db.Model(&model.Storage{}).Where("id=?", file.StorageID).Update("state", "quarantined").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Model(&model.Storage{}).Where("id=?", file.StorageID).Update("state", blob.Active) })
	w := f.json(t, "GET", fmt.Sprintf("/api/files/%d/download", file.ID), f.alice, nil, http.StatusServiceUnavailable)
	if strings.Contains(w.Body.String(), "private-physical-body") || w.Header().Get("Content-Length") != "" {
		t.Fatal("failed lease emitted file response")
	}
}

type leaseInspectResponse struct {
	*httptest.ResponseRecorder
	inspect   func()
	inspected bool
}

func (w *leaseInspectResponse) Write(p []byte) (int, error) {
	if !w.inspected {
		w.inspected = true
		w.inspect()
	}
	return w.ResponseRecorder.Write(p)
}

func TestReadLeasesCoverHTTPCurrentVersionArchiveRangeAndPublic(t *testing.T) {
	f := newFixture(t)
	folder := fixtureFolder(t, f, f.alice, "lease-archive", 0)
	file := f.upload(t, f.alice, "lease-current.txt", []byte("read-protected-by-lease"))
	f.json(t, "POST", "/api/files/move", f.alice, map[string]any{"file_ids": []int64{file.ID}, "target_parent_id": folder.ID}, http.StatusOK)
	waitNoReadLeases(t, f, *file.StorageID)
	var share struct {
		Code string `json:"share_code"`
	}
	decode(t, f.json(t, "POST", fmt.Sprintf("/api/files/%d/share", folder.ID), f.alice, map[string]any{"expire_hours": 1}, http.StatusOK), &share)
	// Metadata snapshots must release their connection before pinning objects.
	sqlDB, err := f.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { sqlDB.SetMaxOpenConns(0) })
	routes := []struct {
		method, path, body, token, rangeHeader string
		status                                 int
	}{
		{"GET", fmt.Sprintf("/api/files/%d/download", file.ID), "", f.alice, "", 200},
		{"GET", fmt.Sprintf("/api/files/%d/download", file.ID), "", f.alice, "bytes=0-3", 206},
		{"GET", fmt.Sprintf("/api/files/%d/versions/1/download", file.ID), "", f.alice, "", 200},
		{"GET", fmt.Sprintf("/api/files/%d/download-zip", folder.ID), "", f.alice, "", 200},
		{"POST", "/api/files/batch-download", fmt.Sprintf(`{"file_ids":[%d]}`, folder.ID), f.alice, "", 200},
		{"GET", "/api/public/shares/" + share.Code + "/download", "", "", "", 200},
	}
	for _, route := range routes {
		t.Run(route.path+route.rangeHeader, func(t *testing.T) {
			req := httptest.NewRequest(route.method, route.path, bytes.NewBufferString(route.body))
			req.RemoteAddr = f.remoteAddr
			if route.token != "" {
				req.Header.Set("Authorization", "Bearer "+route.token)
			}
			if route.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			if route.rangeHeader != "" {
				req.Header.Set("Range", route.rangeHeader)
			}
			w := &leaseInspectResponse{ResponseRecorder: httptest.NewRecorder(), inspect: func() {
				if leaseCount(t, f, *file.StorageID) != 1 {
					t.Error("physical response lacks scope lease")
				}
			}}
			f.router.ServeHTTP(w, req)
			if w.Code != route.status || !w.inspected {
				t.Fatalf("route: %d %s", w.Code, w.Body.String())
			}
			if leaseCount(t, f, *file.StorageID) != 0 {
				t.Fatal("HTTP completion kept read lease")
			}
		})
	}
	// ACL failures must not mint leases for unreadable private objects.
	f.json(t, "GET", fmt.Sprintf("/api/files/%d/download", file.ID), f.bob, nil, http.StatusForbidden)
	if leaseCount(t, f, *file.StorageID) != 0 {
		t.Fatal("unauthorized request minted a lease")
	}
}

func TestReadLeaseForegroundAIRequiresItsOwnScope(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "ai-read-lease.txt", []byte("actual local file"))
	waitNoReadLeases(t, f, *file.StorageID)
	svc := service.NewAIService(storageMysql.NewAIRepo(f.db), f.cfg, nil)
	if _, _, err := svc.GetReadableFileAndStorage(context.Background(), f.aliceID, file.ID); !errors.Is(err, blob.ErrReadUnavailable) {
		t.Fatal("unscoped AI read allowed:", err)
	}
	ctx, scope := blob.NewManager(f.db).Begin(context.Background())
	defer scope.Close()
	if _, _, err := svc.GetReadableFileAndStorage(ctx, f.aliceID, file.ID); err != nil {
		t.Fatal(err)
	}
	if leaseCount(t, f, *file.StorageID) != 1 {
		t.Fatal("foreground AI lacks lease")
	}
	if err := scope.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReadLeaseSchemaExplicitUpgradeAndDriftRefusal(t *testing.T) {
	f := newFixture(t)
	old := storageMetadataForTest(t, f, "lease-schema")
	// Only the guarded disposable schema is downgraded. No existing file path or
	// history is rewritten, and the normal application never migrates on startup.
	if err := f.db.Exec("DROP TABLE storage_read_lease").Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec("ALTER TABLE storage DROP INDEX idx_storage_gc, DROP COLUMN state").Error; err != nil {
		t.Fatal(err)
	}
	if store.CheckFileSchema(f.db) == nil {
		t.Fatal("startup accepted absent read lease schema")
	}
	t.Cleanup(func() {
		if err := store.Migrate(f.db); err != nil {
			t.Error(err)
		}
	})
	if err := store.Migrate(f.db); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckFileSchema(f.db); err != nil {
		t.Fatal(err)
	}
	var current model.Storage
	if err := f.db.First(&current, old.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.FilePath != old.FilePath || current.FileHash != old.FileHash || current.State != blob.Active {
		t.Fatal("explicit upgrade rewrote legacy object")
	}
	if err := f.db.Exec("ALTER TABLE storage_read_lease DROP INDEX idx_storage_read_lease_expires_at").Error; err != nil {
		t.Fatal(err)
	}
	if store.CheckFileSchema(f.db) == nil {
		t.Fatal("startup accepted missing expiry index")
	}
}

func TestReadLeaseCleanupFailureLeavesOnlyOwnExpiringTokens(t *testing.T) {
	f := newFixture(t)
	s := storageMetadataForTest(t, f, "cleanup-failure")
	ctx, a := blob.NewManager(f.db).Begin(context.Background())
	if _, err := blob.Pin(ctx, s); err != nil {
		t.Fatal(err)
	}
	const hook = "test:lease_cleanup_failure"
	if err := f.db.Callback().Delete().Before("gorm:delete").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "storage_read_lease" {
			tx.AddError(errors.New("injected cleanup failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		f.db.Callback().Delete().Remove(hook)
		f.db.Where("storage_id=?", s.ID).Delete(&model.StorageReadLease{})
	})
	if err := a.Close(); err == nil {
		t.Fatal("cleanup failure ignored")
	}
	if err := f.db.Callback().Delete().Remove(hook); err != nil {
		t.Fatal(err)
	}
	if leaseCount(t, f, s.ID) != 1 {
		t.Fatal("failed cleanup discarded protection")
	}
	var future int64
	if err := f.db.Raw("SELECT COUNT(*) FROM storage_read_lease WHERE storage_id=? AND expires_at>UTC_TIMESTAMP(6)", s.ID).Scan(&future).Error; err != nil || future != 1 {
		t.Fatal("leftover token has no expiry:", err)
	}
}

func TestReadLeaseMiddlewarePreservesMetadataAndNotFoundResponses(t *testing.T) {
	f := newFixture(t)
	f.json(t, "GET", "/api/auth/me", f.alice, nil, http.StatusOK)
	f.json(t, "GET", "/missing-read-route", "", nil, http.StatusNotFound)
	var count int64
	if err := f.db.Model(&model.StorageReadLease{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("metadata-only request created a lease:", err)
	}
}

func TestReadLeaseIndexWorkerOutlivesHTTPAndReleasesLease(t *testing.T) {
	f, _ := indexFixture(t)
	entered := make(chan context.Context, 1)
	release := make(chan struct{})
	var unblock sync.Once
	defer unblock.Do(func() { close(release) })
	var payload bytes.Buffer
	if err := png.Encode(&payload, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	file := f.upload(t, f.alice, "background-lease.png", payload.Bytes())
	// Upload HTTP has returned. A separately owned worker now reads the actual
	// object and pauses at its processor boundary. No model output is fabricated.
	worker := indexqueue.NewWorker(f.db, blob.NewManager(f.db), func(ctx context.Context, j model.AIIndexJob) (func(*gorm.DB) error, error) {
		var stored model.File
		if err := f.db.WithContext(ctx).First(&stored, j.FileID).Error; err != nil {
			return nil, err
		}
		in, _, err := blob.OpenPinnedStorage(ctx, model.Storage{ID: *stored.StorageID})
		if err != nil {
			return nil, err
		}
		if err := in.Close(); err != nil {
			return nil, err
		}
		entered <- ctx
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil, &indexqueue.Failure{Code: "model_unavailable", Retryable: true}
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := worker.Step(ctx); done <- err }()
	defer func() {
		unblock.Do(func() { close(release) })
		cancel()
		select {
		case <-done:
		case <-time.After(6 * time.Second):
			t.Error("worker did not stop")
		}
		waitNoReadLeases(t, f, *file.StorageID)
	}()
	select {
	case readerCtx := <-entered:
		if err := blob.Check(readerCtx); err != nil {
			t.Fatal("HTTP completion canceled independent index scope", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("index worker did not reach processor boundary")
	}
	if leaseCount(t, f, *file.StorageID) != 1 {
		t.Fatal("index worker physical read lacks independent lease")
	}
	unblock.Do(func() { close(release) })
	select {
	case err := <-done:
		done <- err // leave completion for deferred shutdown check
		if err == nil {
			t.Fatal("processor failure reported success")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("index worker did not finish failure")
	}
	job := readIndex(t, f, file.ID)
	if job.Status != "retry" || job.ErrorCode != "model_unavailable" {
		t.Fatalf("processor failure not durable: %+v", job)
	}
	waitNoReadLeases(t, f, *file.StorageID)
}
