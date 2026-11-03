//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"cloudenest_go/model"
)

func TestFRPConfigTLSRedactionAndInjection(t *testing.T) {
	f := newFixture(t)
	f.cfg.FRPKeyFile, f.cfg.FRPKeys = frpTestKeys(t, "one", map[string][]byte{"one": bytes.Repeat([]byte{1}, 32)})
	id := agentDevice(t, f)
	t.Cleanup(func() {
		if err := f.db.Where("device_id = ?", id).Delete(&model.FrpConfig{}).Error; err != nil {
			t.Error(err)
		}
	})
	f.json(t, "POST", "/api/frp/mappings", f.alice, map[string]any{"device_id": id, "local_port": 8080, "remote_port": 9000}, 200)
	base, client := startAgentTLSServer(t, f)
	secret := "synthetic-frps-secret #: punctuation"
	payload := map[string]any{"device_id": id, "server_addr": "example.com", "token": secret}
	for _, path := range []string{"/api/frp/configs/preview", "/api/frp/configs/save"} {
		f.json(t, "POST", path, f.alice, payload, 426)
	}
	call := func(method, path, user string, raw []byte, want int) []byte {
		t.Helper()
		req, err := http.NewRequest(method, base+path, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+user)
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != want {
			t.Fatalf("%s %s status %d want %d", method, path, res.StatusCode, want)
		}
		if bytes.Contains(body, []byte(secret)) {
			t.Fatal("credential disclosed in response")
		}
		if method == "POST" && (path == "/api/frp/configs/preview" || path == "/api/frp/configs/save") && res.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("missing no-store")
		}
		return body
	}
	marshal := func(v any) []byte {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	f.json(t, "POST", "https://fixture.invalid/api/frp/configs/save", f.alice, payload, 503)
	preview := call("POST", "/api/frp/configs/preview", f.alice, marshal(payload), 200)
	if !bytes.Contains(preview, []byte("[redacted]")) {
		t.Fatal("preview did not mark secret redaction")
	}
	call("POST", "/api/frp/configs/save", f.bob, marshal(payload), 403)
	checkItem := func(raw []byte) int64 {
		t.Helper()
		var response struct {
			Item struct {
				ID         int64
				ConfigText *string `json:"config_text"`
				Redacted   bool    `json:"config_text_redacted"`
			}
		}
		if err := json.Unmarshal(raw, &response); err != nil {
			t.Fatal(err)
		}
		if response.Item.ID <= 0 || response.Item.ConfigText == nil || *response.Item.ConfigText != "" || !response.Item.Redacted {
			t.Fatal("config response lost metadata or disclosed text")
		}
		return response.Item.ID
	}
	configID := checkItem(call("POST", "/api/frp/configs/save", f.alice, marshal(payload), 200))
	var original model.FrpConfig
	if err := f.db.First(&original, configID).Error; err != nil {
		t.Fatal(err)
	}
	plain, err := f.cfg.FRPKeys.Open(id, original.ID, original.ConfigText)
	if err != nil || !strings.Contains(plain, secret) || strings.Contains(original.ConfigText, secret) {
		t.Fatal("configuration not encrypted correctly", err)
	}
	serialized := marshal(original)
	if bytes.Contains(serialized, []byte(secret)) || bytes.Contains(serialized, []byte("config_text")) {
		t.Fatal("model serialization discloses configuration")
	}
	rollbackID := checkItem(call("POST", fmt.Sprintf("/api/frp/configs/%d/rollback", configID), f.alice, []byte(`{}`), 200))
	var rollback model.FrpConfig
	if err := f.db.First(&rollback, rollbackID).Error; err != nil {
		t.Fatal(err)
	}
	rolledPlain, err := f.cfg.FRPKeys.Open(id, rollback.ID, rollback.ConfigText)
	if err != nil || rolledPlain != plain || rollback.ConfigText == original.ConfigText {
		t.Fatal("rollback did not reencrypt under its own identity", err)
	}
	var audit []model.OperationLog
	if err := f.db.Where("user_id=? AND action IN ?", f.aliceID, []string{"frp_config_save", "frp_config_rollback"}).Find(&audit).Error; err != nil || len(audit) != 2 {
		t.Fatal("missing atomic config audit", err)
	}
	for _, entry := range audit {
		if entry.ClientIP != "127.0.0.1" || strings.Contains(entry.Detail, secret) {
			t.Fatal("audit lost peer IP or exposed secret")
		}
	}

	history := call("GET", fmt.Sprintf("/api/frp/devices/%d/configs", id), f.alice, nil, 200)
	var list struct{ Items []json.RawMessage }
	if err := json.Unmarshal(history, &list); err != nil || len(list.Items) != 2 {
		t.Fatal("history lost versions", err)
	}
	for _, item := range list.Items {
		checkItem(append(append([]byte(`{"item":`), item...), '}'))
	}
	for _, attack := range []string{"x\nwebServer: {}", "{{ .Envs.HOME }}", string([]byte{0})} {
		payload["token"] = attack
		for _, path := range []string{"/api/frp/configs/save", "/api/frp/configs/preview"} {
			call("POST", path, f.alice, marshal(payload), 400)
		}
	}
	for _, raw := range []string{`null`, `{} {}`, `{"unknown":true}`, strings.Repeat(" ", 4097) + `{}`} {
		call("POST", "/api/frp/configs/save", f.alice, []byte(raw), 400)
	}
	var count int64
	if err := f.db.Model(&model.FrpConfig{}).Where("device_id = ?", id).Count(&count).Error; err != nil || count != 2 {
		t.Fatal("invalid requests changed config history", count, err)
	}
}
