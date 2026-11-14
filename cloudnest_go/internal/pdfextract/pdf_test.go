package pdfextract

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if handled, code := Helper(os.Args[1:]); handled {
		os.Exit(code)
	}
	os.Exit(m.Run())
}
func fixture(t *testing.T, name string) *os.File {
	t.Helper()
	f, err := os.Open(filepath.Join("../../integration/testdata/pdf", name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}
func native(t *testing.T) Reader {
	t.Helper()
	dir := os.Getenv("CLOUDNEST_TEST_POPPLER_BIN")
	if dir == "" {
		t.Skip("set CLOUDNEST_TEST_POPPLER_BIN for real Poppler acceptance")
	}
	return Reader{BinDir: dir}
}
func TestPDFRealTextAndDescriptor(t *testing.T) {
	r := native(t)
	raw, err := os.ReadFile("../../integration/testdata/pdf/migration.pdf")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "source.pdf")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	// A replacement pathname must not change the source already verified/pinned.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("untrusted replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	text, err := r.Read(context.Background(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[Page 1]", "[Page 2]", "192.0.2.42", "Spring Boot", "rollback snapshot"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %q", want, text)
		}
	}
	// Seek again verifies the caller retains ownership after subprocess execution.
	if _, err := in.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
}
func TestPDFRealRejectsPartialEncryptedAndPageLimit(t *testing.T) {
	r := native(t)
	for _, tc := range []struct {
		name string
		want error
	}{{"scanned-invoice.pdf", ErrOCRRequired}, {"encrypted.pdf", ErrInvalid}, {"too-many-pages.pdf", ErrLimit}} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.Read(context.Background(), fixture(t, tc.name), nil)
			if !errors.Is(err, tc.want) || got != "" {
				t.Fatalf("text=%q error=%v", got, err)
			}
		})
	}
}
func TestPDFRealRenderPropagatesOCRFailure(t *testing.T) {
	r := native(t)
	calls := 0
	down := errors.New("OCR unavailable")
	text, err := r.Read(context.Background(), fixture(t, "scanned-invoice.pdf"), func(ctx context.Context, png []byte) (string, error) {
		calls++
		if len(png) < 1000 || string(png[:8]) != "\x89PNG\r\n\x1a\n" {
			t.Fatal("not rendered PNG")
		}
		return "", down
	})
	if !errors.Is(err, down) || text != "" || calls != 1 {
		t.Fatalf("text=%q err=%v calls=%d", text, err, calls)
	}
}
func TestPDFLimitsMissingToolsAndCancellation(t *testing.T) {
	in := fixture(t, "migration.pdf")
	if _, err := (Reader{}).Read(context.Background(), in, nil); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := run(ctx, t.TempDir(), in, "info", 0, 100); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	// Saturation never waits and releases no slot it did not acquire.
	slots <- struct{}{}
	slots <- struct{}{}
	_, err := (Reader{}).Read(context.Background(), in, nil)
	<-slots
	<-slots
	if !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	for _, info := range []string{"Pages: 0\nEncrypted: no", "Pages: 2", "Pages: 2\nEncrypted: yes", "Pages: 2\nPages: 3\nEncrypted: no"} {
		if _, err := pageCount(info); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%q: %v", info, err)
		}
	}
}
func toolScript(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pdfinfo"), []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}
func TestPDFSubprocessOutputAndDeadline(t *testing.T) {
	in := fixture(t, "migration.pdf")
	dir := toolScript(t, "while :; do printf '1234567890'; done\n")
	start := time.Now()
	out, err := run(context.Background(), dir, in, "info", 0, 256)
	if !errors.Is(err, ErrLimit) || out != nil || time.Since(start) > 5*time.Second {
		t.Fatalf("overflow: %v (%v)", err, time.Since(start))
	}
	dir = toolScript(t, "exec /bin/sleep 20\n")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start = time.Now()
	out, err = run(ctx, dir, in, "info", 0, 256)
	if !errors.Is(err, context.DeadlineExceeded) || out != nil || time.Since(start) > 3*time.Second {
		t.Fatalf("deadline: %v (%v)", err, time.Since(start))
	}
}
func TestPDFHelperRejectsUnknownArguments(t *testing.T) {
	for _, args := range [][]string{{helperFlag}, {helperFlag, "relative", "info", "0"}, {helperFlag, "/bin/sh", "info", "0"}, {helperFlag, "/tmp/pdftoppm", "render", "65"}, {helperFlag, "/tmp/pdftoppm", "render", "01"}} {
		handled, code := Helper(args)
		if !handled || code != 126 {
			t.Fatalf("%v: %v %d", args, handled, code)
		}
	}
}

func TestPDFSubprocessLimitsAndEnvironment(t *testing.T) {
	t.Setenv("CLOUDNEST_TEST_SECRET", "must-not-inherit")
	dir := toolScript(t, `printf '%s|' "${CLOUDNEST_TEST_SECRET-unset}"
 ulimit -t
 ulimit -n
 ulimit -f
 ulimit -v
 `)
	raw, err := run(context.Background(), dir, fixture(t, "migration.pdf"), "info", 0, 1024)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(string(raw))
	if len(lines) != 4 || lines[0] != "unset|15" || lines[1] != "64" || lines[2] != "0" {
		t.Fatalf("limits/env not inherited: %q", raw)
	}
	if runtime.GOOS == "linux" && lines[3] != "786432" {
		t.Fatalf("address space not limited: %q", raw)
	}
}
func TestPDFRejectsOversizedSourceBeforeNativeParsing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized.pdf")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(MaxSourceBytes + 1); err != nil {
		t.Fatal(err)
	}
	if got, err := (Reader{}).Read(context.Background(), f, nil); got != "" || !errors.Is(err, ErrLimit) {
		t.Fatal(got, err)
	}
}
