package frpagent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func fixtureToken() string {
	return "cna1." + strings.Repeat("a", 32) + "." + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
}
func trustedClient(t *testing.T, s *httptest.Server) *Client {
	t.Helper()
	c, e := NewClient(s.URL, fixtureToken(), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw}))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(c.Close)
	return c
}
func TestClientTLSBoundsRedirectAndRedaction(t *testing.T) {
	for _, u := range []string{"http://localhost", "https://user:secret@localhost", "https://localhost/api", "https://localhost?token=secret", "https://localhost/#x"} {
		if _, e := NewClient(u, fixtureToken(), nil); e != ErrProtocol {
			t.Fatal("invalid endpoint accepted")
		}
	}
	mode := "redirect"
	requests := 0
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch mode {
		case "redirect":
			w.Header().Set("Location", "https://example.com/secret")
			w.WriteHeader(307)
		case "error":
			w.WriteHeader(503)
			_, _ = w.Write([]byte(fixtureToken()))
		case "large":
			_, _ = w.Write([]byte(`{"device_id":1,"padding":"` + strings.Repeat("x", 256<<10) + `"}`))
		case "trailing":
			_, _ = w.Write([]byte(`{"device_id":1} {}`))
		case "empty":
			w.WriteHeader(204)
		case "proxy-status":
			_, _ = w.Write([]byte(`{"device_id":1,"proxy_status_v1":true,"runtime_status_v1":true}`))
		case "older-backend":
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error("invalid heartbeat JSON", err)
			}
			if _, exists := body["runtime"]; exists {
				w.WriteHeader(400)
				return
			}
			_, _ = w.Write([]byte(`{"device_id":1}`))
		default:
			_, _ = w.Write([]byte(`{"device_id":1}`))
		}
	}))
	defer s.Close()
	c := trustedClient(t, s)
	ctx := context.Background()
	if e := c.Heartbeat(ctx); !errors.Is(e, HTTPError(307)) || requests != 1 {
		t.Fatal("followed redirect", e)
	}
	mode = "error"
	if e := c.Heartbeat(ctx); !errors.Is(e, HTTPError(503)) || strings.Contains(e.Error(), fixtureToken()) {
		t.Fatal("server error disclosure")
	}
	for _, m := range []string{"large", "trailing", "empty"} {
		mode = m
		if e := c.Heartbeat(ctx); e != ErrProtocol {
			t.Fatal(m, e)
		}
	}
	mode = "ok"
	if e := c.Heartbeat(ctx); e != nil {
		t.Fatal(e)
	}
	if c.proxyStatusV1 {
		t.Fatal("assumed capability on older backend")
	}
	mode = "proxy-status"
	if e := c.Heartbeat(ctx); e != nil || !c.proxyStatusV1 || !c.runtimeStatusV1 {
		t.Fatal("failed to negotiate per-proxy capability", e)
	}
	mode = "older-backend"
	before := requests
	sample := &RegistrationStatus{State: "stopped", ObservedAt: time.Now().UTC()}
	if e := c.heartbeat(ctx, sample); e != nil || c.proxyStatusV1 || c.runtimeStatusV1 || requests != before+2 {
		t.Fatal("heartbeat did not recover after backend rollback", e)
	}
	untrusted, e := NewClient(s.URL, fixtureToken(), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer untrusted.Close()
	if e = untrusted.Heartbeat(ctx); e != ErrNetwork {
		t.Fatal("trusted unregistered CA")
	}
}
func TestRunnerDurableReceiptAcrossRestartAndNewLease(t *testing.T) {
	s := testStore(t)
	delivery := Delivery{Command: Command{ID: strings.Repeat("b", 32), DeviceID: 1, Action: "diagnose", Status: "leased", LeaseExpires: time.Now().Add(time.Minute)}, LeaseID: strings.Repeat("c", 32)}
	renews, acks, claims := 0, 0, 0
	var observed json.RawMessage
	lost := true
	conflict := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+fixtureToken() {
			t.Error("wrong namespace credential")
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/claim"):
			claims++
			_ = json.NewEncoder(w).Encode(delivery)
		case strings.HasSuffix(r.URL.Path, "/renew"):
			renews++
			_ = json.NewEncoder(w).Encode(map[string]any{"lease_expires_at": time.Now().Add(time.Minute)})
		case strings.HasSuffix(r.URL.Path, "/result"):
			acks++
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["code"] != "diagnostic_failed" || body["success"] != false {
				t.Error("invalid receipt")
			}
			report, err := json.Marshal(body["diagnostic"])
			if err != nil || string(report) == "null" {
				t.Error("diagnostic observation missing")
			}
			if observed == nil {
				observed = report
			} else if string(observed) != string(report) {
				t.Error("retry replaced original diagnostic observation")
			}
			if lost {
				lost = false
				conn, _, e := w.(http.Hijacker).Hijack()
				if e != nil {
					t.Error(e)
				} else {
					conn.Close()
				}
				return
			}
			if conflict {
				conflict = false
				w.WriteHeader(409)
				return
			}
			w.WriteHeader(204)
		default:
			t.Error("unexpected route")
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	c := trustedClient(t, server)
	id := Identity{Version: 1, Registration: Registration{DeviceID: 1, Credential: fixtureToken()}, Policy: testPolicy()}
	r, e := NewRunner(s, c, id)
	if e != nil {
		t.Fatal(e)
	}
	if e = r.Step(context.Background()); e != ErrNetwork {
		t.Fatal("did not exercise lost receipt", e)
	}
	// New object reads the on-disk outcome, does not execute diagnose again.
	r, e = NewRunner(s, c, id)
	if e != nil {
		t.Fatal(e)
	}
	conflict = true
	if e = r.Step(context.Background()); !errors.Is(e, HTTPError(409)) {
		t.Fatal(e)
	}
	delivery.LeaseID = strings.Repeat("d", 32)
	if e = r.Step(context.Background()); e != nil {
		t.Fatal(e)
	}
	if renews != 1 || acks != 3 || claims != 2 {
		t.Fatal("receipt retry repeated side effects", renews, acks, claims)
	}
	var state State
	if e = readJSON(s, "state.json", &state); e != nil || state.Pending != nil || state.Completed[delivery.Command.ID].Code != "diagnostic_failed" {
		t.Fatal("outcome missing", e)
	}
}

func TestRunnerExpiredUnfinishedIntentFailsClosed(t *testing.T) {
	s := testStore(t)
	d := Delivery{Command: Command{ID: strings.Repeat("b", 32), DeviceID: 1, Action: "deploy_config"}, LeaseID: strings.Repeat("c", 32)}
	state := State{Version: 1, ClaimID: strings.Repeat("d", 32), Pending: &d, Completed: map[string]Outcome{}, Running: true}
	if e := writeJSON(s, "state.json", state); e != nil {
		t.Fatal(e)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/renew") {
			t.Error("effect after lost lease")
		}
		w.WriteHeader(409)
	}))
	defer server.Close()
	r, e := NewRunner(s, trustedClient(t, server), Identity{Registration: Registration{DeviceID: 1}, Policy: testPolicy()})
	if e != nil {
		t.Fatal(e)
	}
	if e = r.Step(context.Background()); !errors.Is(e, HTTPError(409)) {
		t.Fatal(e)
	}
	if r.state.Running || r.state.Pending != nil || r.state.ClaimID != "" {
		t.Fatal("expired intent left eligible for automatic restore")
	}
	var reloaded State
	if e = readJSON(s, "state.json", &reloaded); e != nil || reloaded.Running || reloaded.Pending != nil {
		t.Fatal("expired intent not persisted", e)
	}
}

