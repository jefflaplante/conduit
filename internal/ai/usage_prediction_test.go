package ai

import (
	"math"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Snapshot recording and retrieval ---

func TestUsagePredictor_RecordSnapshot(t *testing.T) {
	p := NewUsagePredictor()
	assert.Equal(t, 0, p.SnapshotCount())

	now := time.Now()
	p.RecordSnapshot(PredictionSnapshot{
		Timestamp:    now,
		TotalTokens:  1000,
		TotalCost:    0.50,
		RequestCount: 5,
	})

	assert.Equal(t, 1, p.SnapshotCount())
}

func TestUsagePredictor_MultipleSnapshots(t *testing.T) {
	p := NewUsagePredictor()
	base := time.Now()

	for i := 0; i < 10; i++ {
		p.RecordSnapshot(PredictionSnapshot{
			Timestamp:    base.Add(time.Duration(i) * time.Minute),
			TotalTokens:  int64(i * 1000),
			TotalCost:    float64(i) * 0.50,
			RequestCount: int64(i * 5),
		})
	}

	assert.Equal(t, 10, p.SnapshotCount())
}

// --- Rolling window capacity ---

func TestUsagePredictor_RollingWindowCapacity(t *testing.T) {
	maxCap := 10
	p := NewUsagePredictorWithCapacity(maxCap)
	base := time.Now()

	// Add more than the capacity
	for i := 0; i < 20; i++ {
		p.RecordSnapshot(PredictionSnapshot{
			Timestamp:    base.Add(time.Duration(i) * time.Minute),
			TotalTokens:  int64(i * 100),
			TotalCost:    float64(i) * 0.10,
			RequestCount: int64(i),
		})
	}

	// Should be capped at maxCap
	assert.Equal(t, maxCap, p.SnapshotCount())

	// The oldest snapshots should have been dropped; verify by checking
	// that the first snapshot is snapshot #10 (0-indexed)
	p.mu.RLock()
	firstTokens := p.snapshots[0].TotalTokens
	p.mu.RUnlock()
	assert.Equal(t, int64(1000), firstTokens) // i=10 -> 10*100 = 1000
}

func TestUsagePredictor_MinCapacity(t *testing.T) {
	// Capacity below minDataPoints should be clamped
	p := NewUsagePredictorWithCapacity(1)
	assert.Equal(t, minDataPoints, p.maxSnapshots)
}

// --- EMA calculation ---

func TestUsagePredictor_EMAInitialization(t *testing.T) {
	p := NewUsagePredictor()
	base := time.Now()

	// First snapshot: no EMA yet
	p.RecordSnapshot(PredictionSnapshot{
		Timestamp:    base,
		TotalTokens:  0,
		TotalCost:    0,
		RequestCount: 0,
	})

	p.mu.RLock()
	assert.False(t, p.emaInitialized)
	p.mu.RUnlock()

	// Second snapshot: EMA should initialize
	p.RecordSnapshot(PredictionSnapshot{
		Timestamp:    base.Add(time.Minute),
		TotalTokens:  6000,
		TotalCost:    0.60,
		RequestCount: 10,
	})

	p.mu.RLock()
	assert.True(t, p.emaInitialized)
	// Cost rate: 0.60 / 60s = 0.01 per second
	assert.InDelta(t, 0.01, p.emaCostRate, 0.001)
	p.mu.RUnlock()
}

func TestUsagePredictor_EMASmoothing(t *testing.T) {
	p := NewUsagePredictor()
	base := time.Now()

	// Initial state
	p.RecordSnapshot(PredictionSnapshot{
		Timestamp:    base,
		TotalTokens:  0,
		TotalCost:    0,
		RequestCount: 0,
	})

	// Steady rate: $0.60/min
	p.RecordSnapshot(PredictionSnapshot{
		Timestamp:    base.Add(1 * time.Minute),
		TotalTokens:  6000,
		TotalCost:    0.60,
		RequestCount: 10,
	})

	p.mu.RLock()
	initialRate := p.emaCostRate // 0.01/s
	p.mu.RUnlock()

	// Spike: $6.00 in one minute (10x the normal rate)
	p.RecordSnapshot(PredictionSnapshot{
		Timestamp:    base.Add(2 * time.Minute),
		TotalTokens:  66000,
		TotalCost:    6.60,
		RequestCount: 110,
	})

	p.mu.RLock()
	spikedRate := p.emaCostRate
	p.mu.RUnlock()

	// The EMA should have increased but not jumped to the full spike rate
	assert.Greater(t, spikedRate, initialRate)
	// 0.1/s is the spike rate; EMA = 0.3*0.1 + 0.7*0.01 = 0.037
	assert.InDelta(t, 0.037, spikedRate, 0.001)
}

// --- Linear trend detection ---

func TestUsagePredictor_TrendIncreasing(t *testing.T) {
	p := NewUsagePredictor()
	base := time.Now()

	// Linearly increasing cost
	for i := 0; i < 10; i++ {
		p.RecordSnapshot(PredictionSnapshot{
			Timestamp:    base.Add(time.Duration(i) * time.Minute),
			TotalTokens:  int64(i * 10000),
			TotalCost:    float64(i) * 1.0,
			RequestCount: int64(i * 10),
		})
	}

	assert.Equal(t, TrendIncreasing, p.GetTrend())
}

func TestUsagePredictor_TrendStable(t *testing.T) {
	p := NewUsagePredictor()
	base := time.Now()

	// Cost that starts high and stays roughly the same (flat cumulative line
	// with very small increases)
	for i := 0; i < 10; i++ {
		p.RecordSnapshot(PredictionSnapshot{
			Timestamp:    base.Add(time.Duration(i) * time.Minute),
			TotalTokens:  100000 + int64(i*10), // tiny increment
			TotalCost:    10.0 + float64(i)*0.001,
			RequestCount: 1000 + int64(i),
		})
	}

	assert.Equal(t, TrendStable, p.GetTrend())
}

func TestUsagePredictor_TrendDecreasing(t *testing.T) {
	p := NewUsagePredictor()
	base := time.Now()

	// Decreasing cost (unusual but possible with cumulative data that
	// gets "corrected" — use a purely decreasing series for test clarity)
	for i := 0; i < 10; i++ {
		p.RecordSnapshot(PredictionSnapshot{
			Timestamp:    base.Add(time.Duration(i) * time.Minute),
			TotalTokens:  int64(100000 - i*10000),
			TotalCost:    10.0 - float64(i)*1.0,
			RequestCount: int64(100 - i*10),
		})
	}

	assert.Equal(t, TrendDecreasing, p.GetTrend())
}

func TestUsagePredictor_TrendUnknown_InsufficientData(t *testing.T) {
	p := NewUsagePredictor()

	// No data
	assert.Equal(t, TrendUnknown, p.GetTrend())

	// One data point
	p.RecordSnapshot(PredictionSnapshot{
		Timestamp:   time.Now(),
		TotalTokens: 100,
		TotalCost:   0.01,
	})
	assert.Equal(t, TrendUnknown, p.GetTrend())

	// Two data points (still under minDataPoints=3)
	p.RecordSnapshot(PredictionSnapshot{
		Timestamp:   time.Now().Add(time.Minute),
		TotalTokens: 200,
		TotalCost:   0.02,
	})
	assert.Equal(t, TrendUnknown, p.GetTrend())
}

// --- PredictUsage ---

func TestUsagePredictor_PredictUsage_Basic(t *testing.T) {
	p := NewUsagePredictor()
	base := time.Now()

	// Steady linear growth: +$1/min, +10000 tokens/min, +10 requests/min
	for i := 0; i < 10; i++ {
		p.RecordSnapshot(PredictionSnapshot{
			Timestamp:    base.Add(time.Duration(i) * time.Minute),
			TotalTokens:  int64(i * 10000),
			TotalCost:    float64(i) * 1.0,
			RequestCount: int64(i * 10),
		})
	}

	forecast := p.PredictUsage(1 * time.Hour)
	require.NotNil(t, forecast)

	// With $1/min rate, 1 hour should predict ~$60
	assert.InDelta(t, 60.0, forecast.PredictedCost, 10.0)
	assert.Greater(t, forecast.PredictedTokens, int64(0))
	assert.Greater(t, forecast.PredictedRequests, int64(0))
	assert.Equal(t, 1*time.Hour, forecast.Horizon)
	assert.Greater(t, forecast.Confidence, 0.0)
}

func TestUsagePredictor_PredictUsage_InsufficientData(t *testing.T) {
	p := NewUsagePredictor()

	// No data
	assert.Nil(t, p.PredictUsage(time.Hour))

	// One point
	p.RecordSnapshot(PredictionSnapshot{
		Timestamp: time.Now(),
		TotalCost: 1.0,
	})
	assert.Nil(t, p.PredictUsage(time.Hour))
}

func TestUsagePredictor_PredictUsage_ZeroHorizon(t *testing.T) {
	p := NewUsagePredictor()
	base := time.Now()

	for i := 0; i < 5; i++ {
		p.RecordSnapshot(PredictionSnapshot{
			Timestamp:   base.Add(time.Duration(i) * time.Minute),
			TotalCost:   float64(i),
			TotalTokens: int64(i * 1000),
		})
	}

	assert.Nil(t, p.PredictUsage(0))
	assert.Nil(t, p.PredictUsage(-time.Hour))
}

func TestUsagePredictor_PredictUsage_NonNegative(t *testing.T) {
	p := NewUsagePredictor()
	base := time.Now()

	// Stable/flat usage should not predict negative values
	for i := 0; i < 10; i++ {
		p.RecordSnapshot(PredictionSnapshot{
			Timestamp:    base.Add(time.Duration(i) * time.Minute),
			TotalTokens:  5000,
			TotalCost:    1.0,
			RequestCount: 50,
		})
	}

	forecast := p.PredictUsage(time.Hour)
	require.NotNil(t, forecast)
	assert.GreaterOrEqual(t, forecast.PredictedCost, 0.0)
	assert.GreaterOrEqual(t, forecast.PredictedTokens, int64(0))
	assert.GreaterOrEqual(t, forecast.PredictedRequests, int64(0))
}

// --- Budget exhaustion prediction ---

func TestUsagePredictor_PredictBudgetExhaustion_WillExhaust(t *testing.T) {
	p := NewUsagePredictor()
	base := time.Date(2026, 2, 24, 10, 0, 0, 0, time.UTC) // 10:00 AM

	// Burning $1/min, budget is $100, currently at $80
	for i := 0; i < 10; i++ {
		p.RecordSnapshot(PredictionSnapshot{
			Timestamp:   base.Add(time.Duration(i) * time.Minute),
			TotalCost:   80.0 + float64(i)*1.0,
			TotalTokens: int64(80000 + i*1000),
		})
	}

	forecast := p.PredictBudgetExhaustion(100.0)
	require.NotNil(t, forecast)

	assert.True(t, forecast.WillExhaust)
	assert.False(t, forecast.ExhaustionTime.IsZero())
	assert.Greater(t, forecast.CurrentBurnRate, 0.0)
	assert.InDelta(t, 11.0, forecast.BudgetRemaining, 1.0) // 100 - 89 = 11
	assert.Greater(t, forecast.BudgetUsedPercent, 80.0)
	assert.NotEmpty(t, forecast.RecommendedAction)
}

func TestUsagePredictor_PredictBudgetExhaustion_WontExhaust(t *testing.T) {
	p := NewUsagePredictor()
	base := time.Date(2026, 2, 24, 23, 50, 0, 0, time.UTC) // 11:50 PM

	// Very low burn rate near end of day
	for i := 0; i < 5; i++ {
		p.RecordSnapshot(PredictionSnapshot{
			Timestamp:   base.Add(time.Duration(i) * time.Minute),
			TotalCost:   1.0 + float64(i)*0.001,
			TotalTokens: int64(1000 + i*10),
		})
	}

	forecast := p.PredictBudgetExhaustion(100.0)
	require.NotNil(t, forecast)

	assert.False(t, forecast.WillExhaust)
	assert.InDelta(t, 99.0, forecast.BudgetRemaining, 1.0)
	assert.Contains(t, forecast.RecommendedAction, "sufficient")
}

func TestUsagePredictor_PredictBudgetExhaustion_AlreadyExhausted(t *testing.T) {
	p := NewUsagePredictor()
	base := time.Now()

	for i := 0; i < 5; i++ {
		p.RecordSnapshot(PredictionSnapshot{
			Timestamp:   base.Add(time.Duration(i) * time.Minute),
			TotalCost:   100.0 + float64(i)*1.0, // Already over $100
			TotalTokens: int64(100000 + i*1000),
		})
	}

	forecast := p.PredictBudgetExhaustion(100.0)
	require.NotNil(t, forecast)

	assert.True(t, forecast.WillExhaust)
	assert.Equal(t, 0.0, forecast.BudgetRemaining)
	assert.Equal(t, 100.0, forecast.BudgetUsedPercent)
	assert.Contains(t, forecast.RecommendedAction, "exhausted")
}

func TestUsagePredictor_PredictBudgetExhaustion_NoBudget(t *testing.T) {
	p := NewUsagePredictor()
	base := time.Now()

	for i := 0; i < 5; i++ {
		p.RecordSnapshot(PredictionSnapshot{
			Timestamp: base.Add(time.Duration(i) * time.Minute),
			TotalCost: float64(i),
		})
	}

	forecast := p.PredictBudgetExhaustion(0)
	require.NotNil(t, forecast)
	assert.Contains(t, forecast.RecommendedAction, "no budget")
}

func TestUsagePredictor_PredictBudgetExhaustion_InsufficientData(t *testing.T) {
	p := NewUsagePredictor()
	assert.Nil(t, p.PredictBudgetExhaustion(100.0))
}

// --- Tier adjustment suggestions ---

func TestUsagePredictor_SuggestTierAdjustment_HighUsage(t *testing.T) {
	p := NewUsagePredictor()
	base := time.Now()

	// 96% budget consumed with active burn
	for i := 0; i < 5; i++ {
		p.RecordSnapshot(PredictionSnapshot{
			Timestamp:   base.Add(time.Duration(i) * time.Minute),
			TotalCost:   96.0 + float64(i)*0.5,
			TotalTokens: int64(96000 + i*500),
			ModelBreakdown: map[string]float64{
				"claude-opus-4-6": 96.0 + float64(i)*0.5,
			},
		})
	}

	adj := p.SuggestTierAdjustment(100.0)
	require.NotNil(t, adj)

	assert.True(t, adj.ShouldAdjust)
	assert.Equal(t, TierHaiku, adj.RecommendedTier)
	assert.Equal(t, 1.0, adj.Urgency)
	assert.Contains(t, adj.Reason, "95%")
}

func TestUsagePredictor_SuggestTierAdjustment_ModerateUsage(t *testing.T) {
	p := NewUsagePredictor()
	base := time.Now()

	// ~85% budget consumed
	for i := 0; i < 5; i++ {
		p.RecordSnapshot(PredictionSnapshot{
			Timestamp:   base.Add(time.Duration(i) * time.Minute),
			TotalCost:   85.0 + float64(i)*0.5,
			TotalTokens: int64(85000 + i*500),
			ModelBreakdown: map[string]float64{
				"claude-sonnet-4-6": 85.0 + float64(i)*0.5,
			},
		})
	}

	adj := p.SuggestTierAdjustment(100.0)
	require.NotNil(t, adj)

	assert.True(t, adj.ShouldAdjust)
	assert.Equal(t, TierHaiku, adj.RecommendedTier)
	assert.InDelta(t, 0.7, adj.Urgency, 0.01)
}

func TestUsagePredictor_SuggestTierAdjustment_LowUsage(t *testing.T) {
	p := NewUsagePredictor()
	base := time.Now()

	// Low usage: ~10% budget consumed
	for i := 0; i < 5; i++ {
		p.RecordSnapshot(PredictionSnapshot{
			Timestamp:   base.Add(time.Duration(i) * time.Minute),
			TotalCost:   10.0 + float64(i)*0.01,
			TotalTokens: int64(10000 + i*10),
			ModelBreakdown: map[string]float64{
				"claude-sonnet-4-6": 10.0 + float64(i)*0.01,
			},
		})
	}

	adj := p.SuggestTierAdjustment(100.0)
	require.NotNil(t, adj)

	assert.False(t, adj.ShouldAdjust)
	assert.Equal(t, 0.0, adj.Urgency)
	assert.Contains(t, adj.Reason, "healthy")
}

func TestUsagePredictor_SuggestTierAdjustment_NoBudget(t *testing.T) {
	p := NewUsagePredictor()
	base := time.Now()

	for i := 0; i < 5; i++ {
		p.RecordSnapshot(PredictionSnapshot{
			Timestamp: base.Add(time.Duration(i) * time.Minute),
			TotalCost: float64(i),
		})
	}

	adj := p.SuggestTierAdjustment(0)
	require.NotNil(t, adj)
	assert.False(t, adj.ShouldAdjust)
}

func TestUsagePredictor_SuggestTierAdjustment_InsufficientData(t *testing.T) {
	p := NewUsagePredictor()
	adj := p.SuggestTierAdjustment(100.0)
	require.NotNil(t, adj)
	assert.False(t, adj.ShouldAdjust)
	assert.Contains(t, adj.Reason, "insufficient")
}

// --- Empty state / insufficient data handling ---

func TestUsagePredictor_EmptyState(t *testing.T) {
	p := NewUsagePredictor()

	assert.Equal(t, 0, p.SnapshotCount())
	assert.Equal(t, TrendUnknown, p.GetTrend())
	assert.Nil(t, p.PredictUsage(time.Hour))
	assert.Nil(t, p.PredictBudgetExhaustion(100.0))

	adj := p.SuggestTierAdjustment(100.0)
	require.NotNil(t, adj)
	assert.False(t, adj.ShouldAdjust)
}

// --- Concurrent safety ---

func TestUsagePredictor_ConcurrentAccess(t *testing.T) {
	p := NewUsagePredictor()
	base := time.Now()

	// Seed with enough data
	for i := 0; i < 5; i++ {
		p.RecordSnapshot(PredictionSnapshot{
			Timestamp:    base.Add(time.Duration(i) * time.Minute),
			TotalTokens:  int64(i * 1000),
			TotalCost:    float64(i) * 0.50,
			RequestCount: int64(i * 5),
		})
	}

	var wg sync.WaitGroup

	// Concurrent writers
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			p.RecordSnapshot(PredictionSnapshot{
				Timestamp:    base.Add(time.Duration(5+idx) * time.Minute),
				TotalTokens:  int64((5 + idx) * 1000),
				TotalCost:    float64(5+idx) * 0.50,
				RequestCount: int64((5 + idx) * 5),
			})
		}(i)
	}

	// Concurrent readers
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = p.GetTrend()
			_ = p.PredictUsage(time.Hour)
			_ = p.PredictBudgetExhaustion(100.0)
			_ = p.SuggestTierAdjustment(100.0)
			_ = p.SnapshotCount()
		}()
	}

	wg.Wait()

	// Should not panic and should have recorded all snapshots
	assert.GreaterOrEqual(t, p.SnapshotCount(), 5) // At least the initial 5
}

