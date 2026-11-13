//go:build frpnative

package frpagent

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPinnedFRPCDiagnosticsRequireTLSAndCredentials(t *testing.T) {
	dir := os.Getenv("CLOUDNEST_TEST_FRP_BIN_DIR")
	if !filepath.IsAbs(dir) {
		t.Fatal("official binary directory required")
	}
	s := testStore(t)
	if err := InstallBinary(s, filepath.Join(dir, "frpc")); err != nil {
		t.Fatal(err)
	}
	control, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	policy := testPolicy()
	policy.ServerPort = control.Addr().(*net.TCPAddr).Port
	raw := strings.Replace(validConfig, "serverPort: 7000", "serverPort: "+strconv.Itoa(policy.ServerPort), 1)
	normalized, err := Normalize([]byte(raw), policy)
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.stageRuntime("active.json", normalized)
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	d, key, err := prepareDiagnostics(s)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Close()
	defer d.close()
	p := newRawProcess(s)
	p.diagnosticKey = key
	defer p.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.Start(ctx, token); err != nil {
		t.Fatal(err)
	}
	var status RegistrationStatus
	for {
		status, err = d.read(ctx)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("official API unavailable", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if status.State != "starting" || status.Expected != 1 || status.Registered != 0 || status.Pending != 1 {
		t.Fatal("live process without FRP connection claimed ready", status)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", d.address+"/api/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := d.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal("unauthenticated local API allowed", res.StatusCode)
	}
	password := d.password
	d.password = "wrong"
	if _, err := d.read(ctx); !errors.Is(err, ErrDiagnostics) {
		t.Fatal("incorrect local credential accepted")
	}
	d.password = password
	// Even a loopback listener must present this process's fresh identity.
	spoof := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
	defer spoof.Close()
	d.address = spoof.URL
	if _, err := d.read(ctx); !errors.Is(err, ErrDiagnostics) {
		t.Fatal("untrusted loopback TLS accepted")
	}
}
