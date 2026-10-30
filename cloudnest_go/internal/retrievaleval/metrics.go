// Package retrievaleval measures ranked retrieval against fixed file-level labels.
// It is used by the opt-in real-model benchmark, never by production ranking.
package retrievaleval

import "fmt"

type Metrics struct {
	Recall1    float64 `json:"recall_at_1"`
	Recall3    float64 `json:"recall_at_3"`
	Recall5    float64 `json:"recall_at_5"`
	Precision5 float64 `json:"precision_at_5"`
	MRR10      float64 `json:"mrr_at_10"`
}

// Measure excludes no-answer queries: their empty-result rate is measured
// separately. Precision always uses five slots, including unfilled slots.
func Measure(relevant, ranked []string) (Metrics, error) {
	var m Metrics
	labels := make(map[string]bool)
	for _, id := range relevant {
		if id == "" || labels[id] {
			return m, fmt.Errorf("invalid or duplicate label %q", id)
		}
		labels[id] = true
	}
	seen := make(map[string]bool)
	for i, id := range ranked {
		if id == "" || seen[id] {
			return Metrics{}, fmt.Errorf("invalid or duplicate result %q", id)
		}
		seen[id] = true
		if !labels[id] {
			continue
		}
		if i < 1 {
			m.Recall1++
		}
		if i < 3 {
			m.Recall3++
		}
		if i < 5 {
			m.Recall5++
			m.Precision5++
		}
		if i < 10 && m.MRR10 == 0 {
			m.MRR10 = 1 / float64(i+1)
		}
	}
	if len(labels) == 0 {
		return m, fmt.Errorf("no-answer query requires empty-result evaluation")
	}
	m.Recall1 /= float64(len(labels))
	m.Recall3 /= float64(len(labels))
	m.Recall5 /= float64(len(labels))
	m.Precision5 /= 5
	return m, nil
}

type Summary struct {
	PositiveCount      int     `json:"positive_count"`
	NegativeCount      int     `json:"negative_count"`
	NegativeEmptyCount int     `json:"negative_empty_count"`
	NegativeEmptyRate  float64 `json:"negative_empty_rate"`
	PositiveMacro      Metrics `json:"positive_macro"`
}

func (s *Summary) Add(relevant, ranked []string) error {
	if len(relevant) == 0 {
		// Still validate results, even for no-answer queries.
		seen := make(map[string]bool)
		for _, id := range ranked {
			if id == "" || seen[id] {
				return fmt.Errorf("invalid or duplicate result %q", id)
			}
			seen[id] = true
		}
		s.NegativeCount++
		if len(ranked) == 0 {
			s.NegativeEmptyCount++
		}
		s.NegativeEmptyRate = float64(s.NegativeEmptyCount) / float64(s.NegativeCount)
		return nil
	}
	m, err := Measure(relevant, ranked)
	if err != nil {
		return err
	}
	n := float64(s.PositiveCount)
	mean := func(a, b float64) float64 { return (a*n + b) / (n + 1) }
	s.PositiveMacro = Metrics{mean(s.PositiveMacro.Recall1, m.Recall1), mean(s.PositiveMacro.Recall3, m.Recall3), mean(s.PositiveMacro.Recall5, m.Recall5), mean(s.PositiveMacro.Precision5, m.Precision5), mean(s.PositiveMacro.MRR10, m.MRR10)}
	s.PositiveCount++
	return nil
}
