package service

import (
	"errors"
	"strings"
	"testing"

	"cloudenest_go/model"
	"github.com/goccy/go-yaml"
)

func TestFRPConfigQuotesLiteralCredentials(t *testing.T) {
	for _, token := range []string{"true", "1234", "null", "secret #: test", `quote"slash\`, " 密钥 "} {
		text, err := generateFRPCYaml("::1", 7000, token, []model.FrpMapping{{ID: 1, DeviceID: 1, LocalPort: 8080, RemotePort: 9000}})
		if err != nil {
			t.Fatal(err)
		}
		var parsed struct {
			ServerAddr string `yaml:"serverAddr"`
			Auth       struct {
				Token string `yaml:"token"`
			} `yaml:"auth"`
		}
		if err := yaml.Unmarshal([]byte(text), &parsed); err != nil {
			t.Fatal(err)
		}
		if parsed.ServerAddr != "::1" || parsed.Auth.Token != token {
			t.Fatal("scalar changed during YAML round trip")
		}
	}
}

func TestFRPConfigRejectsInjectionAndInvalidBounds(t *testing.T) {
	valid := model.FrpMapping{ID: 1, DeviceID: 1, LocalPort: 8080, RemotePort: 9000}
	for _, tc := range []struct {
		name, host, token, domain string
		port, local, remote       int
	}{
		{name: "host newline", host: "host\nwebServer: {}"},
		{name: "host template", host: "{{ .Envs.HOME }}"},
		{name: "host zone", host: "fe80::1%en0"},
		{name: "host URL", host: "https://example.com"},
		{name: "token newline", token: "secret\nwebServer: {}"},
		{name: "token template", token: "{{ .Envs.HOME }}"},
		{name: "token close template", token: "a }}"},
		{name: "token control", token: "x\x00y"},
		{name: "token UTF8", token: string([]byte{255})},
		{name: "token limit", token: strings.Repeat("x", 1025)},
		{name: "domain YAML", domain: "example.com\n    plugin: {}"},
		{name: "domain template", domain: "https://{{ .Envs.HOME }}"},
		{name: "domain path", domain: "https://example.com/admin"},
		{name: "server port", port: 65536},
		{name: "local port", local: -1},
		{name: "remote port", remote: 65536},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host, port, m := tc.host, tc.port, valid
			if host == "" {
				host = "example.com"
			}
			if port == 0 {
				port = 7000
			}
			m.Domain = tc.domain
			if tc.local != 0 {
				m.LocalPort = tc.local
			}
			if tc.remote != 0 {
				m.RemotePort = tc.remote
			}
			text, err := generateFRPCYaml(host, port, tc.token, []model.FrpMapping{m})
			if !errors.Is(err, ErrFrpBadConfig) || text != "" {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
	for _, count := range []int{0, 512, 513} {
		mappings := make([]model.FrpMapping, count)
		for i := range mappings {
			mappings[i] = valid
			mappings[i].ID = int64(i + 1)
		}
		if _, err := generateFRPCYaml("example.com", 7000, "", mappings); !errors.Is(err, ErrFrpBadConfig) {
			t.Fatal("empty, oversized output, or too many mappings accepted", count)
		}
	}
}
