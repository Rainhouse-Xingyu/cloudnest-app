package store

import (
	"strings"
	"testing"
)

func TestBootstrapPasswordValidation(t *testing.T) {
	for _, password := range []string{"", "admin", "short", strings.Repeat("a", 73), " padded-test-password ", "CHANGE_ME-123456789"} {
		if err := ValidateBootstrapPassword(password); err == nil {
			t.Fatal("invalid bootstrap password accepted")
		}
	}
	if err := ValidateBootstrapPassword("unique-test-only-bootstrap-password"); err != nil {
		t.Fatal(err)
	}
}
