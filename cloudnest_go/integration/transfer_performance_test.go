//go:build integration && performance && (darwin || linux)

package integration

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"cloudenest_go/model"
)

// performance is intentionally separate from the ordinary six-minute race suite.
// Only fixture setup uses an in-process router; every measured operation goes to
// a separately built, non-race cmd/server over real loopback HTTP/1.1.
func TestTransferGiBRestartAndRange(t *testing.T) {
	const chunkSize int64 = 64 << 20
	const chunks = 16
	const total = chunkSize * chunks
	f := newFixture(t)
	resetRateSettings(t, f)
	rateSetting(t, f, "upload_speed_limit_kbps", "10240")
	rateSetting(t, f, "download_speed_limit_kbps", "10240")
	rateSetting(t, f, "storage_quota_bytes", fmt.Sprint(4<<30))
	var space syscall.Statfs_t
	if err := syscall.Statfs(f.cfg.DataDir, &space); err != nil {
		t.Fatal(err)
	}
	if uint64(space.Bavail)*uint64(space.Bsize) < 4<<30 {
		t.Fatal("1 GiB benchmark requires at least 4 GiB of free temporary disk")
	}
	binary := filepath.Join(t.TempDir(), "cloudnest-server")
	if output, err := exec.Command("go", "build", "-o", binary, "../cmd/server").CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	commit, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	report := map[string]any{
		"source_commit": strings.TrimSpace(string(commit)), "go_version": runtime.Version(),
		"platform": runtime.GOOS + "/" + runtime.GOARCH, "started_utc": time.Now().UTC(),
		"file_bytes": total, "chunk_bytes": chunkSize, "chunks": chunks,
		"rate_limit_kib_s": 10240, "server_race_instrumentation": false,
		"protocol": "loopback HTTP/1.1", "rss_scope": "each server process lifetime, excludes client/MySQL/Redis",
	}
	expected := sha256.New()
	digests := make([]string, chunks)
	for i := range chunks {
		h := md5.New()
		if n, err := io.Copy(io.MultiWriter(expected, h), performancePayload(i, chunkSize)); err != nil || n != chunkSize {
			t.Fatal("generate payload digest", n, err)
		}
		digests[i] = fmt.Sprintf("%x", h.Sum(nil))
	}
	expectedSHA := fmt.Sprintf("%x", expected.Sum(nil))
	report["sha256"] = expectedSHA
	client := &http.Client{Timeout: 4 * time.Minute}
	defer client.CloseIdleConnections()
	server := startPerformanceServer(t, f, binary)
	var init struct {
		UploadID string `json:"upload_id"`
	}
	performanceJSON(t, client, server.url, f.alice, "POST", "/api/files/upload/init", map[string]any{
		"filename": "g03-performance-1gib.bin", "total_size": total, "chunk_size": chunkSize, "total_chunks": chunks,
	}, 0, &init)
	if init.UploadID == "" {
		t.Fatal("missing upload ID")
	}
	var uploadDuration time.Duration
	var epoch int64
	for i := range chunks {
		if i == chunks/2 {
			performanceJSON(t, client, server.url, f.alice, "POST", "/api/files/upload/pause", map[string]any{"upload_id": init.UploadID, "expected_epoch": 0}, 0, nil)
			report["server_before_restart_peak_rss_bytes"] = server.stop(t)
			client.CloseIdleConnections()
			start := time.Now()
			server = startPerformanceServer(t, f, binary)
			report["restart_ready_seconds"] = time.Since(start).Seconds()
			var progress struct {
				Status   int   `json:"status"`
				Epoch    int64 `json:"transfer_epoch"`
				Count    int   `json:"uploaded_count"`
				Uploaded []int `json:"uploaded_chunks"`
			}
			performanceJSON(t, client, server.url, f.alice, "GET", "/api/files/upload/progress?upload_id="+init.UploadID, nil, 0, &progress)
			if progress.Status != 2 || progress.Epoch != 1 || progress.Count != chunks/2 || len(progress.Uploaded) != chunks/2 {
				t.Fatalf("restart lost paused checkpoint: %+v", progress)
			}
			seen := make(map[int]bool)
			for _, index := range progress.Uploaded {
				seen[index] = true
			}
			for index := range chunks / 2 {
				if !seen[index] {
					t.Fatal("missing confirmed chunk", index)
				}
			}
			performanceJSON(t, client, server.url, f.alice, "POST", "/api/files/upload/resume", map[string]any{"upload_id": init.UploadID, "expected_epoch": 1}, 0, nil)
			epoch = 2
			t.Log("server restarted: all eight confirmed chunks retained; resumed at epoch 2")
		}
		body, ct, length := performanceMultipart(t, init.UploadID, i, chunkSize, digests[i])
		req, err := http.NewRequest("POST", server.url+"/api/files/upload/chunk?upload_rate_kbps=10240", body)
		if err != nil {
			t.Fatal(err)
		}
		req.ContentLength = length
		req.Header.Set("Authorization", "Bearer "+f.alice)
		req.Header.Set("Content-Type", ct)
		req.Header.Set("X-Upload-Epoch", fmt.Sprint(epoch))
		start := time.Now()
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		performanceResponse(t, response, 200, nil)
		uploadDuration += time.Since(start)
		t.Logf("confirmed chunk %d/%d", i+1, chunks)
	}
	report["chunk_upload_seconds"] = uploadDuration.Seconds()
	report["chunk_upload_mib_s"] = float64(total) / (1 << 20) / uploadDuration.Seconds()
	var complete struct {
		Item model.File `json:"item"`
	}
	start := time.Now()
	performanceJSON(t, client, server.url, f.alice, "POST", "/api/files/upload/complete", map[string]any{"upload_id": init.UploadID}, epoch, &complete)
	report["complete_seconds"] = time.Since(start).Seconds()
	if complete.Item.ID <= 0 {
		t.Fatal("missing completed file")
	}
	var replay struct {
		Item model.File `json:"item"`
	}
	performanceJSON(t, client, server.url, f.alice, "POST", "/api/files/upload/complete", map[string]any{"upload_id": init.UploadID}, epoch, &replay)
	if replay.Item.ID != complete.Item.ID {
		t.Fatal("complete replay changed file")
	}

	// Deliberately abort a full 200 response after exactly one chunk. Continue
	// hashing from that byte offset through a new authenticated Range request.
	downloadURL := fmt.Sprintf("%s/api/files/%d/download?rate_kbps=10240", server.url, complete.Item.ID)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+f.alice)
	start = time.Now()
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	etag := response.Header.Get("ETag")
	if response.StatusCode != 200 || response.ContentLength != total || etag != `"sha256-`+expectedSHA+`"` {
		t.Fatal("unexpected initial download identity", response.StatusCode, response.ContentLength, etag)
	}
	actual := sha256.New()
	first := make([]byte, 1)
	if _, err := io.ReadFull(response.Body, first); err != nil {
		t.Fatal(err)
	}
	report["initial_first_byte_seconds"] = time.Since(start).Seconds()
	actual.Write(first)
	if n, err := io.CopyN(actual, response.Body, chunkSize-1); err != nil || n != chunkSize-1 {
		t.Fatal("download prefix", n, err)
	}
	cancel()
	response.Body.Close()
	client.CloseIdleConnections()
	prefixDuration := time.Since(start)
	req, err = http.NewRequest("GET", downloadURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+f.alice)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-", chunkSize))
	req.Header.Set("If-Range", etag)
	start = time.Now()
	response, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 206 || response.ContentLength != total-chunkSize || response.Header.Get("ETag") != etag || response.Header.Get("Content-Range") != fmt.Sprintf("bytes %d-%d/%d", chunkSize, total-1, total) {
		t.Fatal("resume did not preserve exact range/identity", response.StatusCode, response.Header)
	}
	if _, err := io.ReadFull(response.Body, first); err != nil {
		t.Fatal(err)
	}
	report["range_first_byte_seconds"] = time.Since(start).Seconds()
	actual.Write(first)
	if n, err := io.Copy(actual, response.Body); err != nil || n != total-chunkSize-1 {
		t.Fatal("download suffix", n, err)
	}
	response.Body.Close()
	downloadDuration := prefixDuration + time.Since(start)
	if fmt.Sprintf("%x", actual.Sum(nil)) != expectedSHA {
		t.Fatal("resumed full-file SHA-256 mismatch")
	}
	report["download_seconds"] = downloadDuration.Seconds()
	report["download_mib_s"] = float64(total) / (1 << 20) / downloadDuration.Seconds()
	report["download_resume_offset"] = chunkSize
	report["server_after_restart_peak_rss_bytes"] = server.stop(t)
	for _, key := range []string{"server_before_restart_peak_rss_bytes", "server_after_restart_peak_rss_bytes"} {
		if rss := report[key].(int64); rss <= 0 || rss >= 512<<20 {
			t.Fatalf("server RSS outside (0, 512 MiB): %s=%d", key, rss)
		}
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("PERFORMANCE_REPORT\n%s", encoded)
}

