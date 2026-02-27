package ai

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- CostPolicy ---

func TestCostPolicy_String(t *testing.T) {
	assert.Equal(t, "strict", PolicyStrict.String())
	assert.Equal(t, "best-effort", PolicyBestEffort.String())
	assert.Equal(t, "quality-first", PolicyQualityFirst.String())
	assert.Equal(t, "unknown", CostPolicy(99).String())
}

// --- NewCostOptimizer ---

func TestNewCostOptimizer_Defaults(t *testing.T) {
	co := NewCostOptimizer(nil)
	require.NotNil(t, co)

	assert.Equal(t, PolicyBestEffort, co.Policy())
	assert.Equal(t, 0, co.RecordCount())
}

func TestNewCostOptimizer_CustomConfig(t *testing.T) {
	cfg := &CostOptimizerConfig{
		Policy:      PolicyStrict,
		DailyBudget: 10.0,
		WindowSize:  1 * time.Hour,
		MaxRecords:  500,
	}
	co := NewCostOptimizer(cfg)
	require.NotNil(t, co)

	assert.Equal(t, PolicyStrict, co.Policy())
}

// --- RecordCost ---

func TestRecordCost_BasicRecording(t *testing.T) {
	co := NewCostOptimizer(nil)

	result := &SmartRoutingResult{
		SelectedModel: "claude-sonnet-4",
		Tier:          TierSonnet,
		Complexity:    ComplexityScore{Level: ComplexityStandard},
	}

	co.RecordCost(result, 1000, 500)
	assert.Equal(t, 1, co.RecordCount())
}

func TestRecordCost_NilResult(t *testing.T) {
	co := NewCostOptimizer(nil)
	co.RecordCost(nil, 1000, 500)
	assert.Equal(t, 0, co.RecordCount())
}

func TestRecordCost_MultipleRecords(t *testing.T) {
	co := NewCostOptimizer(nil)

	for i := 0; i < 5; i++ {
		result := &SmartRoutingResult{
			SelectedModel: "claude-sonnet-4",
			Tier:          TierSonnet,
			Complexity:    ComplexityScore{Level: ComplexityStandard},
		}
		co.RecordCost(result, 1000, 500)
	}
	assert.Equal(t, 5, co.RecordCount())
}

func TestRecordCost_MaxRecordsEviction(t *testing.T) {
	cfg := &CostOptimizerConfig{
		MaxRecords: 5,
	}
	co := NewCostOptimizer(cfg)

	for i := 0; i < 10; i++ {
		result := &SmartRoutingResult{
			SelectedModel: "claude-sonnet-4",
			Tier:          TierSonnet,
			Complexity:    ComplexityScore{Level: ComplexityStandard},
		}
		co.RecordCost(result, 1000, 500)
	}

	// Should be capped at MaxRecords
	assert.Equal(t, 5, co.RecordCount())
}

// --- GetBreakdown ---

func TestGetBreakdown_Empty(t *testing.T) {
	co := NewCostOptimizer(nil)
	breakdown := co.GetBreakdown(1 * time.Hour)

	require.NotNil(t, breakdown)
	assert.Equal(t, 0.0, breakdown.TotalCost)
	assert.Equal(t, 0, breakdown.TotalRequests)
	assert.Empty(t, breakdown.ByModel)
	assert.Empty(t, breakdown.ByTier)
	assert.Empty(t, breakdown.ByComplexity)
}

func TestGetBreakdown_ByModel(t *testing.T) {
	co := NewCostOptimizer(nil)

	// Record sonnet requests
	for i := 0; i < 3; i++ {
		co.RecordCost(&SmartRoutingResult{
			SelectedModel: "claude-sonnet-4",
			Tier:          TierSonnet,
			Complexity:    ComplexityScore{Level: ComplexityStandard},
		}, 10000, 5000)
	}

	// Record haiku requests
	for i := 0; i < 2; i++ {
		co.RecordCost(&SmartRoutingResult{
			SelectedModel: "claude-haiku-4",
			Tier:          TierHaiku,
			Complexity:    ComplexityScore{Level: ComplexitySimple},
		}, 10000, 5000)
	}

	breakdown := co.GetBreakdown(1 * time.Hour)

	assert.Equal(t, 5, breakdown.TotalRequests)
	assert.Len(t, breakdown.ByModel, 2)

	sonnetEntry := breakdown.ByModel["claude-sonnet-4"]
	require.NotNil(t, sonnetEntry)
	assert.Equal(t, 3, sonnetEntry.RequestCount)
	assert.Greater(t, sonnetEntry.TotalCost, 0.0)
	assert.Greater(t, sonnetEntry.AvgCost, 0.0)

	haikuEntry := breakdown.ByModel["claude-haiku-4"]
	require.NotNil(t, haikuEntry)
	assert.Equal(t, 2, haikuEntry.RequestCount)

	// Sonnet should cost more than haiku per request
	assert.Greater(t, sonnetEntry.AvgCost, haikuEntry.AvgCost)
}

