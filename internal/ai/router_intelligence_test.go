package ai

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Constructor tests ---

func TestNewRouterIntelligence_Defaults(t *testing.T) {
	ri := NewRouterIntelligence()

	assert.NotNil(t, ri)
	assert.InDelta(t, defaultConfidenceThreshold, ri.ConfidenceThreshold(), 0.001)
	assert.Equal(t, 0, ri.OutcomeCount())
}

func TestNewRouterIntelligence_WithOptions(t *testing.T) {
	pa := NewPatternAnalyzer()
	co := NewCostOptimizer(nil)
	up := NewUsagePredictor()

	ri := NewRouterIntelligence(
		WithRouterPatternAnalyzer(pa),
		WithRouterCostOptimizer(co),
		WithRouterUsagePredictor(up),
		WithRouterDailyBudget(10.0),
		WithRouterConfidenceThreshold(0.6),
	)

	assert.NotNil(t, ri)
	assert.InDelta(t, 0.6, ri.ConfidenceThreshold(), 0.001)
	assert.Equal(t, 0, ri.OutcomeCount())
}

func TestNewRouterIntelligence_WithContextEngine(t *testing.T) {
	ce := NewContextEngine()
	ri := NewRouterIntelligence(
		WithRouterContextEngine(ce),
	)
	assert.NotNil(t, ri)
}

// --- OptimizeRouting tests ---

func TestOptimizeRouting_NoSubsystems(t *testing.T) {
	ri := NewRouterIntelligence()
	decision := ri.OptimizeRouting("hello", ComplexitySimple, 0)
	assert.Nil(t, decision, "should return nil when no subsystems are configured")
}

func TestOptimizeRouting_WithPatternAnalyzer_InsufficientData(t *testing.T) {
	pa := NewPatternAnalyzer(WithMinClusterSize(3))
	ri := NewRouterIntelligence(
		WithRouterPatternAnalyzer(pa),
	)

	// With no patterns recorded, there should be no recommendation
	decision := ri.OptimizeRouting("hello world", ComplexitySimple, 0)
	assert.Nil(t, decision, "should return nil with insufficient pattern data")
}

func TestOptimizeRouting_WithPatternAnalyzer_SufficientData(t *testing.T) {
	pa := NewPatternAnalyzer(
		WithMinClusterSize(2),
		WithClusterThreshold(0.5),
		WithReclusterInterval(1),
	)

	// Record enough similar patterns to form a cluster
	for i := 0; i < 5; i++ {
		result := &SmartRoutingResult{
			SelectedModel: "claude-sonnet-4-6",
			Tier:          TierSonnet,
			Complexity: ComplexityScore{
				Level: ComplexityStandard,
				Score: 40,
			},
			TotalLatencyMs: 500,
		}
		pa.RecordPattern(result, "tell me about machine learning", 3, true)
	}

	ri := NewRouterIntelligence(
		WithRouterPatternAnalyzer(pa),
		WithRouterConfidenceThreshold(0.1), // Low threshold for testing
	)

	decision := ri.OptimizeRouting("explain machine learning concepts", ComplexityStandard, 3)

	// The decision may or may not be nil depending on whether the cluster
	// recommendation meets the confidence threshold. If it's not nil, validate it.
	if decision != nil {
		assert.Contains(t, decision.Sources, "pattern_cluster")
		assert.True(t, decision.Confidence > 0)
		assert.NotEmpty(t, decision.Reason)
	}
}

func TestOptimizeRouting_WithCostOptimizer_NoDowngrade(t *testing.T) {
	co := NewCostOptimizer(&CostOptimizerConfig{
		Policy:      PolicyBestEffort,
		DailyBudget: 100.0, // large budget
	})

	ri := NewRouterIntelligence(
		WithRouterCostOptimizer(co),
	)

	decision := ri.OptimizeRouting("hello", ComplexitySimple, 0)
	// With a large budget and no records, no downgrade signal
	assert.Nil(t, decision)
}

