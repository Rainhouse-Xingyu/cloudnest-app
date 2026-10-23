//go:build integration

package integration

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"cloudenest_go/model"
)

func identityRequest(f *fixture, token, path string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.RemoteAddr = f.remoteAddr
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}
func TestDownloadProvidesStrongIdentityAndRepresentationChecksum(t *testing.T) {
	f := newFixture(t)
	resetRateSettings(t, f)
	payload := []byte("identity bytes")
	file := f.upload(t, f.alice, "identity.bin", payload)
	w := identityRequest(f, f.alice, fmt.Sprintf("/api/files/%d/download", file.ID), nil)
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), payload) || w.Header().Get("ETag") == "" || w.Header().Get("Repr-Digest") == "" {
		t.Fatal("missing download identity", w.Code, w.Header())
	}
}
func TestDownloadDateRangeCannotMixSameSecondVersions(t *testing.T) {
	f := newFixture(t)
	oldData, newData := []byte("AAAABBBB"), []byte("CCCCDDDD")
	target := f.upload(t, f.alice, "current.bin", oldData)
	source := f.upload(t, f.alice, "source.bin", newData)
	stamp := time.Now().Add(-time.Hour).Truncate(time.Second)
	for _, file := range []model.File{target, source} {
		var storage model.Storage
		if err := f.db.First(&storage, *file.StorageID).Error; err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(storage.FilePath, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	path := fmt.Sprintf("/api/files/%d/download", target.ID)
	first := identityRequest(f, f.alice, path, map[string]string{"Range": "bytes=0-3"})
	if first.Code != 206 || first.Body.String() != "AAAA" {
		t.Fatal("initial partial", first.Code, first.Body.String())
	}
	f.json(t, "POST", fmt.Sprintf("/api/files/%d/versions", target.ID), f.alice, map[string]any{"storage_id": source.StorageID, "base_version": 1}, 200)
	tail := identityRequest(f, f.alice, path, map[string]string{"Range": "bytes=4-", "If-Range": first.Header().Get("Last-Modified")})
	if tail.Code != 200 || !bytes.Equal(tail.Body.Bytes(), newData) {
		t.Fatal("date validator allowed mixed versions", tail.Code, tail.Body.String())
	}
}

func assertIdentity(t *testing.T, w *httptest.ResponseRecorder, whole []byte) {
	t.Helper()
	sum := sha256.Sum256(whole)
	if w.Header().Get("ETag") != fmt.Sprintf(`"sha256-%x"`, sum) || w.Header().Get("Repr-Digest") != "sha-256=:"+base64.StdEncoding.EncodeToString(sum[:])+":" {
		t.Fatal("wrong representation identity", w.Header())
	}
	if w.Header().Get("Content-Digest") != "" {
		t.Fatal("full hash mislabeled as partial content digest")
	}
	if w.Header().Get("Cache-Control") != "private, no-store, no-transform" {
		t.Fatal("unsafe payload cache policy", w.Header())
	}
}
func TestDownloadStrongRangeAndPreconditionsTrackChangedContent(t *testing.T) {
	f := newFixture(t)
	resetRateSettings(t, f)
	oldData, newData := []byte("AAAABBBB"), []byte("CCCCDDDD")
	file := f.upload(t, f.alice, "strong-current.bin", oldData)
	source := f.upload(t, f.alice, "strong-new.bin", newData)
	path := fmt.Sprintf("/api/files/%d/download", file.ID)
	first := identityRequest(f, f.alice, path, map[string]string{"Range": "bytes=0-3"})
	if first.Code != 206 {
		t.Fatal(first.Code)
	}
	assertIdentity(t, first, oldData)
	tag := first.Header().Get("ETag")
	tail := identityRequest(f, f.alice, path, map[string]string{"Range": "bytes=4-", "If-Range": tag})
	if tail.Code != 206 || tail.Header().Get("Content-Range") != "bytes 4-7/8" || string(append(first.Body.Bytes(), tail.Body.Bytes()...)) != string(oldData) {
		t.Fatal("resume did not reconstruct original", tail.Code, tail.Header())
	}
	assertIdentity(t, tail, oldData)
	for _, value := range []string{"W/" + tag, tag + " garbage", first.Header().Get("Last-Modified")} {
		w := identityRequest(f, f.alice, path, map[string]string{"Range": "bytes=4-", "If-Range": value})
		if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), oldData) {
			t.Fatal("weak/invalid validator allowed append", value, w.Code)
		}
	}
	f.json(t, "POST", fmt.Sprintf("/api/files/%d/versions", file.ID), f.alice, map[string]any{"storage_id": source.StorageID, "base_version": 1}, 200)
	restarted := identityRequest(f, f.alice, path, map[string]string{"Range": "bytes=4-", "If-Range": tag})
	if restarted.Code != 200 || !bytes.Equal(restarted.Body.Bytes(), newData) || restarted.Header().Get("Content-Range") != "" {
		t.Fatal("stale ETag did not restart", restarted.Code)
	}
	assertIdentity(t, restarted, newData)
	precondition := identityRequest(f, f.alice, path, map[string]string{"Range": "bytes=4-", "If-Match": tag})
	if precondition.Code != 412 || precondition.Header().Get("ETag") != "" || precondition.Header().Get("Repr-Digest") != "" {
		t.Fatal("stale If-Match not rejected cleanly", precondition.Code, precondition.Header())
	}
	current := restarted.Header().Get("ETag")
	unchanged := identityRequest(f, f.alice, path, map[string]string{"If-None-Match": "W/" + current})
	if unchanged.Code != 304 || unchanged.Body.Len() != 0 {
		t.Fatal("conditional request", unchanged.Code)
	}
	assertIdentity(t, unchanged, newData)
	invalid := identityRequest(f, f.alice, path, map[string]string{"Range": "bytes=99-", "If-Range": current})
	if invalid.Code != 416 || invalid.Header().Get("Content-Range") != "bytes */8" || invalid.Header().Get("Repr-Digest") != "" || invalid.Header().Get("ETag") != "" {
		t.Fatal("invalid range metadata", invalid.Code, invalid.Header())
	}
}
func TestDownloadIdentityCoversHistoricalSharesAndGeneratedArchives(t *testing.T) {
	f := newFixture(t)
	resetRateSettings(t, f)
	payload := []byte("snapshot representation")
	file := f.upload(t, f.alice, "shared-identity.bin", payload)
	path := fmt.Sprintf("/api/files/%d/download", file.ID)
	full := identityRequest(f, f.alice, path, nil)
	assertIdentity(t, full, payload)
	f.json(t, "PUT", fmt.Sprintf("/api/files/%d/rename", file.ID), f.alice, map[string]any{"name": "renamed-identity.bin"}, 200)
	history := identityRequest(f, f.alice, fmt.Sprintf("/api/files/%d/versions/1/download", file.ID), nil)
	assertIdentity(t, history, payload)
	var share struct {
		Code string `json:"share_code"`
	}
	decode(t, f.json(t, "POST", fmt.Sprintf("/api/files/%d/share", file.ID), f.alice, map[string]any{}, 200), &share)
	public := identityRequest(f, "", "/api/public/shares/"+share.Code+"/download", map[string]string{"Range": "bytes=2-", "If-Range": full.Header().Get("ETag")})
	if public.Code != 206 || !bytes.Equal(public.Body.Bytes(), payload[2:]) {
		t.Fatal("public conditional range", public.Code)
	}
	assertIdentity(t, public, payload)
	folder := fixtureFolder(t, f, f.alice, "identity-archive", 0)
	f.json(t, "POST", "/api/files/move", f.alice, map[string]any{"file_ids": []int64{file.ID}, "target_parent_id": folder.ID}, 200)
	zipPath := fmt.Sprintf("/api/files/%d/download-zip", folder.ID)
	zipped := identityRequest(f, f.alice, zipPath, nil)
	if zipped.Code != 200 {
		t.Fatal(zipped.Code)
	}
	archive := append([]byte(nil), zipped.Body.Bytes()...)
	assertIdentity(t, zipped, archive)
	partial := identityRequest(f, f.alice, zipPath, map[string]string{"Range": "bytes=5-", "If-Range": zipped.Header().Get("ETag")})
	if partial.Code != 206 || !bytes.Equal(partial.Body.Bytes(), archive[5:]) {
		t.Fatal("archive bytes/identity not stable", partial.Code)
	}
	assertIdentity(t, partial, archive)
	batch := f.json(t, "POST", "/api/files/batch-download", f.alice, map[string]any{"file_ids": []int64{file.ID}}, 200)
	assertIdentity(t, batch, batch.Body.Bytes())
}
func TestDownloadIntegrityFailureCannotReturnConditionalSuccess(t *testing.T) {
	f := newFixture(t)
	resetRateSettings(t, f)
	payload := []byte("integrity-original")
	file := f.upload(t, f.alice, "integrity-only.bin", payload)
	path := fmt.Sprintf("/api/files/%d/download", file.ID)
	good := identityRequest(f, f.alice, path, nil)
	tag := good.Header().Get("ETag")
	var storage model.Storage
	if err := f.db.First(&storage, *file.StorageID).Error; err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storage.FilePath, bytes.Repeat([]byte("!"), len(payload)), 0600); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"/download", "/versions/1/download"} {
		w := identityRequest(f, f.alice, fmt.Sprintf("/api/files/%d%s", file.ID, suffix), map[string]string{"If-None-Match": tag, "Range": "bytes=0-3"})
		if w.Code != 409 || w.Header().Get("ETag") != "" || w.Header().Get("Repr-Digest") != "" || bytes.Equal(w.Body.Bytes(), payload) {
			t.Fatal("corruption passed conditional verification", w.Code, w.Header())
		}
	}
	if leaseCount(t, f, *file.StorageID) != 0 {
		t.Fatal("failed integrity kept read lease")
	}
}
func TestDownloadStreamAndPreviewCannotBypassRateOrIntegrity(t *testing.T) {
	f := newFixture(t)
	resetRateSettings(t, f)
	payload := bytes.Repeat([]byte("p"), 32<<10)
	for _, ext := range []string{"mp4", "pdf", "docx", "txt"} {
		file := f.upload(t, f.alice, "transport."+ext, payload)
		rateSetting(t, f, "download_speed_limit_kbps", "64")
		endpoint := "preview"
		if ext == "mp4" {
			endpoint = "stream"
		}
		path := fmt.Sprintf("/api/files/%d/%s?rate_kbps=10240", file.ID, endpoint)
		start := time.Now()
		w := identityRequest(f, f.alice, path, nil)
		if w.Code != 200 || time.Since(start) < 425*time.Millisecond {
			t.Fatal("alternate egress bypassed rate", ext, w.Code, time.Since(start))
		}
		if ext == "txt" {
			var preview struct {
				Content string `json:"content"`
			}
			decode(t, w, &preview)
			if preview.Content != string(payload) || w.Header().Get("Repr-Digest") != "" {
				t.Fatal("text JSON mislabeled as original bytes")
			}
		} else {
			if !bytes.Equal(w.Body.Bytes(), payload) {
				t.Fatal("inline bytes changed", ext)
			}
			assertIdentity(t, w, payload)
		}
		rateSetting(t, f, "download_speed_limit_kbps", "broken")
		if got := identityRequest(f, f.alice, path, nil); got.Code != 503 {
			t.Fatal("alternate egress ignored broken policy", ext, got.Code)
		}
		resetRateSettings(t, f)
	}
}
func pngIdentity(t *testing.T, blue bool) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 96, 96))
	color := color.RGBA{R: 255, A: 255}
	if blue {
		color.R, color.B = 0, 255
	}
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color}, image.Point{}, draw.Src)
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func TestDownloadThumbnailIdentityUsesSourceHashAndSealedCache(t *testing.T) {
	f := newFixture(t)
	resetRateSettings(t, f)
	target := f.upload(t, f.alice, "thumb-identity.png", pngIdentity(t, false))
	source := f.upload(t, f.alice, "thumb-blue.png", pngIdentity(t, true))
	stamp := time.Now().Truncate(time.Second)
	if err := f.db.Model(&model.File{}).Where("id=?", target.ID).Update("update_time", stamp).Error; err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/files/%d/thumbnail?size=64", target.ID)
	red := identityRequest(f, f.alice, path, nil)
	if red.Code != 200 {
		t.Fatal(red.Code, red.Body.String())
	}
	assertIdentity(t, red, red.Body.Bytes())
	f.json(t, "POST", fmt.Sprintf("/api/files/%d/versions", target.ID), f.alice, map[string]any{"storage_id": source.StorageID, "base_version": 1}, 200)
	if err := f.db.Model(&model.File{}).Where("id=?", target.ID).Update("update_time", stamp).Error; err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 8)
	for range 8 {
		group.Add(1)
		go func() { defer group.Done(); results <- identityRequest(f, f.alice, path, nil) }()
	}
	group.Wait()
	close(results)
	for blue := range results {
		if blue.Code != 200 || blue.Header().Get("ETag") == red.Header().Get("ETag") {
			t.Fatal("same-second version reused stale thumbnail", blue.Code, blue.Header())
		}
		assertIdentity(t, blue, blue.Body.Bytes())
		img, err := jpeg.Decode(bytes.NewReader(blue.Body.Bytes()))
		if err != nil {
			t.Fatal("partial cache published", err)
		}
		r, _, b, _ := img.At(0, 0).RGBA()
		if b <= r {
			t.Fatal("thumbnail content did not change to blue")
		}
	}
}
func TestDownloadValidatorNeverBypassesCurrentAuthorization(t *testing.T) {
	f := newFixture(t)
	resetRateSettings(t, f)
	file := f.upload(t, f.alice, "authorized-identity.bin", []byte("private-content"))
	path := fmt.Sprintf("/api/files/%d/download", file.ID)
	grant := model.FilePermission{FileID: file.ID, UserID: f.bobID, Permission: "read"}
	if err := f.db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	allowed := identityRequest(f, f.bob, path, nil)
	if allowed.Code != 200 {
		t.Fatal(allowed.Code)
	}
	tag := allowed.Header().Get("ETag")
	if err := f.db.Where("file_id=? AND user_id=?", file.ID, f.bobID).Delete(&model.FilePermission{}).Error; err != nil {
		t.Fatal(err)
	}
	for _, headers := range []map[string]string{{"If-None-Match": tag}, {"Range": "bytes=4-", "If-Range": tag}, {"If-Match": "*"}} {
		denied := identityRequest(f, f.bob, path, headers)
		if denied.Code != 403 || denied.Header().Get("ETag") != "" || denied.Header().Get("Repr-Digest") != "" {
			t.Fatal("validator bypassed ACL", denied.Code, denied.Header())
		}
	}
	if err := f.db.Model(&model.User{}).Where("id=?", f.aliceID).Update("status", 0).Error; err != nil {
		t.Fatal(err)
	}
	if denied := identityRequest(f, f.alice, path, map[string]string{"If-None-Match": tag}); denied.Code != 401 || denied.Header().Get("ETag") != "" {
		t.Fatal("validator bypassed current identity", denied.Code)
	}
}
func TestDownloadInterruptedTCPReassemblesOnlyMatchingRepresentation(t *testing.T) {
	f := newFixture(t)
	resetRateSettings(t, f)
	payload := bytes.Repeat([]byte("network-snapshot"), 32<<10)
	file := f.upload(t, f.alice, "tcp-resume.bin", payload)
	waitNoReadLeases(t, f, *file.StorageID)
	server := httptest.NewServer(f.router)
	defer server.Close()
	path := fmt.Sprintf("%s/api/files/%d/download", server.URL, file.ID)
	req, _ := http.NewRequest("GET", path, nil)
	req.Header.Set("Authorization", "Bearer "+f.alice)
	first, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	prefix := make([]byte, 64<<10)
	if _, err := io.ReadFull(first.Body, prefix); err != nil {
		t.Fatal(err)
	}
	first.Body.Close()
	tag := first.Header.Get("ETag")
	req, _ = http.NewRequest("GET", path, nil)
	req.Header.Set("Authorization", "Bearer "+f.alice)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-", len(prefix)))
	req.Header.Set("If-Range", tag)
	tail, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	rest, err := io.ReadAll(tail.Body)
	tail.Body.Close()
	if err != nil || tail.StatusCode != 206 || tail.Header.Get("ETag") != tag || !bytes.Equal(append(prefix, rest...), payload) {
		t.Fatal("network resume did not match original", tail.StatusCode, err)
	}
	sum := sha256.Sum256(payload)
	if tail.Header.Get("Repr-Digest") != "sha-256=:"+base64.StdEncoding.EncodeToString(sum[:])+":" {
		t.Fatal("network resume digest changed")
	}
	waitNoReadLeases(t, f, *file.StorageID)
}
func TestDownloadMultipartDigestAndEmptyReplacement(t *testing.T) {
	f := newFixture(t)
	resetRateSettings(t, f)
	payload := []byte("ABCDEFGH")
	file := f.upload(t, f.alice, "multipart-empty.bin", payload)
	path := fmt.Sprintf("/api/files/%d/download", file.ID)
	full := identityRequest(f, f.alice, path, nil)
	tag := full.Header().Get("ETag")
	parts := identityRequest(f, f.alice, path, map[string]string{"Range": "bytes=0-1,6-7", "If-Range": tag})
	if parts.Code != 206 {
		t.Fatal(parts.Code)
	}
	assertIdentity(t, parts, payload)
	_, params, err := mime.ParseMediaType(parts.Header().Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	reader := multipart.NewReader(bytes.NewReader(parts.Body.Bytes()), params["boundary"])
	for _, want := range []string{"AB", "GH"} {
		part, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(part)
		if err != nil || string(data) != want {
			t.Fatal("multipart bytes", string(data), err)
		}
	}
	empty := emptyComplete(t, f, f.alice, emptyInit(t, f, f.alice, "empty-source.bin"))
	f.json(t, "POST", fmt.Sprintf("/api/files/%d/versions", file.ID), f.alice, map[string]any{"storage_id": empty.StorageID, "base_version": 1}, 200)
	zero := identityRequest(f, f.alice, path, map[string]string{"Range": "bytes=4-", "If-Range": tag})
	if zero.Code != 200 || zero.Body.Len() != 0 || zero.Header().Get("Content-Length") != "0" {
		t.Fatal("replacement by empty file did not reset", zero.Code, zero.Header())
	}
	assertIdentity(t, zero, nil)
}