func TestGetBreakdown_ByTier(t *testing.T) {
	co := NewCostOptimizer(nil)

	co.RecordCost(&SmartRoutingResult{
		SelectedModel: "claude-sonnet-4",
		Tier:          TierSonnet,
		Complexity:    ComplexityScore{Level: ComplexityStandard},
	}, 10000, 5000)

	co.RecordCost(&SmartRoutingResult{
		SelectedModel: "claude-opus-4",
		Tier:          TierOpus,
		Complexity:    ComplexityScore{Level: ComplexityComplex},
	}, 10000, 5000)

	breakdown := co.GetBreakdown(1 * time.Hour)
	assert.Len(t, breakdown.ByTier, 2)

	sonnetTier := breakdown.ByTier["sonnet"]
	require.NotNil(t, sonnetTier)
	assert.Equal(t, 1, sonnetTier.RequestCount)

	opusTier := breakdown.ByTier["opus"]
	require.NotNil(t, opusTier)
	assert.Equal(t, 1, opusTier.RequestCount)

	// Opus should cost more
	assert.Greater(t, opusTier.TotalCost, sonnetTier.TotalCost)
}

func TestGetBreakdown_ByComplexity(t *testing.T) {
	co := NewCostOptimizer(nil)

	co.RecordCost(&SmartRoutingResult{
		SelectedModel: "claude-haiku-4",
		Tier:          TierHaiku,
		Complexity:    ComplexityScore{Level: ComplexitySimple},
	}, 5000, 2000)

	co.RecordCost(&SmartRoutingResult{
		SelectedModel: "claude-sonnet-4",
		Tier:          TierSonnet,
		Complexity:    ComplexityScore{Level: ComplexityStandard},
	}, 10000, 5000)

	co.RecordCost(&SmartRoutingResult{
		SelectedModel: "claude-opus-4",
		Tier:          TierOpus,
		Complexity:    ComplexityScore{Level: ComplexityComplex},
	}, 20000, 10000)

	breakdown := co.GetBreakdown(1 * time.Hour)
	assert.Len(t, breakdown.ByComplexity, 3)

	simpleEntry := breakdown.ByComplexity[ComplexitySimple]
	require.NotNil(t, simpleEntry)
	assert.Equal(t, 1, simpleEntry.RequestCount)

	standardEntry := breakdown.ByComplexity[ComplexityStandard]
	require.NotNil(t, standardEntry)
	assert.Equal(t, 1, standardEntry.RequestCount)

	complexEntry := breakdown.ByComplexity[ComplexityComplex]
	require.NotNil(t, complexEntry)
	assert.Equal(t, 1, complexEntry.RequestCount)
}

func TestGetBreakdown_TokenAccumulation(t *testing.T) {
	co := NewCostOptimizer(nil)

	co.RecordCost(&SmartRoutingResult{
		SelectedModel: "claude-sonnet-4",
		Tier:          TierSonnet,
		Complexity:    ComplexityScore{Level: ComplexityStandard},
	}, 10000, 5000)

	co.RecordCost(&SmartRoutingResult{
		SelectedModel: "claude-sonnet-4",
		Tier:          TierSonnet,
		Complexity:    ComplexityScore{Level: ComplexityStandard},
	}, 20000, 10000)

	breakdown := co.GetBreakdown(1 * time.Hour)
	entry := breakdown.ByModel["claude-sonnet-4"]
	require.NotNil(t, entry)
	assert.Equal(t, 30000, entry.InputTokens)
	assert.Equal(t, 15000, entry.OutputTokens)
}

