//go:build integration

package integration

import (
	"fmt"
	"strings"
	"testing"

	"cloudenest_go/model"
	"gorm.io/gorm/clause"
)

func TestSettingsValidationPrivacyAndAtomicAudit(t *testing.T) {
	f := newFixture(t)
	admin := model.Role{RoleName: "admin"}
	if err := f.db.Where("role_name = ?", "admin").FirstOrCreate(&admin).Error; err != nil {
		t.Fatal(err)
	}
	grant := model.UserRole{UserID: f.aliceID, RoleID: admin.ID}
	if err := f.db.Create(&grant).Error; err != nil {
		t.Fatal(err)
	}
	const globalKey = "share_default_expire_hours"
	const sentinel = "synthetic-description-excluded-from-audit"
	personalKey := fmt.Sprintf("peak_window_enabled_user_%d", f.bobID)
	keys := []string{globalKey, personalKey, fmt.Sprintf("peak_window_start_user_%d", f.bobID),
		fmt.Sprintf("peak_window_end_user_%d", f.bobID)}
	t.Cleanup(func() { _ = f.db.Where("setting_key IN ?", keys).Delete(&model.SystemSetting{}).Error })

	item := map[string]string{"setting_key": globalKey, "setting_value": "48", "description": sentinel}
	batch := func(items ...map[string]string) map[string]any { return map[string]any{"items": items} }
	f.json(t, "PUT", "/api/settings", f.bob, batch(item), 403)
	f.json(t, "PUT", "/api/settings", f.alice, batch(item, item), 400)
	f.json(t, "POST", "/api/ai/assistant/settings", f.alice, map[string]string{"setting_key": personalKey, "setting_value": "1"}, 400)
	f.json(t, "POST", "/api/ai/assistant/settings", f.alice, map[string]string{"setting_key": globalKey, "setting_value": "-1"}, 400)

	row := model.SystemSetting{SettingKey: globalKey, SettingValue: "12"}
	if err := f.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "setting_key"}}, DoUpdates: clause.AssignmentColumns([]string{"setting_value"}),
	}).Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	const trigger = "settings_test_audit"
	if err := f.db.Exec("CREATE TRIGGER " + trigger + " BEFORE INSERT ON operation_log FOR EACH ROW BEGIN IF NEW.action IN ('settings_update','rate_window_update','ai_apply_setting') THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='settings audit failure'; END IF; END").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Exec("DROP TRIGGER IF EXISTS " + trigger).Error })
	window := map[string]any{"enabled": true, "start": "08:00", "end": "09:00"}
	f.json(t, "PUT", "/api/settings", f.alice, batch(item), 500)
	f.json(t, "POST", "/api/ai/assistant/settings", f.alice, item, 500)
	f.json(t, "PUT", "/api/settings/rate-window", f.bob, window, 500)
	var current model.SystemSetting
	if err := f.db.First(&current, "setting_key=?", globalKey).Error; err != nil || current.SettingValue != "12" {
		t.Fatal("audit failure changed global setting", current.SettingValue, err)
	}
	var personalCount int64
	if err := f.db.Model(&model.SystemSetting{}).Where("setting_key IN ?", keys[1:]).Count(&personalCount).Error; err != nil || personalCount != 0 {
		t.Fatal("audit failure left personal settings", personalCount, err)
	}

	if err := f.db.Exec("DROP TRIGGER " + trigger).Error; err != nil {
		t.Fatal(err)
	}
	f.json(t, "PUT", "/api/settings", f.alice, batch(item), 200)
	f.json(t, "POST", "/api/ai/assistant/settings", f.alice, item, 200)
	f.json(t, "PUT", "/api/settings/rate-window", f.bob, window, 200)
	if w := f.json(t, "GET", "/api/settings", f.bob, nil, 200); strings.Contains(w.Body.String(), personalKey) || !strings.Contains(w.Body.String(), globalKey) {
		t.Fatal("public settings list disclosed personal keys or lost global settings")
	}
	if w := f.json(t, "GET", "/api/settings/rate-window", f.bob, nil, 200); !strings.Contains(w.Body.String(), "08:00") {
		t.Fatal("personal rate window was not available through its own endpoint")
	}
	var logs []model.OperationLog
	if err := f.db.Where("user_id IN ? AND action IN ?", []int64{f.aliceID, f.bobID},
		[]string{"settings_update", "rate_window_update", "ai_apply_setting"}).Order("id").Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 3 || logs[0].Detail != "keys="+globalKey || logs[1].Detail != "keys="+globalKey ||
		logs[2].Detail != "keys="+strings.Join(keys[1:], ",") {
		t.Fatal("expected one audit per committed change with keys only", logs)
	}
}
