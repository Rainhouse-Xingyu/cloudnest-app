package service

import (
	"cloudenest_go/internal/service/contract"
	"context"
)

type StorageService struct{ repo contract.StorageRepository }

func NewStorageService(repo contract.StorageRepository) *StorageService {
	return &StorageService{repo: repo}
}

type StorageOverview struct {
	UsedBytes               int64   `json:"used_bytes"`
	TotalBytes              int64   `json:"total_bytes"`
	RemainingBytes          int64   `json:"remaining_bytes"`
	UsagePercent            float64 `json:"usage_percent"`
	HasQuota                bool    `json:"has_quota"`
	QuotaSource             string  `json:"quota_source"`
	ReservedBytes           int64   `json:"reserved_bytes"`
	AvailableForUploadBytes int64   `json:"available_for_upload_bytes"`
	ActiveBytes             int64   `json:"active_bytes"`
	RecycleBytes            int64   `json:"recycle_bytes"`
	HistoryBytes            int64   `json:"history_bytes"`
	UnmeteredReferences     int64   `json:"unmetered_references"`
	AccountingPolicy        string  `json:"accounting_policy"`
}

func (s *StorageService) GetOverview(ctx context.Context, uid int64) (StorageOverview, error) {
	u, err := s.repo.GetStorageUsage(ctx, uid)
	if err != nil {
		return StorageOverview{}, err
	}
	r := StorageOverview{UsedBytes: u.UsedBytes, TotalBytes: u.LimitBytes, HasQuota: u.LimitBytes > 0, QuotaSource: u.QuotaSource, ReservedBytes: u.ReservedBytes, ActiveBytes: u.ActiveBytes, RecycleBytes: u.RecycleBytes, HistoryBytes: u.HistoryBytes, UnmeteredReferences: u.UnmeteredRefs, AccountingPolicy: "retained_file_objects_v1"}
	if u.LimitBytes > 0 {
		r.RemainingBytes = u.LimitBytes - u.UsedBytes
		if r.RemainingBytes < 0 {
			r.RemainingBytes = 0
		}
		r.AvailableForUploadBytes = r.RemainingBytes - u.ReservedBytes
		if r.AvailableForUploadBytes < 0 {
			r.AvailableForUploadBytes = 0
		}
		r.UsagePercent = float64(u.UsedBytes) * 100 / float64(u.LimitBytes)
		if r.UsagePercent > 100 {
			r.UsagePercent = 100
		}
	}
	return r, nil
}
