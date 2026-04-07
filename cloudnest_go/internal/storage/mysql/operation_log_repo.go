package mysql

import (
	"context"

	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"

	"gorm.io/gorm"
)

// OperationLogRepo 提供操作日志落库实现。
type OperationLogRepo struct {
	db *gorm.DB
}

func NewOperationLogRepo(db *gorm.DB) *OperationLogRepo {
	return &OperationLogRepo{db: db}
}

// Log 将审计日志写入 operation_log 表。
func (r *OperationLogRepo) Log(ctx context.Context, req contract.AuditLogRequest) error {
	row := model.OperationLog{
		UserID:   req.UserID,
		FileID:   req.FileID,
		Action:   req.Action,
		Status:   req.Status,
		Detail:   req.Detail,
		ClientIP: req.ClientIP,
	}
	return r.db.WithContext(ctx).Create(&row).Error
}

var _ contract.AuditLogger = (*OperationLogRepo)(nil)