// --- Trend type string representation ---

func TestUsageTrend_String(t *testing.T) {
	assert.Equal(t, "unknown", TrendUnknown.String())
	assert.Equal(t, "increasing", TrendIncreasing.String())
	assert.Equal(t, "stable", TrendStable.String())
	assert.Equal(t, "decreasing", TrendDecreasing.String())
}

// --- Model name to tier mapping ---

func TestModelNameToTier(t *testing.T) {
	assert.Equal(t, TierHaiku, modelNameToTier("claude-haiku-4-5-20251001"))
	assert.Equal(t, TierOpus, modelNameToTier("claude-opus-4-6"))
	assert.Equal(t, TierSonnet, modelNameToTier("claude-sonnet-4-6"))
	assert.Equal(t, TierSonnet, modelNameToTier("some-unknown-model"))
}

// --- Confidence calculation ---

func TestUsagePredictor_ConfidenceIncreasesWithData(t *testing.T) {
	p := NewUsagePredictor()
	base := time.Now()

	// Add just enough for prediction
	for i := 0; i < minDataPoints; i++ {
		p.RecordSnapshot(PredictionSnapshot{
			Timestamp:   base.Add(time.Duration(i) * 10 * time.Minute),
			TotalCost:   float64(i),
			TotalTokens: int64(i * 1000),
		})
	}

	lowDataForecast := p.PredictUsage(time.Hour)
	require.NotNil(t, lowDataForecast)
	lowConfidence := lowDataForecast.Confidence

	// Add many more data points
	for i := minDataPoints; i < 100; i++ {
		p.RecordSnapshot(PredictionSnapshot{
			Timestamp:   base.Add(time.Duration(i) * 10 * time.Minute),
			TotalCost:   float64(i),
			TotalTokens: int64(i * 1000),
		})
	}

	highDataForecast := p.PredictUsage(time.Hour)
	require.NotNil(t, highDataForecast)
	highConfidence := highDataForecast.Confidence

	assert.Greater(t, highConfidence, lowConfidence)
}

