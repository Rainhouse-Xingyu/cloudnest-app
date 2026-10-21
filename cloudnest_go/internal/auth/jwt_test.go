package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testSecret = "test-only-signing-secret-with-at-least-32-bytes"

func TestTokenRoundTrip(t *testing.T) {
	raw, expires, err := GenerateToken(testSecret, 42, "alice", false, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := ParseToken(testSecret, raw)
	if err != nil {
		t.Fatal(err)
	}
	if claims.UserID != 42 || claims.Username != "alice" || claims.IsAdmin || claims.Subject != "42" {
		t.Fatal("identity did not survive token round trip")
	}
	if !claims.ExpiresAt.Time.Equal(expires.Truncate(time.Second)) {
		t.Fatal("expiry mismatch")
	}
}

func TestRejectInvalidTokens(t *testing.T) {
	expired, _, err := GenerateToken(testSecret, 42, "alice", false, -time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	wrongKey, _, err := GenerateToken("another-test-only-secret-with-at-least-32-bytes", 42, "alice", false, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	wrongMethod, err := jwt.NewWithClaims(jwt.SigningMethodHS384, Claims{UserID: 42}).SignedString([]byte(testSecret))
	if err != nil {
		t.Fatal(err)
	}
	for name, token := range map[string]string{"expired": expired, "wrong key": wrongKey, "wrong algorithm": wrongMethod, "malformed": "invalid", "empty": ""} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseToken(testSecret, token); err == nil {
				t.Fatal("invalid token accepted")
			}
		})
	}
}

func TestRejectIncompleteIdentityClaims(t *testing.T) {
	valid := Claims{UserID: 42, Username: "alice", RegisteredClaims: jwt.RegisteredClaims{Subject: "42", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}
	cases := map[string]func(*Claims){
		"missing expiry":     func(c *Claims) { c.ExpiresAt = nil },
		"zero identity":      func(c *Claims) { c.UserID = 0 },
		"negative identity":  func(c *Claims) { c.UserID = -1 },
		"mismatched subject": func(c *Claims) { c.Subject = "43" },
		"missing username":   func(c *Claims) { c.Username = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			claims := valid
			mutate(&claims)
			raw, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testSecret))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseToken(testSecret, raw); err == nil {
				t.Fatal("incomplete claims accepted")
			}
		})
	}
}
