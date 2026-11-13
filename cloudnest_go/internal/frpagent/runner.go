package frpagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"
)

// Identity and State are AEAD-sealed owner-only files. macOS keys live in the
// selected Keychain; an explicit external private key file is also supported.
// Runtime frpc files contain private routing data; the token uses a pipe.
type Identity struct {
	Version      int          `json:"version"`
	Endpoint     string       `json:"endpoint"`
	Registration Registration `json:"registration"`
	Policy       Policy       `json:"policy"`
	ControlCA    []byte       `json:"control_ca,omitempty"`
}
type Outcome struct {
	Code       string              `json:"code"`
	Diagnostic *RegistrationStatus `json:"diagnostic,omitempty"`
	At         time.Time           `json:"at"`
}
type State struct {
	Version   int                `json:"version"`
	ClaimID   string             `json:"claim_id"`
	Pending   *Delivery          `json:"pending,omitempty"`
	Completed map[string]Outcome `json:"completed"`
	Running   bool               `json:"running"`
	Config    []byte             `json:"config,omitempty"`
	Source    []byte             `json:"source,omitempty"`
	ConfigID  int64              `json:"config_id"`
}

func writeJSON(s *Store, name string, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return ErrStorage
	}
	return s.WriteSecret(name, b)
}
func readJSON(s *Store, name string, v any) error {
	b, e := s.ReadSecret(name)
	if e != nil {
		return e
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(&struct{}{}) != io.EOF {
		return ErrStorage
	}
	return nil
}
func LoadIdentity(s *Store) (Identity, error) {
	var v Identity
	e := readJSON(s, "identity.json", &v)
	if e == nil && (v.Version != 1 || v.Registration.DeviceID <= 0 || !tokenOK(v.Registration.Credential, "cna1.") || v.Policy.Validate() != nil) {
		e = ErrStorage
	}
	return v, e
}
func SaveIdentity(s *Store, v Identity) error {
	if _, e := s.Read("identity.json", 4<<20); !errors.Is(e, os.ErrNotExist) {
		return errors.New("use an empty state directory for enrollment")
	}
	if v.Version != 1 || v.Registration.DeviceID <= 0 || !tokenOK(v.Registration.Credential, "cna1.") || v.Policy.Validate() != nil {
		return ErrStorage
	}
	return writeJSON(s, "identity.json", v)
}

type Runner struct {
	Store    *Store
	Client   *Client
	Identity Identity
	state    State
	process  *Process
	// Report is optional and receives only locally defined, secret-free errors.
	Report         func(error)
	lastDiagnostic *RegistrationStatus
}

func NewRunner(s *Store, c *Client, id Identity) (*Runner, error) {
	r := &Runner{Store: s, Client: c, Identity: id, process: newSupervisedProcess(s)}
	e := readJSON(s, "state.json", &r.state)
	if errors.Is(e, os.ErrNotExist) {
		r.state = State{Version: 1, Completed: map[string]Outcome{}}
	} else if e != nil {
		return nil, e
	}
	if r.state.Version != 1 || r.state.Completed == nil || len(r.state.Completed) > 8192 || len(r.state.Config) > 65536 || len(r.state.Source) > 32768 {
		return nil, ErrStorage
	}
	if r.state.ClaimID != "" && !validID(r.state.ClaimID) {
		return nil, ErrStorage
	}
	if d := r.state.Pending; d != nil && (!validID(d.Command.ID) || !validID(d.LeaseID) || d.Command.DeviceID != id.Registration.DeviceID) {
		return nil, ErrStorage
	}
	for id, out := range r.state.Completed {
		if !validID(id) || out.At.IsZero() {
			return nil, ErrStorage
		}
		if out.Diagnostic != nil && (out.Diagnostic.Validate() != nil || (out.Code == "ok") != (out.Diagnostic.State == "registered")) {
			return nil, ErrStorage
		}
		switch out.Code {
		case "ok", "config_invalid", "start_failed", "stop_failed", "reload_failed", "diagnostic_failed", "internal_error":
		default:
			return nil, ErrStorage
		}
	}
	return r, nil
}

func (r *Runner) Close() error { return r.process.Stop() }
func (r *Runner) save() error {
	for id, out := range r.state.Completed {
		if time.Since(out.At) > 48*time.Hour {
			delete(r.state.Completed, id)
		}
	}
	if len(r.state.Completed) > 8192 {
		return ErrStorage
	}
	return writeJSON(r.Store, "state.json", r.state)
}

// Run is serial: a receipt is persisted and retried before any next command.
// At-least-once effects reconcile to desired running/config state. The separate
// supervisor reaps frpc on Agent death; unknown supervisor loss fails closed.
func (r *Runner) Run(ctx context.Context) (runErr error) {
	defer func() {
		if err := r.process.Stop(); err != nil {
			runErr = err
		}
	}()
	heartbeat := time.Time{}
	lastError := ""
	report := func(e error) {
		if e == nil {
			lastError = ""
			return
		}
		if e.Error() != lastError && r.Report != nil {
			r.Report(e)
		}
		lastError = e.Error()
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.process.Running()
		if r.process.poisoned {
			return ErrSupervisor
		}
		if time.Since(heartbeat) >= 25*time.Second {
			var sample *RegistrationStatus
			if r.Client.runtimeStatusV1 {
				value := r.registrationSnapshot(ctx)
				if r.Client.upstreamTCPV1 && len(value.ProxyStates) != 0 && r.process.diagnostics != nil {
					value.UpstreamStates = probeUpstreams(ctx, r.process.diagnostics.upstreams, r.Identity.Policy.LocalPorts)
					value.ObservedAt = time.Now().UTC()
				}
				sample = &value
			}
			if e := r.Client.heartbeat(ctx, sample); e != nil {
				report(e)
				if permanent(e) {
					return e
				}
				if !wait(ctx, 3*time.Second) {
					return ctx.Err()
				}
				continue
			}
			heartbeat = time.Now()
			// On a clean restart only restore committed desired state when no
			// unfinished command needs lease reconciliation first.
			if r.state.Pending == nil && r.state.Running && !r.process.Running() {
				if e := r.restore(ctx); errors.Is(e, ErrConfig) {
					// An older saved token can have surrounding whitespace that
					// frpc's file source cannot represent. Stay enrolled so a
					// fresh authenticated deployment can replace that version.
					r.state.Running = false
					if err := r.save(); err != nil {
						return err
					}
					report(e)
				} else if e != nil {
					return e
				}
			}
		}
		e := r.Step(ctx)
		report(e)
		if e != nil {
			if permanent(e) {
				return e
			}
		}
		if !wait(ctx, 2*time.Second) {
			return ctx.Err()
		}
	}
}
func permanent(e error) bool {
	return errors.Is(e, HTTPError(400)) || errors.Is(e, HTTPError(401)) || errors.Is(e, HTTPError(403)) || errors.Is(e, HTTPError(426)) || errors.Is(e, ErrStorage) || errors.Is(e, ErrBinary) || errors.Is(e, ErrProtocol) || errors.Is(e, ErrSupervisor)
}
func wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
func (r *Runner) restore(ctx context.Context) error {
	if len(r.state.Config) == 0 {
		return ErrStorage
	}
	normalized, e := Normalize(r.state.Source, r.Identity.Policy)
	if e != nil || !bytes.Equal(normalized, r.state.Config) {
		return ErrStorage
	}
	if r.process.Running() {
		return r.process.Start(ctx, nil)
	}
	reader, e := r.Store.stageRuntime("active.json", normalized)
	if e != nil {
		return e
	}
	defer reader.Close()
	return r.process.Start(ctx, reader)
}
func (r *Runner) Step(ctx context.Context) error {
	if r.state.Pending == nil {
		if r.state.ClaimID == "" {
			id, e := randomID()
			if e != nil {
				return e
			}
			r.state.ClaimID = id
			if e = r.save(); e != nil {
				return e
			}
		}
		d, e := r.Client.Claim(ctx, r.state.ClaimID)
		if e != nil {
			return e
		}
		if d == nil {
			return nil
		}
		if d.Command.DeviceID != r.Identity.Registration.DeviceID {
			return ErrProtocol
		}
		r.state.Pending = d
		if e = r.save(); e != nil {
			return e
		}
	}
	d := *r.state.Pending
	out, done := r.state.Completed[d.Command.ID]
	if !done {
		if e := r.Client.Renew(ctx, d); e != nil {
			return r.conflict(e)
		}
		code, e := r.execute(ctx, d)
		if e != nil {
			return r.conflict(e)
		}
		out = Outcome{Code: code, At: time.Now().UTC(), Diagnostic: r.lastDiagnostic}
		r.state.Completed[d.Command.ID] = out
		if e = r.save(); e != nil {
			return e
		}
	}
	if e := r.Client.FinishWithReport(ctx, d, out.Code, out.Diagnostic); e != nil {
		return r.conflict(e)
	}
	r.state.Pending = nil
	r.state.ClaimID = ""
	return r.save()
}
func (r *Runner) conflict(e error) error {
	if errors.Is(e, HTTPError(409)) || errors.Is(e, HTTPError(404)) {
		if r.state.Pending != nil {
			if _, completed := r.state.Completed[r.state.Pending.Command.ID]; !completed {
				// An unfinished effect with a lost lease is ambiguous. Fail closed
				// until a fresh claim authorizes reconciliation; never restore an
				// unacknowledged intent just because the pending lease expired.
				r.state.Running = false
				if err := r.process.Stop(); err != nil {
					return err
				}
			}
		}
		r.state.Pending = nil
		r.state.ClaimID = ""
		if err := r.save(); err != nil {
			return err
		}
	}
	return e
}
func (r *Runner) execute(ctx context.Context, d Delivery) (string, error) {
	r.lastDiagnostic = nil
	switch d.Command.Action {
	case "deploy_config":
		cfg, e := r.Client.Configuration(ctx, d)
		if e != nil {
			return "", e
		}
		if digest([]byte(cfg.Text)) != d.Command.SHA256 {
			return "config_invalid", nil
		}
		b, e := Normalize([]byte(cfg.Text), r.Identity.Policy)
		if e != nil {
			return "config_invalid", nil
		}
		reader, e := r.Store.stageRuntime("candidate.json", b)
		if errors.Is(e, ErrConfig) {
			return "config_invalid", nil
		}
		if e != nil {
			return "", e
		}
		e = r.process.Verify(ctx, reader)
		reader.Close()
		if e != nil {
			if errors.Is(e, ErrBinary) || errors.Is(e, ErrStorage) {
				return "", e
			}
			return "config_invalid", nil
		}
		// A fresh server-side lease check immediately precedes side effects.
		if e = r.Client.Renew(ctx, d); e != nil {
			return "", e
		}
		r.state.Config = b
		r.state.Source = []byte(cfg.Text)
		r.state.ConfigID = cfg.ConfigID
		r.state.Running = true
		if e = r.save(); e != nil {
			return "", e
		}
		if e = r.process.Stop(); e != nil {
			if errors.Is(e, ErrSupervisor) {
				return "", e
			}
			return "stop_failed", nil
		}
		if e = r.restore(ctx); e != nil {
			r.state.Running = false
			return processOutcome(e, "start_failed")
		}
		return "ok", nil
	case "start", "reload":
		if len(r.state.Config) == 0 {
			return "config_invalid", nil
		}
		r.state.Running = true
		if e := r.save(); e != nil {
			return "", e
		}
		if d.Command.Action == "reload" {
			if e := r.process.Stop(); e != nil {
				if errors.Is(e, ErrSupervisor) {
					return "", e
				}
				return "reload_failed", nil
			}
		}
		if e := r.restore(ctx); e != nil {
			r.state.Running = false
			return processOutcome(e, d.Command.Action+"_failed")
		}
		return "ok", nil
	case "stop", "forget_config":
		r.state.Running = false
		if e := r.save(); e != nil {
			return "", e
		}
		if e := r.process.Stop(); e != nil {
			if errors.Is(e, ErrSupervisor) {
				return "", e
			}
			return "stop_failed", nil
		}
		if d.Command.Action == "forget_config" {
			// The unfinished command remains in the journal across both
			// writes. A crash cannot restore old intent before reconciliation.
			r.state.Config = nil
			r.state.Source = nil
			r.state.ConfigID = 0
			if e := r.save(); e != nil {
				return "", e
			}
		}
		return "ok", nil
	case "diagnose":
		status := r.registrationSnapshot(ctx)
		if !r.Client.proxyStatusV1 {
			status.ProxyStates = nil
		}
		r.lastDiagnostic = &status
		if status.State != "registered" {
			return "diagnostic_failed", nil
		}
		return "ok", nil
	default:
		return "internal_error", nil
	}
}
func processOutcome(e error, code string) (string, error) {
	if errors.Is(e, ErrStorage) || errors.Is(e, ErrBinary) || errors.Is(e, ErrSupervisor) {
		return "", e
	}
	return code, nil
}

func (r *Runner) registrationSnapshot(ctx context.Context) RegistrationStatus {
	status, err := r.process.Registration(ctx)
	if err != nil {
		status = RegistrationStatus{State: "unavailable"}
	}
	status.ConfigID = r.state.ConfigID
	status.ObservedAt = time.Now().UTC()
	return status
}
