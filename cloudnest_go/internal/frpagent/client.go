package frpagent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var ErrProtocol = errors.New("invalid agent protocol response")
var ErrNetwork = errors.New("agent HTTPS request failed")
var ErrLease = errors.New("command lease too close to expiry")

type HTTPError int

func (e HTTPError) Error() string { return fmt.Sprintf("agent service returned HTTP %d", int(e)) }

type Client struct {
	base, credential string
	http             *http.Client
	proxyStatusV1    bool
	runtimeStatusV1  bool
}
type Registration struct {
	DeviceID   int64  `json:"device_id"`
	Credential string `json:"credential"`
}
type Command struct {
	ID           string    `json:"command_id"`
	DeviceID     int64     `json:"device_id"`
	Action       string    `json:"action"`
	ConfigID     int64     `json:"config_id"`
	SHA256       string    `json:"config_sha256"`
	Status       string    `json:"status"`
	LeaseExpires time.Time `json:"lease_expires_at"`
}
type Delivery struct {
	Command Command `json:"command"`
	LeaseID string  `json:"lease_id"`
}
type Configuration struct {
	ConfigID int64  `json:"config_id"`
	Text     string `json:"config_text"`
	SHA256   string `json:"sha256"`
}

func tokenOK(s, prefix string) bool {
	if len(s) != 81 || !strings.HasPrefix(s, prefix) || !validID(s[5:37]) || s[37] != '.' {
		return false
	}
	b, e := base64.RawURLEncoding.Strict().DecodeString(s[38:])
	return e == nil && len(b) == 32
}

