package frpagent

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestRuntimeTokenIsOnlyInInheritedPipe(t *testing.T) {
	s := testStore(t)
	for _, token := range []string{"测试 # token", strings.Repeat("x", 1024), ""} {
		normalized, err := Normalize([]byte(strings.Replace(validConfig, "test # token", token, 1)), testPolicy())
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"active.json", "candidate.json"} {
			pipe, err := s.stageRuntime(name, normalized)
			if err != nil {
				t.Fatal(err)
			}
			value, err := io.ReadAll(pipe)
			pipe.Close()
			if err != nil || string(value) != token {
				t.Fatal("token not delivered intact")
			}
			raw, err := s.Read(name, 65536)
			if err != nil || !bytes.Contains(raw, []byte("/dev/fd/4")) || bytes.Contains(raw, []byte(`"token":`)) || (token != "" && bytes.Contains(raw, value)) {
				t.Fatal("runtime config contains token or lacks fixed local source")
			}
		}
	}
}

func TestRuntimeSourceRejectsChangedCredentialWithoutCreatingCache(t *testing.T) {
	s := testStore(t)
	for _, token := range []string{" bad", "bad ", "bad\u00a0"} {
		b, err := Normalize([]byte(strings.Replace(validConfig, "test # token", token, 1)), testPolicy())
		if err != nil {
			t.Fatal(err)
		}
		if f, err := s.stageRuntime("candidate.json", b); f != nil || !errors.Is(err, ErrConfig) {
			t.Fatal("altered credential accepted")
		}
		if _, err := s.Read("candidate.json", 65536); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("unexpected cache", err)
		}
	}
}