// --- Budget forecast recommended actions ---

func TestUsagePredictor_BudgetForecast_IdleUsage(t *testing.T) {
	p := NewUsagePredictor()
	base := time.Now()

	// Zero burn rate (same cost across all snapshots)
	for i := 0; i < 5; i++ {
		p.RecordSnapshot(PredictionSnapshot{
			Timestamp:   base.Add(time.Duration(i) * time.Minute),
			TotalCost:   1.0,
			TotalTokens: 1000,
		})
	}

	forecast := p.PredictBudgetExhaustion(100.0)
	require.NotNil(t, forecast)

	assert.False(t, forecast.WillExhaust)
	assert.Contains(t, forecast.RecommendedAction, "idle")
}

// --- Prediction accuracy with known linear data ---

func TestUsagePredictor_PredictUsage_LinearAccuracy(t *testing.T) {
	p := NewUsagePredictor()
	base := time.Now()

	// Perfect linear: $1/hour cost rate
	for i := 0; i < 60; i++ {
		p.RecordSnapshot(PredictionSnapshot{
			Timestamp:    base.Add(time.Duration(i) * time.Minute),
			TotalTokens:  int64(i * 1000),
			TotalCost:    float64(i) / 60.0, // $1 after 60 min
			RequestCount: int64(i),
		})
	}

	forecast := p.PredictUsage(1 * time.Hour)
	require.NotNil(t, forecast)

	// Should predict approximately $1 for the next hour
	assert.InDelta(t, 1.0, forecast.PredictedCost, 0.3)
	assert.Equal(t, TrendIncreasing, forecast.Trend)
}

