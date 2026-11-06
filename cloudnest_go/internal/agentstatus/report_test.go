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