func TestGetBreakdown_PeriodFiltering(t *testing.T) {
	co := NewCostOptimizer(nil)

	// Manually inject an old record to test period filtering
	co.mu.Lock()
	co.records = append(co.records, CostRecord{
		Timestamp:    time.Now().Add(-2 * time.Hour),
		Model:        "claude-opus-4",
		Tier:         "opus",
		Complexity:   ComplexityComplex,
		InputTokens:  100000,
		OutputTokens: 50000,
		Cost:         5.25,
	})
	co.mu.Unlock()

	// Record a recent one
	co.RecordCost(&SmartRoutingResult{
		SelectedModel: "claude-haiku-4",
		Tier:          TierHaiku,
		Complexity:    ComplexityScore{Level: ComplexitySimple},
	}, 1000, 500)

	// 1-hour window should only include the recent record
	breakdown := co.GetBreakdown(1 * time.Hour)
	assert.Equal(t, 1, breakdown.TotalRequests)

	// 3-hour window should include both
	breakdown3h := co.GetBreakdown(3 * time.Hour)
	assert.Equal(t, 2, breakdown3h.TotalRequests)
}

// --- GetOptimizationSuggestions ---

func TestGetOptimizationSuggestions_Empty(t *testing.T) {
	co := NewCostOptimizer(nil)
	suggestions := co.GetOptimizationSuggestions()
	assert.Empty(t, suggestions)
}

func TestGetOptimizationSuggestions_SimpleOnExpensive(t *testing.T) {
	co := NewCostOptimizer(nil)

	// Record many simple requests going to sonnet
	for i := 0; i < 20; i++ {
		co.RecordCost(&SmartRoutingResult{
			SelectedModel: "claude-sonnet-4",
			Tier:          TierSonnet,
			Complexity:    ComplexityScore{Level: ComplexitySimple},
		}, 5000, 2000)
	}

	suggestions := co.GetOptimizationSuggestions()
	require.NotEmpty(t, suggestions)

	// Should suggest routing simple requests to haiku
	found := false
	for _, s := range suggestions {
		if s.AffectedPct > 0 && s.EstimatedSavings > 0 {
			found = true
			assert.Contains(t, s.Description, "simple")
			assert.Greater(t, s.Confidence, 0.0)
		}
	}
	assert.True(t, found, "Expected a suggestion about simple requests on expensive models")
}

func TestGetOptimizationSuggestions_StandardOnOpus(t *testing.T) {
	co := NewCostOptimizer(nil)

	// Record many standard requests going to opus
	for i := 0; i < 15; i++ {
		co.RecordCost(&SmartRoutingResult{
			SelectedModel: "claude-opus-4",
			Tier:          TierOpus,
			Complexity:    ComplexityScore{Level: ComplexityStandard},
		}, 10000, 5000)
	}

	suggestions := co.GetOptimizationSuggestions()
	require.NotEmpty(t, suggestions)

	// Should suggest using sonnet for standard requests
	found := false
	for _, s := range suggestions {
		if s.EstimatedSavings > 0 {
			found = true
			assert.Greater(t, s.Confidence, 0.0)
		}
	}
	assert.True(t, found)
}

func TestGetOptimizationSuggestions_NoWaste(t *testing.T) {
	co := NewCostOptimizer(nil)

	// Record properly-routed requests: simple->haiku, standard->sonnet, complex->opus
	co.RecordCost(&SmartRoutingResult{
		SelectedModel: "claude-haiku-4",
		Tier:          TierHaiku,
		Complexity:    ComplexityScore{Level: ComplexitySimple},
	}, 5000, 2000)

	co.RecordCost(&SmartRoutingResult{
		SelectedModel: "claude-sonnet-4",
		Tier:          TierSonnet,
		Complexity:    ComplexityScore{Level: ComplexityStandard},
	}, 10000, 5000)

	co.RecordCost(&SmartRoutingResult{
		SelectedModel: "claude-opus-4",
		Tier:          TierOpus,
		Complexity:    ComplexityScore{Level: ComplexityComplex},
	}, 20000, 10000)

	suggestions := co.GetOptimizationSuggestions()
	// With well-routed traffic and few records, there should be no major suggestions
	// (spike detection requires 10+ records)
	for _, s := range suggestions {
		// Any suggestion should have low savings (no waste scenarios)
		assert.Less(t, s.AffectedPct, 50.0, "Well-routed traffic shouldn't have high-impact suggestions")
	}
}

