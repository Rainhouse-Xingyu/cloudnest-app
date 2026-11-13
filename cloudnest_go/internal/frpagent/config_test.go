package frpagent

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

const validConfig = "serverAddr: 127.0.0.1\nserverPort: 7000\nauth:\n  token: 'test # token'\nproxies:\n  - name: device_1_map_1\n    type: tcp\n    localPort: 8080\n    remotePort: 6000\n"

func testPolicy() Policy {
	return Policy{ServerAddr: "127.0.0.1", ServerPort: 7000, LocalPorts: []int{8080}}
}
func TestNormalizeRejectsRemoteCapabilities(t *testing.T) {
	b, e := Normalize([]byte(validConfig), testPolicy())
	if e != nil {
		t.Fatal(e)
	}
	var doc map[string]any
	if json.Unmarshal(b, &doc) != nil || !bytes.Contains(b, []byte(`"localIP":"127.0.0.1"`)) {
		t.Fatal("canonical config")
	}
	for name, raw := range map[string]string{
		"plugin":            validConfig + "    plugin:\n      type: static_file\n      localPath: /\n",
		"includes":          validConfig + "includes: ['/etc/*']\n",
		"admin":             validConfig + "webServer:\n  port: 7400\n",
		"env":               strings.Replace(validConfig, "'test # token'", `"{{ .Envs.SECRET }}"`, 1),
		"escaped template":  strings.Replace(validConfig, "'test # token'", `"\u007b\u007b .Envs.SECRET \u007d\u007d"`, 1),
		"file credential":   strings.Replace(validConfig, "  token: 'test # token'", "  tokenSource:\n    type: file\n    file:\n      path: /etc/passwd", 1),
		"server change":     strings.Replace(validConfig, "127.0.0.1", "example.com", 1),
		"upstream dns":      validConfig + "    localIP: localhost\n",
		"upstream lan":      validConfig + "    localIP: 192.168.1.1\n",
		"unauthorized port": strings.Replace(validConfig, "8080", "22", 1),
		"unknown type":      strings.Replace(validConfig, "type: tcp", "type: xtcp", 1),
		"duplicate key":     validConfig + "serverPort: 7000\n",
		"extra document":    validConfig + "---\nserverAddr: 127.0.0.1\n",
		"alias":             strings.Replace(validConfig, "test # token", "test", 1) + "unknown: &a [*a]\n",
		"scalar":            "null",
	} {
		t.Run(name, func(t *testing.T) {
			if _, e := Normalize([]byte(raw), testPolicy()); e != ErrConfig {
				t.Fatalf("accepted prohibited configuration: %v", e)
			}
		})
	}
	p := testPolicy()
	p.ServerAddr = "example.com"
	if p.Validate() != ErrConfig {
		t.Fatal("public frps without trust")
	}
	p.TrustedCAFile = "/private/frps-ca.pem"
	if p.Validate() != nil {
		t.Fatal("local CA authorization")
	}
}

func TestNormalizeHTTPAndLimits(t *testing.T) {
	for _, kind := range []string{"http", "https"} {
		raw := strings.Replace(validConfig, "type: tcp", "type: "+kind, 1)
		raw = strings.Replace(raw, "    remotePort: 6000", "    customDomains: ['*.example.com']", 1)
		if _, e := Normalize([]byte(raw), testPolicy()); e != nil {
			t.Fatal(kind, e)
		}
	}
	if _, e := Normalize(bytes.Repeat([]byte{'x'}, 32769), testPolicy()); e != ErrConfig {
		t.Fatal("size cap")
	}
	raw := strings.Replace(validConfig, "remotePort: 6000", "remotePort: 65536", 1)
	if _, e := Normalize([]byte(raw), testPolicy()); e != ErrConfig {
		t.Fatal("port range")
	}
}
