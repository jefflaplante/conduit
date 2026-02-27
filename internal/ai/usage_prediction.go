package ai

import (
	"math"
	"sync"
	"time"
)

// --- Data point types ---

// PredictionSnapshot captures usage metrics at a single point in time.
// This is distinct from UsageSnapshot (in usage_tracker.go) which is a
// cumulative snapshot; PredictionSnapshot records the incremental state
// for time-series analysis.
type PredictionSnapshot struct {
	// Timestamp is when this snapshot was recorded.
	Timestamp time.Time `json:"timestamp"`

	// TotalTokens is the cumulative token count (input + output) at this time.
	TotalTokens int64 `json:"total_tokens"`

	// TotalCost is the cumulative cost in USD at this time.
	TotalCost float64 `json:"total_cost"`

	// RequestCount is the cumulative request count at this time.
	RequestCount int64 `json:"request_count"`

	// ModelBreakdown holds per-model cost at this point (optional).
	ModelBreakdown map[string]float64 `json:"model_breakdown,omitempty"`
}

// --- Trend types ---

// UsageTrend represents the current direction of usage.
type UsageTrend int

const (
	// TrendUnknown means insufficient data to determine a trend.
	TrendUnknown UsageTrend = iota

	// TrendIncreasing means usage is growing.
	TrendIncreasing

	// TrendStable means usage is roughly constant.
	TrendStable

	// TrendDecreasing means usage is shrinking.
	TrendDecreasing
)

// String returns a human-readable name for the trend.
func (t UsageTrend) String() string {
	switch t {
	case TrendIncreasing:
		return "increasing"
	case TrendStable:
		return "stable"
	case TrendDecreasing:
		return "decreasing"
	default:
		return "unknown"
	}
}

// --- Forecast types ---

// UsageForecast holds predicted usage over a given time horizon.
type UsageForecast struct {
	// Horizon is the prediction window.
	Horizon time.Duration `json:"horizon"`

	// PredictedTokens is the estimated additional tokens over the horizon.
	PredictedTokens int64 `json:"predicted_tokens"`

	// PredictedCost is the estimated additional cost (USD) over the horizon.
	PredictedCost float64 `json:"predicted_cost"`

	// PredictedRequests is the estimated additional requests over the horizon.
	PredictedRequests int64 `json:"predicted_requests"`

	// Confidence is a value in [0, 1] indicating prediction reliability.
	// Higher = more data points, more consistent trend.
	Confidence float64 `json:"confidence"`

	// Trend is the detected usage direction.
	Trend UsageTrend `json:"trend"`
}

// BudgetForecast predicts when the daily budget will be exhausted.
type BudgetForecast struct {
	// WillExhaust is true if the budget is predicted to be exhausted
	// within the current day.
	WillExhaust bool `json:"will_exhaust"`

	// ExhaustionTime is the estimated time of budget exhaustion (zero if
	// WillExhaust is false).
	ExhaustionTime time.Time `json:"exhaustion_time,omitempty"`

	// CurrentBurnRate is the smoothed cost per hour (USD/hr).
	CurrentBurnRate float64 `json:"current_burn_rate"`

	// BudgetRemaining is the remaining daily budget (USD).
	BudgetRemaining float64 `json:"budget_remaining"`

	// BudgetUsedPercent is the percentage of budget already consumed [0, 100].
	BudgetUsedPercent float64 `json:"budget_used_percent"`

	// RecommendedAction is a short human-readable suggestion.
	RecommendedAction string `json:"recommended_action"`
}

// TierAdjustment recommends a model tier change.
type TierAdjustment struct {
	// ShouldAdjust is true if a tier change is recommended.
	ShouldAdjust bool `json:"should_adjust"`

	// CurrentTier is the highest tier currently in use (or the default).
	CurrentTier ModelTier `json:"current_tier"`

	// RecommendedTier is the suggested tier to switch to.
	RecommendedTier ModelTier `json:"recommended_tier"`

	// Reason explains why the adjustment is recommended.
	Reason string `json:"reason"`

	// Urgency is a value in [0, 1] where 1 means immediate action needed.
	Urgency float64 `json:"urgency"`
}

// --- Predictor configuration ---

