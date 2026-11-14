//go:build integration && frpnative && frppublic

package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cloudenest_go/internal/agentauth"
	"cloudenest_go/internal/frpagent"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
)

// Requires an operator-provided isolated public frps. All backend data and
// upstreams are disposable. Existing local and remote services are untouched.
func TestAgentPublicThreeTunnelsAndForget(t *testing.T) {
	host := os.Getenv("CLOUDNEST_PUBLIC_FRPS_HOST")
	address, err := netip.ParseAddr(host)
	if err != nil || !address.Is4() || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
		t.Fatal("explicit public IPv4 test server required")
	}
	credentialDir := os.Getenv("CLOUDNEST_PUBLIC_FRPS_CREDENTIALS")
	info, err := os.Lstat(credentialDir)
	if !filepath.IsAbs(credentialDir) || err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		t.Fatal("test credential directory must be absolute, private and not a symlink")
	}
	readPrivate := func(name string, limit int64) []byte {
		t.Helper()
		path := filepath.Join(credentialDir, name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > limit {
			t.Fatal("missing or unsafe test credential file:", name)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal("cannot read test credential file:", name)
		}
		return raw
	}
	token := strings.TrimSpace(string(readPrivate("token", 1024)))
	decoded, err := hex.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		t.Fatal("expected the dedicated script-generated test token")
	}
	ca := readPrivate("ca.pem", 16384)
	block, _ := pem.Decode(ca)
	if block == nil {
		t.Fatal("invalid public server certificate")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil || certificate.VerifyHostname(host) != nil || time.Now().Before(certificate.NotBefore) || time.Now().After(certificate.NotAfter) {
		t.Fatal("public server certificate does not match the requested endpoint or validity period")
	}
	port := func(name string) int {
		t.Helper()
		n, err := strconv.Atoi(os.Getenv("CLOUDNEST_PUBLIC_FRPS_" + name))
		if err != nil || n < 1024 || n > 65535 {
			t.Fatal("invalid isolated test port", name)
		}
		return n
	}
	control, httpPort, httpsPort, remote := port("CONTROL"), port("HTTP"), port("HTTPS"), port("TCP")
	seen := map[int]bool{}
	for _, p := range []int{control, httpPort, httpsPort, remote} {
		if seen[p] {
			t.Fatal("public test listener ports must be distinct")
		}
		seen[p] = true
	}
	verifyControl := func() {
		t.Helper()
		roots := x509.NewCertPool()
		roots.AddCert(certificate)
		connection, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", net.JoinHostPort(host, strconv.Itoa(control)), &tls.Config{RootCAs: roots, ServerName: host, MinVersion: tls.VersionTLS12})
		if err != nil {
			t.Fatal("public frps control TLS verification failed", err)
		}
		connection.Close()
	}
	// A TUN intermediary can accept a local TCP socket before attempting the
	// remote connection. Authenticate the real peer instead of interpreting a
	// successful Dial as proof that a remote proxy port is already occupied.
	verifyControl()
	binDir := os.Getenv("CLOUDNEST_TEST_FRP_BIN_DIR")
	if !filepath.IsAbs(binDir) {
		t.Fatal("verified official frpc directory required")
	}
	f := newFixture(t)
	device := agentDevice(t, f)
	f.cfg.FRPKeyFile, f.cfg.FRPKeys = frpTestKeys(t, "one", map[string][]byte{"one": bytes.Repeat([]byte{1}, 32)})
	base, client, _, _ := restartableAgentTLSServer(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	work := t.TempDir()
	response, err := client.Get(base + "/api/resource/health")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	backendCA := filepath.Join(work, "backend-ca.pem")
	if err := os.WriteFile(backendCA, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: response.TLS.PeerCertificates[0].Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	frpsCA := filepath.Join(work, "frps-ca.pem")
	if err := os.WriteFile(frpsCA, ca, 0600); err != nil {
		t.Fatal(err)
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	marker := "cloudnest-public-" + hex.EncodeToString(nonce[:])
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+marker {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, marker)
	})
	plain, secure := httptest.NewServer(upstream), httptest.NewTLSServer(upstream)
	defer plain.Close()
	defer secure.Close()
	plainPort, securePort := plain.Listener.Addr().(*net.TCPAddr).Port, secure.Listener.Addr().(*net.TCPAddr).Port
	bin, state, keyFile := filepath.Join(work, "cloudnest-agent"), filepath.Join(work, "state"), filepath.Join(work, "vault-key")
	if output, err := exec.Command("go", "build", "-race", "-o", bin, "../cmd/agent").CombinedOutput(); err != nil {
		t.Fatalf("build Agent: %v %s", err, output)
	}
	if err := os.WriteFile(keyFile, bytes.Repeat([]byte{0x53}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	enrollment, err := agentauth.New(f.db).Issue(ctx, f.aliceID, device, "disposable-fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	enroll := exec.CommandContext(ctx, bin, "enroll", "--state", state, "--key-file", keyFile, "--server", base, "--ca", backendCA, "--frpc", filepath.Join(binDir, "frpc"), "--frps-host", host, "--frps-port", strconv.Itoa(control), "--frps-ca", frpsCA, "--local-ports", fmt.Sprintf("%d,%d", plainPort, securePort))
	enroll.Stdin = strings.NewReader(enrollment.Enrollment + "\n")
	if err := enroll.Run(); err != nil {
		t.Fatal("Agent enrollment failed", err)
	}
	var agent *exec.Cmd
	start := func() {
		agent = exec.CommandContext(ctx, bin, "run", "--state", state)
		if err := agent.Start(); err != nil {
			t.Fatal(err)
		}
	}
	stop := func() {
		if agent != nil {
			_ = agent.Process.Signal(os.Interrupt)
			if err := agent.Wait(); err != nil {
				t.Error("fixture Agent exit", err)
			}
			agent = nil
		}
	}
	defer stop()
	start()
	management := func(path string, body any, output any) {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(ctx, "POST", base+path, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+f.alice)
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("public fixture management %s: HTTP %d", path, res.StatusCode)
		}
		if err := json.NewDecoder(io.LimitReader(res.Body, 65536)).Decode(output); err != nil {
			t.Fatal(err)
		}
	}
	var registered struct{ Item contract.FrpServerView }
	management("/api/frp/servers", map[string]any{"name": "public isolated test", "server_addr": host, "server_port": control, "token": token, "tcp_start": remote, "tcp_end": remote, "http_port": httpPort, "https_port": httpsPort}, &registered)
	for _, fields := range []map[string]any{{"local_port": plainPort, "remote_port": remote}, {"local_port": plainPort, "domain": host}, {"local_port": securePort, "domain": "https://example.com"}} {
		fields["device_id"], fields["server_id"] = device, registered.Item.ID
		var created struct{ Item model.FrpMapping }
		management("/api/frp/mappings", fields, &created)
		if created.Item.ServerID != registered.Item.ID {
			t.Fatal("public mapping lost server identity")
		}
	}
	var saved struct{ Item model.FrpConfig }
	management("/api/frp/configs/save", map[string]any{"device_id": device, "server_id": registered.Item.ID, "server_revision_id": registered.Item.RevisionID}, &saved)
	sequence := 0
	command := func(action string, configID int64, want string) model.AgentCommand {
		t.Helper()
		sequence++
		var response struct{ Item model.AgentCommand }
		management(fmt.Sprintf("/api/frp/devices/%d/commands", device), map[string]any{"request_id": fmt.Sprintf("%032x", sequence), "action": action, "config_id": configID}, &response)
		deadline := time.Now().Add(25 * time.Second)
		for {
			var row model.AgentCommand
			if err := f.db.First(&row, "id=?", response.Item.ID).Error; err != nil {
				t.Fatal(err)
			}
			if row.Status == "succeeded" || row.Status == "failed" {
				if want != "" && row.ResultCode != want {
					t.Fatalf("public command %s: %s/%s, want %s", action, row.Status, row.ResultCode, want)
				}
				return row
			}
			if time.Now().After(deadline) || ctx.Err() != nil {
				t.Fatal("public command receipt timed out", action)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	probe := func() {
		t.Helper()
		var failures []string
		for _, route := range []struct {
			name, scheme string
			port         int
		}{{"TCP", "http", remote}, {"HTTP", "http", httpPort}, {"HTTPS", "https", httpsPort}} {
			roots := x509.NewCertPool()
			roots.AddCert(secure.Certificate())
			transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "example.com", MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(route.port)))
			}}
			requestHost := "example.com"
			if route.name == "TCP" || route.name == "HTTP" {
				requestHost = host
			}
			probeClient := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			deadline := time.Now().Add(25 * time.Second)
			last := "no response"
			for {
				res, err := probeClient.Get(fmt.Sprintf("%s://%s:%d/%s", route.scheme, requestHost, route.port, marker))
				ok := false
				if err != nil {
					last = err.Error()
				}
				if err == nil {
					raw, readErr := io.ReadAll(io.LimitReader(res.Body, 1024))
					res.Body.Close()
					ok = readErr == nil && res.StatusCode == 200 && string(raw) == marker
					last = fmt.Sprintf("HTTP %d; nonce matched=%t; read error=%v", res.StatusCode, string(raw) == marker, readErr)
				}
				if ok {
					t.Log("public nonce verified", route.name)
					break
				}
				if time.Now().After(deadline) || ctx.Err() != nil {
					transport.CloseIdleConnections()
					failures = append(failures, route.name+": "+last)
					break
				}
				time.Sleep(200 * time.Millisecond)
			}
			transport.CloseIdleConnections()
		}
		if len(failures) != 0 {
			t.Fatal("public upstream probes failed", strings.Join(failures, "; "))
		}
	}
	closed := func() {
		t.Helper()
		for _, route := range []struct {
			name, scheme string
			port         int
		}{{"TCP", "http", remote}, {"HTTP", "http", httpPort}, {"HTTPS", "https", httpsPort}} {
			roots := x509.NewCertPool()
			roots.AddCert(secure.Certificate())
			transport := &http.Transport{DisableKeepAlives: true, TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "example.com", MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(route.port)))
			}}
			requestHost := "example.com"
			if route.name == "TCP" || route.name == "HTTP" {
				requestHost = host
			}
			probeClient := &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			deadline := time.Now().Add(10 * time.Second)
			for {
				response, err := probeClient.Get(fmt.Sprintf("%s://%s:%d/%s", route.scheme, requestHost, route.port, marker))
				reachable := false
				if err == nil {
					raw, readErr := io.ReadAll(io.LimitReader(response.Body, 1024))
					response.Body.Close()
					reachable = readErr == nil && response.StatusCode == 200 && string(raw) == marker
				}
				if !reachable {
					break
				}
				if time.Now().After(deadline) {
					transport.CloseIdleConnections()
					t.Fatal("test upstream remained publicly reachable after stop", route.name)
				}
				time.Sleep(200 * time.Millisecond)
			}
			transport.CloseIdleConnections()
		}
		// Whole-endpoint outage cannot pass as successful tunnel stop.
		verifyControl()
	}
	claims := func(want int64) {
		t.Helper()
		var count int64
		if err := f.db.Model(&model.FrpReservation{}).Where("device_id=?", device).Count(&count).Error; err != nil || count != want {
			t.Fatal("public runtime claims", count, want, err)
		}
	}
	command("deploy_config", saved.Item.ID, "ok")
	// Registration is asynchronous: this early snapshot is diagnostic only.
	// After the data probes, diagnose must still prove all three registered.
	initial := command("diagnose", 0, "")
	var initialStatus frpagent.RegistrationStatus
	if err := json.Unmarshal([]byte(initial.DiagnosticData), &initialStatus); err != nil {
		t.Fatal(err)
	}
	t.Logf("public initial registration: state=%s registered=%d failed=%d", initialStatus.State, initialStatus.Registered, initialStatus.Failed)
	probe()
	claims(3)
	row := command("diagnose", 0, "ok")
	var diagnostic frpagent.RegistrationStatus
	if err := json.Unmarshal([]byte(row.DiagnosticData), &diagnostic); err != nil || diagnostic.State != "registered" || diagnostic.Registered != 3 {
		t.Fatal("public registration proof missing", err)
	}
	command("stop", 0, "ok")
	closed()
	claims(3)
	command("start", 0, "ok")
	probe()
	stop()
	start()
	probe()
	command("forget_config", 0, "ok")
	closed()
	claims(0)
	stop()
	start()
	command("start", 0, "config_invalid")
	command("reload", 0, "config_invalid")
	closed()
	claims(0)
	command("deploy_config", saved.Item.ID, "ok")
	probe()
	claims(3)
	command("forget_config", 0, "ok")
	closed()
	claims(0)
	t.Log("actual public TCP/HTTP/HTTPS reached nonce-protected temporary upstreams; registration, stop/start, Agent restart, durable forget and reacquisition passed; remote service was not modified")
}
