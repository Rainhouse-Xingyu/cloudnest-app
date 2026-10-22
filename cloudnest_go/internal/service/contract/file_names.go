package contract

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// ValidateFileName validates a single logical path component, not a filesystem
// path. Match the existing VARCHAR(255) character limit without truncating input.
func ValidateFileName(name string) error {
	if name == "" || name == "." || name == ".." || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 255 || strings.ContainsAny(name, "/\\") {
		return ErrInvalidInput
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return ErrInvalidInput
		}
	}
	return nil
}
