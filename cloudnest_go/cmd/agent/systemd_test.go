package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestSystemdUnitPathsAreLiteral(t *testing.T) {
	var out bytes.Buffer
	if err := writeSystemdUnit(&out, `/opt/Agent "100%"/cloudnest-agent`, `/home/test/state %n ${USER} \ folder`); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `ExecStart="/opt/Agent \"100%%\"/cloudnest-agent" run --state "/home/test/state %%n $${USER} \\ folder"`+"\n") {
		t.Fatal("systemd path expansion was not escaped", out.String())
	}
	for _, state := range []string{"state", "/", "/var/../state", "/state\nExecStart=/tmp/other"} {
		out.Reset()
		if err := writeSystemdUnit(&out, "/opt/agent", state); err == nil || out.Len() != 0 {
			t.Fatal("invalid state path emitted a unit")
		}
	}
}
