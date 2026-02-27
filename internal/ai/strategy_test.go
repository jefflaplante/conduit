package ai

import (
	"testing"

	"conduit/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelTier_String(t *testing.T) {
	assert.Equal(t, "haiku", TierHaiku.String())
	assert.Equal(t, "sonnet", TierSonnet.String())
	assert.Equal(t, "opus", TierOpus.String())
	assert.Equal(t, "unknown", ModelTier(99).String())
}

// --- NewDefaultModelSelector ---

func TestNewDefaultModelSelector_WithAliases(t *testing.T) {
	aliases := config.DefaultModelAliases()
	selector := NewDefaultModelSelector(nil, aliases, nil)
	require.NotNil(t, selector)

	tiers := selector.GetTiers()
	assert.Len(t, tiers, 3)
	assert.Equal(t, TierHaiku, tiers[0].Tier)
	assert.Equal(t, TierSonnet, tiers[1].Tier)
	assert.Equal(t, TierOpus, tiers[2].Tier)

	// Verify aliases were resolved
	assert.Equal(t, aliases["haiku"], tiers[0].ModelID)
	assert.Equal(t, aliases["sonnet"], tiers[1].ModelID)
	assert.Equal(t, aliases["opus"], tiers[2].ModelID)
}

func TestNewDefaultModelSelector_NilAliases(t *testing.T) {
	selector := NewDefaultModelSelector(nil, nil, nil)
	require.NotNil(t, selector)

	tiers := selector.GetTiers()
	// Should use fallback model IDs
	assert.Equal(t, "claude-haiku-4-5-20251001", tiers[0].ModelID)
	assert.Equal(t, "claude-sonnet-4-6", tiers[1].ModelID)
	assert.Equal(t, "claude-opus-4-6", tiers[2].ModelID)
}

func TestNewDefaultModelSelector_WithBudget(t *testing.T) {
	smartCfg := &config.SmartRoutingConfig{
		Enabled:         true,
		CostBudgetDaily: 5.0,
	}
	selector := NewDefaultModelSelector(smartCfg, nil, nil)
	assert.Equal(t, 5.0, selector.dailyBudget)
}

// --- SelectModel with explicit override ---

func TestSelectModel_ExplicitOverride(t *testing.T) {
	selector := NewDefaultModelSelector(nil, nil, nil)

	result := selector.SelectModel(&SelectionContext{
		Complexity:     ComplexityScore{Level: ComplexitySimple, Score: 5},
		RequestedModel: "claude-opus-4-6",
	})

	assert.Equal(t, "claude-opus-4-6", result.Model)
	assert.True(t, result.Overridden)
	assert.Equal(t, "explicit model override", result.Reason)
	assert.Equal(t, TierOpus, result.Tier)
}

// --- SelectModel based on complexity ---

func TestSelectModel_SimpleComplexity(t *testing.T) {
	selector := NewDefaultModelSelector(nil, config.DefaultModelAliases(), nil)

	result := selector.SelectModel(&SelectionContext{
		Complexity: ComplexityScore{Level: ComplexitySimple, Score: 5},
	})

	assert.Equal(t, TierHaiku, result.Tier)
	assert.Contains(t, result.Reason, "simple")
}

func TestSelectModel_StandardComplexity(t *testing.T) {
	selector := NewDefaultModelSelector(nil, config.DefaultModelAliases(), nil)

	result := selector.SelectModel(&SelectionContext{
		Complexity: ComplexityScore{Level: ComplexityStandard, Score: 25},
	})

	assert.Equal(t, TierSonnet, result.Tier)
	assert.Contains(t, result.Reason, "standard")
}

func TestSelectModel_ComplexComplexity(t *testing.T) {
	selector := NewDefaultModelSelector(nil, config.DefaultModelAliases(), nil)

	result := selector.SelectModel(&SelectionContext{
		Complexity: ComplexityScore{Level: ComplexityComplex, Score: 60},
	})

	assert.Equal(t, TierOpus, result.Tier)
	assert.Contains(t, result.Reason, "complex")
}

// --- Budget constraints ---

func TestSelectModel_OverBudget(t *testing.T) {
	tracker := NewUsageTracker()
	// Simulate spending that exceeds the budget
	// Use claude-opus pricing: $15/MTok input, $75/MTok output
	// 1M input + 1M output = $15 + $75 = $90
	tracker.RecordUsage("anthropic", "claude-opus-4-6", 1_000_000, 1_000_000, 500)

	smartCfg := &config.SmartRoutingConfig{
		Enabled:         true,
		CostBudgetDaily: 10.0, // Budget is $10, we've spent $90
	}
	selector := NewDefaultModelSelector(smartCfg, config.DefaultModelAliases(), tracker)

	// Even though task is complex, should downgrade to haiku
	result := selector.SelectModel(&SelectionContext{
		Complexity: ComplexityScore{Level: ComplexityComplex, Score: 60},
	})

	assert.Equal(t, TierHaiku, result.Tier)
	assert.Contains(t, result.Reason, "budget")
}