// --- Edge case: all same timestamps ---

func TestUsagePredictor_SameTimestamps(t *testing.T) {
	p := NewUsagePredictor()
	now := time.Now()

	for i := 0; i < 5; i++ {
		p.RecordSnapshot(PredictionSnapshot{
			Timestamp:   now, // all same time
			TotalCost:   float64(i),
			TotalTokens: int64(i * 1000),
		})
	}

	// Should not panic, returns stable or unknown trend
	trend := p.GetTrend()
	assert.Contains(t, []UsageTrend{TrendStable, TrendUnknown}, trend)

	// Prediction may return nil or non-nil but should not panic
	_ = p.PredictUsage(time.Hour)
}

// --- Edge case: very short observation window ---

func TestUsagePredictor_ShortObservationWindow(t *testing.T) {
	p := NewUsagePredictor()
	base := time.Now()

	// 3 points within 1 second
	for i := 0; i < 3; i++ {
		p.RecordSnapshot(PredictionSnapshot{
			Timestamp:   base.Add(time.Duration(i) * 100 * time.Millisecond),
			TotalCost:   float64(i) * 0.01,
			TotalTokens: int64(i * 100),
		})
	}

	forecast := p.PredictUsage(time.Hour)
	require.NotNil(t, forecast)

	// Confidence should be very low for short windows
	assert.Less(t, forecast.Confidence, 0.3)
}

