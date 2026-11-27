//go:build integration && darwin

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cloudenest_go/model"
)

type nativeMediaSeed struct {
	Image      model.File
	Video      model.File
	Static     model.File
	Private    model.File
	Generator  string
	ImageHash  string
	VideoHash  string
	StaticHash string
}

type nativeMediaHeldWriter struct {
	http.ResponseWriter
	ctx     context.Context
	marker  string
	written int
	status  int
}

func (w *nativeMediaHeldWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *nativeMediaHeldWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *nativeMediaHeldWriter) Write(p []byte) (int, error) {
	count := min(256, max(0, len(p)-1))
	n, err := w.ResponseWriter.Write(p[:count])
	w.written += n
	if err != nil {
		return n, err
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
	if err := os.WriteFile(w.marker, []byte("partial native media response\n"), 0600); err != nil {
		return n, err
	}
	select {
	case <-w.ctx.Done():
		return n, w.ctx.Err()
	case <-time.After(8 * time.Second):
		return n, context.DeadlineExceeded
	}
}

func seedNativeMedia(t *testing.T, f *fixture) nativeMediaSeed {
	t.Helper()
	script, err := filepath.Abs("../../scripts/generate-live-photo-fixture.swift")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "generate-live-photo")
	output, err := exec.Command("xcrun", "swiftc", "-parse-as-library", "-swift-version", "6", script, "-o", binary).CombinedOutput()
	if err != nil {
		t.Fatalf("synthetic media generator compile failed: %v\n%s", err, output)
	}
	if len(output) > 0 {
		t.Logf("macOS 26-compatible media fixture compiler diagnostics:\n%s", output)
	}
	root, err := os.MkdirTemp("/tmp", "cloudnest-native-media-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	output, err = exec.Command(binary, root).CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatalf("real PHLivePhoto fixture decode required: %v", err)
	}
	read := func(name string) ([]byte, string) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		return data, hex.EncodeToString(sum[:])
	}
	image, ih := read("Synthetic Live.jpg")
	video, vh := read("Synthetic Live.mov")
	static, sh := read("Synthetic static.png")
	seed := nativeMediaSeed{Generator: binary, ImageHash: ih, VideoHash: vh, StaticHash: sh}
	// Different filenames deliberately prevent upload's same-name auto-binding;
	// native tests and GUI must explicitly associate these two real resources.
	seed.Image = f.upload(t, f.alice, fmt.Sprintf("Native media %d still.jpg", f.aliceID), image)
	seed.Video = f.upload(t, f.alice, fmt.Sprintf("Native media %d motion.mov", f.aliceID), video)
	seed.Static = f.upload(t, f.alice, fmt.Sprintf("Native media %d static.png", f.aliceID), static)
	seed.Private = f.upload(t, f.bob, fmt.Sprintf("Private native media %d.jpg", f.bobID), image)
	return seed
}

