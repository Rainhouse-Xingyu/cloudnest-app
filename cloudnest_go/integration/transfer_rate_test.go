//go:build integration

package integration

import (
	"bytes"
	"cloudenest_go/internal/quota"
	"cloudenest_go/internal/service"
	"cloudenest_go/internal/storage/mysql"
	"context"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"cloudenest_go/model"
	"gorm.io/gorm/clause"
)

func rateSetting(t *testing.T, f *fixture, key, value string) {
	t.Helper()
	row := model.SystemSetting{SettingKey: key, SettingValue: value}
	if err := f.db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "setting_key"}}, DoUpdates: clause.AssignmentColumns([]string{"setting_value"})}).Create(&row).Error; err != nil {
		t.Fatal(err)
	}
}
func resetRateSettings(t *testing.T, f *fixture) {
	t.Helper()
	if err := f.db.Where("setting_key IN ?", []string{"upload_speed_limit_kbps", "download_speed_limit_kbps", "upload_peak_speed_limit_kbps", "download_peak_speed_limit_kbps", "peak_window_enabled", "peak_window_start", "peak_window_end"}).Delete(&model.SystemSetting{}).Error; err != nil {
		t.Fatal(err)
	}
}
func TestTransferRateQueryCannotRaiseUploadCap(t *testing.T) {
	f := newFixture(t)
	resetRateSettings(t, f)
	rateSetting(t, f, "upload_speed_limit_kbps", "64")
	payload := bytes.Repeat([]byte("u"), 64<<10)
	id := immutableInit(t, f, len(payload), len(payload))
	body, ct := chunkBody(t, id, 0, payload, "")
	start := time.Now()
	f.request(t, "POST", "/api/files/upload/chunk?upload_rate_kbps=10240", f.alice, ct, body, 200)
	if elapsed := time.Since(start); elapsed < 850*time.Millisecond {
		t.Fatalf("client raised server upload cap: %v", elapsed)
	}
}
func TestTransferRateQueryAndRangeCannotRaiseDownloadCap(t *testing.T) {
	f := newFixture(t)
	resetRateSettings(t, f)
	payload := bytes.Repeat([]byte("download"), 4096)
	file := f.upload(t, f.alice, "paced.bin", payload)
	rateSetting(t, f, "download_speed_limit_kbps", "64")
	server := httptest.NewServer(f.router)
	defer server.Close()
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprintf("range_%t", partial), func(t *testing.T) {
			req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/files/%d/download?rate_kbps=10240", server.URL, file.ID), nil)
			req.Header.Set("Authorization", "Bearer "+f.alice)
			want := 200
			if partial {
				req.Header.Set("Range", "bytes=0-32767")
				want = 206
			}
			start := time.Now()
			resp, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil || resp.StatusCode != want || !bytes.Equal(data, payload) {
				t.Fatal("download", resp.StatusCode, err, len(data))
			}
			if elapsed := time.Since(start); elapsed < 425*time.Millisecond {
				t.Fatalf("download cap bypass range=%t elapsed=%v", partial, elapsed)
			}
		})
	}
}

