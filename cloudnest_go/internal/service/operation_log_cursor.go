package service

import (
	"cloudenest_go/internal/service/contract"
	"context"
)

func (s *FileService) ListOperationLogCursor(ctx context.Context, uid int64, isAdmin bool, q contract.OperationLogCursorQuery) (contract.OperationLogCursorPage, error) {
	return s.fileRepo.ListOperationLogCursor(ctx, uid, isAdmin, q)
}
