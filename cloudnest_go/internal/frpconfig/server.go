package frpconfig

import (
	"encoding/base64"
	"fmt"
	"net/netip"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ServerSpec is a private decrypted revision. Never serialize it in responses.
// Public metadata must be constructed explicitly without Token.
type ServerSpec struct {
	Name      string `json:"name"`
	Address   string `json:"server_addr"`
	Port      int    `json:"server_port"`
	Token     string `json:"token"`
	TCPStart  int    `json:"tcp_start"`
	TCPEnd    int    `json:"tcp_end"`
	HTTPPort  int    `json:"http_port"`
	HTTPSPort int    `json:"https_port"`
}

func NormalizeServer(spec ServerSpec) (ServerSpec, error) {
	spec.Name = strings.TrimSpace(spec.Name)
	spec.Address = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(spec.Address), "."))
	if ip, err := netip.ParseAddr(spec.Address); err == nil && ip.Zone() == "" {
		spec.Address = ip.Unmap().String()
	}
	if spec.Port == 0 {
		spec.Port = 7000
	}
	if spec.TCPStart == 0 && spec.TCPEnd == 0 {
		spec.TCPStart = 20000
		spec.TCPEnd = 20999
	}
	if spec.Name == "" || utf8.RuneCountInString(spec.Name) > 100 || !serverScalar(spec.Name, 400) || !serverHost(spec.Address) || spec.Port < 1 || spec.Port > 65535 || spec.TCPStart < 1 || spec.TCPEnd > 65535 || spec.TCPStart > spec.TCPEnd || !serverScalar(spec.Token, 1024) || strings.TrimSpace(spec.Token) != spec.Token {
		return ServerSpec{}, ErrInvalid
	}
	used := map[int]bool{}
	for _, port := range []int{spec.Port, spec.HTTPPort, spec.HTTPSPort} {
		if port < 0 || port > 65535 {
			return ServerSpec{}, ErrInvalid
		}
		if port == 0 {
			continue
		}
		if used[port] || port >= spec.TCPStart && port <= spec.TCPEnd {
			return ServerSpec{}, ErrInvalid
		}
		used[port] = true
	}
	return spec, nil
}
func serverScalar(s string, limit int) bool {
	return len(s) <= limit && utf8.ValidString(s) && !strings.Contains(s, "{{") && !strings.Contains(s, "}}") && strings.IndexFunc(s, unicode.IsControl) < 0
}
func serverHost(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Zone() == "" && ip.String() == host
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

const serverPrefix = "cnfrps:1:"

func serverAAD(owner, server, revision int64, key string) []byte {
	return []byte(fmt.Sprintf("cloudnest/frp-server/v1/%d/%d/%d/%s", owner, server, revision, key))
}
func (k *Keyring) SealServer(owner, server, revision int64, plain string) (string, error) {
	if k == nil || k.keys[k.active] == nil {
		return "", ErrUnavailable
	}
	if owner <= 0 || server <= 0 || revision <= 0 || len(plain) == 0 || len(plain) > MaxPlaintext {
		return "", ErrInvalid
	}
	raw := k.keys[k.active].Seal(nil, nil, []byte(plain), serverAAD(owner, server, revision, k.active))
	return serverPrefix + k.active + ":" + base64.RawStdEncoding.EncodeToString(raw), nil
}
func (k *Keyring) OpenServer(owner, server, revision int64, sealed string) (string, error) {
	if k == nil {
		return "", ErrUnavailable
	}
	if owner <= 0 || server <= 0 || revision <= 0 || len(sealed) > 45000 || !strings.HasPrefix(sealed, serverPrefix) {
		return "", ErrInvalid
	}
	parts := strings.Split(strings.TrimPrefix(sealed, serverPrefix), ":")
	if len(parts) != 2 || !validID(parts[0]) {
		return "", ErrInvalid
	}
	cipher := k.keys[parts[0]]
	if cipher == nil {
		return "", ErrUnavailable
	}
	raw, err := base64.RawStdEncoding.Strict().DecodeString(parts[1])
	if err != nil || base64.RawStdEncoding.EncodeToString(raw) != parts[1] {
		return "", ErrInvalid
	}
	plain, err := cipher.Open(nil, nil, raw, serverAAD(owner, server, revision, parts[0]))
	if err != nil || len(plain) == 0 || len(plain) > MaxPlaintext {
		return "", ErrInvalid
	}
	return string(plain), nil
}
func (k *Keyring) IsCurrentServer(text string) bool {
	return k != nil && strings.HasPrefix(text, serverPrefix+k.active+":")
}
