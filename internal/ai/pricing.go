package ai

import "conduit/internal/config"

// ModelPricing holds per-token costs for a model.
type ModelPricing struct {
	InputPerMToken  float64 // Cost per million input tokens
	OutputPerMToken float64 // Cost per million output tokens
}

// DefaultPricingMatrix maps model ID prefixes to their pricing.
// Uses prefix matching (same pattern as ContextWindowSizes in router.go).
var DefaultPricingMatrix = map[string]ModelPricing{
	"claude-opus-4":     {InputPerMToken: 15.0, OutputPerMToken: 75.0},
	"claude-sonnet-4":   {InputPerMToken: 3.0, OutputPerMToken: 15.0},
	"claude-haiku-4":    {InputPerMToken: 0.80, OutputPerMToken: 4.0},
	"claude-3-5-sonnet": {InputPerMToken: 3.0, OutputPerMToken: 15.0},
	"claude-3-5-haiku":  {InputPerMToken: 0.80, OutputPerMToken: 4.0},
	"claude-3-opus":     {InputPerMToken: 15.0, OutputPerMToken: 75.0},
	"claude-3-sonnet":   {InputPerMToken: 3.0, OutputPerMToken: 15.0},
	"claude-3-haiku":    {InputPerMToken: 0.25, OutputPerMToken: 1.25},
	"gpt-4o":            {InputPerMToken: 2.50, OutputPerMToken: 10.0},
	"gpt-4-turbo":       {InputPerMToken: 10.0, OutputPerMToken: 30.0},
	"gpt-4":             {InputPerMToken: 30.0, OutputPerMToken: 60.0},
	"gpt-3.5-turbo":     {InputPerMToken: 0.50, OutputPerMToken: 1.50},
}

// PricingForModel returns the pricing for a given model using prefix matching.
// Falls back to a zero-cost default if no match found.
func PricingForModel(model string) ModelPricing {
	if model == "" {
		return ModelPricing{}
	}
	// Exact match first
	if pricing, ok := DefaultPricingMatrix[model]; ok {
		return pricing
	}
	// Prefix match (handles date-suffixed models like claude-sonnet-4-20250514)
	for prefix, pricing := range DefaultPricingMatrix {
		if len(model) >= len(prefix) && model[:len(prefix)] == prefix {
			return pricing
		}
	}
	return ModelPricing{}
}

// CalculateCost returns the estimated cost for a given model and token usage.
func CalculateCost(model string, inputTokens, outputTokens int) float64 {
	pricing := PricingForModel(model)
	inputCost := float64(inputTokens) / 1_000_000.0 * pricing.InputPerMToken
	outputCost := float64(outputTokens) / 1_000_000.0 * pricing.OutputPerMToken
	return inputCost + outputCost
}

// PricingResolver resolves model pricing using config overrides before
// falling back to the hardcoded default matrix.
type PricingResolver struct {
	overrides map[string]config.PricingOverride
}

// NewPricingResolver creates a resolver. Pass nil for no overrides.
func NewPricingResolver(overrides map[string]config.PricingOverride) *PricingResolver {
	return &PricingResolver{overrides: overrides}
}

// PricingForModel checks config overrides first (exact match, then prefix),
// then falls back to the default pricing matrix.
func (pr *PricingResolver) PricingForModel(model string) ModelPricing {
	if model == "" {
		return ModelPricing{}
	}
	if pr.overrides != nil {
		// Exact match
		if o, ok := pr.overrides[model]; ok {
			return ModelPricing{InputPerMToken: o.InputPerMToken, OutputPerMToken: o.OutputPerMToken}
		}
		// Prefix match
		for prefix, o := range pr.overrides {
			if len(model) >= len(prefix) && model[:len(prefix)] == prefix {
				return ModelPricing{InputPerMToken: o.InputPerMToken, OutputPerMToken: o.OutputPerMToken}
			}
		}
	}
	// Fall back to hardcoded defaults
	return PricingForModel(model)
}

// CalculateCost returns the estimated cost using this resolver's pricing.
func (pr *PricingResolver) CalculateCost(model string, inputTokens, outputTokens int) float64 {
	pricing := pr.PricingForModel(model)
	inputCost := float64(inputTokens) / 1_000_000.0 * pricing.InputPerMToken
	outputCost := float64(outputTokens) / 1_000_000.0 * pricing.OutputPerMToken
	return inputCost + outputCost
}