// --- PredictionSnapshot with model breakdown ---

func TestUsagePredictor_ModelBreakdown(t *testing.T) {
	p := NewUsagePredictor()
	base := time.Now()

	for i := 0; i < 5; i++ {
		p.RecordSnapshot(PredictionSnapshot{
			Timestamp:   base.Add(time.Duration(i) * time.Minute),
			TotalCost:   float64(i) * 2.0,
			TotalTokens: int64(i * 5000),
			ModelBreakdown: map[string]float64{
				"claude-opus-4-6":   float64(i) * 1.5,
				"claude-sonnet-4-6": float64(i) * 0.5,
			},
		})
	}

	// The dominant tier should be opus
	p.mu.RLock()
	tier := p.dominantTier()
	p.mu.RUnlock()
	assert.Equal(t, TierOpus, tier)
}

// --- Verify non-negative predictions for decreasing cumulative data ---

func TestUsagePredictor_PredictUsage_ClampedNonNegative(t *testing.T) {
	p := NewUsagePredictor()
	base := time.Now()

	// Decreasing data (anomalous but possible)
	for i := 0; i < 5; i++ {
		p.RecordSnapshot(PredictionSnapshot{
			Timestamp:    base.Add(time.Duration(i) * time.Minute),
			TotalTokens:  int64(10000 - i*2000),
			TotalCost:    10.0 - float64(i)*2.0,
			RequestCount: int64(100 - i*20),
		})
	}

	forecast := p.PredictUsage(time.Hour)
	require.NotNil(t, forecast)

	// All predicted values must be non-negative
	assert.GreaterOrEqual(t, forecast.PredictedCost, 0.0)
	assert.GreaterOrEqual(t, forecast.PredictedTokens, int64(0))
	assert.GreaterOrEqual(t, forecast.PredictedRequests, int64(0))
}

