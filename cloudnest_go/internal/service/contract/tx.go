package contract

import (
	"context"

	"gorm.io/gorm"
)

// TxRunner 统一事务入口，供 service 层注入使用。
type TxRunner interface {
	WithinTx(ctx context.Context, fn func(tx *gorm.DB) error) error
}
