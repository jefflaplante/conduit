package eval

import (
	"fmt"
	"time"

	"conduit/internal/ai"
)

// Runner replays a corpus through the checks and produces per-case results
// plus an aggregate summary. Replay mode is side-effect-free: the runner
// never executes tools, sends messages, or calls providers — it scores the
// scripted transcripts.
type Runner struct {
	// Judge scores the goal dimension.
	Judge Judge
	// Pricing prices token counts; nil uses the ai package default resolver.
	Pricing *ai.PricingResolver
}

// Run executes every case in the corpus.
func (r *Runner) Run(cases []*Case) (*Summary, error) {
	if r.Judge == nil {
		return nil, fmt.Errorf("runner: judge is required")
	}
	summary := &Summary{
		GeneratedAt: time.Now().UTC(),
		JudgeID:     r.Judge.ID(),
		PerCase:     make([]CaseResult, 0, len(cases)),
	}
	for _, c := range cases {
		res, err := r.scoreCase(c)
		if err != nil {
			return nil, fmt.Errorf("case %s: %w", c.ID, err)
		}
		summary.PerCase = append(summary.PerCase, *res)
		if res.Passed {
			summary.PassedCases++
		}
		summary.TotalCostUSD += res.CostUSD
	}
	summary.TotalCases = len(summary.PerCase)
	summary.MeanScores = meanScores(summary.PerCase)
	summary.WeightedScore = weighted(summary.MeanScores)
	return summary, nil
}

// scoreCase applies every check to one case and assembles its result.
func (r *Runner) scoreCase(c *Case) (*CaseResult, error) {
	res := &CaseResult{
		CaseID:     c.ID,
		Title:      c.Title,
		Tags:       c.Tags,
		Passed:     true,
		JudgeID:    r.Judge.ID(),
		ScoredAt:   time.Now().UTC(),
		TurnCount:  TurnCount(c),
		CostPriced: true,
	}

	goal, err := r.Judge.JudgeGoal(c)
	if err != nil {
		return nil, fmt.Errorf("goal judge: %w", err)
	}
	res.Dimensions.Goal = clamp01(goal)

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

	cost, priced, costViolation := CheckCostBudget(c, r.Pricing)
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

// pricing removed: CheckCostBudget takes r.Pricing directly (nil =
// ai.DefaultPricingResolver inside cost.go).

// weighted computes the weighted aggregate 0..1 over dimension scores.
func weighted(d DimensionScores) float64 {
	total := WeightGoal + WeightCompliance + WeightFragments + WeightCost + WeightBudget
	s := d.Goal*WeightGoal +
		d.Compliance*WeightCompliance +
		d.Fragments*WeightFragments +
		d.Cost*WeightCost +
		d.Budget*WeightBudget
	return clamp01(s / total)
}

// meanScores averages each dimension across results.
func meanScores(results []CaseResult) DimensionScores {
	if len(results) == 0 {
		return DimensionScores{}
	}
	var m DimensionScores
	for _, r := range results {
		m.Goal += r.Dimensions.Goal
		m.Compliance += r.Dimensions.Compliance
		m.Fragments += r.Dimensions.Fragments
		m.Cost += r.Dimensions.Cost
		m.Budget += r.Dimensions.Budget
	}
	n := float64(len(results))
	return DimensionScores{
		Goal:       m.Goal / n,
		Compliance: m.Compliance / n,
		Fragments:  m.Fragments / n,
		Cost:       m.Cost / n,
		Budget:     m.Budget / n,
	}
}

// clamp01 clamps v to [0,1].
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
