//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"cloudenest_go/internal/app"
	"cloudenest_go/model"
)

func TestLoginRateLimitSharedAndUnspoofable(t *testing.T) {
	f := newFixture(t)
	second := app.NewContainer(f.cfg, f.db, f.rdb).NewRouter()
	// One real peer, across two server instances and arbitrary forwarding headers.
	// Two successful fixture logins already used two of its 30 attempts.
	results := make(chan *httptest.ResponseRecorder, 40)
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest("POST", "/api/auth/login", bytes.NewBufferString(`{"username":"missing-rate-test-user","password":"wrong"}`))
			req.RemoteAddr = f.remoteAddr
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Forwarded-For", "198.51.100."+strconv.Itoa(i+1))
			req.Header.Set("X-Real-IP", "203.0.113."+strconv.Itoa(i+1))
			req.Header.Set("Forwarded", "for=198.51.100."+strconv.Itoa(i+1))
			w := httptest.NewRecorder()
			if i%2 == 0 {
				f.router.ServeHTTP(w, req)
			} else {
				second.ServeHTTP(w, req)
			}
			results <- w
		}(i)
	}
	wg.Wait()
	close(results)
	counts := map[int]int{}
	for w := range results {
		counts[w.Code]++
		if w.Code == http.StatusTooManyRequests {
			retry, err := strconv.Atoi(w.Header().Get("Retry-After"))
			if err != nil || retry < 1 || retry > 60 {
				t.Fatalf("invalid Retry-After: %q", w.Header().Get("Retry-After"))
			}
		}
	}
	if counts[401] != 28 || counts[429] != 12 {
		t.Fatalf("parallel login statuses: %v; want 28 unauthorized and 12 throttled", counts)
	}
	// Authentication throttle never disables an already authenticated session.
	f.json(t, "GET", "/api/auth/me", f.alice, nil, http.StatusOK)
	f.json(t, "POST", "/api/auth/logout", f.alice, nil, http.StatusOK)
	// An independent direct peer has a separate budget.
	f.remoteAddr = "[2001:db8:2::1]:12345"
	f.json(t, "POST", "/api/auth/login", "", map[string]string{"username": "missing-rate-test-user", "password": "wrong"}, http.StatusUnauthorized)
}

func TestLoginRateLimitUnavailable(t *testing.T) {
	f := newFixture(t)
	if err := f.rdb.Close(); err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", "/api/auth/login", "", map[string]string{"username": "missing-rate-test-user", "password": "wrong"}, http.StatusServiceUnavailable)
}

// Shorten only this disposable Redis key's TTL to exercise real expiry without
// making the suite wait a full minute. The application still uses its 60s policy.
func TestLoginRateLimitWindowRecovery(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	host, _, err := net.SplitHostPort(f.remoteAddr)
	if err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf("auth:login-rate:%x", sha256.Sum256([]byte(host)))
	ttl, err := f.rdb.PTTL(ctx, key).Result()
	if err != nil || ttl <= 0 || ttl > time.Minute {
		t.Fatalf("initial rate key TTL: %v, %v", ttl, err)
	}
	// Malformed bodies must consume the budget, without doing password work.
	for range 28 {
		f.request(t, "POST", "/api/auth/login", "", "application/json", bytes.NewBufferString("{"), http.StatusBadRequest)
	}
	if err := f.rdb.PExpire(ctx, key, 300*time.Millisecond).Err(); err != nil {
		t.Fatal(err)
	}
	w := f.json(t, "POST", "/api/auth/login", "", map[string]string{"username": "anything", "password": "anything"}, http.StatusTooManyRequests)
	if w.Header().Get("Retry-After") != "1" {
		t.Fatal("sub-second cooldown must round up")
	}
	ttl, err = f.rdb.PTTL(ctx, key).Result()
	if err != nil || ttl > 300*time.Millisecond {
		t.Fatalf("denied request extended cooldown: %v, %v", ttl, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		exists, err := f.rdb.Exists(ctx, key).Result()
		if err != nil {
			t.Fatal(err)
		}
		if exists == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("rate key did not expire")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var alice model.User
	if err := f.db.First(&alice, f.aliceID).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "POST", "/api/auth/login", "", map[string]string{"username": alice.Username, "password": "disposable-fixture-password"}, http.StatusOK)
	ttl, err = f.rdb.PTTL(ctx, key).Result()
	if err != nil || ttl <= 30*time.Second || ttl > time.Minute {
		t.Fatalf("new rate window not initialized: %v, %v", ttl, err)
	}
}
