package frpagent

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"cloudenest_go/internal/agentstatus"
)

var ErrDiagnostics = errors.New("local frpc status unavailable")

// Registration describes frpc's registration state, not application health or
// an end-to-end connection to the local upstream. Raw errors/addresses are not
// included: they are not appropriate for remote diagnostic receipts.
type RegistrationStatus = agentstatus.Report
type localDiagnostics struct {
	client            *http.Client
	address, password string
	expected          map[string]string
}

func (d *localDiagnostics) close() { d.client.CloseIdleConnections() }

func prepareDiagnostics(s *Store) (*localDiagnostics, *os.File, error) {
	raw, err := s.Read("active.json", 65536)
	if err != nil {
		return nil, nil, err
	}
	var config map[string]json.RawMessage
	var proxies []proxy
	if json.Unmarshal(raw, &config) != nil || json.Unmarshal(config["proxies"], &proxies) != nil || len(proxies) > 512 {
		return nil, nil, ErrConfig
	}
	expected := make(map[string]string, len(proxies))
	for _, p := range proxies {
		if p.Name == "" || expected[p.Name] != "" {
			return nil, nil, ErrConfig
		}
		expected[p.Name] = p.Type
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, nil, ErrDiagnostics
	}
	port := listener.Addr().(*net.TCPAddr).Port
	// frpc owns its listener. A process winning this race cannot impersonate
	// the freshly pinned TLS certificate, and startup fails rather than using it.
	if err := listener.Close(); err != nil {
		return nil, nil, ErrDiagnostics
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, ErrDiagnostics
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, ErrDiagnostics
	}
	cert := &x509.Certificate{SerialNumber: serial, NotBefore: time.Now().Add(-time.Minute),
		NotAfter: time.Now().AddDate(10, 0, 0), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		return nil, nil, ErrDiagnostics
	}
	encodedKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, ErrDiagnostics
	}
	defer clear(encodedKey)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encodedKey})
	defer clear(keyPEM)
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, nil, ErrDiagnostics
	}
	_, err = writer.Write(keyPEM)
	closeErr := writer.Close()
	if err != nil || closeErr != nil {
		reader.Close()
		return nil, nil, ErrDiagnostics
	}
	ok := false
	defer func() {
		if !ok {
			reader.Close()
		}
	}()
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := s.Write("diagnostics.pem", certPEM, 0600); err != nil {
		return nil, nil, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, nil, ErrDiagnostics
	}
	password := base64.RawURLEncoding.EncodeToString(secret)
	clear(secret)
	web := map[string]any{"addr": "127.0.0.1", "port": port, "user": "cloudnest", "password": password,
		"tls": map[string]string{"certFile": filepath.Join(s.Path, "diagnostics.pem"), "keyFile": "/dev/fd/5"}}
	config["webServer"], err = json.Marshal(web)
	if err != nil {
		return nil, nil, ErrConfig
	}
	body, err := json.Marshal(config)
	if err != nil {
		return nil, nil, ErrConfig
	}
	if err := s.Write("active.json", body, 0600); err != nil {
		return nil, nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certPEM) {
		return nil, nil, ErrDiagnostics
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		DialContext: (&net.Dialer{Timeout: time.Second}).DialContext, TLSHandshakeTimeout: time.Second,
		ResponseHeaderTimeout: time.Second, MaxResponseHeaderBytes: 8192, DisableKeepAlives: true}
	client := &http.Client{Transport: tr, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ok = true
	return &localDiagnostics{client: client, address: "https://127.0.0.1:" + strconv.Itoa(port), password: password, expected: expected}, reader, nil
}
func (d *localDiagnostics) read(ctx context.Context) (RegistrationStatus, error) {
	result := RegistrationStatus{State: "unavailable", Expected: len(d.expected)}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.address+"/api/status", nil)
	if err != nil {
		return result, ErrDiagnostics
	}
	req.SetBasicAuth("cloudnest", d.password)
	res, err := d.client.Do(req)
	if err != nil {
		return result, ErrDiagnostics
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return result, ErrDiagnostics
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, (512<<10)+1))
	if err != nil || len(raw) > 512<<10 {
		return result, ErrDiagnostics
	}
	return decodeRegistration(raw, d.expected)
}
func decodeRegistration(raw []byte, expected map[string]string) (RegistrationStatus, error) {
	result := RegistrationStatus{State: "unavailable", Expected: len(expected)}
	var groups map[string][]struct {
		Name   string `json:"name"`
		Type   string `json:"type"`
		Status string `json:"status"`
		Error  string `json:"err"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if decoder.Decode(&groups) != nil || groups == nil || decoder.Decode(&struct{}{}) != io.EOF {
		return result, ErrDiagnostics
	}
	seen := map[string]bool{}
	for kind, items := range groups {
		if kind != "tcp" && kind != "http" && kind != "https" {
			return result, ErrDiagnostics
		}
		for _, item := range items {
			if seen[item.Name] || expected[item.Name] != kind || item.Type != kind {
				return result, ErrDiagnostics
			}
			seen[item.Name] = true
			switch item.Status {
			case "running":
				if item.Error != "" {
					result.Failed++
				} else {
					result.Registered++
				}
			case "start error", "check failed", "closed":
				result.Failed++
			case "new", "wait start":
				result.Pending++
			default:
				return result, ErrDiagnostics
			}
		}
	}
	result.Pending += len(expected) - len(seen)
	switch {
	case result.Failed > 0:
		result.State = "degraded"
	case result.Pending > 0 || result.Expected == 0:
		result.State = "starting"
	default:
		result.State = "registered"
	}
	return result, nil
}
func (p *Process) Registration(ctx context.Context) (RegistrationStatus, error) {
	running := p.Running()
	if p.poisoned {
		return RegistrationStatus{State: "unavailable"}, ErrSupervisor
	}
	if !running {
		return RegistrationStatus{State: "stopped"}, nil
	}
	if p.diagnostics == nil {
		return RegistrationStatus{State: "unavailable"}, ErrDiagnostics
	}
	return p.diagnostics.read(ctx)
}
