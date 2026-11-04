//go:build integration && frpnative

package integration

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cloudenest_go/internal/agentauth"
	"cloudenest_go/internal/agentcommand"
	"cloudenest_go/internal/frpagent"
	storageMysql "cloudenest_go/internal/storage/mysql"
	"cloudenest_go/model"
)

// Actual Agent CLI -> actual TLS server -> disposable MySQL -> pinned official
// frpc/frps -> real local upstream. No simulated successful tunnel or executor.
func TestAgentNativeCLIThreeTunnelsRestartAndReconnect(t *testing.T) {
	dir := os.Getenv("CLOUDNEST_TEST_FRP_BIN_DIR")
	if !filepath.IsAbs(dir) {
		t.Fatal("set CLOUDNEST_TEST_FRP_BIN_DIR to verified official 0.71.1 binaries")
	}
	f := newFixture(t)
	device := agentDevice(t, f)
	f.cfg.FRPKeyFile, f.cfg.FRPKeys = frpTestKeys(t, "one", map[string][]byte{"one": bytes.Repeat([]byte{1}, 32)})
	base, httpClient := startAgentTLSServer(t, f)
	resp, e := httpClient.Get(base + "/api/resource/health")
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	work := t.TempDir()
	caPath := filepath.Join(work, "ca.pem")
	if e = os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: resp.TLS.PeerCertificates[0].Raw}), 0600); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 65*time.Second)
	defer cancel()
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "agent-tunnel:"+r.URL.Path) })
	plain := httptest.NewServer(upstream)
	defer plain.Close()
	secure := httptest.NewTLSServer(upstream)
	defer secure.Close()
	frpsCertPath, frpsKeyPath := filepath.Join(work, "frps-cert.pem"), filepath.Join(work, "frps-key.pem")
	if e = os.WriteFile(frpsCertPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: secure.Certificate().Raw}), 0600); e != nil {
		t.Fatal(e)
	}
	keyDER, e := x509.MarshalPKCS8PrivateKey(secure.TLS.Certificates[0].PrivateKey)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(frpsKeyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); e != nil {
		t.Fatal(e)
	}
	var reserved []net.Listener
	port := func() int {
		l, e := net.Listen("tcp4", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		reserved = append(reserved, l)
		return l.Addr().(*net.TCPAddr).Port
	}
	control, httpPort, httpsPort, remote := port(), port(), port(), port()
	defer func() {
		for _, l := range reserved {
			l.Close()
		}
	}()
	plainPort, securePort := plain.Listener.Addr().(*net.TCPAddr).Port, secure.Listener.Addr().(*net.TCPAddr).Port
	serverJSON, e := json.Marshal(map[string]any{"bindAddr": "127.0.0.1", "proxyBindAddr": "127.0.0.1", "bindPort": control, "vhostHTTPPort": httpPort, "vhostHTTPSPort": httpsPort, "auth": map[string]string{"method": "token", "token": "native-fixture # secret"}, "transport": map[string]any{"tls": map[string]any{"force": true, "certFile": frpsCertPath, "keyFile": frpsKeyPath}}, "allowPorts": []map[string]int{{"single": remote}}})
	if e != nil {
		t.Fatal(e)
	}
	serverPath := filepath.Join(work, "frps.json")
	if e = os.WriteFile(serverPath, serverJSON, 0600); e != nil {
		t.Fatal(e)
	}
	for _, l := range reserved {
		l.Close()
	}
	reserved = nil
	var frps *exec.Cmd
	stopFRPS := func() {
		if frps != nil {
			_ = frps.Process.Kill()
			_ = frps.Wait()
			frps = nil
		}
	}
	defer stopFRPS()
	startFRPS := func() {
		frps = exec.CommandContext(ctx, filepath.Join(dir, "frps"), "-c", serverPath)
		frps.Dir = work
		if e := frps.Start(); e != nil {
			t.Fatal(e)
		}
	}
	startFRPS()
	bin := filepath.Join(work, "cloudnest-agent")
	if out, e := exec.Command("go", "build", "-race", "-o", bin, "../cmd/agent").CombinedOutput(); e != nil {
		t.Fatalf("agent build %v %s", e, out)
	}
	enrollment, e := agentauth.New(f.db).Issue(ctx, f.aliceID, device, "disposable-fixture-password")
	if e != nil {
		t.Fatal(e)
	}
	statePath := filepath.Join(work, "agent-state")
	enroll := exec.CommandContext(ctx, bin, "enroll", "--state", statePath, "--server", base, "--ca", caPath, "--frpc", filepath.Join(dir, "frpc"), "--frps-host", "127.0.0.1", "--frps-port", strconv.Itoa(control), "--frps-ca", frpsCertPath, "--local-ports", fmt.Sprintf("%d,%d", plainPort, securePort))
	enroll.Stdin = strings.NewReader(enrollment.Enrollment + "\n")
	if out, e := enroll.CombinedOutput(); e != nil {
		t.Fatalf("agent enroll %v %s", e, out)
	} else if bytes.Contains(out, []byte(enrollment.Enrollment)) {
		t.Fatal("enrollment logged")
	}
	s, e := frpagent.OpenStore(statePath)
	if e != nil {
		t.Fatal(e)
	}
	identity, e := frpagent.LoadIdentity(s)
	s.Close()
	if e != nil || identity.Registration.DeviceID != device {
		t.Fatal("identity not saved", e)
	}
	var agent *exec.Cmd
	logPath := filepath.Join(work, "agent.log")
	log, e := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if e != nil {
		t.Fatal(e)
	}
	defer log.Close()
	startAgent := func() {
		agent = exec.CommandContext(ctx, bin, "run", "--state", statePath)
		agent.Stdout = log
		agent.Stderr = log
		if e := agent.Start(); e != nil {
			t.Fatal(e)
		}
	}
	stopAgent := func() {
		if agent != nil {
			_ = agent.Process.Signal(os.Interrupt)
			if e := agent.Wait(); e != nil {
				t.Error("agent exit", e)
			}
			agent = nil
		}
	}
	defer stopAgent()
	startAgent()
	configText := fmt.Sprintf("serverAddr: 127.0.0.1\nserverPort: %d\nauth:\n  token: 'native-fixture # secret'\nproxies:\n  - name: device_%d_tcp\n    type: tcp\n    localPort: %d\n    remotePort: %d\n  - name: device_%d_http\n    type: http\n    localPort: %d\n    customDomains: ['example.com']\n  - name: device_%d_https\n    type: https\n    localPort: %d\n    customDomains: ['example.com']\n", control, device, plainPort, remote, device, plainPort, device, securePort)
	repo := storageMysql.NewFrpRepo(f.db, f.cfg.FRPKeys)
	cfg, e := repo.CreateConfig(ctx, f.aliceID, false, model.FrpConfig{DeviceID: &device, ConfigText: configText})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { f.db.Where("device_id=?", device).Delete(&model.FrpConfig{}) })
	m := agentcommand.New(f.db, f.cfg.FRPKeys)
	sequence := 0
	command := func(action string, configID int64, want string) {
		t.Helper()
		sequence++
		row, e := m.Enqueue(ctx, f.aliceID, false, device, agentcommand.EnqueueInput{RequestID: fmt.Sprintf("%032x", sequence), Action: action, ConfigID: configID}, "")
		if e != nil {
			t.Fatal(e)
		}
		for {
			var current model.AgentCommand
			if e = f.db.First(&current, "id=?", row.ID).Error; e != nil {
				t.Fatal(e)
			}
			if current.Status == "succeeded" || current.Status == "failed" {
				if current.ResultCode != want {
					t.Fatalf("%s: %s/%s want %s", action, current.Status, current.ResultCode, want)
				}
				break
			}
			if ctx.Err() != nil {
				t.Fatal("command not acknowledged", action)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	command("deploy_config", cfg.ID, "ok")
	roots := x509.NewCertPool()
	roots.AddCert(secure.Certificate())
	probe := func() {
		t.Helper()
		for _, route := range []struct {
			name, scheme string
			port         int
		}{{"tcp", "http", remote}, {"http", "http", httpPort}, {"https", "https", httpsPort}} {
			addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(route.port))
			tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return new(net.Dialer).DialContext(ctx, network, addr)
			}}
			client := &http.Client{Transport: tr, Timeout: time.Second}
			for {
				response, e := client.Get(route.scheme + "://example.com/" + route.name)
				if e == nil {
					b, re := io.ReadAll(io.LimitReader(response.Body, 128))
					response.Body.Close()
					if re == nil && response.StatusCode == 200 && string(b) == "agent-tunnel:/"+route.name {
						break
					}
				}
				if ctx.Err() != nil {
					tr.CloseIdleConnections()
					t.Fatal("tunnel unreachable", route.name)
				}
				time.Sleep(100 * time.Millisecond)
			}
			tr.CloseIdleConnections()
		}
	}
	probe()
	command("diagnose", 0, "ok")
	command("reload", 0, "ok")
	probe()
	command("stop", 0, "ok")
	conn, e := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(remote)), time.Second)
	if e == nil {
		conn.Close()
		t.Fatal("stop left tunnel open")
	}
	command("start", 0, "ok")
	probe()
	// Saved desired state restores on graceful CLI restart after valid heartbeat.
	stopAgent()
	startAgent()
	probe()
	// Real frps disconnection and reconnect, with no new deploy command.
	stopFRPS()
	startFRPS()
	probe()
	bad, e := repo.CreateConfig(ctx, f.aliceID, false, model.FrpConfig{DeviceID: &device, ConfigText: configText + "includes: ['/etc/*']\n"})
	if e != nil {
		t.Fatal(e)
	}
	command("deploy_config", bad.ID, "config_invalid")
	probe()
	stopAgent()
	b, e := os.ReadFile(logPath)
	if e != nil {
		t.Fatal(e)
	}
	for _, secret := range []string{enrollment.Enrollment, identity.Registration.Credential, "native-fixture # secret"} {
		if bytes.Contains(b, []byte(secret)) {
			t.Fatal("agent logs contain secret")
		}
	}
	if bytes.Contains(b, []byte("DATA RACE")) {
		t.Fatal("agent race detector report")
	}
	t.Log("actual CLI enrolled; TCP/HTTP/HTTPS reached upstream; reload/stop/start, graceful restart, frps reconnect and invalid config rejection passed")
}