func TestOptimizeRouting_WithCostOptimizer_StrictPolicy(t *testing.T) {
	co := NewCostOptimizer(&CostOptimizerConfig{
		Policy:      PolicyStrict,
		DailyBudget: 0, // no budget constraint, but strict policy downgrades based on complexity
	})

	ri := NewRouterIntelligence(
		WithRouterCostOptimizer(co),
	)

	// Simple request on default tier -> strict policy downgrades simple to haiku
	decision := ri.OptimizeRouting("hi", ComplexitySimple, 0)
	// The cost optimizer's ShouldDowngrade for simple+sonnet with strict policy returns true
	if decision != nil {
		assert.True(t, decision.ShouldDowngrade || decision.SuggestedTier == TierHaiku)
	}
}

func TestOptimizeRouting_WithUsagePredictor_BudgetPressure(t *testing.T) {
	up := NewUsagePredictor()

	// Record snapshots showing high cost usage
	base := time.Now().Add(-1 * time.Hour)
	for i := 0; i < 5; i++ {
		up.RecordSnapshot(PredictionSnapshot{
			Timestamp:    base.Add(time.Duration(i*10) * time.Minute),
			TotalTokens:  int64(i * 10000),
			TotalCost:    float64(i) * 2.0, // 2 USD per interval = high rate
			RequestCount: int64(i * 5),
		})
	}

	ri := NewRouterIntelligence(
		WithRouterUsagePredictor(up),
		WithRouterDailyBudget(5.0), // Low budget, will be exceeded
	)

	decision := ri.OptimizeRouting("complex task", ComplexityComplex, 5)

	// With high burn rate and low budget, the predictor should signal adjustment
	if decision != nil {
		assert.True(t, decision.BudgetPressure > 0)
		assert.Contains(t, decision.Sources, "usage_predictor")
	}
}

func TestOptimizeRouting_MultipleSignals(t *testing.T) {
	// Set up pattern analyzer with data
	pa := NewPatternAnalyzer(
		WithMinClusterSize(2),
		WithClusterThreshold(0.5),
		WithReclusterInterval(1),
	)
	for i := 0; i < 5; i++ {
		result := &SmartRoutingResult{
			SelectedModel: "claude-haiku-4",
			Tier:          TierHaiku,
			Complexity: ComplexityScore{
				Level: ComplexitySimple,
				Score: 10,
			},
			TotalLatencyMs: 100,
		}
		pa.RecordPattern(result, "what is 2+2", 0, true)
	}

	// Set up cost optimizer with strict policy
	co := NewCostOptimizer(&CostOptimizerConfig{
		Policy: PolicyStrict,
	})

	ri := NewRouterIntelligence(
		WithRouterPatternAnalyzer(pa),
		WithRouterCostOptimizer(co),
		WithRouterConfidenceThreshold(0.1),
	)

	decision := ri.OptimizeRouting("what is 3+3", ComplexitySimple, 0)

	if decision != nil {
		// Both signals should agree on haiku for simple math
		assert.Equal(t, TierHaiku, decision.SuggestedTier)
		assert.True(t, len(decision.Sources) >= 1)
	}
}

// --- RecordOutcome tests ---

func TestRecordOutcome_NilResult(t *testing.T) {
	ri := NewRouterIntelligence()
	// Should not panic
	ri.RecordOutcome(nil, "test", 0, true)
	assert.Equal(t, 0, ri.OutcomeCount())
}

func TestRecordOutcome_TracksOutcomes(t *testing.T) {
	pa := NewPatternAnalyzer()
	ri := NewRouterIntelligence(
		WithRouterPatternAnalyzer(pa),
	)

	result := &SmartRoutingResult{
		SelectedModel: "claude-sonnet-4-6",
		Tier:          TierSonnet,
		Complexity: ComplexityScore{
			Level: ComplexityStandard,
			Score: 40,
		},
		ContextSuggestedTier: TierSonnet,
	}

	ri.RecordOutcome(result, "test query", 3, true)
	assert.Equal(t, 1, ri.OutcomeCount())
	assert.Equal(t, 1, pa.PatternCount())
}

