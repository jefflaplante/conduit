package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mkResult builds a CaseResult with the given knobs — the table shorthand for
// regression fixtures. Zero pages of struct literals per case.
func mkResult(id string, passed bool, goal float64, cost float64, priced bool, checks ...string) CaseResult {
	r := CaseResult{
		CaseID:     id,
		Title:      "t " + id,
		Passed:     passed,
		CostUSD:    cost,
		CostPriced: priced,
		JudgeID:    "test-judge",
		Score:      goal,
	}
	r.Dimensions.Goal = goal
	for _, c := range checks {
		r.Violations = append(r.Violations, Violation{Check: c, Detail: "x"})
	}
	return r
}

// summaryOf assembles a Summary and recomputes aggregates exactly the way
// LoadBaselineFile does, so test summaries compare like loaded ones.
func summaryOf(results ...CaseResult) *Summary {
	s := &Summary{PerCase: results}
	s.TotalCases = len(s.PerCase)
	for i := range s.PerCase {
		if s.PerCase[i].Passed {
			s.PassedCases++
		}
		s.TotalCostUSD += s.PerCase[i].CostUSD
	}
	s.MeanScores = meanScores(s.PerCase)
	s.WeightedScore = weighted(s.MeanScores)
	s.JudgeID = "test-judge"
	return s
}

