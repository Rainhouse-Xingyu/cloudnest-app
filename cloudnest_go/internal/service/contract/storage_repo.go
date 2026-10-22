package contract

import "context"

// StorageRepository 定义存储容量概览相关的数据访问能力。
type StorageRepository interface {
	GetStorageUsage(ctx context.Context, userID int64) (StorageUsage, error)
	GetSystemSettingValue(ctx context.Context, key string) (string, error)
}

type StorageUsage struct {
	UsedBytes     int64
	ReservedBytes int64
	ActiveBytes   int64
	RecycleBytes  int64
	HistoryBytes  int64
	UnmeteredRefs int64
	BrokenRefs    int64
	LimitBytes    int64
	QuotaSource   string
}
