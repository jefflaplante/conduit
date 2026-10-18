// Package eval implements the Conduit evaluation harness (bd-18u).
//
// It measures conversational-agent quality as a score × dimension matrix over
// a corpus of scripted cases, with deterministic checks gating and a pluggable
// judge scoring the subjective dimension. v0.1 is replay-only: assistant turns
// are pre-scripted in the corpus, so runs are free, deterministic, and
// side-effect-free. Live mode (model-under-test generates the turns) is v0.2.
//
// Design doc: workspace/memory/sd-bd-18u-eval-harness-research-2026-10-18.md.
// Key invariants:
//   - Safety is a gate, not a metric: deny-class violations fail the case
//     outright regardless of weighted score.
//   - Everything deterministically checkable stays out of the judge's hands.
//   - Every result records the judge ID so comparisons stay auditable.
package eval

import (
	"fmt"
	"time"
)

// ToolCall is one tool invocation appearing in an assistant turn.
type ToolCall struct {
	// Name is the tool name as invoked.
	Name string `json:"name"`
	// Args is the raw arguments the tool received (free-form JSON or text).
	Args string `json:"args,omitempty"`
}

// Turn is one message in a case transcript. In replay mode every turn is
// pre-scripted; the runner validates the assistant turns against the checks.
type Turn struct {
	// Turn is the 1-based conversation turn number. Turns sharing a number are
	// the same round trip (e.g. a user message and its assistant reply).
	Turn int `json:"turn"`

	// Role is "user" or "assistant".
	Role string `json:"role"`

	// Content is the message text.
	Content string `json:"content"`

	// Tools are the tool calls this assistant turn made (replay: scripted;
	// live: captured from execution hooks).
	Tools []ToolCall `json:"tools,omitempty"`

	// Allow is the per-turn tool allowlist: any tool call outside it is an
	// allowlist violation (checked against Tools).
	Allow []string `json:"allow,omitempty"`
}

// CostInputs carries the synthetic token counts used to price a case.
//
// v0.1 computes cost from these scripted counts rather than from real
// provider telemetry: replay mode makes no provider calls, so there is no
// call log to read. The counts are still priced with the production pricing
// resolver (internal/ai) so per-model cost comparisons behave identically to
// live runs; models absent from the pricing matrix fall back to a documented
// formula (see cost.go).
type CostInputs struct {
	// Model is the model ID the transcript is attributed to (e.g.
	// "synthetic-model-a" for corpus-internal fixtures, or a real ID when a
	// case should be priced at a specific model's rates).
	Model string `yaml:"model" json:"model"`

	// Prompt and Completion are the summed token counts for the case.
	PromptTokens     int `yaml:"prompt_tokens" json:"prompt_tokens"`
	CompletionTokens int `yaml:"completion_tokens" json:"completion_tokens"`

	// Optional cache token counts, priced the same way production prices them.
	CacheReadTokens     int `yaml:"cache_read_tokens" json:"cache_read_tokens,omitempty"`
	CacheCreationTokens int `yaml:"cache_creation_tokens" json:"cache_creation_tokens,omitempty"`
}

// Case is one evaluation case: metadata plus its transcript.
type Case struct {
	// ID is the stable case identifier (e.g. "ev-0001"). Required.
	ID string `yaml:"id" json:"id"`

	// Title is a human-readable summary. Required.
	Title string `yaml:"title" json:"title"`

	// Tags group cases into task families (tool-triage, email-triage,
	// data-summary, persona).
	Tags []string `yaml:"tags" json:"tags,omitempty"`

	// ModelsUnderTest names the models this case targets; informational in
	// replay mode (CostInputs.Model drives pricing).
	ModelsUnderTest []string `yaml:"models_under_test" json:"models_under_test,omitempty"`

	// TurnBudget is the maximum number of conversation turns allowed. 0 or
	// negative disables the budget check.
	TurnBudget int `yaml:"turn_budget" json:"turn_budget,omitempty"`

	// DenyClasses lists denied side-effect classes. Any tool call whose name
	// matches a deny pattern is a hard-gate failure (see checks.go for the
	// pattern classes).
	DenyClasses []string `json:"-" yaml:"deny_classes,omitempty"`

	// ExpectedFragments are content strings that must appear (case-insensitive)
	// somewhere in the assistant turns for the case to pass the fragment check.
	ExpectedFragments []string `yaml:"expected_fragments" json:"expected_fragments,omitempty"`

	// CostBudgetUSD, if > 0, gates the case: computed cost above the budget is
	// a hard-gate failure.
	CostBudgetUSD float64 `yaml:"cost_budget_usd" json:"cost_budget_usd,omitempty"`

	// Cost is the synthetic token accounting for pricing.
	Cost CostInputs `yaml:"cost" json:"cost"`

	// Goal is the judged dimension's rubric description. v0.1 scores it via
	// the Judge interface (stub judge in tests; real judge later).
	Goal string `yaml:"goal" json:"goal,omitempty"`

	// Turns is the transcript.
	Turns []Turn `json:"turns"`
}

