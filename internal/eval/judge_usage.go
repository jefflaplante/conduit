package eval

import (
	"conduit/internal/ai"
)

// Judge accounting (bd-18u v0.3): LLM judge calls are real provider spend,
// so the harness tracks their tokens and cost next to the synthetic case
// costs. Same pricing policy as case costs (cost.go): the production
// resolver when the judge model is priced, the documented fallback rate
// otherwise, with the priced flag saying which happened.

// JudgeUsage is the token/cost accounting for judge calls attributable to
// one case (per-case) or a whole run (summary).
type JudgeUsage struct {
	// Calls is the number of judge calls made (LLM judges: one per case,
	// or Runs per case with median-of-N sampling; deterministic judges
	// make no calls and stay zero-valued).
	Calls int `json:"calls"`

	// Token counts summed over Calls, as reported by the provider. A
	// provider that omits usage on a response contributes a call with
	// zero tokens — visible here as Calls > 0 with zero token counts.
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	CacheReadTokens     int `json:"cache_read_tokens,omitempty"`
	CacheCreationTokens int `json:"cache_creation_tokens,omitempty"`

	// CostUSD prices the token counts at the judge model's rates
	// (production resolver; documented fallback rate when unpriced).
	// 0 for deterministic judges.
	CostUSD float64 `json:"cost_usd"`

	// CostPriced is false when the judge model had no known pricing and
	// CostUSD comes from the fallback rate (mirrors CaseResult.CostPriced).
	CostPriced bool `json:"cost_priced"`
}

// Add folds o into u. CostPriced ANDs: a mixed set of priced and unpriced
// calls reports priced=false (conservative — the sum is not a single-rate
// number).
func (u *JudgeUsage) Add(o JudgeUsage) {
	u.Calls += o.Calls
	u.PromptTokens += o.PromptTokens
	u.CompletionTokens += o.CompletionTokens
	u.CacheReadTokens += o.CacheReadTokens
	u.CacheCreationTokens += o.CacheCreationTokens
	u.CostUSD += o.CostUSD
	u.CostPriced = u.CostPriced && o.CostPriced
}

// JudgeUsageReporter is implemented by judges that can report the provider
// usage of their most recent JudgeGoal call (LLMJudge does; the stub does
// not). The runner probes for the interface and accounts what it reports —
// judges without usage data are simply not probed. Consumed serially by
// the runner (one case at a time, no concurrent JudgeGoal calls).
type JudgeUsageReporter interface {
	// LastUsage returns the provider-reported usage of the most recent
	// JudgeGoal call (per call, not cumulative).
	LastUsage() ai.Usage
}

// JudgeCost prices judge-call usage like case costs: the production
// resolver when the model is priced, the flat fallback rate otherwise
// (priced=false). An empty model is never resolvable — a judge without a
// known model reports tokens but a fallback-rated cost.
func JudgeCost(model string, u ai.Usage, resolver *ai.PricingResolver) (cost float64, priced bool) {
	if resolver == nil {
		resolver = ai.DefaultPricingResolver()
	}
	if p, ok := resolver.Resolve("", model); ok {
		return p.Cost(u, 0), true
	}
	cost = (float64(u.PromptTokens)*fallbackInputPerM +
		float64(u.CompletionTokens)*fallbackOutputPerM +
		float64(u.CacheReadInputTokens)*fallbackInputPerM*ai.DefaultCacheReadMultiplier +
		float64(u.CacheCreationInputTokens)*fallbackInputPerM*ai.CacheWriteMultiplier5m) / 1_000_000.0
	return cost, false
}

// judgeModel returns the model ID behind a judge ("" when the judge does
// not call a model — the stub — or is a third-party implementation).
func judgeModel(j Judge) string {
	if t, ok := j.(*LLMJudge); ok {
		return t.Model
	}
	return ""
}

// judgeUsageFor prices one judge call's usage against the judge's model.
func judgeUsageFor(model string, u ai.Usage, resolver *ai.PricingResolver) JudgeUsage {
	cost, priced := JudgeCost(model, u, resolver)
	return JudgeUsage{
		Calls:               1,
		PromptTokens:        u.PromptTokens,
		CompletionTokens:    u.CompletionTokens,
		CacheReadTokens:     u.CacheReadInputTokens,
		CacheCreationTokens: u.CacheCreationInputTokens,
		CostUSD:             roundUSD(cost),
		CostPriced:          priced,
	}
}

// SumJudgeUsage reduces per-case judge usage into the run aggregate.
// CostUSD sums the per-case figures; CostPriced is the AND of the flags
// (single-judge runs have uniform priced-ness — it is a pure function of
// the model ID). Used by the runner and by baseline loads, so a loaded
// baseline rebuilds identical aggregates.
func SumJudgeUsage(results []CaseResult) JudgeUsage {
	var total JudgeUsage
	total.CostPriced = true
	for i := range results {
		// Zero-call results (deterministic judges) contribute nothing and
		// don't vote on CostPriced — otherwise the stub path would report
		// "priced=false" on a $0 total.
		if results[i].JudgeUsage.Calls == 0 {
			continue
		}
		total.Add(results[i].JudgeUsage)
	}
	total.CostUSD = roundUSD(total.CostUSD)
	return total
}
