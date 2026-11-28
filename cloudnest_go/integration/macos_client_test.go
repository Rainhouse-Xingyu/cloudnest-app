//go:build integration && darwin

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cloudenest_go/model"
)

// TestMacOSClientSession exercises the shipping Swift core against the real Go
// router, MySQL and Redis. The dedicated runner supplies disposable services.
func TestMacOSClientSession(t *testing.T) {
	if os.Getenv("CLOUDNEST_TEST_SWIFT") != "1" {
		t.Skip("run scripts/test-macos-backend.py on a macOS development runner")
	}
	f := newFixture(t)
	var lostInitializations sync.Map
	var telemetryGates sync.Map
	var streamingDownloads atomic.Int32
	gateFor := func(key string) *nativeTelemetryGate {
		value, _ := telemetryGates.LoadOrStore(key, &nativeTelemetryGate{done: make(chan struct{})})
		return value.(*nativeTelemetryGate)
	}
	server := newNativeTLSServer(f, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-CloudNest-Test-Stream") == "native-single-stream" {
			if strings.HasSuffix(r.URL.Path, "/download") {
				streamingDownloads.Add(1)
			} else if r.URL.Path == "/api/auth/me" {
				w.Header().Set("X-CloudNest-Test-Download-Requests", strconv.FormatInt(int64(streamingDownloads.Load()), 10))
			}
		}
		if strings.HasPrefix(r.Header.Get("X-CloudNest-Test-Version-Binding"), "native-version-binding-") && strings.HasSuffix(r.URL.Path, "/versions/upload") {
			receipt := httptest.NewRecorder()
			f.router.ServeHTTP(receipt, r)
			if receipt.Code == 200 {
				var fields map[string]any
				if err := json.Unmarshal(receipt.Body.Bytes(), &fields); err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
				fields["file_id"] = int64(987654321)
				encoded, err := json.Marshal(fields)
				if err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
				receipt.Body.Reset()
				receipt.Body.Write(encoded)
			}
			copyNativeReceipt(w, receipt)
			return
		}
		// Test-only scheduling at the real router boundary. Authentication,
		// multipart processing and file writes still run in the shipping router.
		// Gates prove body flow independently of acknowledged durable progress.
		if marker := r.Header.Get("X-CloudNest-Test-Telemetry"); strings.HasPrefix(marker, "native-rate-") {
			if r.URL.Path == "/api/auth/me" && r.URL.Query().Get("test_transfer_release") != "" {
				receipt := httptest.NewRecorder()
				f.router.ServeHTTP(receipt, r)
				if receipt.Code == http.StatusOK {
					gateFor(marker + ":" + r.URL.Query().Get("test_transfer_release")).release()
				}
				copyNativeReceipt(w, receipt)
				return
			}
			if r.URL.Path == "/api/files/upload/chunk" {
				// The production handler streams MultipartReader rather than
				// populating Request.Form. Inspect a bounded test-only copy.
				body, err := io.ReadAll(io.LimitReader(r.Body, (5<<20)+1))
				if err != nil || len(body) > 5<<20 {
					t.Error("invalid telemetry fixture chunk body")
					w.WriteHeader(400)
					return
				}
				r.Body.Close()
				r.Body = io.NopCloser(bytes.NewReader(body))
				_, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
				if err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				parts := multipart.NewReader(bytes.NewReader(body), parameters["boundary"])
				index := ""
				for {
					part, err := parts.NextPart()
					if err != nil {
						break
					}
					if part.FormName() == "chunk_index" {
						value, _ := io.ReadAll(io.LimitReader(part, 16))
						index = string(value)
						break
					}
				}
				if index == "" {
					t.Error("missing telemetry fixture chunk index")
					w.WriteHeader(400)
					return
				}
				receipt := httptest.NewRecorder()
				f.router.ServeHTTP(receipt, r)
				if receipt.Code == http.StatusOK && !gateFor(marker+":upload-"+index).wait(r.Context()) {
					return
				}
				copyNativeReceipt(w, receipt)
				return
			}
			if strings.HasSuffix(r.URL.Path, "/download") && strings.HasPrefix(r.Header.Get("Range"), "bytes=4194304-") {
				f.router.ServeHTTP(&nativeTelemetryWriter{ResponseWriter: w, gate: gateFor(marker + ":download"), context: r.Context()}, r)
				return
			}
		}
		if r.URL.Path == "/api/files/upload/init" {
			body, err := io.ReadAll(io.LimitReader(r.Body, (16<<10)+1))
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
			var input struct {
				Filename string `json:"filename"`
			}
			if json.Unmarshal(body, &input) == nil && strings.HasPrefix(input.Filename, "lost-init-") {
				key := r.Header.Get("Idempotency-Key")
				if key == "" {
					t.Error("native transfer omitted durable initialization key")
					w.WriteHeader(400)
					return
				}
				if _, alreadyLost := lostInitializations.LoadOrStore(key, true); !alreadyLost {
					var disconnected atomic.Bool
					f.router.ServeHTTP(disconnectReceiptWriter{w, &disconnected}, r)
					if !disconnected.Load() {
						t.Error("native initialization did not reach response loss")
					}
					return
				}
			}
		}
		if r.URL.Path == "/api/files/upload/chunk" || strings.HasSuffix(r.URL.Path, "/download") {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
		f.router.ServeHTTP(w, r)
	}))
	defer server.Close()
	redirectServer := newNativeTLSServer(f, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/login" {
			t.Error("Swift followed the credential-bearing redirect")
			w.WriteHeader(http.StatusTeapot)
			return
		}
		http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
	}))
	defer redirectServer.Close()
	certificatePath := filepath.Join(t.TempDir(), "server.der")
	if err := os.WriteFile(certificatePath, server.Certificate().Raw, 0600); err != nil {
		t.Fatal(err)
	}
	response := f.json(t, "POST", "/api/files/folders", f.bob,
		map[string]any{"name": "Other-user-folder"}, 200)
	var otherFolder struct {
		Item model.File `json:"item"`
	}
	decode(t, response, &otherFolder)
	versioned := fixtureNativeVersionHistory(t, f, "Native version history.bin", bytes.Repeat([]byte{0x31}, 9*1024*1024+173), []byte("Current version for native history\n"))
	searchFile := f.upload(t, f.alice, "CloudNestSemanticFixtureZX9182.md", []byte("Synthetic native AI search fixture.\n"))
	searchOther := f.upload(t, f.bob, "CloudNestSemanticFixtureZX9182.md", []byte("Private other-user native AI fixture.\n"))
	packagePath, err := filepath.Abs("../../cloudnest_macos")
	if err != nil {
		t.Fatal(err)
	}
	var otherUser model.User
	if err := f.db.First(&otherUser, f.bobID).Error; err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		id         int64
		restricted string
	}{
		{"default-permissions", f.aliceID, "0"},
		{"custom-permissions", f.bobID, "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var user model.User
			if err := f.db.First(&user, tc.id).Error; err != nil {
				t.Fatal(err)
			}
			if tc.restricted == "1" {
				permission := model.UserPathPermission{UserID: user.ID, Method: "GET", Path: "/api/files"}
				if err := f.db.Create(&permission).Error; err != nil {
					t.Fatal(err)
				}
			}
			filter := os.Getenv("CLOUDNEST_SWIFT_TEST_FILTER")
			if filter == "" {
				filter = "realBackend"
			}
			command := exec.Command("swift", "test", "--package-path", packagePath,
				"--filter", filter)
			command.Env = append(os.Environ(),
				"CLOUDNEST_SWIFT_ORIGIN="+server.URL,
				"CLOUDNEST_SWIFT_REDIRECT_ORIGIN="+redirectServer.URL,
				"CLOUDNEST_SWIFT_USERNAME="+user.Username,
				"CLOUDNEST_SWIFT_OTHER_USERNAME="+otherUser.Username,
				"CLOUDNEST_SWIFT_PASSWORD=disposable-fixture-password",
				"CLOUDNEST_SWIFT_CERTIFICATE="+certificatePath,
				"CLOUDNEST_SWIFT_RESTRICTED="+tc.restricted,
				"CLOUDNEST_SWIFT_VERSION_FILE="+strconv.FormatInt(versioned.ID, 10),
				"CLOUDNEST_SWIFT_SEARCH_FILE="+strconv.FormatInt(searchFile.ID, 10),
				"CLOUDNEST_SWIFT_SEARCH_OTHER="+strconv.FormatInt(searchOther.ID, 10),
				"CLOUDNEST_SWIFT_FORBIDDEN_FILE="+strconv.FormatInt(otherFolder.Item.ID, 10))
			output, err := command.CombinedOutput()
			t.Log(string(output))
			if err != nil {
				t.Fatal(err)
			}
			if tc.restricted == "0" && streamingDownloads.Load() > 1 {
				t.Fatalf("uninterrupted native download used %d requests; want one bounded stream", streamingDownloads.Load())
			}
		})
	}
}

