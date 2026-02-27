package ai

import (
	"log"
	"strings"
	"time"

	"conduit/internal/config"
)

// ModelSelector picks the best model for a given request based on complexity,
// cost, availability, and rate-limit status.
type ModelSelector interface {
	// SelectModel returns the recommended model ID for the given request context.
	SelectModel(ctx *SelectionContext) SelectionResult
}

// SelectionContext holds everything the selector needs to make a decision.
type SelectionContext struct {
	// Complexity is the analyzed complexity of the current request.
	Complexity ComplexityScore

	// EstimatedInputTokens is a rough estimate of the prompt size.
	EstimatedInputTokens int

	// AvailableTools is the list of tools available for this request.
	AvailableTools []Tool

	// RequestedModel is a user- or config-specified model override (empty = auto).
	RequestedModel string

	// ProviderName is the target provider (e.g., "anthropic").
	ProviderName string
}

// SelectionResult holds the model choice and metadata about why it was chosen.
type SelectionResult struct {
	// Model is the selected model ID (e.g., "claude-sonnet-4-6").
	Model string `json:"model"`

	// Tier indicates which model tier was selected.
	Tier ModelTier `json:"tier"`

	// Reason is a short human-readable explanation of the choice.
	Reason string `json:"reason"`

	// EstimatedCostPer1KTokens is the blended cost estimate for this model.
	EstimatedCostPer1KTokens float64 `json:"estimated_cost_per_1k_tokens"`

	// Overridden is true if the user's explicit model override was honored.
	Overridden bool `json:"overridden,omitempty"`
}

// ModelTier represents a model capability tier.
type ModelTier int

const (
	// TierHaiku is the fastest/cheapest tier for simple tasks.
	TierHaiku ModelTier = iota

	// TierSonnet is the balanced tier for standard tasks.
	TierSonnet

	// TierOpus is the most capable tier for complex tasks.
	TierOpus
)

// String returns a human-readable name for the tier.
func (t ModelTier) String() string {
	switch t {
	case TierHaiku:
		return "haiku"
	case TierSonnet:
		return "sonnet"
	case TierOpus:
		return "opus"
	default:
		return "unknown"
	}
}

// ModelTierConfig maps a tier to its concrete model ID and cost bounds.
type ModelTierConfig struct {
	Tier    ModelTier
	ModelID string
	// MaxComplexity is the highest complexity level this tier should handle.
	// Tasks above this level are escalated to the next tier.
	MaxComplexity ComplexityLevel
}

// DefaultModelSelector is the standard model selection strategy.
type DefaultModelSelector struct {
	// tiers is the ordered list of model tiers (lowest to highest capability).
	tiers []ModelTierConfig

	// usageTracker provides live cost and error rate data.
	usageTracker *UsageTracker

	// dailyBudget is the daily cost ceiling in USD. 0 means unlimited.
	dailyBudget float64

	// complexityAnalyzer is used for on-the-fly complexity adjustments.
	complexityAnalyzer *ComplexityAnalyzer
}

// NewDefaultModelSelector creates a selector from the SmartRoutingConfig and
// model aliases. If smartCfg is nil, the selector returns the default model
// from aliases (typically sonnet).
func NewDefaultModelSelector(smartCfg *config.SmartRoutingConfig, aliases map[string]string, usageTracker *UsageTracker) *DefaultModelSelector {
	// Resolve tier model IDs from aliases (with fallbacks)
	haikuModel := resolveAlias(aliases, "haiku", "claude-haiku-4-5-20251001")
	sonnetModel := resolveAlias(aliases, "sonnet", "claude-sonnet-4-6")
	opusModel := resolveAlias(aliases, "opus", "claude-opus-4-6")

	tiers := []ModelTierConfig{
		{Tier: TierHaiku, ModelID: haikuModel, MaxComplexity: ComplexitySimple},
		{Tier: TierSonnet, ModelID: sonnetModel, MaxComplexity: ComplexityStandard},
		{Tier: TierOpus, ModelID: opusModel, MaxComplexity: ComplexityComplex},
	}

	var budget float64
	if smartCfg != nil {
		budget = smartCfg.CostBudgetDaily
	}

	return &DefaultModelSelector{
		tiers:              tiers,
		usageTracker:       usageTracker,
		dailyBudget:        budget,
		complexityAnalyzer: NewComplexityAnalyzer(),
	}
}

