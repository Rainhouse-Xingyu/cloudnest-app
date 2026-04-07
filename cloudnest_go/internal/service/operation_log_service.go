package service

import (
	"context"
	"strings"

	"cloudenest_go/internal/service/contract"
)

const (
	maxOperationLogDetailLength = 500
	maxOperationLogIPLength     = 64
)

// OperationLogService 负责统一处理操作日志规则。
type OperationLogService struct {
	logger contract.AuditLogger
}

func NewOperationLogService(logger contract.AuditLogger) *OperationLogService {
	return &OperationLogService{logger: logger}
}

// Log 统一处理字段规范化后写入日志。
func (s *OperationLogService) Log(ctx context.Context, req contract.AuditLogRequest) error {
	req.Action = strings.TrimSpace(req.Action)
	if req.Action == "" {
		return contract.ErrInvalidInput
	}

	req.Status = strings.TrimSpace(req.Status)
	if req.Status == "" {
		req.Status = "success"
	}

	req.Detail = strings.TrimSpace(req.Detail)
	if len(req.Detail) > maxOperationLogDetailLength {
		req.Detail = req.Detail[:maxOperationLogDetailLength]
	}

	req.ClientIP = strings.TrimSpace(req.ClientIP)
	if len(req.ClientIP) > maxOperationLogIPLength {
		req.ClientIP = req.ClientIP[:maxOperationLogIPLength]
	}

	if err := s.logger.Log(ctx, req); err != nil {
		return contract.ErrInternal
	}
	return nil
}

// LogSuccess 快捷记录成功日志。
func (s *OperationLogService) LogSuccess(ctx context.Context, userID, fileID *int64, action, detail, clientIP string) error {
	return s.Log(ctx, contract.AuditLogRequest{
		UserID:   userID,
		FileID:   fileID,
		Action:   action,
		Status:   "success",
		Detail:   detail,
		ClientIP: clientIP,
	})
}

// LogFailure 快捷记录失败日志。
func (s *OperationLogService) LogFailure(ctx context.Context, userID, fileID *int64, action, detail, clientIP string) error {
	return s.Log(ctx, contract.AuditLogRequest{
		UserID:   userID,
		FileID:   fileID,
		Action:   action,
		Status:   "failed",
		Detail:   detail,
		ClientIP: clientIP,
	})
}