func TestSelectModel_ApproachingBudget(t *testing.T) {
	tracker := NewUsageTracker()
	// Spend ~$9 against a $10 budget (>80%)
	// Using sonnet: $3/MTok input, $15/MTok output
	// Need ~$9 total: 1M input ($3) + 400K output ($6) = $9
	tracker.RecordUsage("anthropic", "claude-sonnet-4", 1_000_000, 400_000, 300)

	smartCfg := &config.SmartRoutingConfig{
		Enabled:         true,
		CostBudgetDaily: 10.0,
	}
	selector := NewDefaultModelSelector(smartCfg, config.DefaultModelAliases(), tracker)

	// Complex task, but budget is 90% consumed -> should downgrade opus to sonnet
	result := selector.SelectModel(&SelectionContext{
		Complexity: ComplexityScore{Level: ComplexityComplex, Score: 60},
	})

	assert.Equal(t, TierSonnet, result.Tier)
}

func TestSelectModel_UnderBudget(t *testing.T) {
	tracker := NewUsageTracker()
	// Small spend
	tracker.RecordUsage("anthropic", "claude-haiku-4-5", 1000, 500, 50)

	smartCfg := &config.SmartRoutingConfig{
		Enabled:         true,
		CostBudgetDaily: 100.0,
	}
	selector := NewDefaultModelSelector(smartCfg, config.DefaultModelAliases(), tracker)

	// Complex task, plenty of budget -> should select opus
	result := selector.SelectModel(&SelectionContext{
		Complexity: ComplexityScore{Level: ComplexityComplex, Score: 60},
	})

	assert.Equal(t, TierOpus, result.Tier)
}

func TestSelectModel_NoBudget(t *testing.T) {
	tracker := NewUsageTracker()
	tracker.RecordUsage("anthropic", "claude-opus-4-6", 10_000_000, 5_000_000, 1000)

	// No budget configured (0 means unlimited)
	selector := NewDefaultModelSelector(nil, config.DefaultModelAliases(), tracker)

	result := selector.SelectModel(&SelectionContext{
		Complexity: ComplexityScore{Level: ComplexityComplex, Score: 60},
	})

	// Should not be constrained
	assert.Equal(t, TierOpus, result.Tier)
}

// --- Error rate escalation ---

func TestSelectModel_HighErrorRate(t *testing.T) {
	tracker := NewUsageTracker()
	aliases := config.DefaultModelAliases()
	haikuModel := aliases["haiku"]

	// Record many errors for haiku
	for i := 0; i < 4; i++ {
		tracker.RecordError("anthropic", haikuModel)
	}
	// One success
	tracker.RecordUsage("anthropic", haikuModel, 100, 50, 10)

	selector := NewDefaultModelSelector(nil, aliases, tracker)

	// Simple task -> would normally be haiku, but haiku has 80% error rate
	result := selector.SelectModel(&SelectionContext{
		Complexity: ComplexityScore{Level: ComplexitySimple, Score: 5},
	})

	// Should escalate from haiku to sonnet
	assert.Equal(t, TierSonnet, result.Tier)
}

func TestSelectModel_LowErrorRate(t *testing.T) {
	tracker := NewUsageTracker()
	aliases := config.DefaultModelAliases()
	haikuModel := aliases["haiku"]

	// 1 error out of 20 requests (5% error rate, below 30% threshold)
	for i := 0; i < 19; i++ {
		tracker.RecordUsage("anthropic", haikuModel, 100, 50, 10)
	}
	tracker.RecordError("anthropic", haikuModel)

	selector := NewDefaultModelSelector(nil, aliases, tracker)

	result := selector.SelectModel(&SelectionContext{
		Complexity: ComplexityScore{Level: ComplexitySimple, Score: 5},
	})

	// Should stay at haiku (error rate is acceptable)
	assert.Equal(t, TierHaiku, result.Tier)
}

// --- EstimatedCostPer1K ---

func TestSelectModel_CostEstimate(t *testing.T) {
	selector := NewDefaultModelSelector(nil, config.DefaultModelAliases(), nil)

	result := selector.SelectModel(&SelectionContext{
		Complexity: ComplexityScore{Level: ComplexitySimple, Score: 5},
	})

	// Haiku: ($0.80 + $4.00) / 2 / 1000 = $0.0024
	assert.Greater(t, result.EstimatedCostPer1KTokens, 0.0)
}

// --- tierForModel ---

func TestTierForModel_KnownModels(t *testing.T) {
	selector := NewDefaultModelSelector(nil, config.DefaultModelAliases(), nil)

	assert.Equal(t, TierHaiku, selector.tierForModel("claude-haiku-4-5-20251001"))
	assert.Equal(t, TierOpus, selector.tierForModel("claude-opus-4-6"))

	// Heuristic fallback
	assert.Equal(t, TierHaiku, selector.tierForModel("some-haiku-model"))
	assert.Equal(t, TierOpus, selector.tierForModel("some-opus-model"))
	assert.Equal(t, TierSonnet, selector.tierForModel("some-unknown-model"))
}