func TestCheckRegression(t *testing.T) {
	tests := []struct {
		name      string
		cur       *Summary
		base      *Summary
		opts      RegressionOptions
		wantCases []string // "caseID:class" pairs expected, in order
	}{
		{
			name:      "identical runs, no issues",
			cur:       summaryOf(mkResult("a", true, 0.9, 0.01, true)),
			base:      summaryOf(mkResult("a", true, 0.9, 0.01, true)),
			wantCases: nil,
		},
		{
			name:      "improvement, no issues (fail->pass, score up, cost down)",
			cur:       summaryOf(mkResult("a", true, 0.95, 0.005, true)),
			base:      summaryOf(mkResult("a", false, 0.5, 0.02, true)),
			wantCases: nil,
		},
		{
			name: "verdict regression: pass -> fail",
			cur:  summaryOf(mkResult("a", false, 0.9, 0.01, true)),
			base: summaryOf(mkResult("a", true, 0.9, 0.01, true)),
			wantCases: []string{
				"a:verdict",
				// No check flag: the fixture fails with zero violations, so
				// there is no newly failing class — verdict is the only signal.
			},
		},
		{
			name: "score drop exactly at threshold does not flag",
			cur:  summaryOf(mkResult("a", true, 0.80, 0.01, true)),
			base: summaryOf(mkResult("a", true, 0.90, 0.01, true)),
		},
		{
			name:      "score drop beyond threshold flags",
			cur:       summaryOf(mkResult("a", true, 0.75, 0.01, true)),
			base:      summaryOf(mkResult("a", true, 0.90, 0.01, true)),
			wantCases: []string{"a:score"},
		},
		{
			name:      "score drop with custom threshold",
			cur:       summaryOf(mkResult("a", true, 0.5, 0.01, true)),
			base:      summaryOf(mkResult("a", true, 0.8, 0.01, true)),
			opts:      RegressionOptions{ScoreDrop: 0.2},
			wantCases: []string{"a:score"},
		},
		{
			name:      "check class newly failing without verdict flip is still caught",
			cur:       summaryOf(mkResult("a", true, 0.9, 0.01, true, "fragments")),
			base:      summaryOf(mkResult("a", true, 0.9, 0.01, true)),
			wantCases: []string{"a:check"},
		},
		{
			name:      "check already failing at baseline carries no new signal",
			cur:       summaryOf(mkResult("a", true, 0.9, 0.01, true, "fragments")),
			base:      summaryOf(mkResult("a", true, 0.9, 0.01, true, "fragments")),
			wantCases: nil,
		},
		{
			name:      "check class resolved is not a regression",
			cur:       summaryOf(mkResult("a", true, 0.9, 0.01, true)),
			base:      summaryOf(mkResult("a", true, 0.9, 0.01, true, "fragments")),
			wantCases: nil,
		},
		{
			name:      "cost regression beyond +50% (priced both sides)",
			cur:       summaryOf(mkResult("a", true, 0.9, 0.016, true)),
			base:      summaryOf(mkResult("a", true, 0.9, 0.010, true)),
			wantCases: []string{"a:cost"},
		},
		{
			name: "cost increase within threshold does not flag",
			cur:  summaryOf(mkResult("a", true, 0.9, 0.012, true)),
			base: summaryOf(mkResult("a", true, 0.9, 0.010, true)),
		},
		{
			name: "cost jump on unpriced side is not compared",
			cur:  summaryOf(mkResult("a", true, 0.9, 9.99, false)),
			base: summaryOf(mkResult("a", true, 0.9, 0.01, true)),
		},
		{
			name: "cost comparison skipped when baseline cost is zero (division guard)",
			cur:  summaryOf(mkResult("a", true, 0.9, 5.0, true)),
			base: summaryOf(mkResult("a", true, 0.9, 0.0, true)),
		},
		{
			name:      "case missing from current run flags coverage",
			cur:       summaryOf(mkResult("b", true, 0.9, 0.01, true)),
			base:      summaryOf(mkResult("a", true, 0.9, 0.01, true)),
			wantCases: []string{"a:coverage", "b:coverage"}, // a gone, b new
		},
		{
			name:      "case new in current run flags coverage",
			cur:       summaryOf(mkResult("a", true, 0.9, 0.01, true), mkResult("b", true, 0.9, 0.01, true)),
			base:      summaryOf(mkResult("a", true, 0.9, 0.01, true)),
			wantCases: []string{"b:coverage"},
		},
		{
			name: "multiple cases sort hard-first then by ID",
			cur: summaryOf(
				mkResult("c", true, 0.5, 0.01, true),
				mkResult("a", false, 0.9, 0.01, true, "deny_class"),
				mkResult("b", true, 0.9, 0.99, true),
			),
			base: summaryOf(
				mkResult("c", true, 0.9, 0.01, true),
				mkResult("a", true, 0.9, 0.01, true),
				mkResult("b", true, 0.9, 0.01, true),
			),
			wantCases: []string{
				"a:verdict", "a:check",
				"c:score", // soft class: score ranks above cost
				"b:cost",
			},
		},
		{
			name: "one issue across all four classes on a single case",
			cur: summaryOf(
				mkResult("a", false, 0.5, 0.05, true, "deny_class"),
			),
			base: summaryOf(
				mkResult("a", true, 0.9, 0.01, true),
			),
			wantCases: []string{
				"a:verdict", "a:check", "a:score", "a:cost",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CheckRegression(tt.cur, tt.base, tt.opts)
			gotPairs := make([]string, 0, len(got))
			for _, is := range got {
				gotPairs = append(gotPairs, is.CaseID+":"+is.Class)
			}
			if len(gotPairs) == 0 && len(tt.wantCases) == 0 {
				return
			}
			if strings.Join(gotPairs, ",") != strings.Join(tt.wantCases, ",") {
				t.Errorf("issues = %v, want %v", gotPairs, tt.wantCases)
			}
			// Every issue must classify as hard or soft.
			for _, is := range got {
				if sev := is.Severity(); sev != "hard" && sev != "soft" {
					t.Errorf("issue %v has invalid severity %q", is, sev)
				}
			}
		})
	}
}

func TestCheckRegressionNilAndEmpty(t *testing.T) {
	s := summaryOf(mkResult("a", true, 0.9, 0.01, true))
	if got := CheckRegression(s, nil, RegressionOptions{}); len(got) != 0 {
		t.Errorf("nil baseline must return no issues, got %v", got)
	}
	if got := CheckRegression(nil, s, RegressionOptions{}); len(got) != 0 {
		t.Errorf("nil current must return no issues, got %v", got)
	}
}

