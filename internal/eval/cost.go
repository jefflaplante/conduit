package eval

import (
	"fmt"
	"math"

	"conduit/internal/ai"
)

// Cost computation for synthetic cases.
//
// Design doc §3c prices cost with the production PricingResolver; that is
// reused here unchanged. The wrinkle for replay mode: scripted token counts
// are usually attributed to synthetic model IDs ("synthetic-model-a") that
// have no entry in the real pricing matrix, and Resolve correctly reports
// unknown models as unpriced rather than $0. Silently reporting $0 would
// make every cost check pass for the wrong reason.
//
// Documented choice: when the resolver cannot price the model, fall back to
// a flat reference rate of $3/M input + $15/M output (the repo's mid-tier
// Sonnet-class price). The result is marked CostPriced=false so reports can
// distinguish resolver-priced costs from fallback-priced ones. Cases that
// need exact pricing can set cost.model to a real matrix ID instead.
const (
	fallbackInputPerM  = 3.0
	fallbackOutputPerM = 15.0
)

// CaseCost prices one case's synthetic token counts.
func CaseCost(c *Case, resolver *ai.PricingResolver) (cost float64, priced bool) {
	if resolver == nil {
		resolver = ai.DefaultPricingResolver()
	}
	u := ai.Usage{
		PromptTokens:             c.Cost.PromptTokens,
		CompletionTokens:         c.Cost.CompletionTokens,
		CacheReadInputTokens:     c.Cost.CacheReadTokens,
		CacheCreationInputTokens: c.Cost.CacheCreationTokens,
	}
	if p, ok := resolver.Resolve("", c.Cost.Model); ok {
		// Cache write multiplier only matters when cache-creation tokens
		// are present; pass 0 to get the default. Non-5m providers are
		// out of scope for synthetic cases.
		return p.Cost(u, 0), true
	}
	cost = (float64(u.PromptTokens)*fallbackInputPerM +
		float64(u.CompletionTokens)*fallbackOutputPerM +
		float64(u.CacheReadInputTokens)*fallbackInputPerM*ai.DefaultCacheReadMultiplier +
		float64(u.CacheCreationInputTokens)*fallbackInputPerM*ai.CacheWriteMultiplier5m) / 1_000_000.0
	return cost, false
}

// CheckCostBudget prices the case and verifies it against CostBudgetUSD.
// A budget of 0 disables the check (the cost is still computed and reported).
// Over-budget is a hard gate: an uncontrolled-cost run is exactly the failure
// mode the budget exists to catch.
func CheckCostBudget(c *Case, resolver *ai.PricingResolver) (float64, bool, *Violation) {
	cost, priced := CaseCost(c, resolver)
	if c.CostBudgetUSD <= 0 {
		return cost, priced, nil
	}
	if cost > c.CostBudgetUSD {
		return cost, priced, &Violation{
			Check: "cost_budget",
			Detail: fmt.Sprintf("cost $%.6f exceeds budget $%.2f (model %q, priced=%v)",
				cost, c.CostBudgetUSD, c.Cost.Model, priced),
		}
	}
	return cost, priced, nil
}

// CostDimension scores the cost dimension: 1.0 within budget (or no budget
// declared), 0.0 over. Rounds away float noise at the boundary.
func CostDimension(c *Case, cost float64) float64 {
	if c.CostBudgetUSD <= 0 {
		return 1.0
	}
	if cost > c.CostBudgetUSD*(1+1e-9) {
		return 0.0
	}
	return 1.0
}

// roundUSD truncates cost reporting at micro-dollar precision so JSONL
// output stays readable and baseline diffs don't wobble on float noise.
func roundUSD(v float64) float64 {
	return math.Round(v*1e6) / 1e6
}
