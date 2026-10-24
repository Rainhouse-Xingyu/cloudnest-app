//go:build integration

package integration

import (
	"gorm.io/gorm"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"net/http"

	"bytes"
	"cloudenest_go/internal/app"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/internal/store"
	"cloudenest_go/internal/transfer"
	"cloudenest_go/model"
	"crypto/sha256"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	"context"
	"fmt"
	"testing"
)

func TestThumbnailCacheHonorsSharedTemporaryVolume(t *testing.T) {
	f := newFixture(t)
	resetRateSettings(t, f)
	file := f.upload(t, f.alice, "budgeted-thumb.png", pngIdentity(t, false))
	if err := transferManager(f).SetLimit(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	w := identityRequest(f, f.alice, fmt.Sprintf("/api/files/%d/thumbnail?size=64", file.ID), nil)
	if w.Code != 507 {
		t.Fatal("thumbnail bypassed shared temporary capacity", w.Code)
	}
}

func thumbnailKey(t *testing.T, f *fixture, file model.File, size int) string {
	t.Helper()
	var storage model.Storage
	if err := f.db.First(&storage, *file.StorageID).Error; err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("thumbnail-v3:%d:%s:%d", file.ID, storage.FileHash, size))))
}
func cachedThumbnail(t *testing.T, f *fixture, file model.File, size int) model.TransferArtifact {
	t.Helper()
	var row model.TransferArtifact
	if err := f.db.Where("cache_key=? AND kind='thumbnail' AND state='cached'", thumbnailKey(t, f, file, size)).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row
}
func thumbnailGet(f *fixture, file model.File, size int) *httptest.ResponseRecorder {
	return identityRequest(f, f.alice, fmt.Sprintf("/api/files/%d/thumbnail?size=%d", file.ID, size), nil)
}
func TestThumbnailCacheRegisteredReuseExpiryAndReadPin(t *testing.T) {
	f := newFixture(t)
	resetRateSettings(t, f)
	file := f.upload(t, f.alice, "persistent-cache.png", pngIdentity(t, false))
	first := thumbnailGet(f, file, 64)
	if first.Code != 200 {
		t.Fatal(first.Code, first.Body.String())
	}
	row := cachedThumbnail(t, f, file, 64)
	path := filepath.Join(f.cfg.DataDir, "storage", row.RelativePath)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 || row.Size != int64(first.Body.Len()) || row.Bytes != row.Size {
		t.Fatal("invalid durable cache", row, err)
	}
	if row.TaskID != 0 || row.TransferEpoch != 0 || row.CacheKey == nil {
		t.Fatal("cache fabricated upload task", row)
	}
	if r := transferAudit(t, f); r.Usage.Charged != row.Bytes {
		t.Fatal("cache not charged", r)
	}
	secondRouter := fixtureRouter(app.NewContainer(f.cfg, f.db, f.rdb).NewRouter())
	original := f.router
	f.router = secondRouter
	second := thumbnailGet(f, file, 64)
	f.router = original
	if second.Code != 200 || !bytes.Equal(second.Body.Bytes(), first.Body.Bytes()) || cachedThumbnail(t, f, file, 64).Token != row.Token {
		t.Fatal("cache not reused across managers", second.Code)
	}
	reader, err := transferManager(f).OpenThumbnail(context.Background(), *row.CacheKey)
	if err != nil || reader == nil {
		t.Fatal(err)
	}
	defer reader.Close()
	transferExpire(t, f, row.Token)
	report, err := transferManager(f).Reconcile(context.Background())
	if !errors.Is(err, transfer.ErrPending) || report.Busy != 1 || report.Usage.Charged != row.Bytes {
		t.Fatal("expired active reader not protected", report, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	report, err = transferManager(f).Reconcile(context.Background())
	if err != nil || report.Removed != 1 || report.Usage.Charged != 0 {
		t.Fatal("closed cache did not expire", report, err)
	}
	retired := transferArtifact(t, f, row.Token)
	if retired.State != "done" || retired.CacheKey != nil {
		t.Fatal("old key not retired", retired)
	}
	regenerated := thumbnailGet(f, file, 64)
	if regenerated.Code != 200 || !bytes.Equal(regenerated.Body.Bytes(), first.Body.Bytes()) || cachedThumbnail(t, f, file, 64).Token == row.Token {
		t.Fatal("cache generation did not restart", regenerated.Code)
	}
}

func TestThumbnailCachePressureEvictsOnlyIdleRegisteredObjects(t *testing.T) {
	f := newFixture(t)
	resetRateSettings(t, f)
	file := f.upload(t, f.alice, "pressure-cache.png", pngIdentity(t, false))
	first := thumbnailGet(f, file, 64)
	if first.Code != 200 {
		t.Fatal(first.Code)
	}
	row := cachedThumbnail(t, f, file, 64)
	// The cache gets one quarter of the shared volume. This fits one JPEG but
	// cannot fit both variants; actual usage, not output size estimates, is used.
	if err := transferManager(f).SetLimit(context.Background(), 4*(row.Bytes+256)); err != nil {
		t.Fatal(err)
	}
	reader, err := transferManager(f).OpenThumbnail(context.Background(), *row.CacheKey)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	old := filepath.Join(f.cfg.DataDir, "thumbnails", "legacy-user-data")
	if err := os.MkdirAll(filepath.Dir(old), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("unregistered stays"), 0600); err != nil {
		t.Fatal(err)
	}
	blocked := thumbnailGet(f, file, 128)
	if blocked.Code != 507 {
		t.Fatal("cache evicted its live reader", blocked.Code)
	}
	if transferArtifact(t, f, row.Token).State != "cached" {
		t.Fatal("live cache retired")
	}
	reader.Close()
	replacement := thumbnailGet(f, file, 128)
	if replacement.Code != 200 {
		t.Fatal(replacement.Code, replacement.Body.String())
	}
	if transferArtifact(t, f, row.Token).State != "done" {
		t.Fatal("idle old cache not evicted")
	}
	cached := cachedThumbnail(t, f, file, 128)
	if report := transferAudit(t, f); report.Usage.Charged != cached.Bytes || report.Usage.Charged > report.LimitBytes/4 {
		t.Fatal("cache capacity escaped", report)
	}
	if data, err := os.ReadFile(old); err != nil || string(data) != "unregistered stays" {
		t.Fatal("legacy cache removed", err)
	}
}

func TestThumbnailCacheKeyFencesBuildersAndReclaimsAbandonedOutput(t *testing.T) {
	f := newFixture(t)
	m := transferManager(f)
	key := fmt.Sprintf("%x", sha256.Sum256([]byte("single cache generation")))
	first, err := m.CreateThumbnail(context.Background(), f.aliceID, key)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close(true)
	if _, err := first.Write([]byte("partial output")); err != nil {
		t.Fatal(err)
	}
	if _, err := transferManager(f).CreateThumbnail(context.Background(), f.aliceID, key); !errors.Is(err, contract.ErrTransferBusy) {
		t.Fatal("duplicate builder accepted", err)
	}
	if _, err := m.OpenThumbnail(context.Background(), key); !errors.Is(err, contract.ErrTransferBusy) {
		t.Fatal("partial cache opened", err)
	}
	if err := first.Close(false); err != nil {
		t.Fatal(err)
	}
	transferExpire(t, f, first.Token())
	r, err := m.Reconcile(context.Background())
	if err != nil || r.Removed != 1 || r.Usage.Charged != 0 {
		t.Fatal(r, err)
	}
	second, err := transferManager(f).CreateThumbnail(context.Background(), f.aliceID, key)
	if err != nil {
		t.Fatal("abandoned key not released", err)
	}
	defer second.Close(true)
	if _, err := second.Write([]byte("complete output")); err != nil {
		t.Fatal(err)
	}
	transferExpire(t, f, second.Token())
	if err := second.SealThumbnail(); !errors.Is(err, contract.ErrTransferUnavailable) {
		t.Fatal("expired cache writer published", err)
	}
}

func TestThumbnailCacheRejectsTamperingAndRetainsReplacedPath(t *testing.T) {
	f := newFixture(t)
	resetRateSettings(t, f)
	file := f.upload(t, f.alice, "tampered-cache.png", pngIdentity(t, false))
	first := thumbnailGet(f, file, 64)
	if first.Code != 200 {
		t.Fatal(first.Code)
	}
	row := cachedThumbnail(t, f, file, 64)
	reader, err := transferManager(f).OpenThumbnail(context.Background(), *row.CacheKey)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	path := filepath.Join(f.cfg.DataDir, "storage", row.RelativePath)
	corrupt := bytes.Repeat([]byte("?"), first.Body.Len())
	if err := os.WriteFile(path, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	if w := thumbnailGet(f, file, 64); w.Code != 409 || w.Header().Get("ETag") != "" {
		t.Fatal("modified cache bytes trusted", w.Code)
	}
	if err := os.WriteFile(path, first.Body.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	moved := path + ".historical"
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	substitute := bytes.Repeat([]byte("!"), first.Body.Len())
	if err := os.WriteFile(path, substitute, 0600); err != nil {
		t.Fatal(err)
	}
	denied := thumbnailGet(f, file, 64)
	if denied.Code != 409 || denied.Header().Get("ETag") != "" {
		t.Fatal("replacement adopted", denied.Code)
	}
	actual, err := io.ReadAll(reader.File)
	if err != nil || !bytes.Equal(actual, first.Body.Bytes()) {
		t.Fatal("opened cache FD changed", err)
	}
	reader.Close()
	transferExpire(t, f, row.Token)
	r, err := transferManager(f).Reconcile(context.Background())
	if !errors.Is(err, transfer.ErrPending) || r.Blocked != 1 || r.Usage.Charged != row.Bytes {
		t.Fatal("uncertain cache path not retained", r, err)
	}
	if actual, err := os.ReadFile(path); err != nil || !bytes.Equal(actual, substitute) {
		t.Fatal("replacement deleted", err)
	}
}

func TestThumbnailCacheMigrationRequiresExplicitColumnAndUniqueIndex(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "cache-migration.png", pngIdentity(t, false))
	if err := f.db.Exec("ALTER TABLE transfer_artifact DROP INDEX idx_transfer_cache, DROP COLUMN cache_key").Error; err != nil {
		t.Fatal(err)
	}
	if err := store.CheckFileSchema(f.db); err == nil {
		t.Fatal("old cache schema accepted")
	}
	if err := store.Migrate(f.db); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckFileSchema(f.db); err != nil {
		t.Fatal(err)
	}
	if w := thumbnailGet(f, file, 64); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	row := cachedThumbnail(t, f, file, 64)
	if err := f.db.Exec("ALTER TABLE transfer_artifact ALTER INDEX idx_transfer_cache INVISIBLE").Error; err != nil {
		t.Fatal(err)
	}
	if err := store.CheckFileSchema(f.db); err == nil {
		t.Fatal("invisible cache unique index accepted")
	}
	if err := f.db.Exec("ALTER TABLE transfer_artifact ALTER INDEX idx_transfer_cache VISIBLE").Error; err != nil {
		t.Fatal(err)
	}
	// NULL remains valid for ordinary retired upload rows.
	var nilKeys int64
	if err := f.db.Model(&model.TransferArtifact{}).Where("task_id<>0 AND user_id=? AND cache_key IS NULL", f.aliceID).Count(&nilKeys).Error; err != nil || nilKeys == 0 {
		t.Fatal("migration adopted old objects", err)
	}
	if _, err := os.Stat(filepath.Join(f.cfg.DataDir, "storage", row.RelativePath)); err != nil {
		t.Fatal(err)
	}
}

func TestThumbnailCacheCountAdmissionIncludesBlockedReservations(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "cache-count.png", pngIdentity(t, false))
	canonical, err := filepath.EvalSymlinks(filepath.Join(f.cfg.DataDir, "storage"))
	if err != nil {
		t.Fatal(err)
	}
	var volume model.TransferVolume
	if err := f.db.Where("root_path=?", canonical).First(&volume).Error; err != nil {
		t.Fatal(err)
	}
	// These are deliberately blocked/uncertain reservations, not fake images.
	// They must retain their metadata/byte cost and cannot be evicted as caches.
	rows := make([]model.TransferArtifact, transfer.MaxThumbnailEntries)
	for i := range rows {
		token := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("blocked-%d-%d", f.aliceID, i))))
		rows[i] = model.TransferArtifact{Token: token, VolumeID: volume.ID, RelativePath: filepath.Join(".thumbnails-v3", "thumbnail-"+token+".jpg"), UserID: f.aliceID, Kind: "thumbnail", State: "blocked", Bytes: 1, ExpiresAt: time.Now().UTC()}
	}
	if err := f.db.CreateInBatches(&rows, 256).Error; err != nil {
		t.Fatal(err)
	}
	if w := thumbnailGet(f, file, 64); w.Code != 507 {
		t.Fatal("blocked entries ignored by count admission", w.Code)
	}
	var count int64
	if err := f.db.Model(&model.TransferArtifact{}).Where("volume_id=? AND state='blocked'", volume.ID).Count(&count).Error; err != nil || count != transfer.MaxThumbnailEntries {
		t.Fatal("uncertain rows deleted", count, err)
	}
}