// Each chunk uses a distinct deterministic 64 KiB pseudorandom tile. Repeating
// tiles avoid a GiB client allocation; different chunks detect ordering errors.
func performancePayload(index int, size int64) io.Reader {
	tile := make([]byte, 64<<10)
	_, _ = rand.New(rand.NewSource(int64(1000 + index))).Read(tile)
	return &performancePattern{tile: tile, remaining: size}
}

type performancePattern struct {
	tile      []byte
	offset    int
	remaining int64
}

func (p *performancePattern) Read(b []byte) (int, error) {
	if p.remaining == 0 {
		return 0, io.EOF
	}
	if int64(len(b)) > p.remaining {
		b = b[:p.remaining]
	}
	n := 0
	for n < len(b) {
		copied := copy(b[n:], p.tile[p.offset:])
		n += copied
		p.offset = (p.offset + copied) % len(p.tile)
	}
	p.remaining -= int64(n)
	return n, nil
}
func performanceMultipart(t *testing.T, id string, index int, size int64, digest string) (io.Reader, string, int64) {
	t.Helper()
	var metadata bytes.Buffer
	w := multipart.NewWriter(&metadata)
	for _, field := range [][2]string{{"upload_id", id}, {"chunk_index", fmt.Sprint(index)}, {"chunk_md5", digest}} {
		if err := w.WriteField(field[0], field[1]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.CreateFormFile("chunk", "part.bin"); err != nil {
		t.Fatal(err)
	}
	prefix := append([]byte(nil), metadata.Bytes()...)
	metadata.Reset()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	tail := append([]byte(nil), metadata.Bytes()...)
	return io.MultiReader(bytes.NewReader(prefix), performancePayload(index, size), bytes.NewReader(tail)), w.FormDataContentType(), int64(len(prefix)) + size + int64(len(tail))
}
func performanceJSON(t *testing.T, client *http.Client, address, token, method, path string, payload any, epoch int64, out any) {
	t.Helper()
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, address+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Upload-Epoch", fmt.Sprint(epoch))
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	performanceResponse(t, response, 200, out)
}
func performanceResponse(t *testing.T, response *http.Response, status int, out any) {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil || response.StatusCode != status {
		t.Fatalf("HTTP %d (want %d): %s; %v", response.StatusCode, status, body, err)
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			t.Fatal(err)
		}
	}
}

type performanceServer struct {
	cmd     *exec.Cmd
	done    chan error
	url     string
	stopped bool
}

func startPerformanceServer(t *testing.T, f *fixture, binary string) *performanceServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	command := exec.Command(binary)
	command.Dir = t.TempDir()
	command.Env = []string{"MYSQL_DSN=" + f.cfg.MySQLDSN, "REDIS_ADDR=" + f.cfg.RedisAddr, "JWT_SECRET=" + f.cfg.JWTSecret, "JWT_EXPIRE_HOURS=1", "DATA_DIR=" + f.cfg.DataDir, "APP_BIND_ADDR=127.0.0.1", "TRANSFER_MAINTENANCE_DSN=" + f.cfg.MySQLDSN, fmt.Sprintf("APP_PORT=%d", port), "AI_OLLAMA_BASE_URL=http://127.0.0.1:1"}
	logFile, err := os.Create(filepath.Join(command.Dir, "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { logFile.Close() })
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	s := &performanceServer{cmd: command, done: make(chan error, 1), url: fmt.Sprintf("http://127.0.0.1:%d", port)}
	go func() { s.done <- command.Wait() }()
	t.Cleanup(func() {
		if !s.stopped {
			command.Process.Kill()
			<-s.done
		}
	})
	client := &http.Client{Timeout: time.Second}
	defer client.CloseIdleConnections()
	until := time.Now().Add(10 * time.Second)
	for time.Now().Before(until) {
		req, _ := http.NewRequest("GET", s.url+"/api/auth/me", nil)
		req.Header.Set("Authorization", "Bearer "+f.alice)
		response, err := client.Do(req)
		if err == nil {
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode == 200 {
				return s
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("isolated server was not ready in 10 seconds")
	return nil
}
func (s *performanceServer) stop(t *testing.T) int64 {
	t.Helper()
	if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-s.done:
		s.stopped = true
		if err != nil {
			t.Fatal("server shutdown", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("server did not stop in 15 seconds")
	}
	rss := s.cmd.ProcessState.SysUsage().(*syscall.Rusage).Maxrss
	if runtime.GOOS == "linux" {
		rss *= 1024
	}
	return rss
}
