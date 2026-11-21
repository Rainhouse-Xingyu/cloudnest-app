//go:build integration && darwin

package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cloudenest_go/model"
)

type nativeLargePhase struct {
	Phase       string  `json:"phase"`
	PID         int32   `json:"pid"`
	Bytes       int64   `json:"bytes"`
	Confirmed   int64   `json:"confirmedBytes"`
	Seconds     float64 `json:"elapsedSeconds"`
	BaselineRSS int64   `json:"baselineRSSBytes"`
	PeakRSS     int64   `json:"peakRSSBytes"`
	SHA256      string  `json:"sha256"`
}

type nativeLargeRequest struct {
	Phase   string `json:"phase"`
	Range   string `json:"range"`
	IfRange string `json:"if_range"`
	Status  int    `json:"status"`
	Written int64  `json:"written_bytes"`
}

type nativeLargeWriter struct {
	http.ResponseWriter
	status  int
	written int64
}

func (w *nativeLargeWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *nativeLargeWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *nativeLargeWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	n, err := w.ResponseWriter.Write(p)
	w.written += int64(n)
	return n, err
}

// Only the explicit --large-file runner enables this GiB acceptance. It uses
// four real Swift processes, the shipping core/router and disposable services.
func TestMacOSNativeLargeFile(t *testing.T) {
	if os.Getenv("CLOUDNEST_NATIVE_LARGE_FILE") != "1" || os.Getenv("CLOUDNEST_TEST_SWIFT") != "1" {
		t.Skip("run scripts/test-macos-backend.py --large-file")
	}
	f := newFixture(t)
	rateSetting(t, f, "upload_speed_limit_kbps", "10240")
	rateSetting(t, f, "download_speed_limit_kbps", "10240")
	root := filepath.Join(t.TempDir(), "cloudnest-native-large-run")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Version       int                  `json:"version"`
		Passed        bool                 `json:"passed"`
		Generated     string               `json:"generated_utc"`
		GitHead       string               `json:"git_head"`
		ClientSources map[string]string    `json:"client_source_sha256"`
		Bytes         int64                `json:"bytes"`
		RateKiB       int                  `json:"rate_limit_kib_per_second"`
		Phases        []nativeLargePhase   `json:"phases"`
		Requests      []nativeLargeRequest `json:"download_requests"`
	}
	report.Version = 1
	report.Bytes = (1 << 30) + 173
	report.RateKiB = 10240
	report.ClientSources = map[string]string{}
	for _, directory := range []string{"Core", "App"} {
		paths, err := filepath.Glob(filepath.Join("../../cloudnest_macos", directory, "*.swift"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(data)
			report.ClientSources[directory+"/"+filepath.Base(path)] = hex.EncodeToString(sum[:])
		}
	}
	if data, err := exec.Command("git", "rev-parse", "HEAD").Output(); err == nil {
		report.GitHead = strings.TrimSpace(string(data))
	}
	defer func() {
		report.Generated = time.Now().UTC().Format(time.RFC3339)
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Error(err)
			return
		}
		path := os.Getenv("CLOUDNEST_NATIVE_LARGE_FILE_REPORT")
		if path == "" {
			t.Log(string(data))
			return
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Errorf("exclusive native report creation failed: %v", err)
			return
		}
		if _, err = file.Write(append(data, '\n')); err != nil {
			t.Error(err)
		}
		if err = file.Close(); err != nil {
			t.Error(err)
		}
	}()
	var mu sync.Mutex
	active := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/download") {
			f.router.ServeHTTP(w, r)
			return
		}
		mu.Lock()
		active++
		mu.Unlock()
		writer := &nativeLargeWriter{ResponseWriter: w}
		defer func() {
			// A cancelled response deliberately panics with http.ErrAbortHandler.
			// Observe its release even when ServeHTTP does not return normally.
			mu.Lock()
			report.Requests = append(report.Requests, nativeLargeRequest{Phase: r.Header.Get("X-CloudNest-Test-Large-File-Phase"), Range: r.Header.Get("Range"), IfRange: r.Header.Get("If-Range"), Status: writer.status, Written: writer.written})
			active--
			mu.Unlock()
		}()
		f.router.ServeHTTP(writer, r)
	}))
	defer server.Close()
	certificate := filepath.Join(root, "server.der")
	if err := os.WriteFile(certificate, server.Certificate().Raw, 0600); err != nil {
		t.Fatal(err)
	}
	var user model.User
	if err := f.db.First(&user, f.aliceID).Error; err != nil {
		t.Fatal(err)
	}
	packagePath, err := filepath.Abs("../../cloudnest_macos")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int32]bool{}
	for _, phase := range []string{"upload-pause", "upload-resume", "download-pause", "download-resume"} {
		command := exec.CommandContext(t.Context(), "swift", "test", "--package-path", packagePath, "--filter", "nativeLargeFileTransferAcceptance")
		command.Env = append(os.Environ(), "CLOUDNEST_NATIVE_LARGE_FILE=1", "CLOUDNEST_NATIVE_LARGE_FILE_PHASE="+phase, "CLOUDNEST_NATIVE_LARGE_FILE_DIRECTORY="+root, "CLOUDNEST_SWIFT_ORIGIN="+server.URL, "CLOUDNEST_SWIFT_USERNAME="+user.Username, "CLOUDNEST_SWIFT_PASSWORD=disposable-fixture-password", "CLOUDNEST_SWIFT_CERTIFICATE="+certificate)
		output, runErr := command.CombinedOutput()
		t.Logf("native large phase %s:\n%s", phase, output)
		data, readErr := os.ReadFile(filepath.Join(root, phase+".json"))
		if readErr == nil {
			var result nativeLargePhase
			if err := json.Unmarshal(data, &result); err != nil {
				t.Fatal(err)
			}
			report.Phases = append(report.Phases, result)
			if result.Phase != phase || result.Bytes != report.Bytes || result.PID <= 0 || seen[result.PID] || result.PeakRSS <= 0 || result.PeakRSS >= 512<<20 || result.Seconds <= 0 || len(result.SHA256) != 64 {
				t.Fatal("invalid native phase evidence")
			}
			seen[result.PID] = true
		}
		if runErr != nil {
			t.Fatalf("native phase %s failed: %v", phase, runErr)
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			mu.Lock()
			count := active
			mu.Unlock()
			if count == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("native download handler did not release after its client process exited")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	var state struct {
		Confirmed int64  `json:"downloadConfirmed"`
		ETag      string `json:"etag"`
		SHA256    string `json:"sha256"`
	}
	data, err := os.ReadFile(filepath.Join(root, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if state.Confirmed < report.Bytes/2 || state.Confirmed >= report.Bytes || state.ETag != "\"sha256-"+state.SHA256+"\"" {
		t.Fatal("invalid restart checkpoint")
	}
	if len(report.Requests) != 2 {
		t.Fatalf("GiB download required %d requests; want one initial stream and one resumed stream", len(report.Requests))
	}
	for i, r := range report.Requests {
		expectedPhase := "download-pause"
		expectedRange := "bytes=0-"
		expectedTag := ""
		if i == 1 {
			expectedPhase = "download-resume"
			expectedRange = fmt.Sprintf("bytes=%d-", state.Confirmed)
			expectedTag = state.ETag
		}
		if r.Phase != expectedPhase || r.Range != expectedRange || r.IfRange != expectedTag || r.Status != 206 {
			t.Fatalf("native range/identity evidence mismatch: %+v", r)
		}
	}
	for _, phase := range report.Phases {
		if phase.SHA256 != state.SHA256 {
			t.Fatal("source identity changed across client processes")
		}
	}
	report.Passed = true
}
