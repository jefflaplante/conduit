package ai

import (
	"log"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"conduit/internal/config"
)

// ModelPricing holds per-token costs for a model (USD per million tokens).
//
// conduit-31jg.57: cache pricing. Anthropic bills cache writes at a multiple
// of the input price (1.25x for the 5-minute TTL, 2x for the 1-hour TTL) and
// cache reads at 0.1x input for most models. A zero cache field means "derive
// from InputPerMToken"; a non-zero one is an explicit published price (e.g.
// Claude Fable 5.1 reads at $0.25/MTok = 0.025x).
type ModelPricing struct {
	InputPerMToken      float64 // Cost per million uncached input tokens
	OutputPerMToken     float64 // Cost per million output tokens
	CacheReadPerMToken  float64 // 0 = DefaultCacheReadMultiplier x input
	CacheWritePerMToken float64 // 0 = the resolver's write multiplier x input
}

const (
	// DefaultCacheReadMultiplier is the cache-read price as a fraction of the
	// input price. conduit-31jg.57
	DefaultCacheReadMultiplier = 0.1
	// CacheWriteMultiplier5m / CacheWriteMultiplier1h are the cache-write
	// premiums for the two Anthropic TTLs. conduit-31jg.57
	CacheWriteMultiplier5m = 1.25
	CacheWriteMultiplier1h = 2.0
)

// DefaultPricingMatrix maps model ID prefixes to their pricing. Lookup is an
// exact match, else the LONGEST prefix that ends at an ID boundary (so
// "claude-sonnet-4-6-20260101" hits "claude-sonnet-4-6", and
// "claude-opus-4-6" is NOT priced as "claude-opus-4").
//
// conduit-31jg.57: Claude prices refreshed 2026-09-27 from Anthropic's
// published first-party API pricing (claude-api reference, models table
// cached 2026-06-24; cache economics from its prompt-caching page).
// "claude-opus-4" (Opus 4 / 4.1, $15/$75), "claude-sonnet-4" (Sonnet 4 /
// 4.5) and the claude-3 entries were not re-verified and are kept as-is.
var DefaultPricingMatrix = map[string]ModelPricing{
	// Anthropic — current
	"claude-fable-5-1":  {InputPerMToken: 10.0, OutputPerMToken: 50.0, CacheReadPerMToken: 0.25},
	"claude-mythos-5-1": {InputPerMToken: 10.0, OutputPerMToken: 50.0, CacheReadPerMToken: 0.25},
	"claude-fable-5":    {InputPerMToken: 10.0, OutputPerMToken: 50.0},
	"claude-mythos-5":   {InputPerMToken: 10.0, OutputPerMToken: 50.0},
	"claude-opus-5-5":   {InputPerMToken: 4.0, OutputPerMToken: 20.0, CacheReadPerMToken: 0.20},
	"claude-opus-5":     {InputPerMToken: 5.0, OutputPerMToken: 25.0},
	"claude-opus-4-8":   {InputPerMToken: 5.0, OutputPerMToken: 25.0},
	"claude-opus-4-7":   {InputPerMToken: 5.0, OutputPerMToken: 25.0},
	"claude-opus-4-6":   {InputPerMToken: 5.0, OutputPerMToken: 25.0},
	"claude-sonnet-5":   {InputPerMToken: 2.0, OutputPerMToken: 10.0},
	"claude-sonnet-4-6": {InputPerMToken: 3.0, OutputPerMToken: 15.0},
	"claude-haiku-4-5":  {InputPerMToken: 1.0, OutputPerMToken: 5.0},
	// Anthropic — legacy (not re-verified, conduit-31jg.57)
	"claude-opus-4":     {InputPerMToken: 15.0, OutputPerMToken: 75.0},
	"claude-sonnet-4":   {InputPerMToken: 3.0, OutputPerMToken: 15.0},
	"claude-haiku-4":    {InputPerMToken: 0.80, OutputPerMToken: 4.0},
	"claude-3-5-sonnet": {InputPerMToken: 3.0, OutputPerMToken: 15.0},
	"claude-3-5-haiku":  {InputPerMToken: 0.80, OutputPerMToken: 4.0},
	"claude-3-opus":     {InputPerMToken: 15.0, OutputPerMToken: 75.0},
	"claude-3-sonnet":   {InputPerMToken: 3.0, OutputPerMToken: 15.0},
	"claude-3-haiku":    {InputPerMToken: 0.25, OutputPerMToken: 1.25},
	// OpenAI (legacy entries, unchanged)
	"gpt-4o":        {InputPerMToken: 2.50, OutputPerMToken: 10.0},
	"gpt-4-turbo":   {InputPerMToken: 10.0, OutputPerMToken: 30.0},
	"gpt-4":         {InputPerMToken: 30.0, OutputPerMToken: 60.0},
	"gpt-3.5-turbo": {InputPerMToken: 0.50, OutputPerMToken: 1.50},
}

