package retrievaleval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
)

// PhotoSource records attribution and the original bytes, never model output.
// Observations are fixed by visual inspection before inference and are not sent
// to the indexer. The evaluation uploads neutral names and decoded pixels only.
type PhotoSource struct {
	ID           string `json:"id"`
	PageURL      string `json:"page_url"`
	DownloadURL  string `json:"download_url"`
	Author       string `json:"author"`
	License      string `json:"license"`
	LicenseURL   string `json:"license_url"`
	SHA256       string `json:"sha256"`
	Bytes        int64  `json:"bytes"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	Observations string `json:"observations"`
}

type PhotoDocument struct {
	ID        string `json:"id"`
	Source    string `json:"source"`
	Owner     string `json:"owner"`
	Capture   string `json:"capture,omitempty"`
	ShareRead bool   `json:"share_read,omitempty"`
}

type PhotoDataset struct {
	Version     int             `json:"version"`
	Description string          `json:"description"`
	Sources     []PhotoSource   `json:"sources"`
	Documents   []PhotoDocument `json:"documents"`
	Queries     []Query         `json:"queries"`
	Gates       Gates           `json:"quality_gates"`
}

func ParsePhotos(raw []byte) (PhotoDataset, error) {
	var d PhotoDataset
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return d, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return d, fmt.Errorf("trailing photo JSON")
	}
	if len(d.Sources) < 6 || len(d.Sources) > 16 {
		return d, fmt.Errorf("photo corpus needs 6 to 16 independent sources")
	}
	validID := regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	validHash := regexp.MustCompile(`^[0-9a-f]{64}$`)
	urls := func(s, host string) bool {
		u, err := url.Parse(s)
		return err == nil && u.Scheme == "https" && u.Host == host && u.User == nil && u.RawQuery == "" && u.Fragment == "" && len(u.Path) > 1
	}
	sources := map[string]bool{}
	hashes := map[string]bool{}
	for _, s := range d.Sources {
		if !validID.MatchString(s.ID) || sources[s.ID] || !validHash.MatchString(s.SHA256) || hashes[s.SHA256] || s.Bytes <= 0 || s.Bytes > 16<<20 || s.Width <= 0 || s.Height <= 0 || s.Width > 16384 || s.Height > 16384 || int64(s.Width)*int64(s.Height) > 64_000_000 {
			return d, fmt.Errorf("invalid/duplicate photo source %q", s.ID)
		}
		if !urls(s.PageURL, "commons.wikimedia.org") || !urls(s.DownloadURL, "upload.wikimedia.org") || s.Author == "" || s.License == "" || s.LicenseURL == "" || s.Observations == "" || len(s.Observations) > 4096 {
			return d, fmt.Errorf("missing photo provenance %q", s.ID)
		}
		sources[s.ID], hashes[s.SHA256] = true, true
	}
	// Reuse the text benchmark's strict labels, calendar and split validation.
	common := Dataset{Version: d.Version, Description: d.Description, Queries: d.Queries, Gates: d.Gates}
	used := map[string]bool{}
	for _, doc := range d.Documents {
		if !sources[doc.Source] || (doc.ShareRead && doc.Owner != "bob") {
			return d, fmt.Errorf("invalid photo document source/share %q", doc.ID)
		}
		used[doc.Source] = true
		common.Documents = append(common.Documents, Document{ID: doc.ID, Extension: "png", Owner: doc.Owner, Uploaded: "current", Fixture: "images/" + doc.Source + ".png", Capture: doc.Capture, ShareRead: doc.ShareRead})
	}
	if len(used) != len(sources) {
		return d, fmt.Errorf("unused photo sources")
	}
	encoded, err := json.Marshal(common)
	if err != nil {
		return d, err
	}
	_, err = Parse(encoded)
	return d, err
}