const (
	// defaultMaxSnapshots is the maximum number of snapshots retained
	// (24 hours at 1-minute granularity).
	defaultMaxSnapshots = 1440

	// minDataPoints is the minimum number of snapshots needed for prediction.
	minDataPoints = 3

	// emaAlpha is the smoothing factor for exponential moving average.
	// Higher values weight recent data more heavily.
	emaAlpha = 0.3

	// trendThreshold is the minimum absolute slope (normalized) to
	// distinguish increasing/decreasing from stable.
	trendThreshold = 0.05
)

// --- UsagePredictor ---

// UsagePredictor analyzes historical usage data and predicts future usage
// patterns. It maintains a rolling window of PredictionSnapshot data points
// and uses exponential moving averages and linear regression for forecasting.
//
// Thread-safe: all methods may be called concurrently.
type UsagePredictor struct {
	mu           sync.RWMutex
	snapshots    []PredictionSnapshot
	maxSnapshots int

	// Cached EMA values (updated on each RecordSnapshot call).
	emaCostRate    float64 // smoothed cost per second
	emaTokenRate   float64 // smoothed tokens per second
	emaRequestRate float64 // smoothed requests per second
	emaInitialized bool
}

// NewUsagePredictor creates a new prediction engine with the default rolling
// window size (1440 snapshots).
func NewUsagePredictor() *UsagePredictor {
	return &UsagePredictor{
		snapshots:    make([]PredictionSnapshot, 0, 128),
		maxSnapshots: defaultMaxSnapshots,
	}
}

// NewUsagePredictorWithCapacity creates a predictor with a custom window size.
func NewUsagePredictorWithCapacity(maxSnapshots int) *UsagePredictor {
	if maxSnapshots < minDataPoints {
		maxSnapshots = minDataPoints
	}
	return &UsagePredictor{
		snapshots:    make([]PredictionSnapshot, 0, min(maxSnapshots, 256)),
		maxSnapshots: maxSnapshots,
	}
}

// RecordSnapshot records a point-in-time usage data point. Snapshots should
// be recorded at regular intervals (e.g., every minute) for best results.
// The rolling window is enforced: oldest snapshots are dropped when the
// capacity is exceeded.
func (p *UsagePredictor) RecordSnapshot(snap PredictionSnapshot) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.snapshots = append(p.snapshots, snap)

	// Trim to capacity
	if len(p.snapshots) > p.maxSnapshots {
		excess := len(p.snapshots) - p.maxSnapshots
		// Shift in place to avoid repeated allocations
		copy(p.snapshots, p.snapshots[excess:])
		p.snapshots = p.snapshots[:p.maxSnapshots]
	}

	// Update EMA rates
	p.updateEMA()
}

// SnapshotCount returns the number of snapshots currently stored.
func (p *UsagePredictor) SnapshotCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.snapshots)
}

// PredictUsage forecasts usage over the given time horizon.
// Returns nil if there is insufficient data.
func (p *UsagePredictor) PredictUsage(horizon time.Duration) *UsageForecast {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if len(p.snapshots) < minDataPoints {
		return nil
	}

	hours := horizon.Seconds() / 3600.0
	if hours <= 0 {
		return nil
	}

	// Use linear regression on recent cost/token/request rates
	costSlope, costIntercept := p.linearRegression(func(s PredictionSnapshot) float64 {
		return s.TotalCost
	})
	tokenSlope, tokenIntercept := p.linearRegression(func(s PredictionSnapshot) float64 {
		return float64(s.TotalTokens)
	})
	requestSlope, requestIntercept := p.linearRegression(func(s PredictionSnapshot) float64 {
		return float64(s.RequestCount)
	})

	// Current values (from regression line at the latest time)
	latestHours := p.hoursFromFirst(p.snapshots[len(p.snapshots)-1].Timestamp)
	futureHours := latestHours + hours

	currentCost := costSlope*latestHours + costIntercept
	futureCost := costSlope*futureHours + costIntercept
	predictedCost := math.Max(0, futureCost-currentCost)

	currentTokens := tokenSlope*latestHours + tokenIntercept
	futureTokens := tokenSlope*futureHours + tokenIntercept
	predictedTokens := int64(math.Max(0, futureTokens-currentTokens))

	currentRequests := requestSlope*latestHours + requestIntercept
	futureRequests := requestSlope*futureHours + requestIntercept
	predictedRequests := int64(math.Max(0, futureRequests-currentRequests))

	// Blend regression prediction with EMA for robustness
	if p.emaInitialized {
		emaHorizonCost := p.emaCostRate * horizon.Seconds()
		emaHorizonTokens := p.emaTokenRate * horizon.Seconds()
		emaHorizonRequests := p.emaRequestRate * horizon.Seconds()

		// Weight: 60% regression, 40% EMA
		predictedCost = 0.6*predictedCost + 0.4*math.Max(0, emaHorizonCost)
		predictedTokens = int64(0.6*float64(predictedTokens) + 0.4*math.Max(0, emaHorizonTokens))
		predictedRequests = int64(0.6*float64(predictedRequests) + 0.4*math.Max(0, emaHorizonRequests))
	}

	trend := p.detectTrend(costSlope, currentCost)
	confidence := p.calculateConfidence()

	return &UsageForecast{
		Horizon:           horizon,
		PredictedTokens:   predictedTokens,
		PredictedCost:     predictedCost,
		PredictedRequests: predictedRequests,
		Confidence:        confidence,
		Trend:             trend,
	}
}

