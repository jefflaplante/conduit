package eval

import (
	"fmt"
	"sort"
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
	// Runs is the median-of-N sample size for the judge (bd-18u v0.3):
	// each case's goal score is the median of Runs judge scores, damping
	// single-call judge variance. 0 or 1 means one call (prior behavior).
	// Deterministic checks still run once — they have no variance to
	// average away. Judge usage sums across all N calls.
	Runs int
}

// Run executes every case in the corpus.
func (r *Runner) Run(cases []*Case) (*Summary, error) {
	if r.Judge == nil {
		return nil, fmt.Errorf("runner: judge is required")
	}
	summary := &Summary{
		GeneratedAt: time.Now().UTC(),
		JudgeID:     r.Judge.ID(),
		Runs:        r.Runs,
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
	summary.JudgeUsage = SumJudgeUsage(summary.PerCase)
	return summary, nil
}

// scoreCase applies every check to one case and assembles its result.
// v0.4: the body moved to scoring.go as the package-level scoreCase shared
// with LiveRunner — replay and live differ only in where the transcript
// comes from.
func (r *Runner) scoreCase(c *Case) (*CaseResult, error) {
	return scoreCase(r.Judge, r.Pricing, r.Runs, c)
}

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

// median returns the median of scores (mean of the two middles for even
// counts; the lone element when odd). Scores are assumed pre-clamped.
func median(scores []float64) float64 {
	if len(scores) == 0 {
		return 0
	}
	s := append([]float64(nil), scores...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
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
