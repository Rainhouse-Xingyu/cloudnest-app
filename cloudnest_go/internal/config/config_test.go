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

func TestMalformedJWTExpiryFailsValidation(t *testing.T) {
	t.Setenv("MYSQL_DSN", "app:test-fixture@tcp(127.0.0.1:3306)/test")
	t.Setenv("JWT_SECRET", "test-only-random-key-at-least-thirty-two-bytes")
	for _, input := range []string{"one-hour", "1h", "1.5", "999999999999999999999999999", "0", "-1", "8761"} {
		t.Setenv("JWT_EXPIRE_HOURS", input)
		if err := Load().Validate(); err == nil {
			t.Fatalf("invalid JWT expiry accepted: %q", input)
		}
	}
	t.Setenv("JWT_EXPIRE_HOURS", "")
	if cfg := Load(); cfg.JWTExpireHours != 168 || cfg.Validate() != nil {
		t.Fatal("default expiry changed")
	}
	t.Setenv("JWT_EXPIRE_HOURS", "24")
	if cfg := Load(); cfg.JWTExpireHours != 24 || cfg.Validate() != nil {
		t.Fatal("valid expiry rejected")
	}
}

func TestTransferLimitEnvironmentIsStrict(t *testing.T) {
	t.Setenv("MYSQL_DSN", "app:disposable@tcp(127.0.0.1:3306)/test")
	t.Setenv("JWT_SECRET", "test-only-random-key-at-least-thirty-two-bytes")
	for _, value := range []string{"0", "-1", "8GiB", "1.5", "999999999999999999999", "4398046511105"} {
		t.Setenv("TRANSFER_TEMP_LIMIT_BYTES", value)
		if err := Load().Validate(); err == nil {
			t.Fatal("invalid transfer seed limit accepted")
		}
	}
	t.Setenv("TRANSFER_TEMP_LIMIT_BYTES", "")
	if c := Load(); c.Validate() != nil || c.TransferTempBytes != 8<<30 {
		t.Fatal("transfer default changed")
	}
	t.Setenv("TRANSFER_TEMP_LIMIT_BYTES", "1024")
	if c := Load(); c.Validate() != nil || c.TransferTempBytes != 1024 {
		t.Fatal("valid transfer seed limit rejected")
	}
}