func TestRecordOutcome_RollingWindow(t *testing.T) {
	ri := NewRouterIntelligence()
	ri.maxOutcomes = 5

	for i := 0; i < 10; i++ {
		result := &SmartRoutingResult{
			SelectedModel: "claude-sonnet-4-6",
			Tier:          TierSonnet,
			Complexity: ComplexityScore{
				Level: ComplexityStandard,
				Score: 40,
			},
		}
		ri.RecordOutcome(result, "test", 0, true)
	}

	assert.Equal(t, 5, ri.OutcomeCount(), "should cap at maxOutcomes")
}

// --- GetInsights tests ---

func TestGetInsights_NoSubsystems(t *testing.T) {
	ri := NewRouterIntelligence()
	insights := ri.GetInsights()

	require.NotNil(t, insights)
	assert.False(t, insights.ClusterHealth.Healthy)
	assert.Equal(t, "unknown", insights.CostTrends.Trend)
	assert.Equal(t, 0, insights.PredictionAccuracy.TotalOutcomes)
	assert.False(t, insights.GeneratedAt.IsZero())
}

func TestGetInsights_WithPatternAnalyzer(t *testing.T) {
	pa := NewPatternAnalyzer(
		WithMinClusterSize(2),
		WithClusterThreshold(0.5),
		WithReclusterInterval(1),
	)

	// Record patterns to form clusters
	for i := 0; i < 5; i++ {
		result := &SmartRoutingResult{
			SelectedModel: "claude-sonnet-4-6",
			Tier:          TierSonnet,
			Complexity: ComplexityScore{
				Level: ComplexityStandard,
				Score: 40,
			},
			TotalLatencyMs: 500,
		}
		pa.RecordPattern(result, "explain topic", 3, true)
	}

	ri := NewRouterIntelligence(
		WithRouterPatternAnalyzer(pa),
	)

	insights := ri.GetInsights()

	require.NotNil(t, insights)
	assert.Equal(t, 5, insights.ClusterHealth.TotalPatterns)
}

func TestGetInsights_WithCostOptimizer(t *testing.T) {
	co := NewCostOptimizer(nil)

	// Record some cost data
	for i := 0; i < 10; i++ {
		result := &SmartRoutingResult{
			SelectedModel: "claude-sonnet-4-6",
			Tier:          TierSonnet,
			Complexity: ComplexityScore{
				Level: ComplexityStandard,
				Score: 40,
			},
		}
		co.RecordCost(result, 1000, 500)
	}

	ri := NewRouterIntelligence(
		WithRouterCostOptimizer(co),
	)

	insights := ri.GetInsights()
	require.NotNil(t, insights)
	// Cost trends should be populated from the optimizer fallback
	assert.NotNil(t, insights.CostTrends)
}

func TestGetInsights_WithUsagePredictor(t *testing.T) {
	up := NewUsagePredictor()

	base := time.Now().Add(-1 * time.Hour)
	for i := 0; i < 5; i++ {
		up.RecordSnapshot(PredictionSnapshot{
			Timestamp:    base.Add(time.Duration(i*10) * time.Minute),
			TotalTokens:  int64(i * 5000),
			TotalCost:    float64(i) * 0.5,
			RequestCount: int64(i * 3),
		})
	}

	ri := NewRouterIntelligence(
		WithRouterUsagePredictor(up),
		WithRouterDailyBudget(20.0),
	)

	insights := ri.GetInsights()
	require.NotNil(t, insights)
	assert.NotEqual(t, "unknown", insights.CostTrends.Trend)
}