// PredictBudgetExhaustion predicts when the daily budget will run out.
// Returns nil if there is insufficient data.
func (p *UsagePredictor) PredictBudgetExhaustion(dailyBudget float64) *BudgetForecast {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if dailyBudget <= 0 {
		return &BudgetForecast{
			RecommendedAction: "no budget configured",
		}
	}

	if len(p.snapshots) < minDataPoints {
		return nil
	}

	latest := p.snapshots[len(p.snapshots)-1]
	currentCost := latest.TotalCost
	remaining := dailyBudget - currentCost
	usedPercent := (currentCost / dailyBudget) * 100.0

	// Calculate burn rate (cost per hour) using EMA
	var burnRatePerHour float64
	if p.emaInitialized && p.emaCostRate > 0 {
		burnRatePerHour = p.emaCostRate * 3600.0
	} else {
		// Fallback: simple average over the observation window
		first := p.snapshots[0]
		elapsed := latest.Timestamp.Sub(first.Timestamp)
		if elapsed > 0 {
			burnRatePerHour = (latest.TotalCost - first.TotalCost) / elapsed.Hours()
		}
	}

	forecast := &BudgetForecast{
		CurrentBurnRate:   burnRatePerHour,
		BudgetRemaining:   math.Max(0, remaining),
		BudgetUsedPercent: math.Min(100, usedPercent),
	}

	if remaining <= 0 {
		forecast.WillExhaust = true
		forecast.ExhaustionTime = latest.Timestamp // already exhausted
		forecast.RecommendedAction = "budget exhausted; downgrade to cheapest tier immediately"
		return forecast
	}

	if burnRatePerHour <= 0 {
		forecast.WillExhaust = false
		forecast.RecommendedAction = "usage is idle; budget is safe"
		return forecast
	}

	hoursRemaining := remaining / burnRatePerHour
	exhaustionTime := latest.Timestamp.Add(time.Duration(hoursRemaining * float64(time.Hour)))

	// Check if exhaustion falls within the current calendar day
	endOfDay := time.Date(
		latest.Timestamp.Year(), latest.Timestamp.Month(), latest.Timestamp.Day(),
		23, 59, 59, 0, latest.Timestamp.Location(),
	)

	if exhaustionTime.Before(endOfDay) {
		forecast.WillExhaust = true
		forecast.ExhaustionTime = exhaustionTime

		switch {
		case usedPercent >= 90:
			forecast.RecommendedAction = "budget nearly exhausted; switch to cheapest tier"
		case usedPercent >= 70:
			forecast.RecommendedAction = "budget running low; consider downgrading tier"
		default:
			forecast.RecommendedAction = "on track to exhaust budget today; monitor closely"
		}
	} else {
		forecast.WillExhaust = false
		forecast.RecommendedAction = "budget is sufficient for today at current rate"
	}

	return forecast
}