// Cost prices one usage record. writeMult is the cache-write premium used
// when the pricing has no explicit CacheWritePerMToken. Usage.PromptTokens
// is the UNCACHED input (Anthropic's input_tokens excludes cache writes and
// reads, which are billed separately).
func (p ModelPricing) Cost(u Usage, writeMult float64) float64 {
	if writeMult <= 0 {
		writeMult = CacheWriteMultiplier5m
	}
	read := p.CacheReadPerMToken
	if read == 0 {
		read = p.InputPerMToken * DefaultCacheReadMultiplier
	}
	write := p.CacheWritePerMToken
	if write == 0 {
		write = p.InputPerMToken * writeMult
	}
	return (float64(u.PromptTokens)*p.InputPerMToken +
		float64(u.CompletionTokens)*p.OutputPerMToken +
		float64(u.CacheCreationInputTokens)*write +
		float64(u.CacheReadInputTokens)*read) / 1_000_000.0
}

// isIDBoundary reports whether a prefix match of length n on model ends at a
// model-ID boundary: the end of the string, or a separator that introduces a
// date/variant/tag suffix ("-20250514", ":free", "@20251101", "[1m]").
func isIDBoundary(model string, n int) bool {
	if n >= len(model) {
		return true
	}
	switch model[n] {
	case '-', ':', '@', '[', '_':
		return true
	}
	return false
}

// longestPrefix returns the entry of table whose key is the longest
// boundary-aligned prefix of model.
func longestPrefix(table map[string]ModelPricing, model string) (ModelPricing, bool) {
	best, bestLen := ModelPricing{}, 0
	for prefix, p := range table {
		if len(prefix) > bestLen && strings.HasPrefix(model, prefix) && isIDBoundary(model, len(prefix)) {
			best, bestLen = p, len(prefix)
		}
	}
	return best, bestLen > 0
}

// PricingForModel returns the built-in pricing for a model (exact match,
// else longest boundary-aligned prefix), or zero pricing when unknown.
// Prefer PricingResolver, which also applies config overrides and resolves
// provider-prefixed IDs.
func PricingForModel(model string) ModelPricing {
	p, _ := lookupPricing(nil, candidateIDs("", model))
	return p
}

// candidateIDs lists the IDs a (provider, model) pair may be priced under,
// most specific first: the model as given, provider/model, then the model
// with leading "vendor/" segments stripped one at a time
// ("openrouter/z-ai/glm-5.3-flash" → "z-ai/glm-5.3-flash" → "glm-5.3-flash").
// conduit-31jg.57
func candidateIDs(provider, model string) []string {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return nil
	}
	out := []string{m}
	if p := strings.ToLower(strings.TrimSpace(provider)); p != "" && !strings.HasPrefix(m, p+"/") {
		out = append(out, p+"/"+m)
	}
	for rest := m; ; {
		i := strings.IndexByte(rest, '/')
		if i < 0 || i == len(rest)-1 {
			break
		}
		rest = rest[i+1:]
		out = append(out, rest)
	}
	return out
}

