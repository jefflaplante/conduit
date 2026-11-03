package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegressionFlags(t *testing.T) {
	mk := func(ids map[string]bool) *Summary {
		s := &Summary{}
		for id, passed := range ids {
			s.PerCase = append(s.PerCase, CaseResult{CaseID: id, Passed: passed})
		}
		s.TotalCases = len(s.PerCase)
		for _, r := range s.PerCase {
			if r.Passed {
				s.PassedCases++
			}
		}
		return s
	}
	tests := []struct {
		name      string
		curScore  float64
		baseScore float64
		cur       map[string]bool
		base      map[string]bool
		wantN     int
		wantSub   []string
	}{
		{
			name: "no regression", curScore: 0.90, baseScore: 0.90,
			cur: map[string]bool{"a": true}, base: map[string]bool{"a": true},
			wantN: 0,
		},
		{
			name: "big drop flags", curScore: 0.80, baseScore: 0.90,
			cur: map[string]bool{"a": true}, base: map[string]bool{"a": true},
			wantN: 1, wantSub: []string{"weighted score dropped"},
		},
		{
			name: "exactly 5pct boundary flags (strict >)", curScore: 0.855, baseScore: 0.90,
			cur: map[string]bool{"a": true}, base: map[string]bool{"a": true},
			wantN: 1, wantSub: []string{"weighted score dropped"},
		},
		{
			name: "just under 5pct does not flag", curScore: 0.856, baseScore: 0.90,
			cur: map[string]bool{"a": true}, base: map[string]bool{"a": true},
			wantN: 0,
		},
		{
			name: "improvement does not flag", curScore: 0.95, baseScore: 0.90,
			cur: map[string]bool{"a": true}, base: map[string]bool{"a": true},
			wantN: 0,
		},
		{
			name: "case flip flags", curScore: 0.90, baseScore: 0.90,
			cur: map[string]bool{"a": true, "b": false}, base: map[string]bool{"a": true, "b": true},
			wantN: 1, wantSub: []string{"case b"},
		},
		{
			name: "drop and flip both flag", curScore: 0.70, baseScore: 0.90,
			cur: map[string]bool{"a": true, "b": false}, base: map[string]bool{"a": true, "b": true},
			wantN: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cur := mk(tt.cur)
			base := mk(tt.base)
			cur.WeightedScore = tt.curScore
			base.WeightedScore = tt.baseScore
			flags := RegressionFlags(cur, base)
			if len(flags) != tt.wantN {
				t.Fatalf("flags = %v, want %d", flags, tt.wantN)
			}
			for i, sub := range tt.wantSub {
				if i >= len(flags) || !strings.Contains(flags[i], sub) {
					t.Errorf("flags[%d] = %q, want substring %q", i, flags, sub)
				}
			}
		})
	}
}

func TestRegressionFlagsNilBaseline(t *testing.T) {
	if got := RegressionFlags(&Summary{}, nil); got != nil {
		t.Errorf("nil baseline should give no flags, got %v", got)
	}
}

func TestResultsRoundTrip(t *testing.T) {
	cases, err := LoadCorpus("testdata/corpus")
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	sum, err := (&Runner{Judge: StubJudge{}}).Run(cases)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "results.jsonl")
	if err := WriteResultsFile(path, sum); err != nil {
		t.Fatalf("WriteResultsFile: %v", err)
	}
	loaded, err := LoadSummaryJSONL(path)
	if err != nil {
		t.Fatalf("LoadSummaryJSONL: %v", err)
	}
	if loaded.TotalCases != sum.TotalCases {
		t.Errorf("roundtrip cases = %d, want %d", loaded.TotalCases, sum.TotalCases)
	}
	if loaded.PassedCases != sum.PassedCases {
		t.Errorf("roundtrip passed = %d, want %d", loaded.PassedCases, sum.PassedCases)
	}
	// Weighted score must be identical to 9 decimals after recompute.
	if abs(loaded.WeightedScore-sum.WeightedScore) > 1e-9 {
		t.Errorf("roundtrip weighted = %v, want %v", loaded.WeightedScore, sum.WeightedScore)
	}
	for i := range sum.PerCase {
		if loaded.PerCase[i].CaseID != sum.PerCase[i].CaseID {
			t.Errorf("roundtrip case %d = %s", i, loaded.PerCase[i].CaseID)
		}
	}

	// Baseline comparison of a run against itself flags nothing.
	if flags := RegressionFlags(sum, loaded); len(flags) != 0 {
		t.Errorf("self-comparison flagged: %v", flags)
	}
}

func TestFormatSummaryMarkdown(t *testing.T) {
	cases, err := LoadCorpus("testdata/corpus")
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	sum, err := (&Runner{Judge: StubJudge{}}).Run(cases)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	md := FormatSummaryMarkdown(sum, []string{"test flag"})
	for _, want := range []string{"Weighted score", "Per-case results", "ev-0001", "Regression flags", "test flag"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q", want)
		}
	}
}

func TestLoadSummaryJSONL_BadFile(t *testing.T) {
	if _, err := LoadSummaryJSONL(filepath.Join(t.TempDir(), "missing.jsonl")); err == nil {
		t.Error("missing file should error")
	}
	bad := filepath.Join(t.TempDir(), "bad.jsonl")
	if err := os.WriteFile(bad, []byte("{not json}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSummaryJSONL(bad); err == nil {
		t.Error("bad jsonl should error")
	}
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