// GetTrend returns the current usage trend based on cost data.
func (p *UsagePredictor) GetTrend() UsageTrend {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if len(p.snapshots) < minDataPoints {
		return TrendUnknown
	}

	costSlope, _ := p.linearRegression(func(s PredictionSnapshot) float64 {
		return s.TotalCost
	})

	latest := p.snapshots[len(p.snapshots)-1]
	return p.detectTrend(costSlope, latest.TotalCost)
}

// SuggestTierAdjustment provides a proactive tier recommendation based on
// predicted budget consumption.
func (p *UsagePredictor) SuggestTierAdjustment(dailyBudget float64) *TierAdjustment {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if dailyBudget <= 0 || len(p.snapshots) < minDataPoints {
		return &TierAdjustment{
			ShouldAdjust: false,
			Reason:       "insufficient data or no budget configured",
		}
	}

	latest := p.snapshots[len(p.snapshots)-1]
	usedPercent := (latest.TotalCost / dailyBudget) * 100.0

	// Determine dominant tier from model breakdown
	currentTier := p.dominantTier()

	// Calculate projected burn rate
	var burnRatePerHour float64
	if p.emaInitialized && p.emaCostRate > 0 {
		burnRatePerHour = p.emaCostRate * 3600.0
	}

	remaining := dailyBudget - latest.TotalCost
	var hoursRemaining float64
	if burnRatePerHour > 0 {
		hoursRemaining = remaining / burnRatePerHour
	} else {
		hoursRemaining = math.Inf(1)
	}

	adj := &TierAdjustment{
		CurrentTier: currentTier,
	}

	switch {
	case usedPercent >= 95:
		// Budget nearly gone: force cheapest
		adj.ShouldAdjust = true
		adj.RecommendedTier = TierHaiku
		adj.Reason = "budget >95% consumed; force cheapest tier"
		adj.Urgency = 1.0

	case usedPercent >= 80 || (hoursRemaining < 2 && hoursRemaining > 0):
		// Getting close: downgrade by one tier
		adj.ShouldAdjust = currentTier > TierHaiku
		adj.RecommendedTier = currentTier - 1
		if adj.RecommendedTier < TierHaiku {
			adj.RecommendedTier = TierHaiku
		}
		adj.Reason = "budget pressure; reduce tier to extend runway"
		adj.Urgency = 0.7

	case usedPercent >= 60 && hoursRemaining < 4:
		// Moderate pressure: suggest downgrade for opus users
		if currentTier == TierOpus {
			adj.ShouldAdjust = true
			adj.RecommendedTier = TierSonnet
			adj.Reason = "moderate budget pressure; downgrade from opus to sonnet"
			adj.Urgency = 0.4
		} else {
			adj.ShouldAdjust = false
			adj.RecommendedTier = currentTier
			adj.Reason = "moderate usage; current tier is acceptable"
		}

	default:
		// Budget is fine
		adj.ShouldAdjust = false
		adj.RecommendedTier = currentTier
		adj.Reason = "budget is healthy; no adjustment needed"
		adj.Urgency = 0.0
	}

	return adj
}

// --- Internal helpers (must be called with at least a read lock held) ---

// updateEMA recalculates the exponential moving average rates.
// Must be called with the write lock held.
func (p *UsagePredictor) updateEMA() {
	n := len(p.snapshots)
	if n < 2 {
		return
	}

	prev := p.snapshots[n-2]
	curr := p.snapshots[n-1]

	dt := curr.Timestamp.Sub(prev.Timestamp).Seconds()
	if dt <= 0 {
		return
	}

	costRate := (curr.TotalCost - prev.TotalCost) / dt
	tokenRate := float64(curr.TotalTokens-prev.TotalTokens) / dt
	requestRate := float64(curr.RequestCount-prev.RequestCount) / dt

	// Clamp negative rates to zero (can happen if snapshots are not monotonic)
	costRate = math.Max(0, costRate)
	tokenRate = math.Max(0, tokenRate)
	requestRate = math.Max(0, requestRate)

	if !p.emaInitialized {
		p.emaCostRate = costRate
		p.emaTokenRate = tokenRate
		p.emaRequestRate = requestRate
		p.emaInitialized = true
	} else {
		p.emaCostRate = emaAlpha*costRate + (1-emaAlpha)*p.emaCostRate
		p.emaTokenRate = emaAlpha*tokenRate + (1-emaAlpha)*p.emaTokenRate
		p.emaRequestRate = emaAlpha*requestRate + (1-emaAlpha)*p.emaRequestRate
	}
}

