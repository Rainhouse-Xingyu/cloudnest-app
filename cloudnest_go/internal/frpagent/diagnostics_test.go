package frpagent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRegistrationCountsRejectUntrustedState(t *testing.T) {
	expected := map[string]string{"a": "tcp", "b": "https"}
	order := []string{"b", "a"}
	for _, tc := range []struct {
		raw, state                  string
		registered, failed, pending int
	}{
		{`{}`, "starting", 0, 0, 2},
		{`{"tcp":[{"name":"a","type":"tcp","status":"running"}],"https":[{"name":"b","type":"https","status":"running"}]}`, "registered", 2, 0, 0},
		{`{"tcp":[{"name":"a","type":"tcp","status":"running"}],"https":[{"name":"b","type":"https","status":"start error","err":"SYNTHETIC_SECRET","local_addr":"SECRET_ADDRESS"}]}`, "degraded", 1, 1, 0},
		{`{"tcp":[{"name":"a","type":"tcp","status":"wait start"}]}`, "starting", 0, 0, 2},
		{`{"tcp":[{"name":"a","type":"tcp","status":"running","err":"SYNTHETIC_SECRET"}]}`, "degraded", 0, 1, 1},
	} {
		got, err := decodeRegistration([]byte(tc.raw), expected, order)
		if err != nil || got.State != tc.state || got.Registered != tc.registered || got.Failed != tc.failed || got.Pending != tc.pending || got.Expected != 2 {
			t.Fatal("incorrect registration state", got, err)
		}
		if got.State == "degraded" && got.Registered == 1 && (len(got.ProxyStates) != 1 || got.ProxyStates[0] != 6) {
			t.Fatal("report lost configuration order", got.ProxyStates)
		}
		raw, _ := json.Marshal(got)
		if strings.Contains(string(raw), "SECRET") {
			t.Fatal("upstream details leaked")
		}
	}
	for _, raw := range []string{
		`null`, `{} {}`, `{"udp":[]}`,
		`{"tcp":[{"name":"x","type":"tcp","status":"running"}]}`,
		`{"tcp":[{"name":"a","type":"https","status":"running"}]}`,
		`{"tcp":[{"name":"a","type":"tcp","status":"running"},{"name":"a","type":"tcp","status":"running"}]}`,
		`{"tcp":[{"name":"a","type":"tcp","status":"new phase"}]}`,
	} {
		if _, err := decodeRegistration([]byte(raw), expected, order); err != ErrDiagnostics {
			t.Fatal("unknown state accepted", raw, err)
		}
	}
	zero, err := decodeRegistration([]byte(`{}`), nil, nil)
	if err != nil || zero.State == "registered" {
		t.Fatal("empty config claimed healthy")
	}
}
