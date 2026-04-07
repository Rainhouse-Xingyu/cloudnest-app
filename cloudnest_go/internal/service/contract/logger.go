package contract

import "context"

// AuditLogger 定义审计日志接口，后续由 storage 层实现。
type AuditLogger interface {
	Log(ctx context.Context, req AuditLogRequest) error
}

// AuditLogRequest 统一审计日志参数。
type AuditLogRequest struct {
	UserID   *int64
	FileID   *int64
	Action   string
	Status   string
	Detail   string
	ClientIP string
}
