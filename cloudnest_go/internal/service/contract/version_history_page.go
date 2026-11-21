package contract

// VersionHistoryPage selects one bounded page. A continuation is tied to the
// current-version value seen on the first page, never an authorization grant.
type VersionHistoryPage struct {
	Size, Before, After, Snapshot int
}

func (p VersionHistoryPage) Validate() error {
	if p.Size < 1 || p.Size > 100 || p.Before < 0 || p.After < 0 || p.Snapshot < 0 ||
		p.Before > 2147483647 || p.After > 2147483647 || p.Snapshot > 2147483647 ||
		(p.Before > 0 && p.After > 0) ||
		((p.Before > 0 || p.After > 0) && (p.Snapshot == 0 || p.Before > p.Snapshot || p.After > p.Snapshot)) ||
		(p.Before == 0 && p.After == 0 && p.Snapshot != 0) {
		return ErrInvalidInput
	}
	return nil
}

type VersionHistoryPageResult struct {
	Size     int  `json:"size"`
	Snapshot int  `json:"snapshot_version"`
	HasOlder bool `json:"has_older"`
	HasNewer bool `json:"has_newer"`
}
