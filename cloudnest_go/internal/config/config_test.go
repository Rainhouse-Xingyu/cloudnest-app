package config

import (
	"strings"
	"testing"
)

func TestConfigRejectsUnsafeStartup(t *testing.T) {
	valid := Config{JWTSecret: "test-only-random-key-at-least-thirty-two-bytes", MySQLDSN: "app:test-fixture@tcp(127.0.0.1:3306)/test", JWTExpireHours: 1}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"", "change-me-in-production", "replace_with_a_strong_random_secret", "short", strings.Repeat(" ", 40)} {
		c := valid
		c.JWTSecret = key
		if err := c.Validate(); err == nil {
			t.Fatal("unsafe signing key accepted")
		}
	}
	for _, ttl := range []int{-1, 0, 8761} {
		c := valid
		c.JWTExpireHours = ttl
		if err := c.Validate(); err == nil {
			t.Fatal("invalid expiry accepted")
		}
	}
	c := valid
	c.MySQLDSN = ""
	if err := c.Validate(); err == nil {
		t.Fatal("missing database configuration accepted")
	}
}

func TestLoadDoesNotInventCredentials(t *testing.T) {
	t.Setenv("JWT_SECRET", "")
	t.Setenv("MYSQL_DSN", "")
	c := Load()
	if c.JWTSecret != "" || c.MySQLDSN != "" {
		t.Fatal("default credential returned")
	}
	if err := c.Validate(); err == nil {
		t.Fatal("missing credentials accepted")
	}
}