func TestRunnerRestoreRevalidatesCanonicalConfig(t *testing.T) {
	s := testStore(t)
	r := &Runner{Store: s, Identity: Identity{Policy: testPolicy()}, process: newRawProcess(s)}
	r.state.Source = []byte(validConfig)
	r.state.Config = []byte(`{"includes":["/etc/*"]}`)
	if e := r.restore(context.Background()); e != ErrStorage {
		t.Fatal("stored canonical config accepted without source validation", e)
	}
}

func TestLegacyUnrepresentableTokenKeepsCommandChannel(t *testing.T) {
	s := testStore(t)
	source := []byte(strings.Replace(validConfig, "test # token", " test # token", 1))
	config, err := Normalize(source, testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	old := State{Version: 1, Completed: map[string]Outcome{}, Source: source, Config: config, Running: true}
	if err := writeJSON(s, "state.json", old); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/api/agent/heartbeat":
			_, _ = w.Write([]byte(`{"device_id":1}`))
		case "/api/agent/commands/claim":
			w.WriteHeader(403) // proves it got as far as the command channel
		default:
			t.Errorf("unexpected endpoint %s", req.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	r, err := NewRunner(s, trustedClient(t, server), Identity{Registration: Registration{DeviceID: 1}, Policy: testPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	reported := false
	r.Report = func(e error) {
		if errors.Is(e, ErrConfig) {
			reported = true
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := r.Run(ctx); !errors.Is(err, HTTPError(403)) || !reported {
		t.Fatal("old config prevented command polling", err)
	}
	var saved State
	if err := readJSON(s, "state.json", &saved); err != nil || saved.Running || string(saved.Source) != string(source) {
		t.Fatal("old config not safely stopped", err)
	}
}
