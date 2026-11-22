//go:build integration && darwin && frpnative

package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cloudenest_go/internal/agentauth"
	"cloudenest_go/internal/app"
	"cloudenest_go/internal/frpconfig"
	"cloudenest_go/internal/service"
	"cloudenest_go/internal/service/contract"
	storageMysql "cloudenest_go/internal/storage/mysql"
)

func init() { nativeUIFRPAgentSetup = setupNativeUIFRPAgent }

// Keeps only this fixture's official frps, Agent CLI and upstreams alive while
// the real native UI submits commands. No existing local/remote service changes.
func setupNativeUIFRPAgent(t *testing.T, f *fixture, backend *httptest.Server, work string, manifest map[string]any, cleanup *func()) {
	t.Helper()
	dir := os.Getenv("CLOUDNEST_TEST_FRP_BIN_DIR")
	if !filepath.IsAbs(dir) {
		t.Fatal("verified official 0.71.1 binaries required")
	}
	ctx, cancel := context.WithCancel(t.Context())
	var agent, frps *exec.Cmd
	var logs []*os.File
	var upstreams []*httptest.Server
	var reservations []net.Listener
	var once sync.Once
	*cleanup = func() {
		once.Do(func() {
			forced := false
			stop := func(command *exec.Cmd) {
				if command == nil || command.Process == nil {
					return
				}
				_ = command.Process.Signal(os.Interrupt)
				done := make(chan struct{})
				go func() { _ = command.Wait(); close(done) }()
				select {
				case <-done:
				case <-time.After(8 * time.Second):
					forced = true
					_ = command.Process.Kill()
					<-done
				}
			}
			stop(agent)
			stop(frps)
			cancel()
			for _, listener := range reservations {
				_ = listener.Close()
			}
			for _, server := range upstreams {
				server.Close()
			}
			for _, log := range logs {
				_ = log.Close()
			}
			raw, _ := json.Marshal(map[string]any{"stopped": true, "forced_stop": forced, "utc": time.Now().UTC().Format(time.RFC3339Nano)})
			_ = os.WriteFile(filepath.Join(work, "frp-runtime-cleanup.json"), raw, 0600)
			if forced {
				t.Error("live native FRP fixture needed forced shutdown")
			}
		})
	}
	write := func(name string, content []byte) string {
		path := filepath.Join(work, name)
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "cloudnest-native-frp-ok:"+r.URL.Path) })
	plain, secure := httptest.NewServer(upstream), httptest.NewTLSServer(upstream)
	upstreams = append(upstreams, plain, secure)
	ca := write("agent-backend-ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: backend.Certificate().Raw}))
	frpsCA := write("frps-ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: secure.Certificate().Raw}))
	key, err := x509.MarshalPKCS8PrivateKey(secure.TLS.Certificates[0].PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	frpsKey := write("frps-key.pem", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))
	port := func() int {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		reservations = append(reservations, listener)
		return listener.Addr().(*net.TCPAddr).Port
	}
	control, httpPort, httpsPort, remote := port(), port(), port(), port()
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(secret[:])
	config, err := json.Marshal(map[string]any{"bindAddr": "127.0.0.1", "proxyBindAddr": "127.0.0.1", "bindPort": control,
		"vhostHTTPPort": httpPort, "vhostHTTPSPort": httpsPort, "auth": map[string]string{"method": "token", "token": token},
		"transport":  map[string]any{"tls": map[string]any{"force": true, "certFile": frpsCA, "keyFile": frpsKey}},
		"allowPorts": []map[string]int{{"single": remote}}})
	if err != nil {
		t.Fatal(err)
	}
	frpsPath := write("frps.json", config)
	for _, listener := range reservations {
		_ = listener.Close()
	}
	reservations = nil
	start := func(command *exec.Cmd, logName string) {
		log, err := os.OpenFile(filepath.Join(work, logName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			t.Fatal(err)
		}
		logs = append(logs, log)
		command.Stdout, command.Stderr, command.Dir = log, log, work
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
	}
	frps = exec.CommandContext(ctx, filepath.Join(dir, "frps"), "-c", frpsPath)
	start(frps, "frps.log")
	device := agentDevice(t, f)
	f.cfg.FRPKeyFile, f.cfg.FRPKeys = frpTestKeys(t, "native-live", map[string][]byte{"native-live": bytes.Repeat([]byte{0x4e}, 32)})
	repo := storageMysql.NewFrpRepo(f.db, f.cfg.FRPKeys)
	if _, err := repo.UpdateDevice(ctx, contract.FrpActor{UserID: f.aliceID}, device, map[string]any{"device_name": "Native live Agent"}); err != nil {
		t.Fatal(err)
	}
	server, err := repo.WriteServer(ctx, contract.FrpActor{UserID: f.aliceID}, 0, contract.FrpServerInput{
		Spec: frpconfig.ServerSpec{Name: "Native isolated frps", Address: "127.0.0.1", Port: control, TCPStart: remote, TCPEnd: remote, HTTPPort: httpPort, HTTPSPort: httpsPort}, Token: &token})
	if err != nil {
		t.Fatal(err)
	}
	plainPort, securePort := plain.Listener.Addr().(*net.TCPAddr).Port, secure.Listener.Addr().(*net.TCPAddr).Port
	for _, row := range []struct {
		port   int
		domain string
	}{{plainPort, ""}, {plainPort, "http://example.com"}, {securePort, "https://example.com"}} {
		if _, err := repo.CreateMapping(ctx, contract.FrpCreateMappingInput{FrpActor: contract.FrpActor{UserID: f.aliceID}, ServerID: server.ID, DeviceID: device, LocalPort: row.port, Domain: row.domain, Status: 1}); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := service.NewFrpService(repo, f.rdb).SaveConfig(ctx, f.aliceID, false, service.SaveConfigInput{DeviceID: device, ServerID: server.ID, ServerRevisionID: server.RevisionID})
	if err != nil {
		t.Fatal(err)
	}
	f.router = fixtureRouter(app.NewContainer(f.cfg, f.db, f.rdb).NewRouter())
	bin := filepath.Join(work, "cloudnest-agent")
	if output, err := exec.CommandContext(ctx, "go", "build", "-o", bin, "../cmd/agent").CombinedOutput(); err != nil {
		t.Fatalf("build Agent: %v %s", err, output)
	}
	enrollment, err := agentauth.New(f.db).Issue(ctx, f.aliceID, device, "disposable-fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(work, "agent-state")
	if _, err := rand.Read(secret[:]); err != nil {
		t.Fatal(err)
	}
	keyPath := write("agent-key", secret[:])
	enroll := exec.CommandContext(ctx, bin, "enroll", "--state", state, "--key-file", keyPath,
		"--server", backend.URL, "--ca", ca, "--frpc", filepath.Join(dir, "frpc"),
		"--frps-host", "127.0.0.1", "--frps-port", strconv.Itoa(control), "--frps-ca", frpsCA,
		"--local-ports", fmt.Sprintf("%d,%d", plainPort, securePort))
	enroll.Stdin = strings.NewReader(enrollment.Enrollment + "\n")
	if output, err := enroll.CombinedOutput(); err != nil {
		t.Fatalf("enroll Agent: %v", err)
	} else if bytes.Contains(output, []byte(enrollment.Enrollment)) {
		t.Fatal("enrollment leaked into output")
	}
	agent = exec.CommandContext(ctx, bin, "run", "--state", state)
	start(agent, "agent.log")
	manifest["frp_device_id"], manifest["frp_config_id"] = device, cfg.ID
	manifest["frp_live_runtime"] = true
	manifest["frp_runtime_ports"] = map[string]int{"control": control, "http": httpPort, "https": httpsPort, "tcp": remote, "upstream_http": plainPort, "upstream_https": securePort}
	manifest["frp_runtime_ca"] = frpsCA
	fingerprints := map[string]string{}
	for name, path := range map[string]string{"agent": bin, "frpc": filepath.Join(dir, "frpc"), "frps": filepath.Join(dir, "frps")} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		fingerprints[name] = fmt.Sprintf("%x", sha256.Sum256(raw))
	}
	manifest["frp_runtime_binary_sha256"] = fingerprints
}