// --- Budget exhaustion with specific percentage thresholds ---

func TestUsagePredictor_BudgetForecast_PercentThresholds(t *testing.T) {
	tests := []struct {
		name            string
		costStart       float64
		costIncrement   float64
		budget          float64
		expectExhaust   bool
		expectActionSub string
	}{
		{
			name:            "90% consumed",
			costStart:       90.0,
			costIncrement:   0.5,
			budget:          100.0,
			expectExhaust:   true,
			expectActionSub: "nearly exhausted",
		},
		{
			name:            "70% consumed moderate rate",
			costStart:       70.0,
			costIncrement:   1.0,
			budget:          100.0,
			expectExhaust:   true,
			expectActionSub: "running low",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewUsagePredictor()
			base := time.Date(2026, 2, 24, 10, 0, 0, 0, time.UTC)

			for i := 0; i < 10; i++ {
				p.RecordSnapshot(PredictionSnapshot{
					Timestamp: base.Add(time.Duration(i) * time.Minute),
					TotalCost: tt.costStart + float64(i)*tt.costIncrement,
				})
			}

			forecast := p.PredictBudgetExhaustion(tt.budget)
			require.NotNil(t, forecast)
			assert.Equal(t, tt.expectExhaust, forecast.WillExhaust)
			assert.Contains(t, forecast.RecommendedAction, tt.expectActionSub)
		})
	}
}

