package service

import (
	"context"
	"strconv"
	"strings"

	"cloudenest_go/internal/service/contract"

	"gorm.io/gorm"
)

const (
	globalStorageQuotaSettingKey  = "storage_quota_bytes"
	defaultStorageQuotaSettingKey = "storage_default_quota_bytes"
)

// StorageService 提供容量概览业务能力。
type StorageService struct {
	repo contract.StorageRepository
}

func NewStorageService(repo contract.StorageRepository) *StorageService {
	return &StorageService{repo: repo}
}

// StorageOverview 表示用户容量概览。
type StorageOverview struct {
	UsedBytes      int64   `json:"used_bytes"`
	TotalBytes     int64   `json:"total_bytes"`
	RemainingBytes int64   `json:"remaining_bytes"`
	UsagePercent   float64 `json:"usage_percent"`
	HasQuota       bool    `json:"has_quota"`
	QuotaSource    string  `json:"quota_source"`
}

// GetOverview 返回用户容量概览。
func (s *StorageService) GetOverview(ctx context.Context, uid int64) (StorageOverview, error) {
	usedBytes, err := s.repo.GetUserUsedStorageBytes(ctx, uid)
	if err != nil {
		return StorageOverview{}, err
	}

	totalBytes, source, hasQuota, err := s.resolveQuotaBytes(ctx, uid)
	if err != nil {
		return StorageOverview{}, err
	}

	res := StorageOverview{
		UsedBytes:    usedBytes,
		TotalBytes:   totalBytes,
		HasQuota:     hasQuota,
		QuotaSource:  source,
		UsagePercent: 0,
	}

	if totalBytes > 0 {
		remaining := totalBytes - usedBytes
		if remaining < 0 {
			remaining = 0
		}
		res.RemainingBytes = remaining
		res.UsagePercent = float64(usedBytes) * 100 / float64(totalBytes)
		if res.UsagePercent > 100 {
			res.UsagePercent = 100
		}
		return res, nil
	}

	res.RemainingBytes = 0
	return res, nil
}

func (s *StorageService) resolveQuotaBytes(ctx context.Context, uid int64) (int64, string, bool, error) {
	userKey := "storage_quota_bytes_user_" + strconv.FormatInt(uid, 10)
	if quota, ok, err := s.readPositiveInt64Setting(ctx, userKey); err != nil {
		return 0, "", false, err
	} else if ok {
		return quota, userKey, true, nil
	}

	if quota, ok, err := s.readPositiveInt64Setting(ctx, globalStorageQuotaSettingKey); err != nil {
		return 0, "", false, err
	} else if ok {
		return quota, globalStorageQuotaSettingKey, true, nil
	}

	if quota, ok, err := s.readPositiveInt64Setting(ctx, defaultStorageQuotaSettingKey); err != nil {
		return 0, "", false, err
	} else if ok {
		return quota, defaultStorageQuotaSettingKey, true, nil
	}

	return 0, "none", false, nil
}

func (s *StorageService) readPositiveInt64Setting(ctx context.Context, key string) (int64, bool, error) {
	raw, err := s.repo.GetSystemSettingValue(ctx, key)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return 0, false, nil
		}
		return 0, false, err
	}

	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value <= 0 {
		return 0, false, nil
	}
	return value, true, nil
}