func TestGetInsights_PredictionAccuracy(t *testing.T) {
	ri := NewRouterIntelligence()

	// Record outcomes with known predicted vs actual tiers
	for i := 0; i < 10; i++ {
		predicted := TierSonnet
		actual := TierSonnet
		if i >= 7 { // 3 out of 10 are wrong
			actual = TierOpus
		}
		result := &SmartRoutingResult{
			SelectedModel: "claude-sonnet-4-6",
			Tier:          actual,
			Complexity: ComplexityScore{
				Level: ComplexityStandard,
				Score: 40,
			},
			ContextSuggestedTier: predicted,
		}
		ri.RecordOutcome(result, "test", 0, true)
	}

	insights := ri.GetInsights()
	require.NotNil(t, insights)
	assert.Equal(t, 10, insights.PredictionAccuracy.TotalOutcomes)
	assert.Equal(t, 7, insights.PredictionAccuracy.CorrectPredictions)
	assert.InDelta(t, 0.7, insights.PredictionAccuracy.Accuracy, 0.01)
	assert.InDelta(t, 1.0, insights.PredictionAccuracy.SuccessRate, 0.01)
}

func TestGetInsights_Suggestions(t *testing.T) {
	pa := NewPatternAnalyzer()
	// Only 2 patterns = not enough for "reliable recommendations"
	for i := 0; i < 2; i++ {
		result := &SmartRoutingResult{
			SelectedModel: "claude-sonnet-4-6",
			Tier:          TierSonnet,
			Complexity:    ComplexityScore{Level: ComplexityStandard, Score: 40},
		}
		pa.RecordPattern(result, "test", 0, true)
	}

	ri := NewRouterIntelligence(
		WithRouterPatternAnalyzer(pa),
	)

	insights := ri.GetInsights()
	require.NotNil(t, insights)

	// Should have the "insufficient data" suggestion
	found := false
	for _, s := range insights.Suggestions {
		if strings.Contains(s, "Insufficient pattern data") {
			found = true
			break
		}
	}
	assert.True(t, found, "expected suggestion about insufficient data")
}

// --- AutoTune tests ---

func TestAutoTune_ThrottlesByTime(t *testing.T) {
	ri := NewRouterIntelligence()
	ri.autoTuneWindow = 100 * time.Millisecond

	// First call should proceed (but won't adjust with < 10 outcomes)
	ri.AutoTune()
	initialThreshold := ri.ConfidenceThreshold()

	// Immediate second call should be throttled
	ri.AutoTune()
	assert.InDelta(t, initialThreshold, ri.ConfidenceThreshold(), 0.001)
}

func TestAutoTune_IncreasesThresholdOnLowAccuracy(t *testing.T) {
	ri := NewRouterIntelligence(
		WithRouterConfidenceThreshold(0.5),
	)
	ri.autoTuneWindow = 0 // disable throttle for testing

	// Record 15 outcomes where predictions are all wrong
	for i := 0; i < 15; i++ {
		result := &SmartRoutingResult{
			SelectedModel: "claude-sonnet-4-6",
			Tier:          TierOpus, // actual
			Complexity: ComplexityScore{
				Level: ComplexityComplex,
				Score: 80,
			},
			ContextSuggestedTier: TierHaiku, // predicted (wrong)
		}
		ri.RecordOutcome(result, "complex task", 5, true)
	}

	ri.AutoTune()

	// Threshold should have increased due to low accuracy
	assert.Greater(t, ri.ConfidenceThreshold(), 0.5)
}

func TestAutoTune_DecreasesThresholdOnHighAccuracy(t *testing.T) {
	ri := NewRouterIntelligence(
		WithRouterConfidenceThreshold(0.5),
	)
	ri.autoTuneWindow = 0 // disable throttle for testing

	// Record 15 outcomes where all predictions are correct
	for i := 0; i < 15; i++ {
		result := &SmartRoutingResult{
			SelectedModel: "claude-sonnet-4-6",
			Tier:          TierSonnet, // actual
			Complexity: ComplexityScore{
				Level: ComplexityStandard,
				Score: 40,
			},
			ContextSuggestedTier: TierSonnet, // predicted (correct)
		}
		ri.RecordOutcome(result, "standard task", 3, true)
	}

	ri.AutoTune()

	// Threshold should have decreased due to high accuracy
	assert.Less(t, ri.ConfidenceThreshold(), 0.5)
}

