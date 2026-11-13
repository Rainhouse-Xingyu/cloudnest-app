package agentstatus

import (
	"strings"
	"testing"
	"time"
)

func TestReportValidationAndCanonicalRetry(t *testing.T) {
	valid := Report{State: "registered", ConfigID: 4, Expected: 3, Registered: 3, ObservedAt: time.Now()}
	raw, err := Encode(&valid)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(raw)
	if err != nil || !decoded.ObservedAt.Equal(valid.ObservedAt) {
		t.Fatal("time altered", err)
	}
	retry, err := Encode(decoded)
	if err != nil || raw != retry {
		t.Fatal("receipt not canonical")
	}
	for _, mutate := range []func(*Report){
		func(r *Report) { r.State = "SECRET" },
		func(r *Report) { r.Registered = 2 },
		func(r *Report) { r.Failed = -1 },
		func(r *Report) { r.Expected = 513; r.Registered = 513 },
		func(r *Report) { r.ConfigID = 0 },
		func(r *Report) { r.ObservedAt = time.Time{} },
		func(r *Report) { r.State = "stopped" },
		func(r *Report) { r.State = "starting" },
		func(r *Report) { r.State = "degraded" },
	} {
		copy := valid
		mutate(&copy)
		if _, err := Encode(&copy); err == nil {
			t.Fatal("invalid report accepted", copy)
		}
	}
	if _, err := Decode(strings.Replace(raw, `"state":`, `"secret":"value","state":`, 1)); err == nil {
		t.Fatal("unknown diagnostic field allowed")
	}
	if _, err := Decode(raw + " {}"); err == nil {
		t.Fatal("trailing report allowed")
	}
	for _, state := range []string{"stopped", "unavailable", "starting"} {
		r := Report{State: state, ObservedAt: time.Now()}
		if _, err := Encode(&r); err != nil {
			t.Fatal("empty state rejected", state, err)
		}
	}
}

func TestProxyStatesFitLegacyColumnAndMatchCounts(t *testing.T) {
	report := Report{State: "registered", ConfigID: 4, Expected: 512, Registered: 512, ObservedAt: time.Now(), ProxyStates: make([]byte, 128)}
	for i := range report.ProxyStates {
		report.ProxyStates[i] = 0x55
	}
	raw, err := Encode(&report)
	if err != nil || len(raw) > 512 {
		t.Fatal("maximum report exceeded existing storage", len(raw), err)
	}
	if _, err := Decode(raw); err != nil {
		t.Fatal(err)
	}

	report.Expected, report.Registered = 1, 1
	for _, invalid := range [][]byte{{0}, {2}, {3}, {5}, {1, 0}} {
		report.ProxyStates = invalid
		if _, err := Encode(&report); err == nil {
			t.Fatalf("accepted invalid packed status %x", invalid)
		}
	}
	report.ProxyStates = []byte{1}
	if _, err := Encode(&report); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeUpstreamStatesValidation(t *testing.T) {
	report := Report{State: "registered", ConfigID: 9223372036854775807, Expected: 512, Registered: 512, ObservedAt: time.Now(), ProxyStates: make([]byte, 128), UpstreamStates: make([]byte, 128)}
	for i := range report.ProxyStates {
		report.ProxyStates[i], report.UpstreamStates[i] = 0x55, 0xaa
	}
	raw, err := EncodeRuntime(&report)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := DecodeRuntime(raw); err != nil || len(got.UpstreamStates) != 128 {
		t.Fatal("maximum sample failed round trip", err)
	}
	if _, err := Encode(&report); err == nil {
		t.Fatal("command receipt accepted heartbeat-only data")
	}
	report.Expected, report.Registered = 1, 1
	report.ProxyStates = []byte{1}
	for _, invalid := range [][]byte{{3}, {5}, {1, 0}} {
		report.UpstreamStates = invalid
		if _, err := EncodeRuntime(&report); err == nil {
			t.Fatalf("accepted invalid upstream state %x", invalid)
		}
	}
}
