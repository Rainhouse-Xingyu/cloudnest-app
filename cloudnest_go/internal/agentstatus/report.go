// Package agentstatus defines the bounded, secret-free registration receipt.
package agentstatus

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"time"
)

var ErrInvalid = errors.New("invalid registration report")

type Report struct {
	State          string    `json:"state"`
	ConfigID       int64     `json:"config_id"`
	ObservedAt     time.Time `json:"observed_at"`
	Expected       int       `json:"expected"`
	Registered     int       `json:"registered"`
	Failed         int       `json:"failed"`
	Pending        int       `json:"pending"`
	ProxyStates    []byte    `json:"proxy_states,omitempty"`
	UpstreamStates []byte    `json:"upstream_states,omitempty"`
}

// ProxyView is response-only metadata derived from the saved configuration.
type ProxyView struct {
	MappingID int64  `json:"mapping_id,omitempty"`
	State     string `json:"state"`
	Upstream  string `json:"upstream_tcp,omitempty"`
}

func (r Report) Validate() error {
	if r.ConfigID < 0 || r.ObservedAt.IsZero() || r.ObservedAt.Year() < 2020 || r.ObservedAt.Year() > 2200 {
		return ErrInvalid
	}
	for _, n := range []int{r.Expected, r.Registered, r.Failed, r.Pending} {
		if n < 0 || n > 512 {
			return ErrInvalid
		}
	}
	if !r.validProxyStates() || !r.validUpstreamStates() {
		return ErrInvalid
	}
	switch r.State {
	case "stopped":
		if r.Expected != 0 || r.Registered != 0 || r.Failed != 0 || r.Pending != 0 {
			return ErrInvalid
		}
		return nil
	case "unavailable":
		if r.Registered != 0 || r.Failed != 0 || r.Pending != 0 {
			return ErrInvalid
		}
		return nil
	}
	if r.Expected != r.Registered+r.Failed+r.Pending || (r.Expected > 0 && r.ConfigID == 0) {
		return ErrInvalid
	}
	switch r.State {
	case "registered":
		if r.Expected > 0 && r.Registered == r.Expected && r.Failed == 0 && r.Pending == 0 {
			return nil
		}
	case "starting":
		if r.Failed == 0 && (r.Pending > 0 || r.Expected == 0) {
			return nil
		}
	case "degraded":
		if r.Failed > 0 {
			return nil
		}
	}
	return ErrInvalid
}

// Each configuration position uses two bits, from least to most significant.
// Values are 0 pending, 1 registered, and 2 failed. 3 is reserved.
func (r Report) validProxyStates() bool {
	if len(r.ProxyStates) == 0 {
		return true // Older agents send only aggregate counts.
	}
	if r.State == "stopped" || r.State == "unavailable" || len(r.ProxyStates) != (r.Expected+3)/4 {
		return false
	}
	var counts [3]int
	for pos := 0; pos < len(r.ProxyStates)*4; pos++ {
		state := (r.ProxyStates[pos/4] >> (2 * (pos % 4))) & 3
		if pos >= r.Expected {
			if state != 0 {
				return false
			}
			continue
		}
		if state == 3 {
			return false
		}
		counts[state]++
	}
	return counts[0] == r.Pending && counts[1] == r.Registered && counts[2] == r.Failed
}

// UpstreamStates uses the same packing: 0 unknown, 1 reachable, 2 unreachable.
func (r Report) validUpstreamStates() bool {
	if len(r.UpstreamStates) == 0 {
		return true
	}
	if len(r.ProxyStates) == 0 || len(r.UpstreamStates) != len(r.ProxyStates) {
		return false
	}
	for pos := 0; pos < len(r.UpstreamStates)*4; pos++ {
		state := (r.UpstreamStates[pos/4] >> (2 * (pos % 4))) & 3
		if state == 3 || (pos >= r.Expected && state != 0) {
			return false
		}
	}
	return true
}

func Encode(r *Report) (string, error) {
	if r != nil && len(r.UpstreamStates) != 0 {
		return "", ErrInvalid
	}
	return encode(r, 512)
}

func EncodeRuntime(r *Report) (string, error) {
	return encode(r, 640)
}

func encode(r *Report, limit int) (string, error) {
	if r == nil {
		return "", nil
	}
	if r.Validate() != nil {
		return "", ErrInvalid
	}
	copy := *r
	copy.ObservedAt = copy.ObservedAt.UTC()
	b, err := json.Marshal(copy)
	if err != nil || len(b) > limit {
		return "", ErrInvalid
	}
	return string(b), nil
}
func Decode(raw string) (*Report, error) {
	report, err := decode(raw, 512)
	if err == nil && report != nil && len(report.UpstreamStates) != 0 {
		return nil, ErrInvalid
	}
	return report, err
}

func DecodeRuntime(raw string) (*Report, error) {
	return decode(raw, 640)
}

func decode(raw string, limit int) (*Report, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > limit {
		return nil, ErrInvalid
	}
	var r Report
	d := json.NewDecoder(bytes.NewBufferString(raw))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || d.Decode(&struct{}{}) != io.EOF || r.Validate() != nil {
		return nil, ErrInvalid
	}
	return &r, nil
}