// NewClient uses system trust or the explicitly supplied private CA bundle.
// Redirects, ambient HTTP proxies, cookies and insecure TLS are never enabled.
func NewClient(endpoint, credential string, ca []byte) (*Client, error) {
	u, e := url.Parse(endpoint)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Opaque != "" {
		return nil, ErrProtocol
	}
	if credential != "" && !tokenOK(credential, "cna1.") {
		return nil, ErrProtocol
	}
	var roots *x509.CertPool
	if len(ca) > 0 {
		roots = x509.NewCertPool()
		if len(ca) > 1<<20 || !roots.AppendCertsFromPEM(ca) {
			return nil, ErrProtocol
		}
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 16384, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second}
	return &Client{base: strings.TrimSuffix(endpoint, "/"), credential: credential, http: &http.Client{Transport: tr, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Close() { c.http.CloseIdleConnections() }
func (c *Client) post(ctx context.Context, path string, in, out any, want int) (int, error) {
	b, e := json.Marshal(in)
	if e != nil {
		return 0, ErrProtocol
	}
	r, e := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(b))
	if e != nil {
		return 0, ErrProtocol
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Cache-Control", "no-store")
	if c.credential != "" {
		r.Header.Set("Authorization", "Bearer "+c.credential)
	}
	resp, e := c.http.Do(r)
	if e != nil {
		return 0, ErrNetwork
	}
	defer resp.Body.Close()
	if resp.StatusCode == 204 && want == 200 {
		return 204, nil
	}
	if resp.StatusCode != want {
		return resp.StatusCode, HTTPError(resp.StatusCode)
	}
	if out == nil {
		return resp.StatusCode, nil
	}
	raw, e := io.ReadAll(io.LimitReader(resp.Body, (256<<10)+1))
	if e != nil || len(raw) > 256<<10 {
		return 0, ErrProtocol
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if d.Decode(out) != nil || d.Decode(&struct{}{}) != io.EOF {
		return 0, ErrProtocol
	}
	return resp.StatusCode, nil
}
func (c *Client) Enroll(ctx context.Context, enrollment string) (Registration, error) {
	var out Registration
	if !tokenOK(enrollment, "cne1.") || c.credential != "" {
		return out, ErrProtocol
	}
	_, e := c.post(ctx, "/api/agent/enroll", map[string]string{"enrollment": enrollment}, &out, 201)
	if e == nil && (out.DeviceID <= 0 || !tokenOK(out.Credential, "cna1.")) {
		e = ErrProtocol
	}
	return out, e
}
func (c *Client) Heartbeat(ctx context.Context) error {
	return c.heartbeat(ctx, nil)
}

func (c *Client) heartbeat(ctx context.Context, report *RegistrationStatus) error {
	var out struct {
		DeviceID        int64 `json:"device_id"`
		ProxyStatusV1   bool  `json:"proxy_status_v1"`
		RuntimeStatusV1 bool  `json:"runtime_status_v1"`
	}
	body := map[string]any{"client_version": "cloudnest-agent-0.3.0"}
	if c.runtimeStatusV1 && report != nil {
		body["runtime"] = report
	}
	status, e := c.post(ctx, "/api/agent/heartbeat", body, &out, 200)
	if report != nil && body["runtime"] != nil && status == 400 {
		// An older backend may serve this request during a rollback.
		delete(body, "runtime")
		status, e = c.post(ctx, "/api/agent/heartbeat", body, &out, 200)
	}
	if e == nil {
		if status != 200 || out.DeviceID <= 0 {
			return ErrProtocol
		}
		c.proxyStatusV1 = out.ProxyStatusV1
		c.runtimeStatusV1 = out.RuntimeStatusV1
	}
	return e
}
func (c *Client) Claim(ctx context.Context, id string) (*Delivery, error) {
	if !validID(id) {
		return nil, ErrProtocol
	}
	var out Delivery
	status, e := c.post(ctx, "/api/agent/commands/claim", map[string]string{"claim_id": id}, &out, 200)
	if e != nil {
		return nil, e
	}
	if status == 204 {
		return nil, nil
	}
	v := out.Command
	if !validID(v.ID) || !validID(out.LeaseID) || v.DeviceID <= 0 || v.Status != "leased" || v.LeaseExpires.IsZero() {
		return nil, ErrProtocol
	}
	switch v.Action {
	case "deploy_config":
		b, e := hex.DecodeString(v.SHA256)
		if e != nil || len(b) != 32 || strings.ToLower(v.SHA256) != v.SHA256 || v.ConfigID <= 0 {
			return nil, ErrProtocol
		}
	case "start", "stop", "reload", "diagnose", "forget_config":
	default:
		return nil, ErrProtocol
	}
	return &out, nil
}
func (c *Client) lease(ctx context.Context, d Delivery, suffix string, out any) (int, error) {
	if !validID(d.Command.ID) || !validID(d.LeaseID) {
		return 0, ErrProtocol
	}
	return c.post(ctx, "/api/agent/commands/"+d.Command.ID+suffix, map[string]string{"lease_id": d.LeaseID}, out, 200)
}
func (c *Client) Renew(ctx context.Context, d Delivery) error {
	var out struct {
		Until time.Time `json:"lease_expires_at"`
	}
	status, e := c.lease(ctx, d, "/renew", &out)
	if e == nil && (status != 200 || out.Until.IsZero()) {
		return ErrProtocol
	}
	if e == nil && time.Until(out.Until) < 10*time.Second {
		return ErrLease
	}
	return e
}
func (c *Client) Configuration(ctx context.Context, d Delivery) (Configuration, error) {
	var out Configuration
	status, e := c.lease(ctx, d, "/config", &out)
	if e == nil && (status != 200 || out.ConfigID != d.Command.ConfigID || out.SHA256 != d.Command.SHA256 || len(out.Text) > 32768 || len(out.Text) == 0) {
		e = ErrProtocol
	}
	return out, e
}
func (c *Client) Finish(ctx context.Context, d Delivery, code string) error {
	return c.FinishWithReport(ctx, d, code, nil)
}
func (c *Client) FinishWithReport(ctx context.Context, d Delivery, code string, report *RegistrationStatus) error {
	if !validID(d.Command.ID) || !validID(d.LeaseID) || (report != nil && report.Validate() != nil) {
		return ErrProtocol
	}
	body := map[string]any{"lease_id": d.LeaseID, "success": code == "ok", "code": code}
	if report != nil {
		body["diagnostic"] = report
	}
	_, err := c.post(ctx, "/api/agent/commands/"+d.Command.ID+"/result", body, nil, 204)
	return err
}