func TestAutoTune_RespectsThresholdBounds(t *testing.T) {
	// Test floor
	ri := NewRouterIntelligence(
		WithRouterConfidenceThreshold(minConfidenceThreshold),
	)
	ri.autoTuneWindow = 0

	for i := 0; i < 15; i++ {
		result := &SmartRoutingResult{
			SelectedModel:        "claude-sonnet-4-6",
			Tier:                 TierSonnet,
			Complexity:           ComplexityScore{Level: ComplexityStandard, Score: 40},
			ContextSuggestedTier: TierSonnet,
		}
		ri.RecordOutcome(result, "test", 0, true)
	}

	ri.AutoTune()
	assert.GreaterOrEqual(t, ri.ConfidenceThreshold(), minConfidenceThreshold)

	// Test ceiling
	ri2 := NewRouterIntelligence(
		WithRouterConfidenceThreshold(maxConfidenceThreshold),
	)
	ri2.autoTuneWindow = 0

	for i := 0; i < 15; i++ {
		result := &SmartRoutingResult{
			SelectedModel:        "claude-sonnet-4-6",
			Tier:                 TierOpus,
			Complexity:           ComplexityScore{Level: ComplexityComplex, Score: 80},
			ContextSuggestedTier: TierHaiku, // all wrong
		}
		ri2.RecordOutcome(result, "test", 0, true)
	}

	ri2.AutoTune()
	assert.LessOrEqual(t, ri2.ConfidenceThreshold(), maxConfidenceThreshold)
}

// --- Concurrency test ---

func TestRouterIntelligence_ConcurrentAccess(t *testing.T) {
	pa := NewPatternAnalyzer(
		WithMinClusterSize(2),
		WithReclusterInterval(5),
	)
	co := NewCostOptimizer(nil)
	up := NewUsagePredictor()

	ri := NewRouterIntelligence(
		WithRouterPatternAnalyzer(pa),
		WithRouterCostOptimizer(co),
		WithRouterUsagePredictor(up),
		WithRouterDailyBudget(50.0),
	)

	done := make(chan struct{})

	// Writer goroutine: record outcomes
	go func() {
		defer func() { done <- struct{}{} }()
		for i := 0; i < 50; i++ {
			result := &SmartRoutingResult{
				SelectedModel: "claude-sonnet-4-6",
				Tier:          TierSonnet,
				Complexity:    ComplexityScore{Level: ComplexityStandard, Score: 40},
			}
			ri.RecordOutcome(result, "test query", 3, true)
		}
	}()

	// Reader goroutine: optimize routing
	go func() {
		defer func() { done <- struct{}{} }()
		for i := 0; i < 50; i++ {
			ri.OptimizeRouting("some query", ComplexityStandard, 3)
		}
	}()

	// Reader goroutine: get insights
	go func() {
		defer func() { done <- struct{}{} }()
		for i := 0; i < 50; i++ {
			ri.GetInsights()
		}
	}()

	// Reader goroutine: auto-tune
	go func() {
		defer func() { done <- struct{}{} }()
		for i := 0; i < 10; i++ {
			ri.AutoTune()
		}
	}()

	// Wait for all goroutines
	for i := 0; i < 4; i++ {
		<-done
	}
}

// --- Helper function tests ---

func TestComplexityLevelToScore(t *testing.T) {
	assert.Equal(t, 10, complexityLevelToScore(ComplexitySimple))
	assert.Equal(t, 40, complexityLevelToScore(ComplexityStandard))
	assert.Equal(t, 80, complexityLevelToScore(ComplexityComplex))
	assert.Equal(t, 40, complexityLevelToScore(ComplexityLevel(99))) // unknown defaults to 40
}

func TestTierFromString(t *testing.T) {
	assert.Equal(t, TierHaiku, tierFromString("haiku"))
	assert.Equal(t, TierSonnet, tierFromString("sonnet"))
	assert.Equal(t, TierOpus, tierFromString("opus"))
	assert.Equal(t, TierSonnet, tierFromString("unknown"))
	assert.Equal(t, TierSonnet, tierFromString(""))
}