func TestRegressionExitCode(t *testing.T) {
	if got := RegressionExitCode(nil); got != 0 {
		t.Errorf("no issues = exit %d, want 0", got)
	}
	if got := RegressionExitCode([]RegressionIssue{{CaseID: "a", Class: "score", Detail: "d"}}); got != 1 {
		t.Errorf("one issue = exit %d, want 1", got)
	}
}

func TestBaselineSaveLoadRoundTrip(t *testing.T) {
	// A realistic runner output: several cases, mixed pass/fail, pricing and
	// judge fields populated.
	sum := summaryOf(
		mkResult("ev-0001", true, 0.9, 0.010001, true),
		mkResult("ev-0002", true, 0.5, 0.0, true),
		mkResult("ev-0003", false, 0.7, 0.02, false, "deny_class"),
	)
	sum.GeneratedAt = mustTime(t, "2026-10-18T12:00:00Z")
	path := filepath.Join(t.TempDir(), "baseline.jsonl")

	if err := WriteBaselineFile(path, sum); err != nil {
		t.Fatalf("WriteBaselineFile: %v", err)
	}
	got, err := LoadBaselineFile(path)
	if err != nil {
		t.Fatalf("LoadBaselineFile: %v", err)
	}
	if len(got.PerCase) != len(sum.PerCase) {
		t.Fatalf("round trip lost records: %d != %d", len(got.PerCase), len(sum.PerCase))
	}
	for i := range sum.PerCase {
		want, have := sum.PerCase[i], got.PerCase[i]
		if want.CaseID != have.CaseID || want.Passed != have.Passed ||
			want.Dimensions.Goal != have.Dimensions.Goal || want.CostUSD != have.CostUSD ||
			want.CostPriced != have.CostPriced || want.JudgeID != have.JudgeID ||
			len(want.Violations) != len(have.Violations) {
			t.Errorf("case %d changed across round trip:\n want %+v\n got  %+v", i, want, have)
		}
	}
	// Aggregates rebuild identically.
	if got.PassedCases != sum.PassedCases || got.TotalCases != sum.TotalCases {
		t.Errorf("counts: got %d/%d, want %d/%d", got.PassedCases, got.TotalCases, sum.PassedCases, sum.TotalCases)
	}
	if got.WeightedScore != sum.WeightedScore {
		t.Errorf("WeightedScore: got %v, want %v", got.WeightedScore, sum.WeightedScore)
	}
}

// mustTime parses an RFC3339 time or fails the test.
func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return ts
}

func TestLoadBaselineFileErrors(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		_, err := LoadBaselineFile(filepath.Join(t.TempDir(), "nope.jsonl"))
		if err == nil || !os.IsNotExist(err) {
			t.Errorf("want not-exist error, got %v", err)
		}
	})
	t.Run("malformed JSON line", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bad.jsonl")
		if err := os.WriteFile(path, []byte("{\"case_id\": \"a\", \"passed\": true}\nnot json\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := LoadBaselineFile(path)
		if err == nil || !strings.Contains(err.Error(), "line 2") {
			t.Errorf("want line-numbered error, got %v", err)
		}
	})
	t.Run("empty case_id rejected", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "noid.jsonl")
		if err := os.WriteFile(path, []byte("{\"passed\": true}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := LoadBaselineFile(path)
		if err == nil || !strings.Contains(err.Error(), "empty case_id") {
			t.Errorf("want empty-case_id error, got %v", err)
		}
	})
	t.Run("zero records rejected", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "empty.jsonl")
		if err := os.WriteFile(path, []byte("\n\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := LoadBaselineFile(path)
		if err == nil || !strings.Contains(err.Error(), "no case records") {
			t.Errorf("want no-records error, got %v", err)
		}
	})
}

