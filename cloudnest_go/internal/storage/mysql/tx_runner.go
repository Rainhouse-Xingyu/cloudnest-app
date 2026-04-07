package mysql

import (
	"context"

	"gorm.io/gorm"
)

// TxRunner 基于 GORM 实现 service 事务接口。
type TxRunner struct {
	db *gorm.DB
}

func NewTxRunner(db *gorm.DB) *TxRunner {
	return &TxRunner{db: db}
}

func (r *TxRunner) WithinTx(ctx context.Context, fn func(tx *gorm.DB) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(tx)
	})
}