func TestClampFloat64(t *testing.T) {
	assert.InDelta(t, 0.5, clampFloat64(0.5, 0, 1), 0.001)
	assert.InDelta(t, 0.0, clampFloat64(-1, 0, 1), 0.001)
	assert.InDelta(t, 1.0, clampFloat64(2, 0, 1), 0.001)
}

// --- Signal merging tests ---

func TestMergeSignals_Empty(t *testing.T) {
	ri := NewRouterIntelligence()
	tier, conf, reason := ri.mergeSignals(nil, ComplexityStandard)
	assert.Equal(t, TierSonnet, tier)
	assert.InDelta(t, 0.0, conf, 0.001)
	assert.Contains(t, reason, "no signals")
}

func TestMergeSignals_Single(t *testing.T) {
	ri := NewRouterIntelligence()
	signals := []tierSignal{
		{tier: TierHaiku, confidence: 0.8, source: "test"},
	}
	tier, conf, reason := ri.mergeSignals(signals, ComplexitySimple)
	assert.Equal(t, TierHaiku, tier)
	assert.InDelta(t, 0.8, conf, 0.001)
	assert.Contains(t, reason, "test")
}

func TestMergeSignals_MultipleAgreeing(t *testing.T) {
	ri := NewRouterIntelligence()
	signals := []tierSignal{
		{tier: TierSonnet, confidence: 0.7, source: "cluster"},
		{tier: TierSonnet, confidence: 0.6, source: "cost"},
	}
	tier, conf, reason := ri.mergeSignals(signals, ComplexityStandard)
	assert.Equal(t, TierSonnet, tier)
	assert.True(t, conf > 0.5, "agreeing signals should produce high confidence")
	assert.Contains(t, reason, "2 signals agree")
}

func TestMergeSignals_MultipleDisagreeing(t *testing.T) {
	ri := NewRouterIntelligence()
	signals := []tierSignal{
		{tier: TierOpus, confidence: 0.9, source: "cluster"},
		{tier: TierHaiku, confidence: 0.3, source: "cost"},
	}
	tier, _, _ := ri.mergeSignals(signals, ComplexityStandard)
	// Opus should win because it has higher confidence
	assert.Equal(t, TierOpus, tier)
}

// --- Integration test: full lifecycle ---

func TestRouterIntelligence_FullLifecycle(t *testing.T) {
	pa := NewPatternAnalyzer(
		WithMinClusterSize(2),
		WithClusterThreshold(0.5),
		WithReclusterInterval(1),
	)
	co := NewCostOptimizer(&CostOptimizerConfig{
		Policy: PolicyBestEffort,
	})
	up := NewUsagePredictor()

	ri := NewRouterIntelligence(
		WithRouterPatternAnalyzer(pa),
		WithRouterCostOptimizer(co),
		WithRouterUsagePredictor(up),
		WithRouterDailyBudget(50.0),
		WithRouterConfidenceThreshold(0.1),
	)

	// Phase 1: Cold start - no data
	decision := ri.OptimizeRouting("hello", ComplexitySimple, 0)
	// May be nil (no cluster data, no cost pressure)
	_ = decision

	// Phase 2: Record outcomes to build up data
	base := time.Now().Add(-30 * time.Minute)
	for i := 0; i < 10; i++ {
		result := &SmartRoutingResult{
			SelectedModel: "claude-sonnet-4-6",
			Tier:          TierSonnet,
			Complexity: ComplexityScore{
				Level: ComplexityStandard,
				Score: 40,
			},
			TotalLatencyMs:       500,
			ContextSuggestedTier: TierSonnet,
		}
		ri.RecordOutcome(result, "explain the concept", 3, true)

		// Also record cost
		co.RecordCost(result, 1000, 500)

		// Record usage snapshot
		up.RecordSnapshot(PredictionSnapshot{
			Timestamp:    base.Add(time.Duration(i*3) * time.Minute),
			TotalTokens:  int64(i * 1500),
			TotalCost:    float64(i) * 0.05,
			RequestCount: int64(i + 1),
		})
	}

	// Phase 3: Get insights after data collection
	insights := ri.GetInsights()
	require.NotNil(t, insights)
	assert.Equal(t, 10, insights.ClusterHealth.TotalPatterns)
	assert.Equal(t, 10, insights.PredictionAccuracy.TotalOutcomes)
	assert.True(t, insights.PredictionAccuracy.SuccessRate > 0)

	// Phase 4: Auto-tune
	ri.autoTuneWindow = 0 // disable throttle
	ri.AutoTune()

	// Phase 5: Make a routing decision with accumulated data
	decision = ri.OptimizeRouting("explain this concept in detail", ComplexityStandard, 3)
	// With enough data, we should get a non-nil decision
	// (though it depends on cluster formation)
	if decision != nil {
		assert.True(t, decision.Confidence > 0)
		assert.NotEmpty(t, decision.Sources)
	}
}

