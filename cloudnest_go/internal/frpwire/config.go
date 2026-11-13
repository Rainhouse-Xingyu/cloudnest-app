// Package frpwire shares the bounded configuration decoder between the Agent
// and control-plane resource accounting. Neither path evaluates FRP templates.
package frpwire

import (
	"bytes"
	"errors"
	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/lexer"
	"github.com/goccy/go-yaml/token"
	"io"
	"unicode/utf8"
)

var ErrInvalid = errors.New("invalid FRP wire configuration")

type Proxy struct {
	Name          string   `yaml:"name" json:"name"`
	Type          string   `yaml:"type" json:"type"`
	LocalIP       string   `yaml:"localIP" json:"localIP"`
	LocalPort     int      `yaml:"localPort" json:"localPort"`
	RemotePort    int      `yaml:"remotePort" json:"remotePort,omitempty"`
	CustomDomains []string `yaml:"customDomains" json:"customDomains,omitempty"`
}
type Config struct {
	ServerAddr string `yaml:"serverAddr" json:"serverAddr"`
	ServerPort int    `yaml:"serverPort" json:"serverPort"`
	Auth       struct {
		Token string `yaml:"token" json:"token"`
	} `yaml:"auth" json:"auth"`
	Proxies []Proxy `yaml:"proxies" json:"proxies"`
}

func Decode(raw []byte) (Config, error) {
	var config Config
	if len(raw) == 0 || len(raw) > 32768 || !utf8.Valid(raw) || bytes.Contains(raw, []byte("{{")) || bytes.Contains(raw, []byte("}}")) {
		return config, ErrInvalid
	}
	for _, t := range lexer.Tokenize(string(raw)) {
		switch t.Type {
		case token.AnchorType, token.AliasType, token.TagType, token.DirectiveType:
			return config, ErrInvalid
		}
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw), yaml.Strict())
	if decoder.Decode(&config) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return Config{}, ErrInvalid
	}
	return config, nil
}
