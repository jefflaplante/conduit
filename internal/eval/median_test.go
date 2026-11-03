package eval

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"conduit/internal/ai"
)

// varyingJudge returns scores[callIdx % len(scores)] per JudgeGoal call —
// deterministic across calls but different per call, which is what the
// median tests need. It also implements JudgeUsageReporter with one token
// of usage per call, so sampling-time usage summation is observable.
type varyingJudge struct {
	scores []float64
	calls  int
}

func (v *varyingJudge) JudgeGoal(*Case) (float64, error) {
	s := v.scores[v.calls%len(v.scores)]
	v.calls++
	return s, nil
}
func (v *varyingJudge) ID() string { return "varying-test-judge" }

func TestMedian(t *testing.T) {
	tests := []struct {
		name   string
		scores []float64
		want   float64
	}{
		{"single", []float64{0.7}, 0.7},
		{"odd", []float64{0.9, 0.1, 0.5}, 0.5},
		{"even", []float64{0.9, 0.1}, 0.5},
		{"even-preserves-input-order", []float64{0.1, 0.9, 0.3, 0.7}, 0.5},
		{"unsorted input", []float64{0.2, 0.8, 0.4}, 0.4},
		{"empty", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := append([]float64(nil), tt.scores...)
			if got := median(tt.scores); got != tt.want {
				t.Errorf("median(%v) = %v, want %v", tt.scores, got, tt.want)
			}
			// median must not reorder the caller's slice (GoalScores is
			// the audit trail and must stay in call order).
			for i := range orig {
				if orig[i] != tt.scores[i] {
					t.Fatalf("median mutated input: %v -> %v", orig, tt.scores)
				}
			}
		})
	}
}

func TestRunnerMedianOfN(t *testing.T) {
	// scores 0.9, 0.1, 0.5: median 0.5, not the mean 0.5 — distinguish by
	// an even case: 0.9, 0.1 -> median 0.5 vs mean 0.5... use 0.8, 0.2 ->
	// median 0.5 vs mean 0.5? No: use three values whose median != mean:
	// 0.9, 0.1, 0.5 -> median 0.5, mean 0.5. Use 1.0, 0.0, 0.0: median 0.0
	// vs mean 0.333. And 1.0, 1.0, 0.0: median 1.0 vs mean 0.667.
	for _, tt := range []struct {
		scores []float64
		want   float64
	}{
		{[]float64{1.0, 0.0, 0.0}, 0.0},
		{[]float64{1.0, 1.0, 0.0}, 1.0},
		{[]float64{0.8, 0.2}, 0.5},
	} {
		j := &varyingJudge{scores: tt.scores}
		sum, err := (&Runner{Judge: j, Runs: len(tt.scores)}).Run([]*Case{judgeTestCase()})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		r := sum.PerCase[0]
		if r.Dimensions.Goal != tt.want {
			t.Errorf("median of %v = %v, want %v", tt.scores, r.Dimensions.Goal, tt.want)
		}
		if r.Dimensions.GoalRuns != len(tt.scores) {
			t.Errorf("GoalRuns = %d, want %d", r.Dimensions.GoalRuns, len(tt.scores))
		}
		if len(r.Dimensions.GoalScores) != len(tt.scores) {
			t.Errorf("GoalScores len = %d, want %d (audit trail)", len(r.Dimensions.GoalScores), len(tt.scores))
		}
		if sum.Runs != len(tt.scores) {
			t.Errorf("summary Runs = %d, want %d", sum.Runs, len(tt.scores))
		}
		// The judge was actually called N times.
		if j.calls != len(tt.scores) {
			t.Errorf("judge calls = %d, want %d", j.calls, len(tt.scores))
		}
	}
}