// lookupPricing resolves candidates against overrides then the built-in
// matrix. Any exact match beats any prefix match; within a match kind,
// overrides beat built-ins and earlier (more specific) candidates win.
func lookupPricing(overrides map[string]ModelPricing, candidates []string) (ModelPricing, bool) {
	tables := []map[string]ModelPricing{overrides, DefaultPricingMatrix}
	for _, t := range tables {
		for _, c := range candidates {
			if p, ok := t[c]; ok {
				return p, true
			}
		}
	}
	for _, t := range tables {
		for _, c := range candidates {
			if p, ok := longestPrefix(t, c); ok {
				return p, true
			}
		}
	}
	return ModelPricing{}, false
}

// PricingResolver resolves model pricing from config overrides
// (ai.pricing_overrides) before falling back to DefaultPricingMatrix.
//
// conduit-31jg.57: salvaged from the smart-routing dead code. It no longer
// depends on SmartRoutingConfig: the gateway builds ONE resolver from
// AIConfig at init (NewPricingResolverFromConfig) and every cost path —
// the per-call metering hook, UsageTracker, TurnRunner session cost, and
// the package-level CalculateCost — prices through it.
type PricingResolver struct {
	overrides map[string]ModelPricing // lower-cased keys
	// writeMult is the cache-write premium (1.25x for the 5-minute TTL, 2x
	// when prompt_caching.extended_ttl selects the 1-hour TTL);
	// providerWriteMult holds per-provider overrides of it.
	writeMult         float64
	providerWriteMult map[string]float64
	warned            sync.Map // unknown model key → struct{}: log once
}

// NewPricingResolver creates a resolver. Pass nil for no overrides.
func NewPricingResolver(overrides map[string]config.PricingOverride) *PricingResolver {
	pr := &PricingResolver{
		overrides: make(map[string]ModelPricing, len(overrides)),
		writeMult: CacheWriteMultiplier5m,
	}
	for k, o := range overrides {
		key := strings.ToLower(strings.TrimSpace(k))
		if key == "" || o.InputPerMToken < 0 || o.OutputPerMToken < 0 || o.CacheReadPerMToken < 0 || o.CacheWritePerMToken < 0 {
			log.Printf("[Pricing] ignoring invalid pricing override %q (conduit-31jg.57)", k)
			continue
		}
		pr.overrides[key] = ModelPricing{
			InputPerMToken:      o.InputPerMToken,
			OutputPerMToken:     o.OutputPerMToken,
			CacheReadPerMToken:  o.CacheReadPerMToken,
			CacheWritePerMToken: o.CacheWritePerMToken,
		}
	}
	return pr
}

// NewPricingResolverFromConfig builds the gateway's resolver from AIConfig:
// ai.pricing_overrides merged with the deprecated
// ai.smart_routing.pricing_overrides alias, and the cache-write premium from
// prompt_caching.extended_ttl (global, with provider-level overrides).
func NewPricingResolverFromConfig(cfg config.AIConfig) *PricingResolver {
	pr := NewPricingResolver(cfg.EffectivePricingOverrides())
	if cfg.PromptCaching.ExtendedTTL {
		pr.writeMult = CacheWriteMultiplier1h
	}
	for _, p := range cfg.Providers {
		if p.PromptCaching == nil {
			continue
		}
		if pr.providerWriteMult == nil {
			pr.providerWriteMult = make(map[string]float64)
		}
		m := CacheWriteMultiplier5m
		if p.PromptCaching.ExtendedTTL {
			m = CacheWriteMultiplier1h
		}
		pr.providerWriteMult[strings.ToLower(p.Name)] = m
	}
	return pr
}

