package auth

import (
	"errors"
	"strings"
)

// ValidateNewPassword bounds bcrypt input and rejects unsafe setup placeholders.
// Password values must never be included in an error, audit entry or log.
func ValidateNewPassword(password string) error {
	if len(password) < 14 || len(password) > 72 || strings.TrimSpace(password) != password || strings.ContainsAny(password, "\r\n\x00") || strings.Contains(strings.ToLower(password), "change_me") {
		return errors.New("new password must contain 14 to 72 bytes without surrounding whitespace, line breaks or placeholders; use a unique random password")
	}
	return nil
}