// SelectModel implements ModelSelector.
func (s *DefaultModelSelector) SelectModel(ctx *SelectionContext) SelectionResult {
	// Honor explicit model override
	if ctx.RequestedModel != "" {
		tier := s.tierForModel(ctx.RequestedModel)
		return SelectionResult{
			Model:                    ctx.RequestedModel,
			Tier:                     tier,
			Reason:                   "explicit model override",
			Overridden:               true,
			EstimatedCostPer1KTokens: s.estimateCostPer1K(ctx.RequestedModel),
		}
	}

	// Step 1: Pick tier based on complexity
	selectedTier := s.tierForComplexity(ctx.Complexity.Level)

	// Step 2: Check budget constraints -- downgrade if over daily budget
	if s.dailyBudget > 0 && s.usageTracker != nil {
		currentCost := s.usageTracker.TotalCost()
		if currentCost >= s.dailyBudget {
			// Over budget: force haiku (cheapest)
			log.Printf("[Strategy] Over daily budget ($%.2f >= $%.2f), downgrading to haiku",
				currentCost, s.dailyBudget)
			selectedTier = TierHaiku
		} else if currentCost >= s.dailyBudget*0.8 && selectedTier == TierOpus {
			// Approaching budget: downgrade opus to sonnet
			log.Printf("[Strategy] Approaching daily budget ($%.2f / $%.2f), downgrading opus to sonnet",
				currentCost, s.dailyBudget)
			selectedTier = TierSonnet
		}
	}

	// Step 3: Check error rate -- if a model has high error rate, try to avoid it
	model := s.modelForTier(selectedTier)
	if s.usageTracker != nil {
		if modelUsage, ok := s.usageTracker.GetModelUsage(model); ok {
			errorRate := s.errorRate(modelUsage)
			if errorRate > 0.3 && selectedTier < TierOpus {
				// High error rate (>30%): escalate to next tier
				log.Printf("[Strategy] High error rate (%.0f%%) for %s, escalating tier",
					errorRate*100, model)
				selectedTier++
				model = s.modelForTier(selectedTier)
			}
		}
	}

	// Step 4: Check token estimate against context window
	if ctx.EstimatedInputTokens > 0 {
		contextWindow := ContextWindowForModel(model)
		if ctx.EstimatedInputTokens > contextWindow*3/4 {
			// Large context: prefer a model with the largest context window
			// All Claude 4+ models have 200K, so this is mainly future-proofing
			log.Printf("[Strategy] Large input (%d tokens), ensuring adequate context window",
				ctx.EstimatedInputTokens)
		}
	}

	// Step 5: Latency consideration -- if average latency is very high for the
	// selected model, note it but don't change (latency is model-inherent).
	reason := s.buildReason(ctx, selectedTier)

	return SelectionResult{
		Model:                    model,
		Tier:                     selectedTier,
		Reason:                   reason,
		EstimatedCostPer1KTokens: s.estimateCostPer1K(model),
	}
}

// GetTiers returns the configured model tier definitions. Useful for
// inspection and testing.
func (s *DefaultModelSelector) GetTiers() []ModelTierConfig {
	return s.tiers
}

// tierForComplexity maps a complexity level to the appropriate model tier.
func (s *DefaultModelSelector) tierForComplexity(level ComplexityLevel) ModelTier {
	switch level {
	case ComplexitySimple:
		return TierHaiku
	case ComplexityStandard:
		return TierSonnet
	case ComplexityComplex:
		return TierOpus
	default:
		return TierSonnet // default to balanced tier
	}
}

