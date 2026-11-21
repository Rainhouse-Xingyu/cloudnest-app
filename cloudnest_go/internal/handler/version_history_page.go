package handler

import (
	"net/url"
	"strconv"

	"cloudenest_go/internal/service/contract"
)

func parseVersionHistoryPage(raw string) (*contract.VersionHistoryPage, error) {
	if raw == "" {
		return nil, nil // The legacy response remains complete and unpaged.
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return nil, contract.ErrInvalidInput
	}
	page := &contract.VersionHistoryPage{}
	fields := map[string]*int{"page_size": &page.Size, "before_version": &page.Before, "after_version": &page.After, "snapshot_version": &page.Snapshot}
	for key, entries := range values {
		target, ok := fields[key]
		if !ok || len(entries) != 1 {
			return nil, contract.ErrInvalidInput
		}
		n, err := strconv.Atoi(entries[0])
		if err != nil || n <= 0 || strconv.Itoa(n) != entries[0] {
			return nil, contract.ErrInvalidInput
		}
		*target = n
	}
	if err := page.Validate(); err != nil {
		return nil, err
	}
	return page, nil
}
