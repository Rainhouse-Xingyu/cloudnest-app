package frpreservation

import (
	"cloudenest_go/internal/frpwire"
	"sort"
	"strconv"
	"strings"
)

type Key struct{ Kind, Resource string }

// Unknown input remains a claim; invalid config/lease expiry is not evidence
// that no older version is running. Only an explicit forget can retire a claim.
func Keys(raw string) []Key {
	unknown := []Key{{Kind: "unknown"}}
	config, err := frpwire.Decode([]byte(raw))
	if err != nil || len(config.Proxies) > 512 {
		return unknown
	}
	keys := map[Key]bool{}
	for _, proxy := range config.Proxies {
		switch proxy.Type {
		case "tcp":
			if proxy.RemotePort < 1 || proxy.RemotePort > 65535 || len(proxy.CustomDomains) != 0 {
				return unknown
			}
			keys[Key{"tcp", strconv.Itoa(proxy.RemotePort)}] = true
		case "http", "https":
			if proxy.RemotePort != 0 || len(proxy.CustomDomains) == 0 || len(proxy.CustomDomains) > 16 {
				return unknown
			}
			for _, domain := range proxy.CustomDomains {
				domain = strings.TrimSuffix(strings.ToLower(domain), ".")
				if !validDomain(domain) {
					return unknown
				}
				keys[Key{proxy.Type, domain}] = true
			}
		default:
			return unknown
		}
	}
	out := make([]Key, 0, len(keys))
	for k := range keys {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Resource < out[j].Resource
	})
	return out
}
func validDomain(domain string) bool {
	if len(domain) == 0 || len(domain) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.TrimPrefix(domain, "*."), ".") {
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
