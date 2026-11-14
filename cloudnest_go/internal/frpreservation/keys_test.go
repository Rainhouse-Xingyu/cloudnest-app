package frpreservation

import (
	"reflect"
	"testing"
)

func TestResourceKeysCanonicalizeAndRetainUnknownInput(t *testing.T) {
	raw := `serverAddr: localhost
serverPort: 7000
proxies:
 - name: tcp
   type: tcp
   localPort: 8080
   remotePort: 20000
 - name: http
   type: http
   localPort: 8080
   customDomains: [Example.COM., example.com, '*.Example.COM']
 - name: tls
   type: https
   localPort: 8443
   customDomains: [example.com]
`
	want := []Key{{"http", "*.example.com"}, {"http", "example.com"}, {"https", "example.com"}, {"tcp", "20000"}}
	if got := Keys(raw); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	for _, bad := range []string{"not a configuration", raw + "includes: [/etc/*]\n", `proxies: [{type: udp, remotePort: 20000}]`, `proxies: [{type: tcp, remotePort: 0}]`, `proxies: [{type: http, customDomains: [bad/name]}]`, `proxies: &p []`, "{{ .Envs.CONFIG }}"} {
		if got := Keys(bad); !reflect.DeepEqual(got, []Key{{Kind: "unknown"}}) {
			t.Fatal("unknown resources silently dropped", got)
		}
	}
	if got := Keys("serverAddr: localhost\nserverPort: 7000\n"); len(got) != 0 {
		t.Fatal("no-proxy config invented listeners", got)
	}
}