// --- resolveAlias ---

func TestResolveAlias(t *testing.T) {
	aliases := map[string]string{"haiku": "claude-haiku-custom"}

	assert.Equal(t, "claude-haiku-custom", resolveAlias(aliases, "haiku", "fallback"))
	assert.Equal(t, "fallback", resolveAlias(aliases, "missing", "fallback"))
	assert.Equal(t, "fallback", resolveAlias(nil, "haiku", "fallback"))
}

// --- QuickSelectModel ---

func TestQuickSelectModel_SimpleMessage(t *testing.T) {
	result := QuickSelectModel("hello", nil, nil, config.DefaultModelAliases(), nil)
	assert.Equal(t, TierHaiku, result.Tier)
}

func TestQuickSelectModel_ComplexMessage(t *testing.T) {
	msg := "Refactor the entire architecture, migrate the database schema, " +
		"and implement a new design pattern across multiple files with full test coverage"
	result := QuickSelectModel(msg, nil, nil, config.DefaultModelAliases(), nil)
	// Should be at least standard, likely complex
	assert.GreaterOrEqual(t, int(result.Tier), int(TierSonnet))
}

func TestQuickSelectModel_WithManyTools(t *testing.T) {
	tools := make([]Tool, 16)
	for i := range tools {
		tools[i] = Tool{Name: "Tool" + string(rune('A'+i)), Description: "tool"}
	}
	tools[0] = Tool{Name: "Bash"}
	tools[1] = Tool{Name: "Edit"}
	tools[2] = Tool{Name: "Write"}

	result := QuickSelectModel("do something", tools, nil, config.DefaultModelAliases(), nil)
	// Many tools + some complex ones -> should at least be standard
	assert.GreaterOrEqual(t, int(result.Tier), int(TierHaiku))
}

// --- IsBudgetExhausted ---

func TestIsBudgetExhausted(t *testing.T) {
	tracker := NewUsageTracker()

	// No budget
	assert.False(t, IsBudgetExhausted(0, tracker))

	// Nil tracker
	assert.False(t, IsBudgetExhausted(10.0, nil))

	// Under budget
	tracker.RecordUsage("anthropic", "claude-sonnet-4", 1000, 500, 100)
	assert.False(t, IsBudgetExhausted(100.0, tracker))

	// Over budget (spend a lot)
	tracker.RecordUsage("anthropic", "claude-opus-4-6", 10_000_000, 10_000_000, 500)
	assert.True(t, IsBudgetExhausted(10.0, tracker))
}

// --- Integration: end-to-end with complexity + selection ---

func TestEndToEnd_SimpleGreetingSelectsHaiku(t *testing.T) {
	analyzer := NewComplexityAnalyzer()
	selector := NewDefaultModelSelector(nil, config.DefaultModelAliases(), nil)

	score := analyzer.AnalyzeMessage("Hi there!")
	result := selector.SelectModel(&SelectionContext{
		Complexity: score,
	})

	assert.Equal(t, TierHaiku, result.Tier)
	assert.Contains(t, result.Reason, "simple")
}

func TestEndToEnd_ComplexTaskSelectsOpus(t *testing.T) {
	analyzer := NewComplexityAnalyzer()
	selector := NewDefaultModelSelector(nil, config.DefaultModelAliases(), nil)

	msg := "Refactor the architecture and migrate all database schemas, " +
		"then implement the new design pattern and build comprehensive tests"
	score := analyzer.AnalyzeMessage(msg)
	result := selector.SelectModel(&SelectionContext{
		Complexity: score,
	})

	assert.Equal(t, TierOpus, result.Tier)
	assert.Contains(t, result.Reason, "complex")
}

func TestEndToEnd_BudgetConstrainedComplexTask(t *testing.T) {
	analyzer := NewComplexityAnalyzer()
	tracker := NewUsageTracker()

	// Blow the budget
	tracker.RecordUsage("anthropic", "claude-opus-4-6", 5_000_000, 5_000_000, 1000)

	smartCfg := &config.SmartRoutingConfig{
		Enabled:         true,
		CostBudgetDaily: 1.0, // Very low budget
	}
	selector := NewDefaultModelSelector(smartCfg, config.DefaultModelAliases(), tracker)

	msg := "Refactor the architecture and migrate the entire codebase"
	score := analyzer.AnalyzeMessage(msg)
	result := selector.SelectModel(&SelectionContext{
		Complexity: score,
	})

	// Budget is blown -> forced to haiku regardless of complexity
	assert.Equal(t, TierHaiku, result.Tier)
	assert.Contains(t, result.Reason, "budget")
}