func TestRunnerRunsOneCallPerRun(t *testing.T) {
	// Runs=1 must behave exactly like the pre-v0.3 path: one call, no
	// sample array serialized, GoalRuns omitted from JSON.
	j := &varyingJudge{scores: []float64{0.42}}
	sum, err := (&Runner{Judge: j, Runs: 1}).Run([]*Case{judgeTestCase()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	r := sum.PerCase[0]
	if r.Dimensions.Goal != 0.42 {
		t.Errorf("goal = %v, want 0.42", r.Dimensions.Goal)
	}
	// Contract: sampling metadata only when Runs>1 (single-run JSONL
	// lines stay byte-identical to pre-sampling baselines).
	if r.Dimensions.GoalRuns != 0 {
		t.Errorf("GoalRuns = %d, want 0 (unset for single runs)", r.Dimensions.GoalRuns)
	}
	if r.Dimensions.GoalScores != nil {
		t.Errorf("GoalScores serialized on single run: %v", r.Dimensions.GoalScores)
	}
	if j.calls != 1 {
		t.Errorf("judge calls = %d, want 1", j.calls)
	}
	// JSON omitempty contract: the single-run JSONL line must not carry
	// goal_scores/goal_runs.
	line, _ := json.Marshal(r)
	if strings.Contains(string(line), "goal_scores") || strings.Contains(string(line), "goal_runs") {
		t.Errorf("single-run line carries sampling fields: %s", line)
	}
}

func TestRunnerRunsZeroDefaultsToOne(t *testing.T) {
	j := &varyingJudge{scores: []float64{0.6}}
	sum, err := (&Runner{Judge: j, Runs: 0}).Run([]*Case{judgeTestCase()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sum.PerCase[0].Dimensions.GoalRuns != 0 {
		t.Errorf("GoalRuns = %d, want 0 (Runs=0 means one unsampled call)", sum.PerCase[0].Dimensions.GoalRuns)
	}
	if j.calls != 1 {
		t.Errorf("judge calls = %d, want 1", j.calls)
	}
}

func TestRunnerMedianUsageSumsAcrossRuns(t *testing.T) {
	// varyingJudge reports 0 usage; use the usageProvider-backed LLMJudge
	// with Runs=3 and verify per-case Calls==3 and tokens summed x3.
	p := &usageProvider{
		mockJudgeProvider: mockJudgeProvider{resp: &ai.GenerateResponse{
			Content: `{"score": 0.5, "rationale": "ok"}`,
		}},
		usage: ai.Usage{PromptTokens: 100, CompletionTokens: 10},
	}
	j := &LLMJudge{Provider: p, Model: "claude-sonnet-5"}
	sum, err := (&Runner{Judge: j, Runs: 3}).Run([]*Case{judgeTestCase()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	jv := sum.PerCase[0].JudgeUsage
	if jv.Calls != 3 {
		t.Errorf("calls = %d, want 3 (one per run)", jv.Calls)
	}
	if jv.PromptTokens != 300 || jv.CompletionTokens != 30 {
		t.Errorf("tokens = %d/%d, want 300/30", jv.PromptTokens, jv.CompletionTokens)
	}
	if sum.JudgeUsage.Calls != 3 {
		t.Errorf("summary calls = %d, want 3", sum.JudgeUsage.Calls)
	}
}

func TestRunnerMedianErrorOnSecondRun(t *testing.T) {
	// A judge that succeeds once then fails: run 2/3 must surface with
	// the run index in the error.
	j := &flakyJudge{failOn: 2}
	_, err := (&Runner{Judge: j, Runs: 3}).Run([]*Case{judgeTestCase()})
	if err == nil {
		t.Fatal("second-run failure swallowed")
	}
	if !strings.Contains(err.Error(), "run 2/3") {
		t.Errorf("error missing run index: %v", err)
	}
}

// flakyJudge fails the call numbered failOn (1-based), succeeds otherwise.
type flakyJudge struct {
	failOn int
	calls  int
}

func (f *flakyJudge) JudgeGoal(*Case) (float64, error) {
	f.calls++
	if f.calls == f.failOn {
		return 0, fmt.Errorf("judge exploded")
	}
	return 0.5, nil
}
func (f *flakyJudge) ID() string { return "flaky-test-judge" }