func TestThumbnailCacheSharesRequestAdmissionAndAuditsPublication(t *testing.T) {
	f := newFixture(t)
	m := transferManager(f)
	var handles []*transfer.Handle
	defer func() {
		for _, h := range handles {
			h.Close(true)
		}
	}()
	for i, uid := range []int64{f.aliceID, f.aliceID, f.bobID, f.bobID} {
		key := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("admission-%d", i))))
		h, err := m.CreateThumbnail(context.Background(), uid, key)
		if err != nil {
			t.Fatal(err)
		}
		handles = append(handles, h)
	}
	if _, err := transferManager(f).CreateArchive(context.Background(), f.aliceID); !errors.Is(err, contract.ErrTransferBusy) {
		t.Fatal("archive bypassed thumbnail admission", err)
	}
	for _, h := range handles {
		if err := h.Close(true); err != nil {
			t.Fatal(err)
		}
	}
	file := f.upload(t, f.alice, "cache-audit.png", pngIdentity(t, false))
	hook := "test-thumbnail-publish-failure"
	if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if row, ok := tx.Statement.Dest.(*model.OperationLog); ok && row.Action == "transfer_thumbnail_cache" {
			tx.AddError(errors.New("injected cache publish failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	failed := thumbnailGet(f, file, 64)
	f.db.Callback().Create().Remove(hook)
	if failed.Code != 503 || failed.Header().Get("ETag") != "" {
		t.Fatal("unaudited cache published", failed.Code)
	}
	var cached int64
	if err := f.db.Model(&model.TransferArtifact{}).Where("user_id=? AND kind='thumbnail' AND state='cached'", f.aliceID).Count(&cached).Error; err != nil || cached != 0 {
		t.Fatal("cache audit failure not atomic", cached, err)
	}
	if r := transferAudit(t, f); r.Usage.Charged != 0 {
		t.Fatal("failed cache leaked a live reservation", r)
	}
	if w := thumbnailGet(f, file, 64); w.Code != 200 {
		t.Fatal("retry did not regenerate", w.Code)
	}
}

func TestThumbnailCacheDeletionJournalRetainsChargeUntilDoneAudit(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "cache-delete-journal.png", pngIdentity(t, false))
	if w := thumbnailGet(f, file, 64); w.Code != 200 {
		t.Fatal(w.Code)
	}
	row := cachedThumbnail(t, f, file, 64)
	transferExpire(t, f, row.Token)
	hook := "test-thumbnail-delete-done-failure"
	if err := f.db.Callback().Create().Before("gorm:create").Register(hook, func(tx *gorm.DB) {
		if row, ok := tx.Statement.Dest.(*model.OperationLog); ok && row.Action == "transfer_delete_done" {
			tx.AddError(errors.New("injected thumbnail done failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	_, err := transferManager(f).Reconcile(context.Background())
	f.db.Callback().Create().Remove(hook)
	if err == nil {
		t.Fatal("cache delete done audit failure ignored")
	}
	if row := transferArtifact(t, f, row.Token); row.State != "deleting" || row.CacheKey != nil {
		t.Fatal("deletion intent not durable", row)
	}
	if r := transferAudit(t, f); r.Usage.Charged != row.Bytes {
		t.Fatal("uncertain delete released charge", r)
	}
	if _, err := os.Stat(filepath.Join(f.cfg.DataDir, "storage", row.RelativePath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old payload still present", err)
	}
	r, err := transferManager(f).Reconcile(context.Background())
	if err != nil || r.Removed != 1 || r.Usage.Charged != 0 {
		t.Fatal("cache journal did not recover", r, err)
	}
}

func TestThumbnailCacheTCPDisconnectReleasesReaderForExpiry(t *testing.T) {
	f := newFixture(t)
	resetRateSettings(t, f)
	img := image.NewRGBA(image.Rect(0, 0, 512, 512))
	rng := rand.New(rand.NewSource(41))
	for y := 0; y < 512; y++ {
		for x := 0; x < 512; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(rng.Intn(256)), G: uint8(rng.Intn(256)), B: uint8(rng.Intn(256)), A: 255})
		}
	}
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, img); err != nil {
		t.Fatal(err)
	}
	file := f.upload(t, f.alice, "tcp-cache.png", pngBytes.Bytes())
	server := httptest.NewServer(f.router)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/api/files/%d/thumbnail?size=1024&rate_kbps=1", server.URL, file.ID), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+f.alice)
	done := make(chan error, 1)
	go func() {
		resp, err := server.Client().Do(req)
		if resp != nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		done <- err
	}()
	key := thumbnailKey(t, f, file, 1024)
	var row model.TransferArtifact
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := f.db.Where("cache_key=? AND state='cached'", key).First(&row).Error
		if err == nil {
			break
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) || time.Now().After(deadline) {
			t.Fatal("TCP cache never published", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	transferExpire(t, f, row.Token)
	r, err := transferManager(f).Reconcile(context.Background())
	if !errors.Is(err, transfer.ErrPending) || r.Busy != 1 {
		t.Fatal("HTTP reader has no kernel pin", r, err)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled HTTP request unexpectedly completed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP cancellation did not finish")
	}
	deadline = time.Now().Add(3 * time.Second)
	for {
		r, err = transferManager(f).Reconcile(context.Background())
		if err == nil && r.Usage.Charged == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("disconnected HTTP reader leaked pin", r, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestThumbnailCacheOutputBoundFailsBeforeIOOrEviction(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "cache-output-cap.png", pngIdentity(t, false))
	if w := thumbnailGet(f, file, 64); w.Code != 200 {
		t.Fatal(w.Code)
	}
	retained := cachedThumbnail(t, f, file, 64)
	key := fmt.Sprintf("%x", sha256.Sum256([]byte("oversized derivative")))
	h, err := transferManager(f).CreateThumbnail(context.Background(), f.aliceID, key)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close(true)
	if n, err := h.Write(make([]byte, transfer.MaxThumbnailBytes+1)); n != 0 || !errors.Is(err, contract.ErrTransferSpace) {
		t.Fatal("oversized output accepted", n, err)
	}
	if info, err := os.Stat(h.Path()); err != nil || info.Size() != 0 {
		t.Fatal("oversized output written before budget", err)
	}
	if row := transferArtifact(t, f, retained.Token); row.State != "cached" || row.CacheKey == nil {
		t.Fatal("oversized output evicted unrelated cache", row)
	}
	if row := transferArtifact(t, f, h.Token()); row.Bytes != 1 {
		t.Fatal("rejected write changed reservation", row.Bytes)
	}
}

func TestThumbnailCacheHitsStillCheckSourceAuthorizationAndIntegrity(t *testing.T) {
	f := newFixture(t)
	original := pngIdentity(t, false)
	file := f.upload(t, f.alice, "cache-auth.png", original)
	first := thumbnailGet(f, file, 64)
	if first.Code != 200 {
		t.Fatal(first.Code)
	}
	row := cachedThumbnail(t, f, file, 64)
	grant := model.FilePermission{FileID: file.ID, UserID: f.bobID, Permission: "read"}
	if err := f.db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/files/%d/thumbnail?size=64", file.ID)
	allowed := identityRequest(f, f.bob, path, nil)
	if allowed.Code != 200 || allowed.Header().Get("ETag") != first.Header().Get("ETag") {
		t.Fatal("shared source could not reuse cache", allowed.Code)
	}
	if err := f.db.Delete(&grant).Error; err != nil {
		t.Fatal(err)
	}
	if denied := identityRequest(f, f.bob, path, map[string]string{"If-None-Match": first.Header().Get("ETag")}); denied.Code != 403 || denied.Header().Get("ETag") != "" {
		t.Fatal("cache conditional bypassed current ACL", denied.Code)
	}
	var storage model.Storage
	if err := f.db.First(&storage, *file.StorageID).Error; err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storage.FilePath, bytes.Repeat([]byte("!"), len(original)), 0600); err != nil {
		t.Fatal(err)
	}
	if denied := identityRequest(f, f.alice, path, map[string]string{"If-None-Match": first.Header().Get("ETag")}); denied.Code != 409 || denied.Header().Get("ETag") != "" {
		t.Fatal("cache hid corrupt source", denied.Code)
	}
	if cached := transferArtifact(t, f, row.Token); cached.State != "cached" || cached.CacheKey == nil {
		t.Fatal("source error modified cache", cached)
	}
}

func TestThumbnailCacheBadPolicyOrRootBindingCannotEvictOrServe(t *testing.T) {
	f := newFixture(t)
	file := f.upload(t, f.alice, "cache-policy.png", pngIdentity(t, false))
	if w := thumbnailGet(f, file, 64); w.Code != 200 {
		t.Fatal(w.Code)
	}
	row := cachedThumbnail(t, f, file, 64)
	var volume model.TransferVolume
	if err := f.db.First(&volume, "id=?", row.VolumeID).Error; err != nil {
		t.Fatal(err)
	}
	originalRoot := volume.RootPath
	if err := f.db.Model(&volume).Update("limit_bytes", -1).Error; err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{64, 128} {
		if w := thumbnailGet(f, file, size); w.Code != 503 {
			t.Fatal("bad policy accepted", w.Code)
		}
	}
	if cached := transferArtifact(t, f, row.Token); cached.State != "cached" || cached.CacheKey == nil {
		t.Fatal("bad policy caused cache eviction", cached)
	}
	if err := f.db.Model(&volume).Updates(map[string]any{"limit_bytes": transfer.DefaultLimit, "root_path": "/unbound-test-root"}).Error; err != nil {
		t.Fatal(err)
	}
	if w := thumbnailGet(f, file, 64); w.Code != 503 {
		t.Fatal("cache read adopted mismatched root", w.Code)
	}
	if err := f.db.Model(&volume).Update("root_path", originalRoot).Error; err != nil {
		t.Fatal(err)
	}
}