// DimensionScores holds the per-dimension 0..1 scores for one case.
type DimensionScores struct {
	// Goal is the median of the sampled judge scores (Runs>1): the median
	// dampens judge variance without the sensitivity of a mean to a single
	// outlier call.
	//
	// GoalRuns/GoalScores carry the sampling metadata and are populated
	// only when Runs>1 — single-run results keep them zero/nil so their
	// JSONL lines stay byte-compatible with pre-sampling baselines.
	Goal       float64 `json:"goal"`
	GoalRuns   int     `json:"goal_runs,omitempty"`
	GoalScores []float64 `json:"goal_scores,omitempty"`
	// Compliance covers allowlist + deny-class adherence (1.0 clean, 0 on
	// violations — though deny violations also hard-fail the case).
	Compliance float64 `json:"compliance"`
	// Fragments is the fraction of expected content fragments present.
	Fragments float64 `json:"fragments"`
	// Cost is 1.0 when cost is within budget, 0.0 over budget (or when over
	// the implicit budget of unknown — see cost.go).
	Cost float64 `json:"cost"`
	// Budget is 1.0 within the turn budget, 0.0 over it.
	Budget float64 `json:"budget"`
}

// Weights for the weighted aggregate score. Deterministic checks dominate;
// the judge only scores.
const (
	WeightGoal       = 3.0
	WeightCompliance = 2.0
	WeightFragments  = 1.0
	WeightCost       = 1.0
	WeightBudget     = 1.0
)

// CaseResult is the per-case JSON result emitted by the runner.
type CaseResult struct {
	// CaseID, Title, Tags echo the case metadata.
	CaseID string   `json:"case_id"`
	Title  string   `json:"title"`
	Tags   []string `json:"tags,omitempty"`

	// Passed is false if any hard gate tripped, else true.
	Passed bool `json:"passed"`

	// Score is the weighted aggregate 0..1.
	Score float64 `json:"score"`

	// Dimensions holds the per-dimension scores.
	Dimensions DimensionScores `json:"dimensions"`

	// Violations lists every check failure with its class.
	Violations []Violation `json:"violations,omitempty"`

	// CostUSD is the priced cost of the scripted token counts.
	CostUSD float64 `json:"cost_usd"`

	// CostPriced is false when the model had no known pricing and the fallback
	// formula was used (CostUSD is still populated, via costFallback).
	CostPriced bool `json:"cost_priced"`

	// TurnCount is the highest turn number in the transcript.
	TurnCount int `json:"turn_count"`

	// JudgeID records which judge implementation scored the case.
	JudgeID string `json:"judge_id"`

	// JudgeUsage accounts the judge's own provider calls for this case
	// (tokens + priced cost). Zero-valued for deterministic judges (no
	// calls made) and for results whose judge did not report usage.
	JudgeUsage JudgeUsage `json:"judge_usage,omitempty"`

	// ScoredAt records when the case was scored.
	ScoredAt time.Time `json:"scored_at"`
}

// Violation is one failed check.
type Violation struct {
	// Check names the check ("turn_budget", "tool_allowlist", "deny_class",
	// "fragments", "cost_budget").
	Check string `json:"check"`
	// Detail is a human-readable explanation.
	Detail string `json:"detail"`
}

// Summary is the aggregate result across a corpus.
type Summary struct {
	// GeneratedAt is when the run finished.
	GeneratedAt time.Time `json:"generated_at"`

	// TotalCases, PassedCases are corpus-level counts.
	TotalCases  int `json:"total_cases"`
	PassedCases int `json:"passed_cases"`

	// MeanScores is the per-dimension mean across cases.
	MeanScores DimensionScores `json:"mean_scores"`

	// WeightedScore is the corpus-level weighted aggregate 0..1.
	WeightedScore float64 `json:"weighted_score"`

	// TotalCostUSD sums per-case cost.
	TotalCostUSD float64 `json:"total_cost_usd"`

	// JudgeID is the judge that scored every case in this run.
	JudgeID string `json:"judge_id"`

	// Runs is the median-of-N sample size (bd-18u v0.3); 1 or 0 means a
	// single judge call per case.
	Runs int `json:"runs,omitempty"`

	// JudgeUsage aggregates the judge's own provider spend across the run
	// (bd-18u v0.3): calls, tokens, and priced cost. Distinct from
	// TotalCostUSD, which prices the *cases'* synthetic token counts.
	JudgeUsage JudgeUsage `json:"judge_usage,omitempty"`

	// PerCase holds the individual results.
	PerCase []CaseResult `json:"per_case"`
}

// Regression compares the current run against a baseline summary. The design
// doc pins the rule: flag when weighted score drops by more than 5%.
const regressionThreshold = 0.05

// RegressionFlags reports whether cur regressed vs base: a >5% relative drop
// in weighted score, or any case that passed at baseline failing now.
func RegressionFlags(cur, base *Summary) []string {
	var flags []string
	if base == nil {
		return nil
	}
	if base.WeightedScore > 0 {
		drop := (base.WeightedScore - cur.WeightedScore) / base.WeightedScore
		if drop > regressionThreshold {
			flags = append(flags, fmt.Sprintf(
				"weighted score dropped %.1f%% vs baseline (%.3f -> %.3f), threshold 5%%",
				drop*100, base.WeightedScore, cur.WeightedScore))
		}
	}
	failed := make(map[string]bool, len(cur.PerCase))
	for _, r := range cur.PerCase {
		if !r.Passed {
			failed[r.CaseID] = true
		}
	}
	for _, r := range base.PerCase {
		if r.Passed && failed[r.CaseID] {
			flags = append(flags, "case "+r.CaseID+" passed at baseline but failed now")
		}
	}
	return flags
}
