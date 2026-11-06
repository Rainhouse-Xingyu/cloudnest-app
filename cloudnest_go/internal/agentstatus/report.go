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
	State      string    `json:"state"`
	ConfigID   int64     `json:"config_id"`
	ObservedAt time.Time `json:"observed_at"`
	Expected   int       `json:"expected"`
	Registered int       `json:"registered"`
	Failed     int       `json:"failed"`
	Pending    int       `json:"pending"`
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
func Encode(r *Report) (string, error) {
	if r == nil {
		return "", nil
	}
	if r.Validate() != nil {
		return "", ErrInvalid
	}
	copy := *r
	copy.ObservedAt = copy.ObservedAt.UTC()
	b, err := json.Marshal(copy)
	if err != nil || len(b) > 512 {
		return "", ErrInvalid
	}
	return string(b), nil
}
func Decode(raw string) (*Report, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > 512 {
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
