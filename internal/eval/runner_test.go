package eval

import (
	"fmt"
	"testing"
)

func TestStubJudge(t *testing.T) {
	tests := []struct {
		name      string
		fragments []string
		assistant string
		want      float64
	}{
		{"all fragments present", []string{"alpha", "beta"}, "alpha and beta here", 1.0},
		{"partial", []string{"alpha", "beta"}, "alpha only", 0.5},
		{"none declared", nil, "anything", 0.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Case{
				ExpectedFragments: tt.fragments,
				Turns:             []Turn{{Turn: 1, Role: "assistant", Content: tt.assistant}},
			}
			got, err := StubJudge{}.JudgeGoal(c)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("score = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestJudgeByName(t *testing.T) {
	if _, err := JudgeByName("stub"); err != nil {
		t.Errorf("stub rejected: %v", err)
	}
	if _, err := JudgeByName(""); err != nil {
		t.Errorf("empty rejected: %v", err)
	}
	if _, err := JudgeByName("llm/gpt-4o"); err == nil {
		t.Error("unknown judge accepted; v0.1 must error rather than fall back")
	}
}

// fixedJudge returns a constant score; used to test runner wiring.
type fixedJudge struct{ score float64 }

func (f fixedJudge) JudgeGoal(*Case) (float64, error) { return f.score, nil }
func (f fixedJudge) ID() string                       { return "fixed-test-judge" }

func TestRunnerHardGateDeny(t *testing.T) {
	// Perfect judge score and clean deterministic dims, but a deny-class
	// violation: case must FAIL. Score reflects the compliance dimension
	// dropping to 0 (violation), but stays high — the gate is what fails it.
	cases := []*Case{{
		ID: "x", Title: "hard gate test",
		DenyClasses: []string{"send"},
		Turns: []Turn{
			{Turn: 1, Role: "assistant", Tools: []ToolCall{{Name: "send_message"}}},
		},
	}}
	sum, err := (&Runner{Judge: fixedJudge{1.0}}).Run(cases)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	r := sum.PerCase[0]
	if r.Passed {
		t.Error("deny violation must fail the case regardless of score")
	}
	if r.Score <= 0.7 {
		t.Errorf("score = %v, want >0.7 (gate affects pass, not the whole score)", r.Score)
	}
	if r.Dimensions.Compliance != 0.0 {
		t.Errorf("compliance = %v, want 0.0 on deny violation", r.Dimensions.Compliance)
	}
	if sum.PassedCases != 0 {
		t.Errorf("PassedCases = %d, want 0", sum.PassedCases)
	}
}

func TestRunnerCleanCase(t *testing.T) {
	cases := []*Case{{
		ID: "ok", Title: "clean case",
		TurnBudget:        4,
		DenyClasses:       []string{"send"},
		ExpectedFragments: []string{"done"},
		CostBudgetUSD:     0.05,
		Cost:              CostInputs{Model: "synthetic-model-a", PromptTokens: 500, CompletionTokens: 100},
		Turns: []Turn{
			{Turn: 1, Role: "user", Content: "go"},
			{Turn: 1, Role: "assistant", Content: "All done.", Tools: []ToolCall{{Name: "read"}}, Allow: []string{"read"}},
		},
	}}
	sum, err := (&Runner{Judge: fixedJudge{0.9}}).Run(cases)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	r := sum.PerCase[0]
	if !r.Passed {
		t.Fatalf("clean case failed: %+v", r.Violations)
	}
	if r.Score <= 0.9 {
		t.Errorf("score = %v, want > 0.9 (perfect deterministic dims lift it)", r.Score)
	}
	if r.CostPriced {
		t.Error("synthetic model should be CostPriced=false (fallback formula)")
	}
	want := 0.9*WeightGoal*1.0 + 1.0*WeightCompliance + 1.0*WeightFragments + 1.0*WeightCost + 1.0*WeightBudget
	want /= WeightGoal + WeightCompliance + WeightFragments + WeightCost + WeightBudget
	if diff := r.Score - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("score = %v, want %v", r.Score, want)
	}
	if sum.JudgeID != "fixed-test-judge" {
		t.Errorf("JudgeID = %q", sum.JudgeID)
	}
}

func TestRunnerCorpusEndToEnd(t *testing.T) {
	cases, err := LoadCorpus("testdata/corpus")
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	sum, err := (&Runner{Judge: StubJudge{}}).Run(cases)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sum.TotalCases != 12 {
		t.Fatalf("TotalCases = %d, want 12", sum.TotalCases)
	}
	byID := make(map[string]CaseResult, len(sum.PerCase))
	for _, r := range sum.PerCase {
		byID[r.CaseID] = r
	}
	// Negative fixtures must fail.
	for _, id := range []string{"ev-0003", "ev-0004", "ev-0009", "ev-0012"} {
		if byID[id].Passed {
			t.Errorf("%s should fail (negative fixture), violations: %v", id, byID[id].Violations)
		}
	}
	// Positive fixtures must pass.
	for _, id := range []string{"ev-0001", "ev-0002", "ev-0005", "ev-0006", "ev-0008", "ev-0010", "ev-0011"} {
		if !byID[id].Passed {
			t.Errorf("%s should pass, violations: %v", id, byID[id].Violations)
		}
	}
	// ev-0007 misses one of two fragments: passes with fragments < 1.
	r7 := byID["ev-0007"]
	if !r7.Passed {
		t.Errorf("ev-0007 should pass (fragment miss is soft): %v", r7.Violations)
	}
	if r7.Dimensions.Fragments != 0.5 {
		t.Errorf("ev-0007 fragments = %v, want 0.5", r7.Dimensions.Fragments)
	}
	// ev-0004: deny gate with good content — fails via the gate, score stays
	// well above zero because fragments and goal are fine.
	if r4 := byID["ev-0004"]; r4.Passed || r4.Score < 0.5 {
		t.Errorf("ev-0004: passed=%v score=%v — deny must fail without tanking score", r4.Passed, r4.Score)
	}
	// ev-0010 prices exactly at the $0.01 boundary on gpt-4o and passes.
	if r10 := byID["ev-0010"]; !r10.Passed || !r10.CostPriced {
		t.Errorf("ev-0010: passed=%v priced=%v violations=%v", r10.Passed, r10.CostPriced, r10.Violations)
	}
	// Every result records the judge.
	for _, r := range sum.PerCase {
		if r.JudgeID != "stub-v1" {
			t.Errorf("%s JudgeID = %q", r.CaseID, r.JudgeID)
		}
	}
}

func TestWeightedAggregates(t *testing.T) {
	// All-perfect dimensions -> 1.0.
	if got := weighted(DimensionScores{1, 1, 1, 1, 1}); got != 1.0 {
		t.Errorf("weighted(all 1) = %v", got)
	}
	// All-zero -> 0.
	if got := weighted(DimensionScores{}); got != 0.0 {
		t.Errorf("weighted(all 0) = %v", got)
	}
	// Weight check: goal=1 others=0 -> 3/8.
	want := WeightGoal / (WeightGoal + WeightCompliance + WeightFragments + WeightCost + WeightBudget)
	if got := weighted(DimensionScores{Goal: 1}); got != want {
		t.Errorf("weighted(goal only) = %v, want %v", got, want)
	}
}

func TestRunnerMeanScores(t *testing.T) {
	cases := []*Case{
		{ID: "a", Turns: []Turn{{Turn: 1, Role: "user"}}},
		{ID: "b", Turns: []Turn{{Turn: 1, Role: "user"}}},
	}
	j := fixedJudge{0.5}
	sum, err := (&Runner{Judge: j}).Run(cases)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sum.MeanScores.Goal != 0.5 {
		t.Errorf("mean goal = %v, want 0.5", sum.MeanScores.Goal)
	}
	if sum.TotalCases != 2 || sum.PassedCases != 2 {
		t.Errorf("totals = %d/%d", sum.PassedCases, sum.TotalCases)
	}
}

func TestRunnerNilJudge(t *testing.T) {
	if _, err := (&Runner{}).Run([]*Case{{ID: "x", Turns: []Turn{{Turn: 1, Role: "user"}}}}); err == nil {
		t.Error("nil judge must error, not panic")
	}
}

// errJudge always errors; verifies the runner surfaces judge failures.
type errJudge struct{}

func (errJudge) JudgeGoal(*Case) (float64, error) { return 0, fmt.Errorf("judge boom") }
func (errJudge) ID() string                       { return "err-judge" }

func TestRunnerJudgeErrorSurfaces(t *testing.T) {
	_, err := (&Runner{Judge: errJudge{}}).Run([]*Case{{
		ID: "x", Turns: []Turn{{Turn: 1, Role: "user"}},
	}})
	if err == nil {
		t.Fatal("judge error swallowed")
	}
}
