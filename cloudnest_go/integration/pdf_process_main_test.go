//go:build integration

package integration

import (
	"cloudenest_go/internal/pdfextract"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if handled, code := pdfextract.Helper(os.Args[1:]); handled {
		os.Exit(code)
	}
	os.Exit(m.Run())
}
