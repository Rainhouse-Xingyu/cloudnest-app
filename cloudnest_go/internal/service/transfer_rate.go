package service

import (
	"context"
	"strconv"
	"strings"
	"time"

	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
)

// TransferRate resolves a request-start policy snapshot. A client or personal
// window may only tighten the administrator's cap, never replace it.
func (s *FileService) TransferRate(ctx context.Context, uid int64, direction, requested string) (int64, error) {
	if direction != "upload" && direction != "download" {
		return 0, contract.ErrInvalidInput
	}
	rows, err := s.fileRepo.GetTransferRateSettings(ctx, uid)
	if err != nil {
		return 0, contract.ErrRatePolicy
	}
	return resolveTransferRate(rows, uid, direction, requested, time.Now())
}
func resolveTransferRate(rows []model.SystemSetting, uid int64, direction, requested string, now time.Time) (int64, error) {
	values := map[string]string{}
	for _, row := range rows {
		key := strings.ToLower(strings.TrimSpace(row.SettingKey))
		if _, ok := values[key]; ok {
			return 0, contract.ErrRatePolicy
		}
		values[key] = strings.TrimSpace(row.SettingValue)
	}
	base, err := configuredRate(values, direction+"_speed_limit_kbps", 2048)
	if err != nil {
		return 0, err
	}
	peak, err := configuredRate(values, direction+"_peak_speed_limit_kbps", base)
	if err != nil {
		return 0, err
	}
	global, err := rateWindowActive(values, "", now)
	if err != nil {
		return 0, err
	}
	personal := false
	if uid > 0 {
		personal, err = rateWindowActive(values, "_user_"+strconv.FormatInt(uid, 10), now)
		if err != nil {
			return 0, err
		}
	}
	if global || personal {
		base = min(base, peak)
	}
	if requested != "" {
		value, err := strconv.ParseInt(strings.TrimSpace(requested), 10, 64)
		if err != nil || value < 1 || value > 10240 {
			return 0, contract.ErrInvalidInput
		}
		base = min(base, value)
	}
	return base * 1024, nil
}
func configuredRate(values map[string]string, key string, fallback int64) (int64, error) {
	raw, ok := values[key]
	if !ok {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 64 || value > 10240 {
		return 0, contract.ErrRatePolicy
	}
	return value, nil
}
func rateWindowActive(values map[string]string, suffix string, now time.Time) (bool, error) {
	raw, exists := values["peak_window_enabled"+suffix]
	if !exists || raw == "0" {
		return false, nil
	}
	if raw != "1" {
		return false, contract.ErrRatePolicy
	}
	start, ok1 := parseHHMMToMinuteInService(values["peak_window_start"+suffix])
	end, ok2 := parseHHMMToMinuteInService(values["peak_window_end"+suffix])
	if !ok1 || !ok2 {
		return false, contract.ErrRatePolicy
	}
	minute := now.Hour()*60 + now.Minute()
	if start == end {
		return true, nil
	}
	if start < end {
		return minute >= start && minute < end, nil
	}
	return minute >= start || minute < end, nil
}