func TestMacOSClientPhotos(t *testing.T) {
	if os.Getenv("CLOUDNEST_TEST_SWIFT") != "1" {
		t.Skip("set CLOUDNEST_TEST_SWIFT=1 for native media acceptance")
	}
	f := newFixture(t)
	media := seedNativeMedia(t, f)
	root := t.TempDir()
	marker := filepath.Join(root, "video-inflight")
	var slow atomic.Bool
	type cancelledMedia struct {
		cancelled bool
		bytes     int
		status    int
	}
	stopped := make(chan cancelledMedia, 1)
	var traceMu sync.Mutex
	pinned, unpinned := map[string]int{}, map[string]int{}
	server := newNativeTLSServer(f, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if slow.Load() && r.URL.Path == fmt.Sprintf("/api/files/%d/versions/1/download", media.Video.ID) {
			observed := &nativeMediaHeldWriter{ResponseWriter: w, ctx: r.Context(), marker: marker}
			w = observed
			defer func() {
				stopped <- cancelledMedia{cancelled: r.Context().Err() != nil, bytes: observed.written, status: observed.status}
			}()
		}
		if strings.HasSuffix(r.URL.Path, "/download") {
			traceMu.Lock()
			if strings.Contains(r.URL.Path, "/versions/") {
				pinned[r.URL.Path]++
			} else {
				unpinned[r.URL.Path]++
			}
			traceMu.Unlock()
		}
		f.router.ServeHTTP(w, r)
	}))
	defer server.Close()
	certificate := filepath.Join(root, "server.der")
	if err := os.WriteFile(certificate, server.Certificate().Raw, 0600); err != nil {
		t.Fatal(err)
	}
	packagePath, err := filepath.Abs("../../cloudnest_macos")
	if err != nil {
		t.Fatal(err)
	}
	var owner, reader model.User
	if err := f.db.First(&owner, f.aliceID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.First(&reader, f.bobID).Error; err != nil {
		t.Fatal(err)
	}
	var pair model.LivePhoto
	var imageGrant, videoGrant model.FilePermission
	for _, role := range []string{"owner", "cancelled", "image-reader", "pair-reader", "revoked", "restricted"} {
		t.Run(role, func(t *testing.T) {
			user := reader
			switch role {
			case "owner":
				user = owner
			case "cancelled":
				defer slow.Store(false)
				user = owner
				if err := f.db.First(&pair, "image_file_id=? AND video_file_id=?", media.Image.ID, media.Video.ID).Error; err != nil {
					t.Fatal(err)
				}
				slow.Store(true)
			case "image-reader":
				if err := f.db.First(&pair, "image_file_id=? AND video_file_id=?", media.Image.ID, media.Video.ID).Error; err != nil {
					t.Fatal(err)
				}
				imageGrant = model.FilePermission{FileID: media.Image.ID, UserID: f.bobID, Permission: "read"}
				if err := f.db.Create(&imageGrant).Error; err != nil {
					t.Fatal(err)
				}
			case "pair-reader":
				videoGrant = model.FilePermission{FileID: media.Video.ID, UserID: f.bobID, Permission: "read"}
				if err := f.db.Create(&videoGrant).Error; err != nil {
					t.Fatal(err)
				}
			case "revoked":
				if err := f.db.Delete(&videoGrant).Error; err != nil {
					t.Fatal(err)
				}
			case "restricted":
				if err := f.db.Create(&model.UserPathPermission{UserID: f.bobID, Method: "GET", Path: "/api/files"}).Error; err != nil {
					t.Fatal(err)
				}
			}
			directory := filepath.Join(root, role)
			if err := os.Mkdir(directory, 0700); err != nil {
				t.Fatal(err)
			}
			fixture := map[string]any{"origin": server.URL, "certificate": certificate, "username": user.Username, "password": "disposable-fixture-password", "role": role, "user_id": user.ID,
				"owner_id": f.aliceID, "image_id": media.Image.ID, "video_id": media.Video.ID, "static_id": media.Static.ID, "private_id": media.Private.ID, "pair_id": pair.ID,
				"image_sha256": media.ImageHash, "video_sha256": media.VideoHash, "static_sha256": media.StaticHash, "generator": media.Generator, "directory": directory}
			fixture["cancel_marker"] = marker
			if pair.ID > 0 {
				var known struct {
					Item map[string]any `json:"item"`
				}
				decode(t, f.json(t, "GET", fmt.Sprintf("/api/live-photos/%d", pair.ID), f.alice, nil, 200), &known)
				fixture["pair"] = known.Item
			}
			data, err := json.Marshal(fixture)
			if err != nil {
				t.Fatal(err)
			}
			manifest := filepath.Join(directory, "fixture.json")
			if err := os.WriteFile(manifest, data, 0600); err != nil {
				t.Fatal(err)
			}
			command := exec.Command("swift", "test", "--package-path", packagePath, "--filter", "realBackendPhotos")
			command.Env = append(os.Environ(), "CLOUDNEST_SWIFT_PHOTO_FIXTURE="+manifest)
			output, err := command.CombinedOutput()
			t.Log(string(output))
			if err != nil {
				t.Fatal(err)
			}
			if role == "cancelled" {
				slow.Store(false)
				select {
				case observed := <-stopped:
					if observed.status != http.StatusPartialContent || !observed.cancelled || observed.bytes <= 0 || int64(observed.bytes) >= media.Video.Size {
						t.Fatal("native media response did not cancel partway", observed)
					}
					t.Logf("Native media partial response cancelled: status=%d bytes=%d parent_cancelled=%v", observed.status, observed.bytes, observed.cancelled)
				case <-time.After(3 * time.Second):
					t.Fatal("native media close did not disconnect server")
				}
				waitNoReadLeases(t, f, *media.Video.StorageID)
			}
		})
	}
	traceMu.Lock()
	defer traceMu.Unlock()
	if len(unpinned) != 0 {
		t.Fatal("native media preview used latest instead of fixed version", unpinned)
	}
	for _, id := range []int64{media.Image.ID, media.Video.ID, media.Static.ID} {
		path := fmt.Sprintf("/api/files/%d/versions/1/download", id)
		if pinned[path] == 0 {
			t.Fatal("fixed source version was not downloaded", path)
		}
	}
	t.Logf("Native media fixed-version requests: %v; no latest downloads", pinned)
	for _, file := range []model.File{media.Image, media.Video, media.Static, media.Private} {
		var current model.File
		if err := f.db.First(&current, file.ID).Error; err != nil || current.IsDeleted != 0 {
			t.Fatal("media management removed source", file.ID, err)
		}
	}
	var count int64
	if err := f.db.Model(&model.LivePhoto{}).Where("id=?", pair.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("reader changed retained pair", count, err)
	}
}
