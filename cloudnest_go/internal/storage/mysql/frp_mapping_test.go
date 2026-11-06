package mysql

import (
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	"errors"
	"testing"
)

func TestMappingCanonicalDomainsAndBounds(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"", ""}, {" HTTP://Example.COM. ", "example.com"}, {"HTTPS://Example.COM.", "https://example.com"}, {"*.Example.COM.", "*.example.com"},
	} {
		m := model.FrpMapping{DeviceID: 1, LocalPort: 8080, RemotePort: 20000, Domain: tc.input, Status: 0}
		if err := normalizeMapping(&m); err != nil || m.Domain != tc.want || m.Status != 0 {
			t.Fatalf("%q => %q: %v", tc.input, m.Domain, err)
		}
	}
	for _, input := range []string{"http://", "https://", "example.com/path", "x:80", "a@b", "a..b", "-x.test", "x-.test", "{{x}}", "x\ny.test", "https://http://x.test", "é.test", "*."} {
		m := model.FrpMapping{DeviceID: 1, LocalPort: 8080, RemotePort: 20000, Domain: input, Status: 1}
		if err := normalizeMapping(&m); !errors.Is(err, contract.ErrInvalidInput) {
			t.Fatalf("accepted %q", input)
		}
	}
}