func TestGetOptimizationSuggestions_CostSpike(t *testing.T) {
	co := NewCostOptimizer(nil)

	// First half: cheap requests
	for i := 0; i < 8; i++ {
		co.RecordCost(&SmartRoutingResult{
			SelectedModel: "claude-haiku-4",
			Tier:          TierHaiku,
			Complexity:    ComplexityScore{Level: ComplexitySimple},
		}, 1000, 500)
	}

	// Second half: expensive requests (cost spike)
	for i := 0; i < 8; i++ {
		co.RecordCost(&SmartRoutingResult{
			SelectedModel: "claude-opus-4",
			Tier:          TierOpus,
			Complexity:    ComplexityScore{Level: ComplexityComplex},
		}, 100000, 50000)
	}

	suggestions := co.GetOptimizationSuggestions()
	require.NotEmpty(t, suggestions)

	// Should detect cost spike
	found := false
	for _, s := range suggestions {
		if s.AffectedPct == 100.0 {
			found = true
			assert.Contains(t, s.Description, "spike")
		}
	}
	assert.True(t, found, "Expected a cost spike suggestion")
}

func TestGetOptimizationSuggestions_SortedBySavings(t *testing.T) {
	co := NewCostOptimizer(nil)

	// Create a scenario with multiple suggestion types
	for i := 0; i < 15; i++ {
		co.RecordCost(&SmartRoutingResult{
			SelectedModel: "claude-opus-4",
			Tier:          TierOpus,
			Complexity:    ComplexityScore{Level: ComplexitySimple},
		}, 10000, 5000)
	}

	suggestions := co.GetOptimizationSuggestions()
	if len(suggestions) >= 2 {
		for i := 1; i < len(suggestions); i++ {
			assert.GreaterOrEqual(t, suggestions[i-1].EstimatedSavings, suggestions[i].EstimatedSavings,
				"Suggestions should be sorted by estimated savings descending")
		}
	}
}

// --- ShouldDowngrade ---

func TestShouldDowngrade_BestEffort_SimpleOnOpus(t *testing.T) {
	co := NewCostOptimizer(nil)

	shouldDowngrade, suggestedTier := co.ShouldDowngrade(ComplexitySimple, "opus")
	assert.True(t, shouldDowngrade)
	assert.Equal(t, "haiku", suggestedTier)
}

func TestShouldDowngrade_BestEffort_SimpleOnSonnet(t *testing.T) {
	co := NewCostOptimizer(nil)

	shouldDowngrade, suggestedTier := co.ShouldDowngrade(ComplexitySimple, "sonnet")
	assert.True(t, shouldDowngrade)
	assert.Equal(t, "haiku", suggestedTier)
}

func TestShouldDowngrade_BestEffort_ComplexOnOpus(t *testing.T) {
	co := NewCostOptimizer(nil)

	shouldDowngrade, _ := co.ShouldDowngrade(ComplexityComplex, "opus")
	assert.False(t, shouldDowngrade)
}

func TestShouldDowngrade_BestEffort_StandardOnSonnet(t *testing.T) {
	co := NewCostOptimizer(nil)

	shouldDowngrade, _ := co.ShouldDowngrade(ComplexityStandard, "sonnet")
	assert.False(t, shouldDowngrade)
}

func TestShouldDowngrade_BestEffort_OverBudget(t *testing.T) {
	cfg := &CostOptimizerConfig{
		Policy:      PolicyBestEffort,
		DailyBudget: 10.0,
		WindowSize:  24 * time.Hour,
	}
	co := NewCostOptimizer(cfg)

	// Spend over the budget
	for i := 0; i < 10; i++ {
		co.RecordCost(&SmartRoutingResult{
			SelectedModel: "claude-opus-4",
			Tier:          TierOpus,
			Complexity:    ComplexityScore{Level: ComplexityComplex},
		}, 100000, 50000)
	}

	shouldDowngrade, suggestedTier := co.ShouldDowngrade(ComplexityComplex, "opus")
	assert.True(t, shouldDowngrade)
	assert.Equal(t, "haiku", suggestedTier)
}

func TestShouldDowngrade_Strict_SimpleOnSonnet(t *testing.T) {
	cfg := &CostOptimizerConfig{
		Policy: PolicyStrict,
	}
	co := NewCostOptimizer(cfg)

	shouldDowngrade, suggestedTier := co.ShouldDowngrade(ComplexitySimple, "sonnet")
	assert.True(t, shouldDowngrade)
	assert.Equal(t, "haiku", suggestedTier)
}