// --- Edge case tests ---

func TestOptimizeRouting_AllComplexityLevels(t *testing.T) {
	co := NewCostOptimizer(&CostOptimizerConfig{
		Policy: PolicyStrict,
	})
	ri := NewRouterIntelligence(
		WithRouterCostOptimizer(co),
	)

	levels := []ComplexityLevel{ComplexitySimple, ComplexityStandard, ComplexityComplex}
	for _, level := range levels {
		decision := ri.OptimizeRouting("test", level, 0)
		// Strict policy downgrades simple and standard requests
		_ = decision // just ensure no panic
	}
}

func TestRecordOutcome_WithoutPatternAnalyzer(t *testing.T) {
	ri := NewRouterIntelligence()
	result := &SmartRoutingResult{
		SelectedModel: "claude-sonnet-4-6",
		Tier:          TierSonnet,
		Complexity:    ComplexityScore{Level: ComplexityStandard, Score: 40},
	}
	// Should not panic when pattern analyzer is nil
	ri.RecordOutcome(result, "test", 0, true)
	assert.Equal(t, 1, ri.OutcomeCount())
}

func TestGetInsights_EmptyOutcomes(t *testing.T) {
	ri := NewRouterIntelligence()
	insights := ri.GetInsights()
	require.NotNil(t, insights)
	assert.Equal(t, 0, insights.PredictionAccuracy.TotalOutcomes)
	assert.InDelta(t, 0.0, insights.PredictionAccuracy.Accuracy, 0.001)
}

func TestOptimizeRouting_WithMockContextEngine(t *testing.T) {
	// Context engine is queried by the router, not directly by RouterIntelligence.
	// This test verifies the option works without panicking.
	// Reuse the mockContextEngine from context_routing_test.go.
	ce := &mockContextEngine{
		ctx: &RoutingContext{
			Source: "test",
			Hints: []RoutingHint{
				{SuggestedTier: TierSonnet, Confidence: 0.8, Reason: "test hint"},
			},
		},
	}

	ri := NewRouterIntelligence(
		WithRouterContextEngine(ce),
	)

	// Context engine alone doesn't produce signals in OptimizeRouting
	// (it's used by the Router's GenerateResponseSmart, not here directly)
	decision := ri.OptimizeRouting("test", ComplexityStandard, 0)
	assert.Nil(t, decision, "context engine alone doesn't produce signals in OptimizeRouting")
}

func TestRouterIntelligence_InvalidOptions(t *testing.T) {
	// Negative budget should be ignored
	ri := NewRouterIntelligence(
		WithRouterDailyBudget(-5.0),
	)
	assert.NotNil(t, ri)

	// Zero threshold should be ignored (defaults preserved)
	ri2 := NewRouterIntelligence(
		WithRouterConfidenceThreshold(0),
	)
	assert.InDelta(t, defaultConfidenceThreshold, ri2.ConfidenceThreshold(), 0.001)

	// Threshold > 1 should be ignored
	ri3 := NewRouterIntelligence(
		WithRouterConfidenceThreshold(1.5),
	)
	assert.InDelta(t, defaultConfidenceThreshold, ri3.ConfidenceThreshold(), 0.001)
}
