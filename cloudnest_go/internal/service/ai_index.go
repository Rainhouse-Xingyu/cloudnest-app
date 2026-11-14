package service

import (
	"cloudenest_go/internal/service/contract"
	"context"
	"sort"
)

func (s *AIService) ListIndex(ctx context.Context, uid, after int64, limit int) ([]contract.AIIndexStatus, bool, error) {
	if uid <= 0 || after < 0 || limit < 1 || limit > 100 {
		return nil, false, contract.ErrInvalidInput
	}
	rows, err := s.repo.ListIndex(ctx, uid, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	return rows, more, nil
}

func (s *AIService) IndexStatus(ctx context.Context, uid, id int64) (contract.AIIndexStatus, error) {
	if uid <= 0 || id <= 0 {
		return contract.AIIndexStatus{}, contract.ErrInvalidInput
	}
	return s.repo.IndexStatus(ctx, uid, id)
}

// Rebuild accepts a bounded, explicit set and uses generation CAS. Retrying a
// stale request cannot restart already queued work or invalidate another rebuild.
func (s *AIService) RebuildIndex(ctx context.Context, uid int64, items []contract.AIRebuildItem, ip string) ([]contract.AIRebuildItem, error) {
	if uid <= 0 || len(items) == 0 || len(items) > 100 {
		return nil, contract.ErrInvalidInput
	}
	items = append([]contract.AIRebuildItem(nil), items...)
	sort.Slice(items, func(i, j int) bool { return items[i].FileID < items[j].FileID })
	for i, item := range items {
		if item.FileID <= 0 || item.Generation < 0 || (i > 0 && items[i-1].FileID == item.FileID) {
			return nil, contract.ErrInvalidInput
		}
	}
	return s.repo.RebuildIndex(ctx, uid, items, ip)
}
