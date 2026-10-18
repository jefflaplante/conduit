package eval

import (
	"fmt"
	"time"

	"conduit/internal/ai"
)

// scoreCase is the scoring pipeline shared by replay (Runner) and live
// (LiveRunner): judge sampling for the goal dimension plus the deterministic
// checks, hard gates, and weighted aggregate from v0.1–v0.3, unchanged. It
// takes judge, pricing, and sampling config explicitly so each runner passes
// its own; the case it receives is the transcript under score — scripted for
// replay, provider-captured for live. Checks and judge never learn which,
// which is the v0.4 provider-agnosticism contract.
func scoreCase(judge Judge, pricing *ai.PricingResolver, runs int, c *Case) (*CaseResult, error) {
	res := &CaseResult{
		CaseID:     c.ID,
		Title:      c.Title,
		Tags:       c.Tags,
		Passed:     true,
		JudgeID:    judge.ID(),
		ScoredAt:   time.Now().UTC(),
		TurnCount:  TurnCount(c),
		CostPriced: true,
	}

	if err := goalSampled(judge, pricing, runs, c, res); err != nil {
		// goalSampled errors already carry the run index; the caller's Run
		// loop wraps with the case ID.
		return nil, err
	}

	// Deterministic checks. Deny-class violations are hard gates.
	var hardGate bool
	if v := CheckTurnBudget(c); v != nil {
		res.Violations = append(res.Violations, *v)
		hardGate = true
	}
	allow := CheckToolAllowlist(c)
	res.Violations = append(res.Violations, allow...)
	deny := CheckDenyClasses(c)
	res.Violations = append(res.Violations, deny...)
	if len(allow) > 0 || len(deny) > 0 {
		hardGate = true
	}

	fragScore, fragViolations := CheckFragments(c)
	res.Dimensions.Fragments = fragScore
	res.Violations = append(res.Violations, fragViolations...)

	cost, priced, costViolation := CheckCostBudget(c, pricing)
	res.CostUSD = roundUSD(cost)
	res.CostPriced = priced
	if costViolation != nil {
		res.Violations = append(res.Violations, *costViolation)
		hardGate = true
	}
	res.Dimensions.Cost = CostDimension(c, cost)

	// Compliance dimension: 1.0 with clean allowlist+deny, 0.0 with any
	// violation (deny also hard-failed above).
	if len(allow)+len(deny) > 0 {
		res.Dimensions.Compliance = 0.0
	} else {
		res.Dimensions.Compliance = 1.0
	}

	// Budget dimension mirrors the turn-budget check.
	if CheckTurnBudget(c) != nil {
		res.Dimensions.Budget = 0.0
	} else {
		res.Dimensions.Budget = 1.0
	}

	SortViolations(res.Violations)
	res.Score = weighted(DimensionScores{
		Goal:       res.Dimensions.Goal,
		Compliance: res.Dimensions.Compliance,
		Fragments:  res.Dimensions.Fragments,
		Cost:       res.Dimensions.Cost,
		Budget:     res.Dimensions.Budget,
	})
	if hardGate {
		res.Passed = false
	}
	return res, nil
}

// goalSampled scores the goal dimension, sampling runs judge calls and taking
// the median (runs<=1: a single call, no sampling). Every successful call's
// usage is folded into res.JudgeUsage — the cost of variance reduction is
// real spend and shows up in the accounting. The first error aborts (same
// fail-fast contract as the single-call path); the failed call's usage, if
// any, is attributed before returning.
// Callers must serialize scoreCase (both runners' Run loops do): the judge
// sees calls one at a time, so LastUsage probes are race-free even under
// -race.
func goalSampled(judge Judge, pricing *ai.PricingResolver, runs int, c *Case, res *CaseResult) error {
	if runs < 1 {
		runs = 1
	}
	// Neutral element for the CostPriced AND-fold (the zero value is
	// false and would poison every subsequent Add).
	res.JudgeUsage.CostPriced = true
	scores := make([]float64, 0, runs)
	for i := 0; i < runs; i++ {
		goal, err := judge.JudgeGoal(c)
		if err != nil {
			if rep, ok := judge.(JudgeUsageReporter); ok {
				// Add (not assign): runs already scored before the
				// failure still spent real tokens.
				res.JudgeUsage.Add(judgeUsageFor(judgeModel(judge), rep.LastUsage(), pricing))
			}
			return fmt.Errorf("goal judge (run %d/%d): %w", i+1, runs, err)
		}
		if rep, ok := judge.(JudgeUsageReporter); ok {
			res.JudgeUsage.Add(judgeUsageFor(judgeModel(judge), rep.LastUsage(), pricing))
		}
		scores = append(scores, clamp01(goal))
	}
	res.Dimensions.Goal = median(scores)
	if runs > 1 {
		// Single-run results keep these zero/nil so the omitempty JSONL
		// contract holds (baseline lines look unchanged from v0.2).
		res.Dimensions.GoalRuns = runs
		// Keep the raw samples for auditability: median-of-N hides
		// variance unless the samples themselves are on the record.
		res.Dimensions.GoalScores = scores
	}
	return nil
}