// modelForTier returns the concrete model ID for a tier.
func (s *DefaultModelSelector) modelForTier(tier ModelTier) string {
	for _, tc := range s.tiers {
		if tc.Tier == tier {
			return tc.ModelID
		}
	}
	// Fallback to middle tier (sonnet)
	if len(s.tiers) >= 2 {
		return s.tiers[1].ModelID
	}
	if len(s.tiers) >= 1 {
		return s.tiers[0].ModelID
	}
	return ""
}

// tierForModel determines which tier a model ID belongs to by checking
// against configured tiers.
func (s *DefaultModelSelector) tierForModel(model string) ModelTier {
	lower := strings.ToLower(model)
	for _, tc := range s.tiers {
		if tc.ModelID == model || strings.ToLower(tc.ModelID) == lower {
			return tc.Tier
		}
	}
	// Heuristic: check model name for tier keywords
	if strings.Contains(lower, "haiku") {
		return TierHaiku
	}
	if strings.Contains(lower, "opus") {
		return TierOpus
	}
	return TierSonnet // default
}

// errorRate calculates the error rate for a model from its usage record.
func (s *DefaultModelSelector) errorRate(usage *ModelUsageRecord) float64 {
	if usage.TotalRequests == 0 {
		return 0
	}
	return float64(usage.ErrorCount) / float64(usage.TotalRequests)
}

// estimateCostPer1K returns the estimated blended cost per 1,000 tokens
// (assuming roughly equal input/output) for a given model.
func (s *DefaultModelSelector) estimateCostPer1K(model string) float64 {
	pricing := PricingForModel(model)
	// Blended: average of input and output cost, per 1K tokens
	return (pricing.InputPerMToken + pricing.OutputPerMToken) / 2.0 / 1000.0
}

// buildReason constructs a human-readable reason for the model selection.
func (s *DefaultModelSelector) buildReason(ctx *SelectionContext, tier ModelTier) string {
	switch tier {
	case TierHaiku:
		if s.dailyBudget > 0 && s.usageTracker != nil && s.usageTracker.TotalCost() >= s.dailyBudget {
			return "budget exceeded, using cheapest model"
		}
		return "simple task, using fast/cheap model"
	case TierSonnet:
		return "standard complexity, using balanced model"
	case TierOpus:
		return "complex task, using most capable model"
	default:
		return "default model selection"
	}
}

// resolveAlias resolves a model alias from the aliases map, falling back to
// the provided default if the alias is not found.
func resolveAlias(aliases map[string]string, alias, fallback string) string {
	if aliases != nil {
		if model, ok := aliases[alias]; ok && model != "" {
			return model
		}
	}
	return fallback
}

// --- Convenience function for quick one-shot selection ---

// QuickSelectModel is a convenience function that creates a transient selector
// and picks a model based on a message and optional usage tracker.
// This is useful for callers that don't need to persist the selector.
func QuickSelectModel(message string, tools []Tool, smartCfg *config.SmartRoutingConfig, aliases map[string]string, usageTracker *UsageTracker) SelectionResult {
	analyzer := NewComplexityAnalyzer()
	selector := NewDefaultModelSelector(smartCfg, aliases, usageTracker)

	msgScore := analyzer.AnalyzeMessage(message)
	toolScore := analyzer.AnalyzeToolDefinitions(tools)
	combined := analyzer.CombineScores(msgScore, toolScore)

	return selector.SelectModel(&SelectionContext{
		Complexity:     combined,
		AvailableTools: tools,
	})
}

// --- Time-based budget tracking helper ---

// IsBudgetExhausted returns true if the daily cost budget has been reached.
// Relies on the usage tracker having been reset at the start of each day.
func IsBudgetExhausted(budget float64, tracker *UsageTracker) bool {
	if budget <= 0 || tracker == nil {
		return false
	}
	snapshot := tracker.GetSnapshot()
	// Only count cost accumulated today (since the tracker's start time)
	if time.Since(snapshot.Since) > 24*time.Hour {
		// Tracker hasn't been reset in >24h; treat as non-exhausted
		// (the caller should reset the tracker daily)
		return false
	}
	return tracker.TotalCost() >= budget
}