func TestShouldDowngrade_Strict_SimpleOnOpus(t *testing.T) {
	cfg := &CostOptimizerConfig{
		Policy: PolicyStrict,
	}
	co := NewCostOptimizer(cfg)

	shouldDowngrade, suggestedTier := co.ShouldDowngrade(ComplexitySimple, "opus")
	assert.True(t, shouldDowngrade)
	assert.Equal(t, "haiku", suggestedTier)
}

func TestShouldDowngrade_Strict_StandardOnOpus(t *testing.T) {
	cfg := &CostOptimizerConfig{
		Policy: PolicyStrict,
	}
	co := NewCostOptimizer(cfg)

	shouldDowngrade, suggestedTier := co.ShouldDowngrade(ComplexityStandard, "opus")
	assert.True(t, shouldDowngrade)
	assert.Equal(t, "sonnet", suggestedTier)
}

func TestShouldDowngrade_Strict_ComplexOnOpus(t *testing.T) {
	cfg := &CostOptimizerConfig{
		Policy: PolicyStrict,
	}
	co := NewCostOptimizer(cfg)

	shouldDowngrade, _ := co.ShouldDowngrade(ComplexityComplex, "opus")
	assert.False(t, shouldDowngrade)
}

func TestShouldDowngrade_Strict_BudgetAt70Pct(t *testing.T) {
	cfg := &CostOptimizerConfig{
		Policy:      PolicyStrict,
		DailyBudget: 10.0,
		WindowSize:  24 * time.Hour,
	}
	co := NewCostOptimizer(cfg)

	// Spend 75% of the budget using sonnet
	// sonnet: $3/MTok input, $15/MTok output
	// Need ~$7.5: 500K input ($1.5) + 400K output ($6.0) = $7.5
	co.RecordCost(&SmartRoutingResult{
		SelectedModel: "claude-sonnet-4",
		Tier:          TierSonnet,
		Complexity:    ComplexityScore{Level: ComplexityStandard},
	}, 500000, 400000)

	// Opus should be downgraded at 70% utilization
	shouldDowngrade, suggestedTier := co.ShouldDowngrade(ComplexityComplex, "opus")
	assert.True(t, shouldDowngrade)
	assert.Equal(t, "sonnet", suggestedTier)
}

func TestShouldDowngrade_Strict_BudgetAt90Pct(t *testing.T) {
	cfg := &CostOptimizerConfig{
		Policy:      PolicyStrict,
		DailyBudget: 10.0,
		WindowSize:  24 * time.Hour,
	}
	co := NewCostOptimizer(cfg)

	// Spend ~$9.5 (95% of $10 budget) using opus
	// opus: $15/MTok input, $75/MTok output
	// 100K input ($1.5) + 106K output (~$7.95) = ~$9.45
	co.RecordCost(&SmartRoutingResult{
		SelectedModel: "claude-opus-4",
		Tier:          TierOpus,
		Complexity:    ComplexityScore{Level: ComplexityComplex},
	}, 100000, 106000)

	// Sonnet should be downgraded to haiku at 90%+ utilization
	shouldDowngrade, suggestedTier := co.ShouldDowngrade(ComplexityStandard, "sonnet")
	assert.True(t, shouldDowngrade)
	assert.Equal(t, "haiku", suggestedTier)
}

func TestShouldDowngrade_QualityFirst_NoDowngradeWithinBudget(t *testing.T) {
	cfg := &CostOptimizerConfig{
		Policy:      PolicyQualityFirst,
		DailyBudget: 100.0,
		WindowSize:  24 * time.Hour,
	}
	co := NewCostOptimizer(cfg)

	// Small spend, within budget
	co.RecordCost(&SmartRoutingResult{
		SelectedModel: "claude-sonnet-4",
		Tier:          TierSonnet,
		Complexity:    ComplexityScore{Level: ComplexitySimple},
	}, 1000, 500)

	// Even simple on opus should NOT be downgraded in quality-first mode
	shouldDowngrade, _ := co.ShouldDowngrade(ComplexitySimple, "opus")
	assert.False(t, shouldDowngrade)
}

