package contract

import (
	"strings"
	"testing"
)

func TestLogicalNamesRejectPathsAndUnsupportedComponents(t *testing.T) {
	for _, name := range []string{"", ".", "..", "a/b", "a\\b", "nul\x00name", "line\nbreak", "bad\x7f", string([]byte{0xff}), strings.Repeat("中", 256)} {
		if ValidateFileName(name) == nil {
			t.Fatalf("invalid component accepted: %q", name)
		}
	}
	for _, name := range []string{"report.txt", ".hidden", "résumé 文档", strings.Repeat("中", 255)} {
		if err := ValidateFileName(name); err != nil {
			t.Fatalf("valid component rejected: %q", name)
		}
	}
}