func TestTransferRateGlobalPeakSurvivesPersonalDisable(t *testing.T) {
	f := newFixture(t)
	resetRateSettings(t, f)
	file := f.upload(t, f.alice, "peak.bin", bytes.Repeat([]byte("p"), 32<<10))
	rateSetting(t, f, "download_speed_limit_kbps", "128")
	rateSetting(t, f, "download_peak_speed_limit_kbps", "64")
	rateSetting(t, f, "peak_window_enabled", "1")
	rateSetting(t, f, "peak_window_start", "00:00")
	rateSetting(t, f, "peak_window_end", "00:00")
	f.json(t, "PUT", "/api/settings/rate-window", f.alice, map[string]any{"enabled": false}, 200)
	start := time.Now()
	f.json(t, "GET", fmt.Sprintf("/api/files/%d/download?rate_kbps=10240", file.ID), f.alice, nil, 200)
	if time.Since(start) < 425*time.Millisecond {
		t.Fatal("personal flag bypassed global peak")
	}
}
func TestTransferRateInvalidConfigurationRejectsEveryDownload(t *testing.T) {
	f := newFixture(t)
	resetRateSettings(t, f)
	file := f.upload(t, f.alice, "private.bin", []byte("private-content"))
	var folder struct {
		Item model.File `json:"item"`
	}
	decode(t, f.json(t, "POST", "/api/files/folders", f.alice, map[string]any{"name": "rate-folder"}, 200), &folder)
	var share, folderShare struct {
		Code string `json:"share_code"`
	}
	decode(t, f.json(t, "POST", fmt.Sprintf("/api/files/%d/share", file.ID), f.alice, map[string]any{}, 200), &share)
	decode(t, f.json(t, "POST", fmt.Sprintf("/api/files/%d/share", folder.Item.ID), f.alice, map[string]any{}, 200), &folderShare)
	rateSetting(t, f, "download_speed_limit_kbps", "broken")
	for _, tc := range []struct {
		method, path, token string
		body                any
	}{
		{"GET", fmt.Sprintf("/api/files/%d/download?rate_kbps=10240", file.ID), f.alice, nil},
		{"GET", fmt.Sprintf("/api/files/%d/versions/1/download", file.ID), f.alice, nil},
		{"GET", fmt.Sprintf("/api/files/%d/download-zip", folder.Item.ID), f.alice, nil},
		{"POST", "/api/files/batch-download", f.alice, map[string]any{"file_ids": []int64{file.ID}}},
		{"GET", "/api/public/shares/" + share.Code + "/download", "", nil},
		{"GET", "/api/public/shares/" + folderShare.Code + "/download", "", nil},
	} {
		response := f.json(t, tc.method, tc.path, tc.token, tc.body, 503)
		if bytes.Contains(response.Body.Bytes(), []byte("private-content")) {
			t.Fatal("policy failure leaked payload")
		}
	}
}
func TestTransferRateBadPolicyOrQueryAllocatesNoUploadArtifact(t *testing.T) {
	f := newFixture(t)
	resetRateSettings(t, f)
	id := immutableInit(t, f, 4, 4)
	rateSetting(t, f, "upload_speed_limit_kbps", "broken")
	body, ct := chunkBody(t, id, 0, []byte("data"), "")
	f.request(t, "POST", "/api/files/upload/chunk?upload_rate_kbps=10240", f.alice, ct, body, 503)
	rateSetting(t, f, "upload_speed_limit_kbps", "64")
	for _, query := range []string{"upload_rate_kbps=0", "upload_rate_kbps=-1", "upload_rate_kbps=9223372036854775808", "upload_rate_kbps=64&upload_rate_kbps=10240", "upload_rate_kbps="} {
		body, ct := chunkBody(t, id, 0, []byte("data"), "")
		f.request(t, "POST", "/api/files/upload/chunk?"+query, f.alice, ct, body, 400)
	}
	var count int64
	if err := f.db.Model(&model.TransferArtifact{}).Where("user_id=?", f.aliceID).Count(&count).Error; err != nil || count != 0 || chunkFiles(t, f) != 0 {
		t.Fatal("bad policy allocated artifact", count, err)
	}
}

type rateBrokenWriter struct {
	http.ResponseWriter
	writes int
}

func (w *rateBrokenWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *rateBrokenWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes > 1 {
		return 0, io.ErrClosedPipe
	}
	return w.ResponseWriter.Write(p)
}
func TestTransferRateWriteFailureAbortsActualHTTPWithoutJSON(t *testing.T) {
	f := newFixture(t)
	resetRateSettings(t, f)
	payload := bytes.Repeat([]byte("original"), 4096)
	file := f.upload(t, f.alice, "broken-stream.bin", payload)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.router.ServeHTTP(&rateBrokenWriter{ResponseWriter: w}, r)
	}))
	defer server.Close()
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/files/%d/download", server.URL, file.ID), nil)
	req.Header.Set("Authorization", "Bearer "+f.alice)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err == nil || len(data) == len(payload) || !bytes.Equal(data, payload[:len(data)]) {
		t.Fatal("failed stream not visibly truncated original content", len(data), err)
	}
	if leaseCount(t, f, *file.StorageID) != 0 {
		t.Fatal("stream abort retained lease")
	}
}