// linearRegression computes the slope and intercept for a value extracted
// from the snapshot series. Time is measured in hours from the first snapshot.
// Returns (slope, intercept) where y = slope*x + intercept.
func (p *UsagePredictor) linearRegression(valueFn func(PredictionSnapshot) float64) (float64, float64) {
	n := float64(len(p.snapshots))
	if n < 2 {
		return 0, 0
	}

	var sumX, sumY, sumXY, sumX2 float64
	for _, s := range p.snapshots {
		x := p.hoursFromFirst(s.Timestamp)
		y := valueFn(s)
		sumX += x
		sumY += y
		sumXY += x * y
		sumX2 += x * x
	}

	denom := n*sumX2 - sumX*sumX
	if denom == 0 {
		// All points at the same time; return flat line at mean
		return 0, sumY / n
	}

	slope := (n*sumXY - sumX*sumY) / denom
	intercept := (sumY - slope*sumX) / n
	return slope, intercept
}

// hoursFromFirst returns the hours elapsed between the first snapshot and t.
func (p *UsagePredictor) hoursFromFirst(t time.Time) float64 {
	if len(p.snapshots) == 0 {
		return 0
	}
	return t.Sub(p.snapshots[0].Timestamp).Hours()
}

// detectTrend classifies the slope relative to the current value.
func (p *UsagePredictor) detectTrend(slope, currentValue float64) UsageTrend {
	// Normalize slope by current value to get a relative rate of change.
	// If currentValue is near zero, use absolute slope.
	var normalizedSlope float64
	if currentValue > 0.001 {
		normalizedSlope = slope / currentValue
	} else {
		normalizedSlope = slope
	}

	switch {
	case normalizedSlope > trendThreshold:
		return TrendIncreasing
	case normalizedSlope < -trendThreshold:
		return TrendDecreasing
	default:
		return TrendStable
	}
}

// calculateConfidence returns a confidence score in [0, 1] based on the
// number of data points and their consistency.
func (p *UsagePredictor) calculateConfidence() float64 {
	n := len(p.snapshots)
	if n < minDataPoints {
		return 0
	}

	// Base confidence from data quantity (more points = higher confidence).
	// Ramps from 0.3 at minDataPoints to 0.9 at maxSnapshots.
	dataFraction := float64(n) / float64(p.maxSnapshots)
	baseConfidence := 0.3 + 0.6*math.Min(1.0, dataFraction)

	// Penalize for short observation windows (< 10 minutes)
	first := p.snapshots[0]
	last := p.snapshots[n-1]
	windowMinutes := last.Timestamp.Sub(first.Timestamp).Minutes()
	if windowMinutes < 10 {
		baseConfidence *= windowMinutes / 10.0
	}

	return math.Max(0, math.Min(1.0, baseConfidence))
}

// dominantTier examines model breakdown data and returns the most expensive
// tier that has significant usage. Falls back to TierSonnet.
func (p *UsagePredictor) dominantTier() ModelTier {
	if len(p.snapshots) == 0 {
		return TierSonnet
	}

	latest := p.snapshots[len(p.snapshots)-1]
	if len(latest.ModelBreakdown) == 0 {
		return TierSonnet
	}

	// Find the model with the highest cost
	var maxCost float64
	var maxModel string
	for model, cost := range latest.ModelBreakdown {
		if cost > maxCost {
			maxCost = cost
			maxModel = model
		}
	}

	if maxModel == "" {
		return TierSonnet
	}

	// Map model name to tier using string heuristics
	return modelNameToTier(maxModel)
}

// modelNameToTier uses simple string matching to map a model name to a tier.
func modelNameToTier(model string) ModelTier {
	for i := 0; i < len(model)-3; i++ {
		switch {
		case matchesSubstring(model, i, "haiku"):
			return TierHaiku
		case matchesSubstring(model, i, "opus"):
			return TierOpus
		}
	}
	return TierSonnet
}

// matchesSubstring checks if model[i:] starts with sub.
func matchesSubstring(model string, i int, sub string) bool {
	if i+len(sub) > len(model) {
		return false
	}
	return model[i:i+len(sub)] == sub
}
