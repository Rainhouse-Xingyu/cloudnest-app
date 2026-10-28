package retrievaleval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
)

type Document struct {
	ID        string `json:"id"`
	Extension string `json:"extension"`
	Owner     string `json:"owner"`
	Uploaded  string `json:"uploaded"`
	Text      string `json:"text,omitempty"`
	Fixture   string `json:"fixture,omitempty"`
	Capture   string `json:"capture,omitempty"`
	ShareRead bool   `json:"share_read,omitempty"`
}
type Query struct {
	ID       string   `json:"id"`
	Family   string   `json:"family"`
	Split    string   `json:"split"`
	Question string   `json:"question"`
	Relevant []string `json:"relevant"`
}
type Gates struct {
	Recall5           float64 `json:"positive_macro_recall_at_5"`
	MRR10             float64 `json:"positive_mrr_at_10"`
	NegativeEmptyRate float64 `json:"negative_empty_rate"`
}
type Dataset struct {
	Version     int        `json:"version"`
	Description string     `json:"description"`
	Documents   []Document `json:"documents"`
	Queries     []Query    `json:"queries"`
	Gates       Gates      `json:"quality_gates"`
}

func Parse(raw []byte) (Dataset, error) {
	var d Dataset
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return d, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return d, fmt.Errorf("trailing JSON")
	}
	if d.Version != 1 || len(d.Documents) == 0 || len(d.Documents) > 100 || len(d.Queries) == 0 {
		return d, fmt.Errorf("invalid dataset size/version")
	}
	for _, g := range []float64{d.Gates.Recall5, d.Gates.MRR10, d.Gates.NegativeEmptyRate} {
		if g <= 0 || g > 1 {
			return d, fmt.Errorf("invalid gate")
		}
	}
	ids := map[string]Document{}
	validID := regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	for _, doc := range d.Documents {
		if _, exists := ids[doc.ID]; exists || !validID.MatchString(doc.ID) {
			return d, fmt.Errorf("invalid/duplicate document %q", doc.ID)
		}
		if doc.Owner != "alice" && doc.Owner != "bob" {
			return d, fmt.Errorf("invalid owner")
		}
		if doc.Uploaded != "current" && doc.Uploaded != "previous_month" && doc.Uploaded != "two_months_ago" {
			return d, fmt.Errorf("invalid upload calendar")
		}
		if doc.Capture != "" && (doc.Extension != "png" || (doc.Capture != "previous_year" && doc.Capture != "current_year")) {
			return d, fmt.Errorf("invalid capture calendar")
		}
		if !regexp.MustCompile(`^[a-z0-9]{1,8}$`).MatchString(doc.Extension) || (doc.Text == "") == (doc.Fixture == "") {
			return d, fmt.Errorf("invalid source")
		}
		if doc.Fixture != "" && (path.IsAbs(doc.Fixture) || path.Clean(doc.Fixture) != doc.Fixture || strings.HasPrefix(doc.Fixture, "../") || strings.Contains(doc.Fixture, "\\") || path.Ext(doc.Fixture) != "."+doc.Extension) {
			return d, fmt.Errorf("invalid fixture path")
		}
		ids[doc.ID] = doc
	}
	queries := map[string]bool{}
	counts := map[string][2]int{}
	for _, q := range d.Queries {
		if queries[q.ID] || !validID.MatchString(q.ID) || q.Question == "" || len(q.Question) > 4096 || (q.Split != "dev" && q.Split != "heldout") || !validID.MatchString(q.Family) {
			return d, fmt.Errorf("invalid query %q", q.ID)
		}
		queries[q.ID] = true
		labels := map[string]bool{}
		for _, id := range q.Relevant {
			doc, exists := ids[id]
			if !exists || labels[id] || (doc.Owner != "alice" && !doc.ShareRead) {
				return d, fmt.Errorf("invalid label %q", id)
			}
			labels[id] = true
		}
		c := counts[q.Split]
		if len(q.Relevant) == 0 {
			c[1]++
		} else {
			c[0]++
		}
		counts[q.Split] = c
	}
	for _, split := range []string{"dev", "heldout"} {
		c := counts[split]
		if c[0] == 0 || c[1] == 0 {
			return d, fmt.Errorf("split %s needs positives and negatives", split)
		}
	}
	return d, nil
}

func (g Gates) Pass(s Summary) bool {
	return s.PositiveCount > 0 && s.NegativeCount > 0 && s.PositiveMacro.Recall5+1e-12 >= g.Recall5 && s.PositiveMacro.MRR10+1e-12 >= g.MRR10 && s.NegativeEmptyRate+1e-12 >= g.NegativeEmptyRate
}