func TestShouldDowngrade_QualityFirst_OverBudget(t *testing.T) {
	cfg := &CostOptimizerConfig{
		Policy:      PolicyQualityFirst,
		DailyBudget: 1.0,
		WindowSize:  24 * time.Hour,
	}
	co := NewCostOptimizer(cfg)

	// Blow the budget
	co.RecordCost(&SmartRoutingResult{
		SelectedModel: "claude-opus-4",
		Tier:          TierOpus,
		Complexity:    ComplexityScore{Level: ComplexityComplex},
	}, 1000000, 500000)

	shouldDowngrade, suggestedTier := co.ShouldDowngrade(ComplexityComplex, "opus")
	assert.True(t, shouldDowngrade)
	assert.Equal(t, "haiku", suggestedTier)
}

func TestShouldDowngrade_NoBudgetConfigured(t *testing.T) {
	// No budget means no budget-based downgrade
	co := NewCostOptimizer(nil)

	// Standard on sonnet, no budget pressure
	shouldDowngrade, _ := co.ShouldDowngrade(ComplexityStandard, "sonnet")
	assert.False(t, shouldDowngrade)
}

// --- GetSavingsEstimate ---

func TestGetSavingsEstimate_Empty(t *testing.T) {
	co := NewCostOptimizer(nil)
	estimate := co.GetSavingsEstimate()

	require.NotNil(t, estimate)
	assert.Equal(t, 0.0, estimate.CurrentSpend)
	assert.Equal(t, 0.0, estimate.OptimalSpend)
	assert.Equal(t, 0.0, estimate.PotentialSavings)
	assert.Equal(t, 0.0, estimate.SavingsPct)
}

func TestGetSavingsEstimate_NoWaste(t *testing.T) {
	co := NewCostOptimizer(nil)

	// All requests go to optimal tier
	co.RecordCost(&SmartRoutingResult{
		SelectedModel: "claude-haiku-4",
		Tier:          TierHaiku,
		Complexity:    ComplexityScore{Level: ComplexitySimple},
	}, 10000, 5000)

	estimate := co.GetSavingsEstimate()
	require.NotNil(t, estimate)
	assert.Greater(t, estimate.CurrentSpend, 0.0)
	// Optimal spend should be close to current when properly routed
	assert.InDelta(t, estimate.CurrentSpend, estimate.OptimalSpend, estimate.CurrentSpend*0.5)
}

func TestGetSavingsEstimate_WithWaste(t *testing.T) {
	co := NewCostOptimizer(nil)

	// Simple requests routed to opus (wasteful)
	for i := 0; i < 10; i++ {
		co.RecordCost(&SmartRoutingResult{
			SelectedModel: "claude-opus-4",
			Tier:          TierOpus,
			Complexity:    ComplexityScore{Level: ComplexitySimple},
		}, 10000, 5000)
	}

	estimate := co.GetSavingsEstimate()
	require.NotNil(t, estimate)
	assert.Greater(t, estimate.CurrentSpend, 0.0)
	assert.Greater(t, estimate.PotentialSavings, 0.0)
	assert.Greater(t, estimate.SavingsPct, 0.0)
	assert.Less(t, estimate.OptimalSpend, estimate.CurrentSpend)
}

func TestGetSavingsEstimate_SavingsNonNegative(t *testing.T) {
	co := NewCostOptimizer(nil)

	// Edge case: cheapest model used
	co.RecordCost(&SmartRoutingResult{
		SelectedModel: "claude-haiku-4",
		Tier:          TierHaiku,
		Complexity:    ComplexityScore{Level: ComplexityComplex},
	}, 10000, 5000)

	estimate := co.GetSavingsEstimate()
	assert.GreaterOrEqual(t, estimate.PotentialSavings, 0.0)
}

func TestGetSavingsEstimate_IncludesSuggestions(t *testing.T) {
	co := NewCostOptimizer(nil)

	// Create a scenario that generates suggestions
	for i := 0; i < 20; i++ {
		co.RecordCost(&SmartRoutingResult{
			SelectedModel: "claude-opus-4",
			Tier:          TierOpus,
			Complexity:    ComplexityScore{Level: ComplexitySimple},
		}, 10000, 5000)
	}

	estimate := co.GetSavingsEstimate()
	assert.NotEmpty(t, estimate.Suggestions)
}

// --- Concurrent Safety ---

func TestConcurrentRecordCost(t *testing.T) {
	co := NewCostOptimizer(nil)
	var wg sync.WaitGroup

	n := 100
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			co.RecordCost(&SmartRoutingResult{
				SelectedModel: "claude-sonnet-4",
				Tier:          TierSonnet,
				Complexity:    ComplexityScore{Level: ComplexityStandard},
			}, 1000, 500)
		}()
	}
	wg.Wait()

	assert.Equal(t, n, co.RecordCount())
}