type nativeTelemetryGate struct {
	done chan struct{}
	once sync.Once
}

func (g *nativeTelemetryGate) release() { g.once.Do(func() { close(g.done) }) }
func (g *nativeTelemetryGate) wait(ctx context.Context) bool {
	select {
	case <-g.done:
		return true
	case <-ctx.Done():
		return false
	}
}

func copyNativeReceipt(w http.ResponseWriter, receipt *httptest.ResponseRecorder) {
	for key, values := range receipt.Header() {
		w.Header()[key] = values
	}
	w.WriteHeader(receipt.Code)
	w.Write(receipt.Body.Bytes())
}

type nativeTelemetryWriter struct {
	http.ResponseWriter
	gate    *nativeTelemetryGate
	context context.Context
	sent    int
}

func (w *nativeTelemetryWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *nativeTelemetryWriter) Flush()                      { http.NewResponseController(w.ResponseWriter).Flush() }
func (w *nativeTelemetryWriter) Write(p []byte) (int, error) {
	const prefix = 32 << 10
	n := 0
	if w.sent < prefix {
		count := min(len(p), prefix-w.sent)
		written, err := w.ResponseWriter.Write(p[:count])
		w.sent += written
		n = written
		w.Flush()
		if err != nil {
			return n, err
		}
		p = p[written:]
	}
	if w.sent >= prefix && !w.gate.wait(w.context) {
		return n, w.context.Err()
	}
	written, err := w.ResponseWriter.Write(p)
	w.sent += written
	return n + written, err
}

func fixtureNativeVersionHistory(t *testing.T, f *fixture, name string, original, current []byte) model.File {
	t.Helper()
	file := f.upload(t, f.alice, name, original)
	source := f.upload(t, f.alice, "Source for "+name, current)
	if source.StorageID == nil {
		t.Fatal("missing fixture storage")
	}
	f.json(t, "POST", "/api/files/"+strconv.FormatInt(file.ID, 10)+"/versions", f.alice,
		map[string]any{"storage_id": *source.StorageID, "size": source.Size, "base_version": 1, "comment": "Native history fixture replacement"}, 200)
	if err := f.db.First(&file, file.ID).Error; err != nil {
		t.Fatal(err)
	}
	return file
}
