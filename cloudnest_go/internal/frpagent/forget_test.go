package frpagent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestForgetConfigPersistsBeforeReceiptAndSurvivesLostReply(t *testing.T) {
	store := testStore(t)
	delivery := Delivery{Command: Command{ID: strings.Repeat("b", 32), DeviceID: 1, Action: "forget_config", Status: "leased", LeaseExpires: time.Now().Add(time.Minute)}, LeaseID: strings.Repeat("c", 32)}
	renews, acks := 0, 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/claim"):
			json.NewEncoder(w).Encode(delivery)
		case strings.HasSuffix(req.URL.Path, "/renew"):
			renews++
			json.NewEncoder(w).Encode(map[string]any{"lease_expires_at": time.Now().Add(time.Minute)})
		case strings.HasSuffix(req.URL.Path, "/result"):
			acks++
			var receipt struct {
				Success bool
				Code    string
			}
			if json.NewDecoder(req.Body).Decode(&receipt) != nil || !receipt.Success || receipt.Code != "ok" {
				t.Error("invalid forget receipt")
			}
			var saved State
			if err := readJSON(store, "state.json", &saved); err != nil || saved.Running || len(saved.Config) != 0 || len(saved.Source) != 0 || saved.ConfigID != 0 || saved.Completed[delivery.Command.ID].Code != "ok" {
				t.Error("receipt before durable clearing", err)
			}
			if acks == 1 {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
				} else {
					conn.Close()
				}
				return
			}
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	client := trustedClient(t, server)
	id := Identity{Version: 1, Registration: Registration{DeviceID: 1, Credential: fixtureToken()}, Policy: testPolicy()}
	runner, err := NewRunner(store, client, id)
	if err != nil {
		t.Fatal(err)
	}
	runner.state.Running = true
	runner.state.ConfigID = 42
	runner.state.Config = []byte(`{"old":"configuration"}`)
	runner.state.Source = []byte("old secret configuration")
	if err := runner.save(); err != nil {
		t.Fatal(err)
	}
	if err := runner.Step(context.Background()); !errors.Is(err, ErrNetwork) {
		t.Fatal("did not lose receipt", err)
	}
	restarted, err := NewRunner(store, client, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if renews != 1 || acks != 2 {
		t.Fatal("lost receipt repeated side effect", renews, acks)
	}
	if restarted.state.Pending != nil || restarted.state.Running || restarted.state.ConfigID != 0 || len(restarted.state.Config) != 0 || len(restarted.state.Source) != 0 {
		t.Fatal("restart resurrected config")
	}
	for _, action := range []string{"start", "reload"} {
		code, err := restarted.execute(context.Background(), Delivery{Command: Command{Action: action}})
		if err != nil || code != "config_invalid" {
			t.Fatal("cleared config could restart", action, code, err)
		}
	}
}

func TestForgetConfigLostLeaseDoesNotClearSavedConfiguration(t *testing.T) {
	store := testStore(t)
	delivery := Delivery{Command: Command{ID: strings.Repeat("b", 32), DeviceID: 1, Action: "forget_config", Status: "leased", LeaseExpires: time.Now().Add(time.Minute)}, LeaseID: strings.Repeat("c", 32)}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/claim") {
			json.NewEncoder(w).Encode(delivery)
			return
		}
		if strings.HasSuffix(req.URL.Path, "/renew") {
			w.WriteHeader(409)
			return
		}
		t.Error("effect or receipt after rejected lease")
		w.WriteHeader(500)
	}))
	defer server.Close()
	runner, err := NewRunner(store, trustedClient(t, server), Identity{Version: 1, Registration: Registration{DeviceID: 1, Credential: fixtureToken()}, Policy: testPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	runner.state.ConfigID = 42
	runner.state.Config = []byte("saved")
	runner.state.Source = []byte("source")
	if err := runner.save(); err != nil {
		t.Fatal(err)
	}
	if err := runner.Step(context.Background()); !errors.Is(err, HTTPError(409)) {
		t.Fatal(err)
	}
	var saved State
	if err := readJSON(store, "state.json", &saved); err != nil || saved.ConfigID != 42 || string(saved.Config) != "saved" || len(saved.Completed) != 0 {
		t.Fatal("lost lease falsely cleared or acknowledged config", err)
	}
}