func TestConcurrentRecordAndRead(t *testing.T) {
	co := NewCostOptimizer(nil)
	var wg sync.WaitGroup

	// Writers
	n := 50
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			co.RecordCost(&SmartRoutingResult{
				SelectedModel: "claude-sonnet-4",
				Tier:          TierSonnet,
				Complexity:    ComplexityScore{Level: ComplexityStandard},
			}, 1000, 500)
		}()
	}

	// Readers
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			co.GetBreakdown(1 * time.Hour)
			co.GetOptimizationSuggestions()
			co.GetSavingsEstimate()
			co.RecordCount()
		}()
	}

	wg.Wait()
	// No panics means the test passes
}

func TestConcurrentShouldDowngrade(t *testing.T) {
	cfg := &CostOptimizerConfig{
		Policy:      PolicyStrict,
		DailyBudget: 100.0,
		WindowSize:  24 * time.Hour,
	}
	co := NewCostOptimizer(cfg)

	var wg sync.WaitGroup
	n := 50

	// Record costs concurrently with ShouldDowngrade checks
	wg.Add(n * 2)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			co.RecordCost(&SmartRoutingResult{
				SelectedModel: "claude-opus-4",
				Tier:          TierOpus,
				Complexity:    ComplexityScore{Level: ComplexityComplex},
			}, 10000, 5000)
		}()
		go func() {
			defer wg.Done()
			co.ShouldDowngrade(ComplexityStandard, "opus")
		}()
	}

	wg.Wait()
}

// --- PruneOlderThan ---

func TestPruneOlderThan(t *testing.T) {
	co := NewCostOptimizer(nil)

	// Inject old records
	co.mu.Lock()
	for i := 0; i < 5; i++ {
		co.records = append(co.records, CostRecord{
			Timestamp:    time.Now().Add(-3 * time.Hour),
			Model:        "claude-sonnet-4",
			Tier:         "sonnet",
			Complexity:   ComplexityStandard,
			InputTokens:  1000,
			OutputTokens: 500,
			Cost:         0.01,
		})
	}
	co.mu.Unlock()

	// Add recent records
	for i := 0; i < 3; i++ {
		co.RecordCost(&SmartRoutingResult{
			SelectedModel: "claude-sonnet-4",
			Tier:          TierSonnet,
			Complexity:    ComplexityScore{Level: ComplexityStandard},
		}, 1000, 500)
	}

	assert.Equal(t, 8, co.RecordCount())

	removed := co.PruneOlderThan(1 * time.Hour)
	assert.Equal(t, 5, removed)
	assert.Equal(t, 3, co.RecordCount())
}

func TestPruneOlderThan_NothingToRemove(t *testing.T) {
	co := NewCostOptimizer(nil)

	co.RecordCost(&SmartRoutingResult{
		SelectedModel: "claude-sonnet-4",
		Tier:          TierSonnet,
		Complexity:    ComplexityScore{Level: ComplexityStandard},
	}, 1000, 500)

	removed := co.PruneOlderThan(1 * time.Hour)
	assert.Equal(t, 0, removed)
	assert.Equal(t, 1, co.RecordCount())
}

// --- Rolling Window Limits ---

func TestRollingWindowEviction(t *testing.T) {
	cfg := &CostOptimizerConfig{
		MaxRecords: 10,
	}
	co := NewCostOptimizer(cfg)

	// Add 15 records, should only keep the last 10
	for i := 0; i < 15; i++ {
		co.RecordCost(&SmartRoutingResult{
			SelectedModel: "claude-sonnet-4",
			Tier:          TierSonnet,
			Complexity:    ComplexityScore{Level: ComplexityStandard},
		}, 1000, 500)
	}

	assert.Equal(t, 10, co.RecordCount())
}

// --- Model Concentration Analysis ---

