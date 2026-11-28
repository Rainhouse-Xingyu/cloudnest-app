//go:build integration && darwin

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"cloudenest_go/internal/agentcommand"
	"cloudenest_go/internal/app"
	"cloudenest_go/internal/service"
	"cloudenest_go/internal/service/contract"
	storageMysql "cloudenest_go/internal/storage/mysql"
	"cloudenest_go/model"
)

type nativeFRPFixture struct {
	Devices []int64
	Mapping model.FrpMapping
	Config  model.FrpConfig
	Command model.AgentCommand
}

func fixtureNativeFRP(t *testing.T, f *fixture) nativeFRPFixture {
	t.Helper()
	f.cfg.FRPKeyFile, f.cfg.FRPKeys = frpTestKeys(t, "native", map[string][]byte{"native": []byte(strings.Repeat("n", 32))})
	repo := storageMysql.NewFrpRepo(f.db, f.cfg.FRPKeys)
	devices := []int64{mappingDevice(t, f, f.aliceID), mappingDevice(t, f, f.aliceID), mappingDevice(t, f, f.bobID)}
	server := scopedServer(t, f, repo, f.aliceID, 1)
	ctx := context.Background()
	if _, err := repo.UpdateDevice(ctx, contract.FrpActor{UserID: f.aliceID}, devices[0], map[string]any{"device_name": "Native FRP acceptance"}); err != nil {
		t.Fatal(err)
	}
	mapping, err := repo.CreateMapping(ctx, contract.FrpCreateMappingInput{FrpActor: contract.FrpActor{UserID: f.aliceID}, ServerID: server.ID, DeviceID: devices[0], LocalPort: 8080, Status: 1})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := service.NewFrpService(repo, f.rdb).SaveConfig(ctx, f.aliceID, false, service.SaveConfigInput{DeviceID: devices[0], ServerID: server.ID, ServerRevisionID: server.RevisionID})
	if err != nil {
		t.Fatal(err)
	}
	commandIdentity(t, f, devices[0])
	command, err := agentcommand.New(f.db, f.cfg.FRPKeys).Enqueue(ctx, f.aliceID, false, devices[0], agentcommand.EnqueueInput{RequestID: strings.Repeat("a", 32), Action: "diagnose"}, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.db.Where("device_id IN ?", devices).Delete(&model.FrpConfig{}).Error; err != nil {
			t.Error(err)
		}
	})
	return nativeFRPFixture{Devices: devices, Mapping: mapping, Config: cfg, Command: command}
}

// Validates real management metadata and authorization, not a running tunnel.
func TestMacOSClientFRPRead(t *testing.T) {
	if os.Getenv("CLOUDNEST_TEST_SWIFT") != "1" {
		t.Skip("requires scripts/test-macos-backend.py")
	}
	f := newFixture(t)
	seed := fixtureNativeFRP(t, f)
	legacyPort := 20010
	legacy, err := storageMysql.NewFrpRepo(f.db, f.cfg.FRPKeys).CreateMapping(context.Background(), contract.FrpCreateMappingInput{
		FrpActor: contract.FrpActor{UserID: f.aliceID}, DeviceID: seed.Devices[1], LocalPort: 8090, RemotePort: &legacyPort,
		Domain: "legacy-native.example", Status: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.router = fixtureRouter(app.NewContainer(f.cfg, f.db, f.rdb).NewRouter())
	var lost sync.Map
	server := newNativeTLSServer(f, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/commands") && r.Header.Get("X-CloudNest-Test-FRP-Lost") == "native-command-receipt" {
			raw, err := io.ReadAll(io.LimitReader(r.Body, (16<<10)+1))
			if err != nil || len(raw) > 16<<10 {
				w.WriteHeader(400)
				return
			}
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(raw))
			var body struct {
				RequestID string `json:"request_id"`
			}
			if json.Unmarshal(raw, &body) != nil || !agentcommand.ValidID(body.RequestID) {
				w.WriteHeader(400)
				return
			}
			receipt := httptest.NewRecorder()
			f.router.ServeHTTP(receipt, r)
			if receipt.Code == 200 {
				if _, exists := lost.LoadOrStore(body.RequestID, true); !exists {
					// The production transaction committed. Send actual HTTP
					// headers and only one response byte, then drop this socket.
					for name, values := range receipt.Header() {
						w.Header()[name] = values
					}
					w.Header().Set("Content-Length", strconv.Itoa(receipt.Body.Len()))
					w.WriteHeader(200)
					_, _ = w.Write(receipt.Body.Bytes()[:1])
					if err := http.NewResponseController(w).Flush(); err != nil {
						t.Error(err)
						return
					}
					connection, _, err := http.NewResponseController(w).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = connection.Close()
					return
				}
			}
			copyNativeReceipt(w, receipt)
			return
		}
		f.router.ServeHTTP(w, r)
	}))
	defer server.Close()
	certificate := filepath.Join(t.TempDir(), "server.der")
	if err := os.WriteFile(certificate, server.Certificate().Raw, 0600); err != nil {
		t.Fatal(err)
	}
	packagePath, err := filepath.Abs("../../cloudnest_macos")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		id   int64
	}{{"owner", f.aliceID}, {"restricted", f.bobID}} {
		t.Run(tc.name, func(t *testing.T) {
			var user model.User
			if err := f.db.First(&user, tc.id).Error; err != nil {
				t.Fatal(err)
			}
			if tc.name == "restricted" {
				if err := f.db.Create(&model.UserPathPermission{UserID: tc.id, Method: "GET", Path: "/api/files"}).Error; err != nil {
					t.Fatal(err)
				}
			}
			test := exec.Command("swift", "test", "--package-path", packagePath, "--filter", "realBackendFRP")
			test.Env = append(os.Environ(), "CLOUDNEST_SWIFT_FRP_TEST=1", "CLOUDNEST_SWIFT_ORIGIN="+server.URL,
				"CLOUDNEST_SWIFT_USERNAME="+user.Username, "CLOUDNEST_SWIFT_PASSWORD=disposable-fixture-password",
				"CLOUDNEST_SWIFT_CERTIFICATE="+certificate, "CLOUDNEST_SWIFT_FRP_ROLE="+tc.name,
				"CLOUDNEST_SWIFT_FRP_DEVICE="+strconv.FormatInt(seed.Devices[0], 10),
				"CLOUDNEST_SWIFT_FRP_OTHER="+strconv.FormatInt(seed.Devices[2], 10),
				"CLOUDNEST_SWIFT_FRP_MAPPING="+strconv.FormatInt(seed.Mapping.ID, 10),
				"CLOUDNEST_SWIFT_FRP_LEGACY_MAPPING="+strconv.FormatInt(legacy.ID, 10),
				"CLOUDNEST_SWIFT_FRP_CONFIG="+strconv.FormatInt(seed.Config.ID, 10), "CLOUDNEST_SWIFT_FRP_COMMAND="+seed.Command.ID)
			output, err := test.CombinedOutput()
			t.Log(string(output))
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
