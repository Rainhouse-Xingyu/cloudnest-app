//go:build frpnative

package service

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
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

	"cloudenest_go/model"
)

func TestFRPNativeGeneratedConfigConnectsTCPHTTPAndHTTPS(t *testing.T) {
	dir := os.Getenv("CLOUDNEST_TEST_FRP_BIN_DIR")
	if !filepath.IsAbs(dir) {
		t.Fatal("set absolute CLOUDNEST_TEST_FRP_BIN_DIR")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	work := t.TempDir()
	pc, ps := filepath.Join(dir, "frpc"), filepath.Join(dir, "frps")
	for _, bin := range []string{pc, ps} {
		b, err := exec.CommandContext(ctx, bin, "--version").Output()
		if err != nil || strings.TrimSpace(string(b)) != "0.71.1" {
			t.Fatal("requires official FRP 0.71.1 binaries")
		}
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(random[:]) + " #: test"
	target := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, "cloudnest-reached:%s", r.URL.Path) })
	plain := httptest.NewServer(target)
	defer plain.Close()
	secure := httptest.NewTLSServer(target)
	defer secure.Close()
	// Hold each port until all four have been reserved to avoid selecting the
	// same port twice. Both FRP listeners and upstream services use loopback.
	var reservations []net.Listener
	defer func() {
		for _, l := range reservations {
			l.Close()
		}
	}()
	port := func() int {
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		reservations = append(reservations, l)
		return l.Addr().(*net.TCPAddr).Port
	}
	control, httpPort, httpsPort, remote := port(), port(), port(), port()
	serverCfg := map[string]any{"bindAddr": "127.0.0.1", "proxyBindAddr": "127.0.0.1", "bindPort": control, "vhostHTTPPort": httpPort, "vhostHTTPSPort": httpsPort, "auth": map[string]string{"method": "token", "token": token}, "allowPorts": []map[string]int{{"single": remote}}}
	serverJSON, err := json.Marshal(serverCfg)
	if err != nil {
		t.Fatal(err)
	}
	maps := []model.FrpMapping{
		{ID: 1, DeviceID: 1, LocalPort: plain.Listener.Addr().(*net.TCPAddr).Port, RemotePort: remote},
		{ID: 2, DeviceID: 1, LocalPort: plain.Listener.Addr().(*net.TCPAddr).Port, Domain: "http://example.com"},
		{ID: 3, DeviceID: 1, LocalPort: secure.Listener.Addr().(*net.TCPAddr).Port, Domain: "https://example.com"},
	}
	clientConfig, err := generateFRPCYaml("127.0.0.1", control, token, maps)
	if err != nil {
		t.Fatal(err)
	}
	serverPath, clientPath := filepath.Join(work, "frps.json"), filepath.Join(work, "frpc.yaml")
	if err = os.WriteFile(serverPath, serverJSON, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(clientPath, []byte(clientConfig), 0600); err != nil {
		t.Fatal(err)
	}
	for _, component := range [][2]string{{ps, serverPath}, {pc, clientPath}} {
		cmd := exec.CommandContext(ctx, component[0], "verify", "-c", component[1])
		cmd.Dir = work
		if err = cmd.Run(); err != nil {
			t.Fatal(filepath.Base(component[0]), "verify", err)
		}
	}
	for _, l := range reservations {
		l.Close()
	}
	reservations = nil
	start := func(bin, path string) {
		cmd := exec.CommandContext(ctx, bin, "-c", path)
		cmd.Dir = work
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	}
	start(ps, serverPath)
	listenAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(control))
	for {
		conn, err := net.DialTimeout("tcp", listenAddr, 150*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if ctx.Err() != nil {
			t.Fatal("frps control listener unavailable")
		}
		time.Sleep(50 * time.Millisecond)
	}
	start(pc, clientPath)
	roots := x509.NewCertPool()
	roots.AddCert(secure.Certificate())
	for _, route := range []struct {
		name, scheme string
		port         int
	}{{"tcp", "http", remote}, {"http", "http", httpPort}, {"https", "https", httpsPort}} {
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(route.port))
		tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return new(net.Dialer).DialContext(ctx, network, addr)
		}}
		client := &http.Client{Transport: tr, Timeout: time.Second}
		want := "cloudnest-reached:/" + route.name
		for {
			resp, err := client.Get(route.scheme + "://example.com/" + route.name)
			if err == nil {
				body, readErr := io.ReadAll(io.LimitReader(resp.Body, 128))
				resp.Body.Close()
				if readErr == nil && resp.StatusCode == 200 && string(body) == want {
					break
				}
			}
			if ctx.Err() != nil {
				t.Fatal(route.name, "did not reach upstream", err)
			}
			time.Sleep(75 * time.Millisecond)
		}
		tr.CloseIdleConnections()
		t.Log(route.name, "reached local upstream through frps and frpc")
	}
}
