//go:build integration && darwin

package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"cloudenest_go/model"
)

// Independent native suites share a disposable Redis but all real URLSession
// connections physically originate at 127.0.0.1. Assign the same fixture-scoped
// peer identity already used by in-process fixture requests, before dispatch to
// the unchanged production middleware. TLS, body IO, deadlines and cancellation
// remain real. This is test isolation, not a production ingress-rate test.
func newNativeTLSServer(f *fixture, next http.Handler) *httptest.Server {
	peer := f.remoteAddr
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		ip := net.ParseIP(host)
		if err != nil || ip == nil || !ip.IsLoopback() {
			http.Error(w, "native fixture requires loopback", 403)
			return
		}
		request := r.Clone(r.Context())
		request.RemoteAddr = peer
		next.ServeHTTP(w, request)
	}))
}

func TestMacOSNativePeerBudgetsStayIndependent(t *testing.T) {
	first, second := newFixture(t), newFixture(t)
	a, b := newNativeTLSServer(first, first.router), newNativeTLSServer(second, second.router)
	defer a.Close()
	defer b.Close()
	login := func(f *fixture, server *httptest.Server, want int) {
		t.Helper()
		var user model.User
		if err := f.db.First(&user, f.aliceID).Error; err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(map[string]string{"username": user.Username, "password": "disposable-fixture-password"})
		if err != nil {
			t.Fatal(err)
		}
		request, _ := http.NewRequest("POST", server.URL+"/api/auth/login", bytes.NewReader(data))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Forwarded-For", "198.51.100.254")
		request.Header.Set("X-Real-IP", "198.51.100.253")
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		if response.StatusCode != want {
			t.Fatalf("fixture peer login: got %d want %d", response.StatusCode, want)
		}
		if want == 429 {
			seconds, err := strconv.Atoi(response.Header.Get("Retry-After"))
			if err != nil || seconds < 1 || seconds > 60 {
				t.Fatal("missing production rate receipt")
			}
		}
	}
	// Each fixture has exactly two seed logins. The next 28 consume the real
	// production 30/minute limit; unrelated fixture traffic cannot reset it.
	for i := 0; i < 28; i++ {
		login(first, a, 200)
	}
	login(first, a, 429)
	login(second, b, 200)
	login(first, a, 429)
}
