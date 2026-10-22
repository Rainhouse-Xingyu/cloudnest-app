package main

import (
	"strings"
	"testing"
)

func TestMaintenanceRejectsAmbiguousTargetsAndPasswordArguments(t *testing.T) {
	for _, args := range [][]string{
		{"reset-password"}, {"reset-password", "1"}, {"reset-password", "0", "alice"},
		{"reset-password", "-1", "alice"}, {"reset-password", "1", ""},
		{"reset-password", "alice", "alice"}, {"reset-password", "1", "alice", "secret-must-not-be-argv"},
		{"migrate-file-names", "extra"}, {"audit-file-names", "extra"}, {"migrate", "extra"}, {"bootstrap-admin", "extra"},
	} {
		_, recognized, err := parseMaintenanceArgs(args)
		if !recognized || err == nil {
			t.Fatalf("unsafe maintenance args accepted: %q", args[:1])
		}
		if strings.Contains(err.Error(), "secret-must-not-be-argv") {
			t.Fatal("error leaked input")
		}
	}
	cmd, recognized, err := parseMaintenanceArgs([]string{"reset-password", "42", "Alice"})
	if err != nil || !recognized || cmd.userID != 42 || cmd.username != "Alice" {
		t.Fatal("exact target lost")
	}
}

func TestPasswordStdinBoundaries(t *testing.T) {
	for _, input := range []string{"", "short", strings.Repeat("a", 73), strings.Repeat("a", 5000), " secret-is-padded ", "password-with\nembedded-newline", "password-with\x00null", "CHANGE_ME-123456789"} {
		if _, err := readResetPassword(strings.NewReader(input)); err == nil {
			t.Fatal("invalid password input accepted")
		}
	}
	for _, suffix := range []string{"", "\n", "\r\n"} {
		want := strings.Repeat("a", 72)
		got, err := readResetPassword(strings.NewReader(want + suffix))
		if err != nil || got != want {
			t.Fatal("valid bounded stdin password rejected")
		}
	}
}