func TestGetOptimizationSuggestions_ModelConcentration(t *testing.T) {
	co := NewCostOptimizer(nil)

	// 90% of requests and cost on opus
	for i := 0; i < 18; i++ {
		co.RecordCost(&SmartRoutingResult{
			SelectedModel: "claude-opus-4",
			Tier:          TierOpus,
			Complexity:    ComplexityScore{Level: ComplexityComplex},
		}, 50000, 25000)
	}

	// 10% on haiku (very small cost)
	for i := 0; i < 2; i++ {
		co.RecordCost(&SmartRoutingResult{
			SelectedModel: "claude-haiku-4",
			Tier:          TierHaiku,
			Complexity:    ComplexityScore{Level: ComplexitySimple},
		}, 1000, 500)
	}

	suggestions := co.GetOptimizationSuggestions()
	require.NotEmpty(t, suggestions)

	// Should detect model concentration
	found := false
	for _, s := range suggestions {
		if s.AffectedPct > 70 {
			found = true
			assert.Contains(t, s.Description, "opus")
		}
	}
	assert.True(t, found, "Expected a model concentration suggestion for opus")
}

// --- Edge Cases ---

func TestGetBreakdown_ZeroPeriod(t *testing.T) {
	co := NewCostOptimizer(nil)

	co.RecordCost(&SmartRoutingResult{
		SelectedModel: "claude-sonnet-4",
		Tier:          TierSonnet,
		Complexity:    ComplexityScore{Level: ComplexityStandard},
	}, 1000, 500)

	// Zero duration means "now minus 0" = no records match
	breakdown := co.GetBreakdown(0)
	assert.Equal(t, 0, breakdown.TotalRequests)
}

func TestShouldDowngrade_UnknownTier(t *testing.T) {
	co := NewCostOptimizer(nil)
	shouldDowngrade, _ := co.ShouldDowngrade(ComplexityStandard, "unknown-tier")
	// Unknown tier should not trigger any downgrade
	assert.False(t, shouldDowngrade)
}

func TestCostBreakdown_Period(t *testing.T) {
	co := NewCostOptimizer(nil)

	period := 2 * time.Hour
	breakdown := co.GetBreakdown(period)
	assert.Equal(t, period, breakdown.Period)
}

// --- Integration: full flow ---

func TestIntegration_RecordAndAnalyze(t *testing.T) {
	cfg := &CostOptimizerConfig{
		Policy:      PolicyBestEffort,
		DailyBudget: 50.0,
		WindowSize:  24 * time.Hour,
		MaxRecords:  1000,
	}
	co := NewCostOptimizer(cfg)

	// Simulate a realistic workload mix
	// 60% simple (ideally haiku), 30% standard (ideally sonnet), 10% complex (opus)
	for i := 0; i < 60; i++ {
		tier := TierHaiku
		model := "claude-haiku-4"
		if i < 12 {
			// 20% of simple requests mistakenly go to sonnet
			tier = TierSonnet
			model = "claude-sonnet-4"
		}
		co.RecordCost(&SmartRoutingResult{
			SelectedModel: model,
			Tier:          tier,
			Complexity:    ComplexityScore{Level: ComplexitySimple},
		}, 5000, 2000)
	}

	for i := 0; i < 30; i++ {
		co.RecordCost(&SmartRoutingResult{
			SelectedModel: "claude-sonnet-4",
			Tier:          TierSonnet,
			Complexity:    ComplexityScore{Level: ComplexityStandard},
		}, 10000, 5000)
	}

	for i := 0; i < 10; i++ {
		co.RecordCost(&SmartRoutingResult{
			SelectedModel: "claude-opus-4",
			Tier:          TierOpus,
			Complexity:    ComplexityScore{Level: ComplexityComplex},
		}, 20000, 10000)
	}

	// Verify breakdown
	breakdown := co.GetBreakdown(24 * time.Hour)
	assert.Equal(t, 100, breakdown.TotalRequests)
	assert.Greater(t, breakdown.TotalCost, 0.0)
	assert.Len(t, breakdown.ByModel, 3) // haiku, sonnet, opus

	// Verify suggestions
	suggestions := co.GetOptimizationSuggestions()
	assert.NotEmpty(t, suggestions, "Should have suggestions due to simple requests on sonnet")

	// Verify savings estimate
	estimate := co.GetSavingsEstimate()
	assert.Greater(t, estimate.CurrentSpend, 0.0)
	// There should be some potential savings (from the 12 simple requests on sonnet)
	assert.Greater(t, estimate.PotentialSavings, 0.0)

	// Verify downgrade suggestions work
	shouldDowngrade, suggestedTier := co.ShouldDowngrade(ComplexitySimple, "sonnet")
	assert.True(t, shouldDowngrade)
	assert.Equal(t, "haiku", suggestedTier)
}