func TestWriteBaselineFileCreatesDirsAndIsAtomic(t *testing.T) {
	sum := summaryOf(mkResult("a", true, 0.9, 0.01, true))
	path := filepath.Join(t.TempDir(), "nested", "dir", "baseline.jsonl")
	if err := WriteBaselineFile(path, sum); err != nil {
		t.Fatalf("WriteBaselineFile: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("baseline missing after write: %v", err)
	}
	// No temp litter left behind in the target directory.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// TestBaselineVersusFreshRun is the end-to-end shape: save a baseline from
// one corpus run, re-run identically, expect a clean check; then degrade the
// judge and expect the score-drop flag.
func TestBaselineVersusFreshRun(t *testing.T) {
	cases, err := LoadCorpus("testdata/corpus")
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	runner := &Runner{Judge: fixedJudge{0.9}}
	base, err := runner.Run(cases)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	path := filepath.Join(t.TempDir(), "baseline.jsonl")
	if err := WriteBaselineFile(path, base); err != nil {
		t.Fatalf("WriteBaselineFile: %v", err)
	}

	// Identical re-run: no regressions.
	loaded, err := LoadBaselineFile(path)
	if err != nil {
		t.Fatalf("LoadBaselineFile: %v", err)
	}
	fresh, err := runner.Run(cases)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if issues := CheckRegression(fresh, loaded, RegressionOptions{}); len(issues) != 0 {
		t.Errorf("identical re-run flagged %d issues: %+v", len(issues), issues)
	}

	// Degraded judge: every case drops 0.9 -> 0.6 — score regressions, plus
	// verdict flips on the negative fixtures (they failed at baseline too, so
	// no verdict flags; only the score class fires).
	runner2 := &Runner{Judge: fixedJudge{0.6}}
	worse, err := runner2.Run(cases)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	issues := CheckRegression(worse, loaded, RegressionOptions{})
	if len(issues) == 0 {
		t.Fatal("degraded judge flagged nothing")
	}
	scoreFlags := 0
	for _, is := range issues {
		if is.Class == "score" {
			scoreFlags++
		}
	}
	if scoreFlags != 12 {
		t.Errorf("score flags = %d, want 12 (one per case)", scoreFlags)
	}
}

// TestEndToEndRegressionFromRunnerOutput exercises the full save -> load ->
// compare path over results that differ in verdict, check, score, and cost.
func TestEndToEndRegressionFromRunnerOutput(t *testing.T) {
	cases, err := LoadCorpus("testdata/corpus")
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	base, err := (&Runner{Judge: fixedJudge{0.9}}).Run(cases)
	if err != nil {
		t.Fatalf("Run baseline: %v", err)
	}
	path := filepath.Join(t.TempDir(), "baseline.jsonl")
	if err := WriteBaselineFile(path, base); err != nil {
		t.Fatalf("WriteBaselineFile: %v", err)
	}
	loaded, err := LoadBaselineFile(path)
	if err != nil {
		t.Fatalf("LoadBaselineFile: %v", err)
	}
	// Same corpus, judge unchanged, nothing differs beyond ScoredAt.
	cur, err := (&Runner{Judge: fixedJudge{0.9}}).Run(cases)
	if err != nil {
		t.Fatalf("Run current: %v", err)
	}
	if issues := CheckRegression(cur, loaded, RegressionOptions{}); len(issues) != 0 {
		t.Errorf("same-corpus re-run flagged: %+v", issues)
	}
	// The report must render cleanly for both outcomes.
	if out := FormatRegressionMarkdown(cur, loaded, nil); !strings.Contains(out, "No regressions detected") {
		t.Errorf("clean report missing clean line:\n%s", out)
	}
	bad := CheckRegression(summaryOf(mkResult("ev-0001", false, 0.1, 0.5, true, "deny_class")),
		summaryOf(mkResult("ev-0001", true, 0.9, 0.01, true)), RegressionOptions{})
	if out := FormatRegressionMarkdown(summaryOf(mkResult("ev-0001", false, 0.1, 0.5, true, "deny_class")),
		summaryOf(mkResult("ev-0001", true, 0.9, 0.01, true)), bad); !strings.Contains(out, "regression") {
		t.Errorf("issue report missing regression header:\n%s", out)
	}
}
