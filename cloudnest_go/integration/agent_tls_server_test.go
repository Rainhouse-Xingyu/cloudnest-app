//go:build integration

package integration

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Start the actual server with fresh keys and the fixture's guarded datastores.
// Only the generated leaf is trusted by this client. No host TLS settings change.
func startAgentTLSServer(t *testing.T, f *fixture) (string, *http.Client) {
	t.Helper()
	dir := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	pemCert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err = os.WriteFile(certFile, pemCert, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pemCert) {
		t.Fatal("test certificate invalid")
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}, Timeout: 3 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	bin := filepath.Join(dir, "server")
	if b, err := exec.Command("go", "build", "-o", bin, "../cmd/server").CombinedOutput(); err != nil {
		t.Fatalf("server build: %v %s", err, b)
	}
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := reservation.Addr().String()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	if err = reservation.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin)
	cmd.Dir = dir
	cmd.Env = []string{"APP_BIND_ADDR=127.0.0.1", "APP_PORT=" + port, "DATA_DIR=" + f.cfg.DataDir, "MYSQL_DSN=" + f.cfg.MySQLDSN, "REDIS_ADDR=" + f.cfg.RedisAddr, "JWT_SECRET=" + f.cfg.JWTSecret, "CLOUDNEST_TLS_CERT_FILE=" + certFile, "CLOUDNEST_TLS_KEY_FILE=" + keyFile, "GIN_MODE=release"}
	if err = cmd.Start(); err != nil {
		stop()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait(); close(done) }()
	t.Cleanup(func() { stop(); <-done })
	base := "https://" + addr
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("TLS server exited before readiness: %v", err)
		default:
		}
		resp, err := client.Get(base + "/api/resource/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 401 {
				return base, client
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("TLS server readiness deadline")
	return "", nil
}
