// Package frpagent implements the bounded device-side FRP executor. It never
// executes commands, paths or plugins supplied by the control plane.
package frpagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/lexer"
	"github.com/goccy/go-yaml/token"
)

var ErrConfig = errors.New("configuration rejected by local policy")

// Policy is local operator authorization, not remotely writable configuration.
// Public FRP servers require an operator-provided CA. Loopback test servers may
// use FRP's ephemeral certificate. Upstreams are always literal loopback IPs.
type Policy struct {
	ServerAddr    string `json:"server_addr"`
	ServerPort    int    `json:"server_port"`
	LocalPorts    []int  `json:"local_ports"`
	TrustedCAFile string `json:"trusted_ca_file,omitempty"`
	ServerName    string `json:"server_name,omitempty"`
}

type proxy struct {
	Name          string   `yaml:"name" json:"name"`
	Type          string   `yaml:"type" json:"type"`
	LocalIP       string   `yaml:"localIP" json:"localIP"`
	LocalPort     int      `yaml:"localPort" json:"localPort"`
	RemotePort    int      `yaml:"remotePort" json:"remotePort,omitempty"`
	CustomDomains []string `yaml:"customDomains" json:"customDomains,omitempty"`
}
type inputConfig struct {
	ServerAddr string `yaml:"serverAddr" json:"serverAddr"`
	ServerPort int    `yaml:"serverPort" json:"serverPort"`
	Auth       struct {
		Token string `yaml:"token" json:"token"`
	} `yaml:"auth" json:"auth"`
	Proxies []proxy `yaml:"proxies" json:"proxies"`
}

func port(p int) bool { return p > 0 && p <= 65535 }
func safeString(s string, max int) bool {
	if len(s) > max || !utf8.ValidString(s) || strings.Contains(s, "{{") || strings.Contains(s, "}}") {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func host(s string) bool {
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Zone() == "" && !a.IsUnspecified() && !a.IsMulticast()
	}
	if len(s) == 0 || len(s) > 253 {
		return false
	}
	for _, part := range strings.Split(s, ".") {
		if len(part) == 0 || len(part) > 63 || part[0] == '-' || part[len(part)-1] == '-' {
			return false
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
func (p Policy) Validate() error {
	if !host(p.ServerAddr) || !port(p.ServerPort) || len(p.LocalPorts) == 0 || len(p.LocalPorts) > 512 {
		return ErrConfig
	}
	for _, n := range p.LocalPorts {
		if !port(n) {
			return ErrConfig
		}
	}
	a, _ := netip.ParseAddr(p.ServerAddr)
	if !a.IsLoopback() && p.TrustedCAFile == "" {
		return ErrConfig
	}
	if p.ServerName != "" && !host(p.ServerName) {
		return ErrConfig
	}
	if !safeString(p.TrustedCAFile, 4096) {
		return ErrConfig
	}
	return nil
}

// Normalize deliberately does not pass the received YAML to frpc. Strictly
// decoded values are validated and encoded as a new JSON document, preventing
// parser differences, template evaluation, includes, plugins and file loaders.
func Normalize(raw []byte, p Policy) ([]byte, error) {
	if p.Validate() != nil || len(raw) == 0 || len(raw) > 32768 || !utf8.Valid(raw) || bytes.Contains(raw, []byte("{{")) || bytes.Contains(raw, []byte("}}")) {
		return nil, ErrConfig
	}
	var c inputConfig
	for _, t := range lexer.Tokenize(string(raw)) {
		switch t.Type {
		case token.AnchorType, token.AliasType, token.TagType, token.DirectiveType:
			return nil, ErrConfig
		}
	}
	d := yaml.NewDecoder(bytes.NewReader(raw), yaml.Strict())
	if d.Decode(&c) != nil || d.Decode(&struct{}{}) != io.EOF {
		return nil, ErrConfig
	}
	if c.ServerAddr != p.ServerAddr || c.ServerPort != p.ServerPort || !safeString(c.Auth.Token, 1024) || len(c.Proxies) > 512 {
		return nil, ErrConfig
	}
	names, ports := map[string]bool{}, map[int]bool{}
	for _, n := range p.LocalPorts {
		ports[n] = true
	}
	for i := range c.Proxies {
		v := &c.Proxies[i]
		if len(v.Name) == 0 || !safeString(v.Name, 128) || names[v.Name] || !ports[v.LocalPort] {
			return nil, ErrConfig
		}
		for _, r := range v.Name {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
				return nil, ErrConfig
			}
		}
		names[v.Name] = true
		if v.LocalIP == "" {
			v.LocalIP = "127.0.0.1"
		}
		ip, err := netip.ParseAddr(v.LocalIP)
		if err != nil || !ip.IsLoopback() || ip.Zone() != "" {
			return nil, ErrConfig
		}
		switch v.Type {
		case "tcp":
			if !port(v.RemotePort) || len(v.CustomDomains) != 0 {
				return nil, ErrConfig
			}
		case "http", "https":
			if v.RemotePort != 0 || len(v.CustomDomains) == 0 || len(v.CustomDomains) > 16 {
				return nil, ErrConfig
			}
			for _, domain := range v.CustomDomains {
				if !host(strings.TrimPrefix(domain, "*.")) {
					return nil, ErrConfig
				}
			}
		default:
			return nil, ErrConfig
		}
	}
	// No admin endpoint or log file. Automatic reconnect stays in official frpc.
	out := struct {
		inputConfig
		LoginFailExit bool              `json:"loginFailExit"`
		Transport     map[string]any    `json:"transport"`
		Log           map[string]string `json:"log"`
	}{c, false, map[string]any{"tls": map[string]any{"enable": true, "trustedCaFile": p.TrustedCAFile, "serverName": p.ServerName}}, map[string]string{"to": "console", "level": "error"}}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, ErrConfig
	}
	return b, nil
}