// --- matchesSubstring ---

func TestMatchesSubstring(t *testing.T) {
	assert.True(t, matchesSubstring("claude-haiku-4", 7, "haiku"))
	assert.False(t, matchesSubstring("claude-haiku-4", 0, "haiku"))
	assert.False(t, matchesSubstring("abc", 0, "abcde")) // sub longer than remaining
	assert.True(t, matchesSubstring("opus", 0, "opus"))
}

// --- EMA with zero time delta ---

func TestUsagePredictor_EMA_ZeroTimeDelta(t *testing.T) {
	p := NewUsagePredictor()
	now := time.Now()

	p.RecordSnapshot(PredictionSnapshot{
		Timestamp: now,
		TotalCost: 1.0,
	})

	// Same timestamp: should not initialize EMA (dt=0)
	p.RecordSnapshot(PredictionSnapshot{
		Timestamp: now,
		TotalCost: 2.0,
	})

	p.mu.RLock()
	assert.False(t, p.emaInitialized)
	p.mu.RUnlock()
}

// --- Budget forecast burn rate calculation ---

func TestUsagePredictor_BudgetForecast_BurnRate(t *testing.T) {
	p := NewUsagePredictor()
	base := time.Date(2026, 2, 24, 12, 0, 0, 0, time.UTC)

	// $1/min = $60/hr
	for i := 0; i < 10; i++ {
		p.RecordSnapshot(PredictionSnapshot{
			Timestamp: base.Add(time.Duration(i) * time.Minute),
			TotalCost: float64(i) * 1.0,
		})
	}

	forecast := p.PredictBudgetExhaustion(1000.0)
	require.NotNil(t, forecast)

	// Burn rate should be approximately $60/hr
	assert.InDelta(t, 60.0, forecast.CurrentBurnRate, 15.0)
	assert.False(t, math.IsNaN(forecast.CurrentBurnRate))
	assert.False(t, math.IsInf(forecast.CurrentBurnRate, 0))
}
