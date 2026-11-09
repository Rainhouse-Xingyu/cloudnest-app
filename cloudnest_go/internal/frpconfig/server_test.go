package frpconfig

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestServerPolicyNormalizationAndRejection(t *testing.T) {
	base := ServerSpec{Name: " test ", Address: " ExAmPle.COM. ", Token: "secret #: punctuation"}
	got, err := NormalizeServer(base)
	if err != nil || got.Name != "test" || got.Address != "example.com" || got.Port != 7000 || got.TCPStart != 20000 || got.TCPEnd != 20999 {
		t.Fatal("normalization", got, err)
	}
	for _, entry := range []struct{ host, want string }{{"::ffff:127.0.0.1", "127.0.0.1"}, {"2001:0db8::1", "2001:db8::1"}} {
		p := base
		p.Address = entry.host
		s, e := NormalizeServer(p)
		if e != nil || s.Address != entry.want {
			t.Fatal("IP normalization", s, e)
		}
	}
	cases := []func(*ServerSpec){
		func(s *ServerSpec) { s.Name = "" }, func(s *ServerSpec) { s.Name = strings.Repeat("界", 101) },
		func(s *ServerSpec) { s.Name = "a\x00b" }, func(s *ServerSpec) { s.Address = "https://example.com" },
		func(s *ServerSpec) { s.Address = "host:7000" }, func(s *ServerSpec) { s.Address = "fe80::1%en0" },
		func(s *ServerSpec) { s.Address = "example.com.." }, func(s *ServerSpec) { s.Address = "-bad.example" },
		func(s *ServerSpec) { s.Token = " x " }, func(s *ServerSpec) { s.Token = "{{ .Envs.HOME }}" },
		func(s *ServerSpec) { s.Token = "line\nbreak" }, func(s *ServerSpec) { s.Token = strings.Repeat("x", 1025) },
		func(s *ServerSpec) { s.Port = -1 }, func(s *ServerSpec) { s.Port = 20000 },
		func(s *ServerSpec) { s.TCPStart = 21000; s.TCPEnd = 20000 }, func(s *ServerSpec) { s.TCPEnd = 65536 },
		func(s *ServerSpec) { s.HTTPPort = 7000 }, func(s *ServerSpec) { s.HTTPPort = 80; s.HTTPSPort = 80 },
		func(s *ServerSpec) { s.HTTPSPort = 20001 }, func(s *ServerSpec) { s.HTTPPort = -1 },
	}
	for i, change := range cases {
		s := base
		change(&s)
		if _, e := NormalizeServer(s); e == nil {
			t.Fatalf("accepted invalid case %d", i)
		}
	}
}

func TestServerEnvelopeAndManagedConfigAuthenticateAllAssociations(t *testing.T) {
	k, err := parse(testDocument(t))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := k.SealServer(1, 2, 3, "private server")
	if err != nil {
		t.Fatal(err)
	}
	second, _ := k.SealServer(1, 2, 3, "private server")
	if second == sealed || strings.Contains(sealed, "private server") {
		t.Fatal("nonce or plaintext")
	}
	if got, e := k.OpenServer(1, 2, 3, sealed); e != nil || got != "private server" {
		t.Fatal("roundtrip", e)
	}
	for _, ids := range [][3]int64{{2, 2, 3}, {1, 3, 3}, {1, 2, 4}, {0, 2, 3}} {
		if _, e := k.OpenServer(ids[0], ids[1], ids[2], sealed); e == nil {
			t.Fatal("server association swapped")
		}
	}
	raw, _ := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(sealed, serverPrefix+"one:"))
	for i := range raw {
		changed := bytes.Clone(raw)
		changed[i] ^= 1
		if _, e := k.OpenServer(1, 2, 3, serverPrefix+"one:"+base64.RawStdEncoding.EncodeToString(changed)); !errors.Is(e, ErrInvalid) {
			t.Fatal("tampering", i)
		}
	}
	for _, bad := range []string{"legacy", sealed + "=", strings.Replace(sealed, "cnfrps:1:", "cnfrps:2:", 1)} {
		if _, e := k.OpenServer(1, 2, 3, bad); e == nil {
			t.Fatal("invalid server envelope")
		}
	}
	managed, err := k.SealConfig(1, 2, 3, 4, "frpc config")
	if err != nil {
		t.Fatal(err)
	}
	if got, e := k.OpenConfig(1, 2, 3, 4, managed); e != nil || got != "frpc config" {
		t.Fatal("managed roundtrip", e)
	}
	for _, ids := range [][4]int64{{2, 2, 3, 4}, {1, 3, 3, 4}, {1, 2, 4, 4}, {1, 2, 3, 5}, {1, 2, 0, 0}, {1, 2, 0, 4}} {
		if _, e := k.OpenConfig(ids[0], ids[1], ids[2], ids[3], managed); e == nil {
			t.Fatal("managed association swapped")
		}
	}
	legacy, _ := k.Seal(1, 2, "legacy sealed config")
	if _, e := k.OpenConfig(1, 2, 3, 4, legacy); e == nil {
		t.Fatal("invented legacy association")
	}
	if _, e := k.OpenServer(1, 2, 3, managed); e == nil {
		t.Fatal("cross namespace")
	}
	if _, e := k.OpenConfig(1, 2, 3, 4, sealed); e == nil {
		t.Fatal("cross namespace")
	}
	if !k.IsCurrent(managed) || !IsEnvelope(managed) || !k.IsCurrentServer(sealed) {
		t.Fatal("rotation classification")
	}
	var absent *Keyring
	if _, e := absent.OpenServer(1, 2, 3, sealed); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	if _, e := absent.SealConfig(1, 2, 3, 4, "x"); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
}
