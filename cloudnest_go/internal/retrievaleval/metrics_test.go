package retrievaleval

import (
	"math"
	"testing"
)

func TestMetricsRankDenominatorsAndMissingHits(t *testing.T) {
	m, err := Measure([]string{"a", "b", "c", "d"}, []string{"x", "a", "y", "b", "z", "c"})
	if err != nil || m != (Metrics{0, .25, .5, .4, .5}) {
		t.Fatal(m, err)
	}
	m, err = Measure([]string{"a"}, []string{"a"})
	if err != nil || m != (Metrics{1, 1, 1, .2, 1}) {
		t.Fatal(m, err)
	}
	m, err = Measure([]string{"a"}, nil)
	if err != nil || m != (Metrics{}) {
		t.Fatal(m, err)
	}
	m, err = Measure([]string{"a"}, []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "a"})
	if err != nil || m != (Metrics{}) {
		t.Fatal("rank 11 should not receive MRR credit", m, err)
	}
}

func TestMetricsRejectDuplicateAndEmptyIDs(t *testing.T) {
	for _, tc := range []struct{ labels, ranked []string }{
		{nil, nil}, {[]string{"a", "a"}, nil}, {[]string{""}, nil},
		{[]string{"a"}, []string{"a", "a"}}, {[]string{"a"}, []string{""}},
	} {
		if _, err := Measure(tc.labels, tc.ranked); err == nil {
			t.Fatal(tc)
		}
	}
}

func TestSummarySeparatesNegativesAndMacroAverages(t *testing.T) {
	var s Summary
	for _, tc := range []struct{ labels, ranked []string }{
		{[]string{"a"}, []string{"a"}}, {[]string{"b", "c"}, []string{"b"}},
		{nil, nil}, {nil, []string{"unrelated"}},
	} {
		if err := s.Add(tc.labels, tc.ranked); err != nil {
			t.Fatal(err)
		}
	}
	if s.PositiveCount != 2 || s.NegativeCount != 2 || s.NegativeEmptyRate != .5 || math.Abs(s.PositiveMacro.Recall5-.75) > 1e-12 || s.PositiveMacro.MRR10 != 1 {
		t.Fatal(s)
	}
	before := s
	if err := s.Add(nil, []string{"x", "x"}); err == nil || s != before {
		t.Fatal("invalid query altered summary", s)
	}
}