func TestTransferRatePersonalWindowUpdateRollsBackAllFields(t *testing.T) {
	f := newFixture(t)
	resetRateSettings(t, f)
	f.json(t, "PUT", "/api/settings/rate-window", f.alice, map[string]any{"enabled": true, "start": "08:00", "end": "09:00"}, 200)
	endKey := fmt.Sprintf("peak_window_end_user_%d", f.aliceID)
	const hook = "test:rate_window_atomic"
	if err := f.db.Callback().Update().Before("gorm:update").Register(hook, func(tx *gorm.DB) {
		if row, ok := tx.Statement.Model.(*model.SystemSetting); ok && row.SettingKey == endKey {
			tx.AddError(errors.New("injected last window field failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer f.db.Callback().Update().Remove(hook)
	f.json(t, "PUT", "/api/settings/rate-window", f.alice, map[string]any{"enabled": true, "start": "10:00", "end": "11:00"}, 500)
	for prefix, want := range map[string]string{"enabled": "1", "start": "08:00", "end": "09:00"} {
		var row model.SystemSetting
		if err := f.db.Where("setting_key=?", fmt.Sprintf("peak_window_%s_user_%d", prefix, f.aliceID)).First(&row).Error; err != nil || row.SettingValue != want {
			t.Fatal("partial rate policy update", row, err)
		}
	}
}
func TestTransferRateDatabaseFailureDoesNotFallBackToDefault(t *testing.T) {
	f := newFixture(t)
	resetRateSettings(t, f)
	file := f.upload(t, f.alice, "rate-db-failure.bin", []byte("private"))
	id := immutableInit(t, f, 4, 4)
	const hook = "test:rate_setting_read_failure"
	if err := f.db.Callback().Query().Before("gorm:query").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "system_setting" {
			tx.AddError(errors.New("injected setting read failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer f.db.Callback().Query().Remove(hook)
	f.json(t, "GET", fmt.Sprintf("/api/files/%d/download?rate_kbps=10240", file.ID), f.alice, nil, 503)
	body, ct := chunkBody(t, id, 0, []byte("data"), "")
	f.request(t, "POST", "/api/files/upload/chunk?upload_rate_kbps=10240", f.alice, ct, body, 503)
	if chunkFiles(t, f) != 0 || leaseCount(t, f, *file.StorageID) != 0 {
		t.Fatal("failed policy kept bytes/reader lease")
	}
}
func TestTransferRateSnapshotWaitsForAtomicAdministratorPolicy(t *testing.T) {
	f := newFixture(t)
	resetRateSettings(t, f)
	rateSetting(t, f, "download_speed_limit_kbps", "128")
	rateSetting(t, f, "download_peak_speed_limit_kbps", "64")
	rateSetting(t, f, "peak_window_enabled", "0")
	rateSetting(t, f, "peak_window_start", "00:00")
	rateSetting(t, f, "peak_window_end", "00:00")
	held, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	committed := make(chan error, 1)
	go func() {
		committed <- f.db.Transaction(func(tx *gorm.DB) error {
			if err := quota.LockPolicy(tx, true); err != nil {
				return err
			}
			for k, v := range map[string]string{"download_speed_limit_kbps": "1024", "download_peak_speed_limit_kbps": "512", "peak_window_enabled": "1"} {
				if err := tx.Model(&model.SystemSetting{}).Where("setting_key=?", k).Update("setting_value", v).Error; err != nil {
					return err
				}
			}
			close(held)
			<-release
			return nil
		})
	}()
	select {
	case <-held:
	case err := <-committed:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("writer did not acquire policy")
	}
	starting := make(chan struct{}, 1)
	const hook = "test:rate_policy_snapshot_lock"
	if err := f.db.Callback().Query().Before("gorm:query").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Table == "storage_quota_policy" {
			select {
			case starting <- struct{}{}:
			default:
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer f.db.Callback().Query().Remove(hook)
	type result struct {
		rate int64
		err  error
	}
	done := make(chan result, 1)
	go func() {
		value, err := service.NewFileService(mysql.NewFileRepo(f.db)).TransferRate(context.Background(), f.aliceID, "download", "10240")
		done <- result{value, err}
	}()
	select {
	case <-starting:
	case <-time.After(2 * time.Second):
		t.Fatal("reader did not start lock")
	}
	select {
	case r := <-done:
		t.Fatal("read partial uncommitted policy", r)
	case <-time.After(50 * time.Millisecond):
	}
	once.Do(func() { close(release) })
	if err := <-committed; err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.err != nil || r.rate != 512*1024 {
			t.Fatal("mixed policy snapshot", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("policy read did not resume")
	}
}
func TestTransferRatePacesMultipartSuffixAndConditionalHTTP(t *testing.T) {
	f := newFixture(t)
	resetRateSettings(t, f)
	payload := bytes.Repeat([]byte("ranges"), 8192) // 48 KiB
	file := f.upload(t, f.alice, "range-response.bin", payload)
	rateSetting(t, f, "download_speed_limit_kbps", "64")
	server := httptest.NewServer(f.router)
	defer server.Close()
	for _, tc := range []struct {
		rangeValue, ifRange string
		status              int
		minimum             time.Duration
	}{
		{"bytes=-32768", "", 206, 425 * time.Millisecond},
		{"bytes=0-16383,32768-49151", "", 206, 425 * time.Millisecond},
		{"bytes=0-3", "Wed, 21 Oct 2015 07:28:00 GMT", 200, 650 * time.Millisecond},
		{"bytes=999999-", "", 416, 0},
	} {
		req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/files/%d/download?rate_kbps=10240", server.URL, file.ID), nil)
		req.Header.Set("Authorization", "Bearer "+f.alice)
		req.Header.Set("Range", tc.rangeValue)
		if tc.ifRange != "" {
			req.Header.Set("If-Range", tc.ifRange)
		}
		start := time.Now()
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != tc.status || time.Since(start) < tc.minimum {
			t.Fatal(tc, resp.StatusCode, len(data), err, time.Since(start))
		}
		if strings.HasPrefix(tc.rangeValue, "bytes=-") && !bytes.Equal(data, payload[len(payload)-32768:]) {
			t.Fatal("suffix bytes changed")
		}
		if strings.Contains(tc.rangeValue, ",") {
			_, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
			if err != nil {
				t.Fatal(err)
			}
			reader := multipart.NewReader(bytes.NewReader(data), params["boundary"])
			for _, expected := range [][]byte{payload[:16384], payload[32768:]} {
				part, err := reader.NextPart()
				if err != nil {
					t.Fatal(err)
				}
				actual, err := io.ReadAll(part)
				if err != nil || !bytes.Equal(actual, expected) {
					t.Fatal("multipart range bytes changed", err)
				}
			}
			if _, err := reader.NextPart(); err != io.EOF {
				t.Fatal("extra range")
			}
		}
		if tc.ifRange != "" && !bytes.Equal(data, payload) {
			t.Fatal("If-Range fallback bytes changed")
		}
	}
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/files/%d/download", server.URL, file.ID), nil)
	req.Header.Set("Authorization", "Bearer "+f.alice)
	req.Header.Set("If-Modified-Since", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat))
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 304 || len(data) != 0 {
		t.Fatal("conditional response", resp.StatusCode, len(data), err)
	}
}
