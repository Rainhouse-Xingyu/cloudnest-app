package contract

import "context"

// StorageRepository 定义存储容量概览相关的数据访问能力。
type StorageRepository interface {
	GetUserUsedStorageBytes(ctx context.Context, userID int64) (int64, error)
	GetSystemSettingValue(ctx context.Context, key string) (string, error)
}
