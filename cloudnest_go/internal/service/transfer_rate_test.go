package service

import (
	"errors"
	"testing"
	"time"

	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
)

func TestTransferRatePolicyCannotBeRaisedByClientOrPersonalWindow(t *testing.T) {
	cases := []struct {
		name, query string
		settings    map[string]string
		want        int64
		wantErr     error
	}{
		{name: "default client cannot raise", query: "10240", want: 2048},
		{name: "client can slow below old minimum", query: "1", want: 1},
		{name: "server cap", query: "10240", settings: map[string]string{"download_speed_limit_kbps": "128"}, want: 128},
		{name: "global peak despite personal disabled", query: "10240", settings: map[string]string{"peak_window_enabled": "1", "peak_window_start": "22:00", "peak_window_end": "06:00", "peak_window_enabled_user_7": "0", "download_peak_speed_limit_kbps": "64"}, want: 64},
		{name: "peak cannot raise normal", settings: map[string]string{"download_speed_limit_kbps": "128", "download_peak_speed_limit_kbps": "512", "peak_window_enabled": "1", "peak_window_start": "00:00", "peak_window_end": "00:00"}, want: 128},
		{name: "personal adds restriction", settings: map[string]string{"peak_window_enabled": "0", "peak_window_enabled_user_7": "1", "peak_window_start_user_7": "00:00", "peak_window_end_user_7": "00:00", "download_peak_speed_limit_kbps": "64"}, want: 64},
		{name: "invalid server not default", query: "64", settings: map[string]string{"download_speed_limit_kbps": ""}, wantErr: contract.ErrRatePolicy},
		{name: "overflow server", settings: map[string]string{"download_speed_limit_kbps": "9223372036854775808"}, wantErr: contract.ErrRatePolicy},
		{name: "signed window hour", settings: map[string]string{"peak_window_enabled": "1", "peak_window_start": "+1:00", "peak_window_end": "02:00"}, wantErr: contract.ErrRatePolicy},
		{name: "invalid enabled", settings: map[string]string{"peak_window_enabled": "yes"}, wantErr: contract.ErrRatePolicy},
		{name: "missing enabled times", settings: map[string]string{"peak_window_enabled": "1"}, wantErr: contract.ErrRatePolicy},
		{name: "bad personal time", settings: map[string]string{"peak_window_enabled_user_7": "1", "peak_window_start_user_7": "24:00", "peak_window_end_user_7": "01:00"}, wantErr: contract.ErrRatePolicy},
		{name: "bad query", query: "-1", wantErr: contract.ErrInvalidInput},
		{name: "overflow query", query: "9223372036854775808", wantErr: contract.ErrInvalidInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var rows []model.SystemSetting
			for k, v := range tc.settings {
				rows = append(rows, model.SystemSetting{SettingKey: k, SettingValue: v})
			}
			rate, err := resolveTransferRate(rows, 7, "download", tc.query, time.Date(2026, 10, 23, 23, 0, 0, 0, time.UTC))
			if !errors.Is(err, tc.wantErr) || rate != tc.want*1024 {
				t.Fatal(rate, err)
			}
		})
	}
}
func TestTransferRateWindowBoundaries(t *testing.T) {
	for _, tc := range []struct {
		start, end   string
		hour, minute int
		want         bool
	}{
		{"22:00", "06:00", 22, 0, true}, {"22:00", "06:00", 5, 59, true}, {"22:00", "06:00", 6, 0, false}, {"22:00", "06:00", 12, 0, false},
		{"09:00", "10:00", 9, 0, true}, {"09:00", "10:00", 10, 0, false}, {"00:00", "00:00", 12, 0, true},
	} {
		active, err := rateWindowActive(map[string]string{"peak_window_enabled": "1", "peak_window_start": tc.start, "peak_window_end": tc.end}, "", time.Date(2026, 10, 23, tc.hour, tc.minute, 0, 0, time.UTC))
		if err != nil || active != tc.want {
			t.Fatal(tc, active, err)
		}
	}
}

func TestTransferRateNormalizesSettingAliasesWithoutDefaultFallback(t *testing.T) {
	for _, key := range []string{"DOWNLOAD_SPEED_LIMIT_KBPS", "download_speed_limit_kbps "} {
		rate, err := resolveTransferRate([]model.SystemSetting{{SettingKey: key, SettingValue: "64"}}, 0, "download", "10240", time.Now())
		if err != nil || rate != 64*1024 {
			t.Fatal("setting alias silently used default", key, rate, err)
		}
	}
	_, err := resolveTransferRate([]model.SystemSetting{{SettingKey: "download_speed_limit_kbps", SettingValue: "64"}, {SettingKey: "DOWNLOAD_SPEED_LIMIT_KBPS", SettingValue: "128"}}, 0, "download", "", time.Now())
	if !errors.Is(err, contract.ErrRatePolicy) {
		t.Fatal("ambiguous policy accepted", err)
	}
}