// OverrideModels returns the configured override keys, sorted.
func (pr *PricingResolver) OverrideModels() []string {
	if pr == nil {
		return nil
	}
	out := make([]string, 0, len(pr.overrides))
	for k := range pr.overrides {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Resolve returns the pricing for model served by provider (provider may be
// ""). ok=false means the model is unknown — its cost must be treated as
// unknown, not $0. OpenRouter ":free" variants are known and free.
func (pr *PricingResolver) Resolve(provider, model string) (ModelPricing, bool) {
	cands := candidateIDs(provider, model)
	if len(cands) == 0 {
		return ModelPricing{}, false
	}
	var overrides map[string]ModelPricing
	if pr != nil {
		overrides = pr.overrides
	}
	if p, ok := lookupPricing(overrides, cands); ok {
		return p, true
	}
	if strings.HasSuffix(cands[0], ":free") {
		return ModelPricing{}, true
	}
	return ModelPricing{}, false
}

// PricingForModel checks config overrides first, then the built-in matrix.
// Unknown models return zero pricing; use Resolve to tell the difference.
func (pr *PricingResolver) PricingForModel(model string) ModelPricing {
	p, _ := pr.Resolve("", model)
	return p
}

// cacheWriteMultiplier returns the cache-write premium for provider.
func (pr *PricingResolver) cacheWriteMultiplier(provider string) float64 {
	if pr == nil {
		return CacheWriteMultiplier5m
	}
	if m, ok := pr.providerWriteMult[strings.ToLower(provider)]; ok {
		return m
	}
	return pr.writeMult
}

// Cost prices one usage record (a single call or a whole-turn sum on one
// model), cache tokens included. priced=false means the model is unknown:
// the returned cost is 0 but must be reported as unknown, and a warning is
// logged once per (provider, model).
func (pr *PricingResolver) Cost(provider, model string, u Usage) (cost float64, priced bool) {
	p, ok := pr.Resolve(provider, model)
	if !ok {
		pr.warnUnknown(provider, model)
		return 0, false
	}
	return p.Cost(u, pr.cacheWriteMultiplier(provider)), true
}

func (pr *PricingResolver) warnUnknown(provider, model string) {
	key := strings.ToLower(provider) + "\x00" + strings.ToLower(model)
	if pr == nil {
		log.Printf("[Pricing] no price for model %q (provider %q) — cost recorded as unknown (conduit-31jg.57)", model, provider)
		return
	}
	if _, loaded := pr.warned.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	log.Printf("[Pricing] no price for model %q (provider %q) — cost recorded as unknown, not $0; add it to ai.pricing_overrides (conduit-31jg.57)", model, provider)
}

// CalculateCost returns the estimated cost using this resolver's pricing
// (uncached input and output only).
func (pr *PricingResolver) CalculateCost(model string, inputTokens, outputTokens int) float64 {
	c, _ := pr.Cost("", model, Usage{PromptTokens: inputTokens, CompletionTokens: outputTokens})
	return c
}

// defaultResolver backs the package-level CalculateCost so legacy call
// sites (smart-routing cost optimizer) price with the same overrides. The
// gateway installs its resolver at init. conduit-31jg.57
var defaultResolver atomic.Pointer[PricingResolver]

// SetDefaultPricingResolver installs pr as the package-level resolver (nil
// restores built-in pricing only).
func SetDefaultPricingResolver(pr *PricingResolver) { defaultResolver.Store(pr) }

// DefaultPricingResolver returns the package-level resolver (never nil).
func DefaultPricingResolver() *PricingResolver {
	if pr := defaultResolver.Load(); pr != nil {
		return pr
	}
	return builtinResolver
}

var builtinResolver = NewPricingResolver(nil)

// CalculateCost returns the estimated cost for a given model and token usage
// via the package-level resolver.
func CalculateCost(model string, inputTokens, outputTokens int) float64 {
	return DefaultPricingResolver().CalculateCost(model, inputTokens, outputTokens)
}
