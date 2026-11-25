//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"cloudenest_go/model"
)

func preserveSettings(t *testing.T, f *fixture, keys []string) {
	t.Helper()
	var original []model.SystemSetting
	if err := f.db.Where("setting_key IN ?", keys).Find(&original).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.db.Where("setting_key IN ?", keys).Delete(&model.SystemSetting{}).Error; err != nil {
			t.Error(err)
			return
		}
		if len(original) > 0 {
			if err := f.db.Create(&original).Error; err != nil {
				t.Error(err)
			}
		}
	})
}

func concurrentSettings(t *testing.T, f *fixture, path, token string, inputs []any) {
	t.Helper()
	start := make(chan struct{})
	codes := make(chan int, len(inputs))
	var group sync.WaitGroup
	for _, input := range inputs {
		data, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			req := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(data))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, req)
			codes <- w.Code
		}()
	}
	close(start)
	group.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[200] != 1 || counts[409] != 1 || len(counts) != 2 {
		t.Fatal("conditional settings race", counts)
	}
}

func TestSettingsConditionalWritesAndLegacyCompatibility(t *testing.T) {
	f := newFixture(t)
	admin := model.Role{RoleName: "admin"}
	if err := f.db.Where("role_name=?", "admin").FirstOrCreate(&admin).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&model.UserRole{UserID: f.aliceID, RoleID: admin.ID}).Error; err != nil {
		t.Fatal(err)
	}
	const key = "upload_max_concurrency"
	privateKey := fmt.Sprintf("private_setting_fixture_%d", f.aliceID)
	preserveSettings(t, f, []string{key, privateKey})
	rateSetting(t, f, privateKey, "private-value-fixture")
	var scoped struct {
		Conditional bool `json:"conditional_writes"`
	}
	w := f.json(t, "GET", "/api/settings?scope=system", f.alice, nil, 200)
	decode(t, w, &scoped)
	if !scoped.Conditional || strings.Contains(w.Body.String(), privateKey) || strings.Contains(w.Body.String(), "private-value-fixture") {
		t.Fatal("scoped settings disclosed private rows or omitted capability")
	}
	f.json(t, "GET", "/api/settings?scope=unknown", f.alice, nil, 400)
	if err := f.db.Where("setting_key=?", key).Delete(&model.SystemSetting{}).Error; err != nil {
		t.Fatal(err)
	}
	batch := func(value string, expected map[string]*string) map[string]any {
		return map[string]any{"items": []any{map[string]string{"setting_key": key, "setting_value": value}}, "expected": expected}
	}
	baseline := map[string]*string{key: nil}
	concurrentSettings(t, f, "/api/settings", f.alice, []any{batch("2", baseline), batch("3", baseline)})
	var count int64
	if err := f.db.Model(&model.OperationLog{}).Where("user_id=? AND action='settings_update'", f.aliceID).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("conditional race audit", count, err)
	}
	f.json(t, "PUT", "/api/settings", f.alice, batch("4", baseline), 409)
	f.json(t, "PUT", "/api/settings", f.alice, batch("4", map[string]*string{}), 400)
	f.json(t, "PUT", "/api/settings", f.bob, batch("4", baseline), 403)
	// Omitted/null baseline retains the existing unconditional protocol.
	f.json(t, "PUT", "/api/settings", f.alice, batch("5", nil), 200)
	old := "5"
	f.json(t, "PUT", "/api/settings", f.alice, batch("6", map[string]*string{key: &old}), 200)
	f.json(t, "PUT", "/api/settings", f.alice, batch("7", map[string]*string{key: &old}), 409)
	var row model.SystemSetting
	if err := f.db.First(&row, "setting_key=?", key).Error; err != nil || row.SettingValue != "6" {
		t.Fatal("stale settings overwritten", row.SettingValue, err)
	}
	if err := f.db.Model(&model.OperationLog{}).Where("user_id=? AND action='settings_update'", f.aliceID).Count(&count).Error; err != nil || count != 3 {
		t.Fatal("failed writes produced audit", count, err)
	}
}

func TestPersonalRateWindowSnapshotPrivacyAndConditions(t *testing.T) {
	f := newFixture(t)
	globals := []string{"peak_window_enabled", "peak_window_start", "peak_window_end"}
	keys := append([]string{}, globals...)
	for _, id := range []int64{f.aliceID, f.bobID} {
		for _, key := range globals {
			keys = append(keys, fmt.Sprintf("%s_user_%d", key, id))
		}
	}
	preserveSettings(t, f, keys)
	if err := f.db.Where("setting_key IN ?", keys).Delete(&model.SystemSetting{}).Error; err != nil {
		t.Fatal(err)
	}
	rateSetting(t, f, globals[0], "0")
	rateSetting(t, f, globals[1], "08:00")
	rateSetting(t, f, globals[2], "09:00")
	rateSetting(t, f, fmt.Sprintf("%s_user_%d", globals[1], f.bobID), "private-fixture")
	type window struct {
		Enabled  bool               `json:"enabled"`
		Source   string             `json:"source"`
		Expected map[string]*string `json:"expected"`
	}
	get := func() window {
		var response struct{ Item window }
		decode(t, f.json(t, "GET", "/api/settings/rate-window", f.alice, nil, 200), &response)
		return response.Item
	}
	before := get()
	if before.Source != "global" || len(before.Expected) != 6 {
		t.Fatal("missing rate baseline")
	}
	for key := range before.Expected {
		for _, global := range globals {
			if key == fmt.Sprintf("%s_user_%d", global, f.bobID) {
				t.Fatal("another account baseline disclosed")
			}
		}
	}
	input := func(expected map[string]*string, start string) map[string]any {
		return map[string]any{"enabled": true, "start": start, "end": "20:00", "expected": expected}
	}
	rateSetting(t, f, globals[1], "10:00")
	f.json(t, "PUT", "/api/settings/rate-window", f.alice, input(before.Expected, "11:00"), 409)
	fresh := get()
	concurrentSettings(t, f, "/api/settings/rate-window", f.alice, []any{input(fresh.Expected, "11:00"), input(fresh.Expected, "12:00")})
	var count int64
	if err := f.db.Model(&model.OperationLog{}).Where("user_id=? AND action='rate_window_update'", f.aliceID).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("rate window conditional audit", count, err)
	}
	fresh = get()
	if fresh.Source != "user" || !fresh.Enabled {
		t.Fatal("personal window not saved")
	}
	malformed := map[string]*string{}
	for key, value := range fresh.Expected {
		malformed[key] = value
	}
	delete(malformed, globals[0])
	malformed[fmt.Sprintf("%s_user_%d", globals[0], f.bobID)] = nil
	f.json(t, "PUT", "/api/settings/rate-window", f.alice, input(malformed, "13:00"), 400)
	f.json(t, "PUT", "/api/settings/rate-window", f.alice, map[string]any{"enabled": false}, 200)
}
