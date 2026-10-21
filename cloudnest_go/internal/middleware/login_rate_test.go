package middleware

import (
	"testing"
)

func TestLoginPeerCanonicalization(t *testing.T) {
	for _, pair := range [][2]string{
		{"192.0.2.1:1", "192.0.2.1:65535"},
		{"192.0.2.1:1234", "[::ffff:192.0.2.1]:80"},
		{"[2001:db8::1]:1234", "[2001:0db8:0:0:0:0:0:1]:80"},
	} {
		first, ok1 := loginPeerKey(pair[0])
		second, ok2 := loginPeerKey(pair[1])
		if !ok1 || !ok2 || first != second {
			t.Errorf("equivalent peers have different budgets: %v", pair)
		}
	}
	for _, remote := range []string{"", "192.0.2.1", "client.example:80", "not-an-ip:80"} {
		if _, ok := loginPeerKey(remote); ok {
			t.Errorf("accepted invalid peer %q", remote)
		}
	}
	first, _ := loginPeerKey("192.0.2.1:80")
	second, _ := loginPeerKey("192.0.2.2:80")
	if first == second {
		t.Fatal("different IPs share a budget")
	}
}
