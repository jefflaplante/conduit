package ai

import (
	"fmt"
	"math"
	"sync"
	"time"
)

// --- Configuration defaults ---

const (
	// defaultConfidenceThreshold is the minimum confidence required to accept
	// a cluster-based routing recommendation. Below this, the intelligence
	// layer defers to standard complexity-based routing.
	defaultConfidenceThreshold = 0.4

	// defaultAutoTuneInterval is the minimum time between auto-tune runs.
	defaultAutoTuneInterval = 5 * time.Minute

	// defaultOutcomeWindowSize is how many recent outcomes to retain for
	// prediction accuracy tracking.
	defaultOutcomeWindowSize = 500

	// confidenceAdjustStep is how much the confidence threshold adjusts per
	// auto-tune cycle. Small steps prevent oscillation.
	confidenceAdjustStep = 0.02

	// minConfidenceThreshold is the floor for auto-tuned confidence threshold.
	minConfidenceThreshold = 0.2

	// maxConfidenceThreshold is the ceiling for auto-tuned confidence threshold.
	maxConfidenceThreshold = 0.8
)

// --- Types ---

// RoutingDecision is the output of OptimizeRouting. It combines signals from
// all available subsystems into a single actionable recommendation.
type RoutingDecision struct {
	// SuggestedTier is the recommended model tier.
	SuggestedTier ModelTier `json:"suggested_tier"`

	// SuggestedModel is a specific model ID (may be empty if only tier is known).
	SuggestedModel string `json:"suggested_model,omitempty"`

	// Confidence is the overall confidence in this decision (0.0-1.0).
	Confidence float64 `json:"confidence"`

	// Reason explains why this decision was made.
	Reason string `json:"reason"`

	// Sources lists which subsystems contributed to the decision.
	Sources []string `json:"sources"`

	// ShouldDowngrade is true if the cost optimizer recommends a cheaper model.
	ShouldDowngrade bool `json:"should_downgrade,omitempty"`

	// DowngradeTier is the tier to downgrade to (valid only if ShouldDowngrade).
	DowngradeTier ModelTier `json:"downgrade_tier,omitempty"`

	// PredictedCostImpact is the estimated cost for this request, if available.
	PredictedCostImpact float64 `json:"predicted_cost_impact,omitempty"`

	// BudgetPressure is a 0.0-1.0 value indicating how much budget pressure
	// exists. 0 = no pressure, 1 = budget exhausted.
	BudgetPressure float64 `json:"budget_pressure,omitempty"`
}

// RoutingOutcome records the result of a completed request for learning.
type RoutingOutcome struct {
	// Request is the original request text.
	Request string

	// Complexity is the analyzed complexity level.
	Complexity ComplexityLevel

	// ToolCount is the number of tools available.
	ToolCount int

	// ActualTier is the tier that was actually used.
	ActualTier ModelTier

	// PredictedTier is the tier that the intelligence layer recommended.
	PredictedTier ModelTier

	// Success indicates whether the request completed successfully.
	Success bool

	// RecordedAt is when this outcome was recorded.
	RecordedAt time.Time
}

// RoutingInsights is an aggregated view of the intelligence layer's health,
// including cluster quality, cost trends, prediction accuracy, and suggestions.
type RoutingInsights struct {
	// ClusterHealth summarizes the state of pattern clusters.
	ClusterHealth *ClusterHealthInsight `json:"cluster_health"`

	// CostTrends summarizes recent cost patterns.
	CostTrends *CostTrendInsight `json:"cost_trends"`

	// PredictionAccuracy summarizes how well the router predicts outcomes.
	PredictionAccuracy *PredictionAccuracyInsight `json:"prediction_accuracy"`

	// Suggestions are actionable optimization recommendations.
	Suggestions []string `json:"suggestions"`

	// GeneratedAt is when this insight was computed.
	GeneratedAt time.Time `json:"generated_at"`
}

// ClusterHealthInsight summarizes the state of pattern clustering.
type ClusterHealthInsight struct {
	// TotalPatterns is the number of recorded patterns.
	TotalPatterns int `json:"total_patterns"`

	// TotalClusters is the number of active clusters.
	TotalClusters int `json:"total_clusters"`

	// AvgClusterSize is the average number of members per cluster.
	AvgClusterSize float64 `json:"avg_cluster_size"`

	// AvgSuccessRate is the weighted average success rate across clusters.
	AvgSuccessRate float64 `json:"avg_success_rate"`

	// Healthy is true if clusters have sufficient data for reliable recommendations.
	Healthy bool `json:"healthy"`
}

// CostTrendInsight summarizes recent cost patterns.
type CostTrendInsight struct {
	// Trend is the detected cost direction.
	Trend string `json:"trend"`

	// BurnRatePerHour is the current cost rate (USD/hr).
	BurnRatePerHour float64 `json:"burn_rate_per_hour"`

	// ProjectedDailyCost is the estimated total daily cost at current rate.
	ProjectedDailyCost float64 `json:"projected_daily_cost"`

	// BudgetUtilization is the fraction of daily budget used (0.0-1.0).
	// -1 if no budget is configured.
	BudgetUtilization float64 `json:"budget_utilization"`
}

// PredictionAccuracyInsight summarizes how well the router predicts.
type PredictionAccuracyInsight struct {
	// TotalOutcomes is the number of recorded outcomes.
	TotalOutcomes int `json:"total_outcomes"`

	// CorrectPredictions is how many times the predicted tier matched the actual.
	CorrectPredictions int `json:"correct_predictions"`

	// Accuracy is CorrectPredictions / TotalOutcomes (0.0-1.0).
	Accuracy float64 `json:"accuracy"`

	// SuccessRate is the fraction of outcomes that succeeded.
	SuccessRate float64 `json:"success_rate"`
}

// --- Functional options ---

// RouterIntelligenceOption configures a RouterIntelligence instance.
type RouterIntelligenceOption func(*RouterIntelligence)

// WithRouterPatternAnalyzer sets the pattern analyzer subsystem.
func WithRouterPatternAnalyzer(pa *PatternAnalyzer) RouterIntelligenceOption {
	return func(ri *RouterIntelligence) {
		ri.patternAnalyzer = pa
	}
}

// WithRouterCostOptimizer sets the cost optimizer subsystem.
func WithRouterCostOptimizer(co *CostOptimizer) RouterIntelligenceOption {
	return func(ri *RouterIntelligence) {
		ri.costOptimizer = co
	}
}

// WithRouterUsagePredictor sets the usage predictor subsystem.
func WithRouterUsagePredictor(up *UsagePredictor) RouterIntelligenceOption {
	return func(ri *RouterIntelligence) {
		ri.usagePredictor = up
	}
}

// WithRouterContextEngine sets the context engine subsystem.
func WithRouterContextEngine(ce ContextEngine) RouterIntelligenceOption {
	return func(ri *RouterIntelligence) {
		ri.contextEngine = ce
	}
}

// WithRouterDailyBudget sets the daily budget for cost-aware decisions.
func WithRouterDailyBudget(budget float64) RouterIntelligenceOption {
	return func(ri *RouterIntelligence) {
		if budget >= 0 {
			ri.dailyBudget = budget
		}
	}
}

// WithRouterConfidenceThreshold sets the minimum confidence for accepting
// cluster-based recommendations.
func WithRouterConfidenceThreshold(threshold float64) RouterIntelligenceOption {
	return func(ri *RouterIntelligence) {
		if threshold > 0 && threshold <= 1.0 {
			ri.confidenceThreshold = threshold
		}
	}
}

// --- RouterIntelligence ---

// RouterIntelligence is the orchestration layer that ties together all smart
// routing subsystems (pattern clustering, cost optimization, usage prediction,
// context engine) into a single optimized routing decision.
//
// All subsystems are optional. When a subsystem is unavailable, the
// intelligence layer gracefully degrades by omitting that signal from
// the decision. With no subsystems configured, OptimizeRouting returns a
// nil decision, signaling the caller to use default routing.
//
// Thread-safe: all public methods are safe for concurrent use.
type RouterIntelligence struct {
	mu sync.RWMutex

	// Subsystems (all optional)
	patternAnalyzer *PatternAnalyzer
	costOptimizer   *CostOptimizer
	usagePredictor  *UsagePredictor
	contextEngine   ContextEngine

	// Configuration
	dailyBudget         float64
	confidenceThreshold float64

	// Outcome tracking for prediction accuracy
	outcomes       []RoutingOutcome
	maxOutcomes    int
	lastAutoTune   time.Time
	autoTuneWindow time.Duration
}

// NewRouterIntelligence creates a new RouterIntelligence with the given options.
// All subsystems default to nil (graceful degradation).
func NewRouterIntelligence(opts ...RouterIntelligenceOption) *RouterIntelligence {
	ri := &RouterIntelligence{
		confidenceThreshold: defaultConfidenceThreshold,
		outcomes:            make([]RoutingOutcome, 0, 128),
		maxOutcomes:         defaultOutcomeWindowSize,
		autoTuneWindow:      defaultAutoTuneInterval,
	}

	for _, opt := range opts {
		opt(ri)
	}

	return ri
}

// OptimizeRouting combines signals from all available subsystems into a single
// optimized routing decision. Returns nil if no subsystems are configured or
// if there is insufficient data to make a recommendation.
//
// The decision-making process:
//  1. Query pattern analyzer for cluster-based recommendation
//  2. Check cost optimizer for downgrade signals
//  3. Consult usage predictor for budget pressure
//  4. Merge all signals with confidence-weighted voting
func (ri *RouterIntelligence) OptimizeRouting(request string, complexity ComplexityLevel, toolCount int) *RoutingDecision {
	ri.mu.RLock()
	defer ri.mu.RUnlock()

	// If no subsystems are available, return nil (use default routing)
	if ri.patternAnalyzer == nil && ri.costOptimizer == nil && ri.usagePredictor == nil {
		return nil
	}

	decision := &RoutingDecision{
		// Default to complexity-based tier
		SuggestedTier: complexityLevelToTier(complexity),
	}

	var signals []tierSignal
	var sources []string

	// Signal 1: Pattern cluster recommendation
	if ri.patternAnalyzer != nil {
		complexityScore := complexityLevelToScore(complexity)
		rec := ri.patternAnalyzer.GetClusterRecommendation(request, complexityScore, complexity, toolCount)
		if rec != nil && rec.Confidence >= ri.confidenceThreshold {
			signals = append(signals, tierSignal{
				tier:       rec.SuggestedTier,
				confidence: rec.Confidence,
				source:     "pattern_cluster",
			})
			sources = append(sources, "pattern_cluster")
			decision.SuggestedModel = rec.SuggestedModel
		}
	}

	// Signal 2: Cost optimizer downgrade recommendation
	if ri.costOptimizer != nil {
		baseTier := complexityLevelToTier(complexity)
		shouldDowngrade, targetTierStr := ri.costOptimizer.ShouldDowngrade(complexity, baseTier.String())
		if shouldDowngrade {
			downgradeTier := tierFromString(targetTierStr)
			decision.ShouldDowngrade = true
			decision.DowngradeTier = downgradeTier
			signals = append(signals, tierSignal{
				tier:       downgradeTier,
				confidence: 0.7, // cost signals have moderate confidence
				source:     "cost_optimizer",
			})
			sources = append(sources, "cost_optimizer")
		}
	}

	// Signal 3: Usage predictor budget pressure
	if ri.usagePredictor != nil && ri.dailyBudget > 0 {
		adj := ri.usagePredictor.SuggestTierAdjustment(ri.dailyBudget)
		if adj != nil && adj.ShouldAdjust {
			signals = append(signals, tierSignal{
				tier:       adj.RecommendedTier,
				confidence: adj.Urgency,
				source:     "usage_predictor",
			})
			sources = append(sources, "usage_predictor")
		}

		// Compute budget pressure
		forecast := ri.usagePredictor.PredictBudgetExhaustion(ri.dailyBudget)
		if forecast != nil {
			decision.BudgetPressure = forecast.BudgetUsedPercent / 100.0
			if decision.BudgetPressure > 1.0 {
				decision.BudgetPressure = 1.0
			}
		}
	}

	// If no signals were produced, return nil
	if len(signals) == 0 {
		return nil
	}

	// Merge signals using confidence-weighted voting
	finalTier, finalConfidence, reason := ri.mergeSignals(signals, complexity)
	decision.SuggestedTier = finalTier
	decision.Confidence = finalConfidence
	decision.Reason = reason
	decision.Sources = sources

	return decision
}

// RecordOutcome feeds a completed request outcome back into the subsystems
// for continuous learning. It updates the pattern analyzer, cost optimizer,
// and usage predictor with the result data.
func (ri *RouterIntelligence) RecordOutcome(result *SmartRoutingResult, request string, toolCount int, success bool) {
	if result == nil {
		return
	}

	// Feed into pattern analyzer
	if ri.patternAnalyzer != nil {
		ri.patternAnalyzer.RecordPattern(result, request, toolCount, success)
	}

	// Track outcome for prediction accuracy
	ri.mu.Lock()
	outcome := RoutingOutcome{
		Request:       request,
		Complexity:    result.Complexity.Level,
		ToolCount:     toolCount,
		ActualTier:    result.Tier,
		PredictedTier: result.ContextSuggestedTier,
		Success:       success,
		RecordedAt:    time.Now(),
	}

	ri.outcomes = append(ri.outcomes, outcome)
	if len(ri.outcomes) > ri.maxOutcomes {
		excess := len(ri.outcomes) - ri.maxOutcomes
		ri.outcomes = ri.outcomes[excess:]
	}
	ri.mu.Unlock()
}

// GetInsights returns an aggregated view of the intelligence layer's health,
// including cluster quality, cost trends, prediction accuracy, and optimization
// suggestions.
func (ri *RouterIntelligence) GetInsights() *RoutingInsights {
	ri.mu.RLock()
	defer ri.mu.RUnlock()

	insights := &RoutingInsights{
		GeneratedAt: time.Now(),
	}

	// Cluster health
	insights.ClusterHealth = ri.computeClusterHealth()

	// Cost trends
	insights.CostTrends = ri.computeCostTrends()

	// Prediction accuracy
	insights.PredictionAccuracy = ri.computePredictionAccuracy()

	// Suggestions
	insights.Suggestions = ri.generateSuggestions()

	return insights
}

// AutoTune performs periodic self-optimization. It adjusts the confidence
// threshold based on prediction accuracy and rebalances tier preferences
// based on cost/quality tradeoffs. Safe to call frequently; it short-circuits
// if not enough time has elapsed since the last tune.
func (ri *RouterIntelligence) AutoTune() {
	ri.mu.Lock()
	defer ri.mu.Unlock()

	// Throttle auto-tune frequency
	if time.Since(ri.lastAutoTune) < ri.autoTuneWindow {
		return
	}
	ri.lastAutoTune = time.Now()

	// Need sufficient outcomes for meaningful tuning
	if len(ri.outcomes) < 10 {
		return
	}

	// Compute current prediction accuracy
	accuracy := ri.predictionAccuracyLocked()

	// Adjust confidence threshold based on accuracy
	// High accuracy -> lower threshold (trust cluster recommendations more)
	// Low accuracy -> higher threshold (be more conservative)
	if accuracy.Accuracy > 0.8 && ri.confidenceThreshold > minConfidenceThreshold {
		// Predictions are good, we can trust lower-confidence recommendations
		ri.confidenceThreshold -= confidenceAdjustStep
		if ri.confidenceThreshold < minConfidenceThreshold {
			ri.confidenceThreshold = minConfidenceThreshold
		}
	} else if accuracy.Accuracy < 0.5 && ri.confidenceThreshold < maxConfidenceThreshold {
		// Predictions are poor, require higher confidence
		ri.confidenceThreshold += confidenceAdjustStep
		if ri.confidenceThreshold > maxConfidenceThreshold {
			ri.confidenceThreshold = maxConfidenceThreshold
		}
	}

	// If success rate is low despite correct tier predictions, the issue
	// is not with routing but with the models themselves — no adjustment needed.
}

// ConfidenceThreshold returns the current confidence threshold.
// Useful for testing and monitoring.
func (ri *RouterIntelligence) ConfidenceThreshold() float64 {
	ri.mu.RLock()
	defer ri.mu.RUnlock()
	return ri.confidenceThreshold
}

// OutcomeCount returns the number of recorded outcomes.
func (ri *RouterIntelligence) OutcomeCount() int {
	ri.mu.RLock()
	defer ri.mu.RUnlock()
	return len(ri.outcomes)
}

// --- Internal types ---

// tierSignal represents a single routing signal from a subsystem.
type tierSignal struct {
	tier       ModelTier
	confidence float64
	source     string
}

// --- Internal methods ---

// mergeSignals combines multiple tier signals using confidence-weighted voting.
// The result is the tier with the highest weighted vote total.
func (ri *RouterIntelligence) mergeSignals(signals []tierSignal, baseComplexity ComplexityLevel) (ModelTier, float64, string) {
	if len(signals) == 0 {
		baseTier := complexityLevelToTier(baseComplexity)
		return baseTier, 0, "no signals available"
	}

	// Single signal: use it directly
	if len(signals) == 1 {
		s := signals[0]
		return s.tier, s.confidence, fmt.Sprintf("%s recommends %s tier (confidence=%.2f)", s.source, s.tier, s.confidence)
	}

	// Multiple signals: weighted voting
	tierVotes := make(map[ModelTier]float64)
	tierSources := make(map[ModelTier][]string)

	for _, s := range signals {
		tierVotes[s.tier] += s.confidence
		tierSources[s.tier] = append(tierSources[s.tier], s.source)
	}

	// Find the tier with the highest weighted votes
	var bestTier ModelTier
	var bestVote float64
	for tier, vote := range tierVotes {
		if vote > bestVote {
			bestVote = vote
			bestTier = tier
		}
	}

	// Normalize confidence: the best vote divided by the total possible confidence
	totalConfidence := 0.0
	for _, s := range signals {
		totalConfidence += s.confidence
	}
	normalizedConfidence := bestVote / totalConfidence
	if normalizedConfidence > 1.0 {
		normalizedConfidence = 1.0
	}

	// Build reason string
	reason := fmt.Sprintf("%d signals agree on %s tier (weighted confidence=%.2f)",
		len(tierSources[bestTier]), bestTier, normalizedConfidence)

	return bestTier, normalizedConfidence, reason
}

// computeClusterHealth builds the cluster health insight.
// Must be called with at least a read lock held.
func (ri *RouterIntelligence) computeClusterHealth() *ClusterHealthInsight {
	if ri.patternAnalyzer == nil {
		return &ClusterHealthInsight{Healthy: false}
	}

	clusters := ri.patternAnalyzer.GetClusters()
	patternCount := ri.patternAnalyzer.PatternCount()

	insight := &ClusterHealthInsight{
		TotalPatterns: patternCount,
		TotalClusters: len(clusters),
	}

	if len(clusters) == 0 {
		insight.Healthy = patternCount < 3 // Not enough data yet is not unhealthy
		return insight
	}

	var totalMembers int
	var weightedSuccessRate float64
	for _, c := range clusters {
		totalMembers += c.MemberCount
		weightedSuccessRate += c.AvgSuccessRate * float64(c.MemberCount)
	}

	insight.AvgClusterSize = float64(totalMembers) / float64(len(clusters))
	if totalMembers > 0 {
		insight.AvgSuccessRate = weightedSuccessRate / float64(totalMembers)
	}

	// Clusters are healthy if we have sufficient data and reasonable success rate
	insight.Healthy = len(clusters) >= 2 && insight.AvgSuccessRate > 0.5

	return insight
}

// computeCostTrends builds the cost trend insight.
// Must be called with at least a read lock held.
func (ri *RouterIntelligence) computeCostTrends() *CostTrendInsight {
	insight := &CostTrendInsight{
		BudgetUtilization: -1, // default: no budget
	}

	if ri.usagePredictor != nil {
		trend := ri.usagePredictor.GetTrend()
		insight.Trend = trend.String()

		forecast := ri.usagePredictor.PredictUsage(24 * time.Hour)
		if forecast != nil {
			insight.ProjectedDailyCost = forecast.PredictedCost
		}

		if ri.dailyBudget > 0 {
			budgetForecast := ri.usagePredictor.PredictBudgetExhaustion(ri.dailyBudget)
			if budgetForecast != nil {
				insight.BurnRatePerHour = budgetForecast.CurrentBurnRate
				insight.BudgetUtilization = budgetForecast.BudgetUsedPercent / 100.0
			}
		}
	} else if ri.costOptimizer != nil {
		// Fallback: derive from cost optimizer breakdown
		breakdown := ri.costOptimizer.GetBreakdown(1 * time.Hour)
		if breakdown != nil && breakdown.TotalRequests > 0 {
			insight.BurnRatePerHour = breakdown.TotalCost
			insight.ProjectedDailyCost = breakdown.TotalCost * 24
		}
		insight.Trend = "unknown"
	} else {
		insight.Trend = "unknown"
	}

	return insight
}

// computePredictionAccuracy builds the prediction accuracy insight.
// Must be called with at least a read lock held.
func (ri *RouterIntelligence) computePredictionAccuracy() *PredictionAccuracyInsight {
	return ri.predictionAccuracyLocked()
}

// predictionAccuracyLocked computes prediction accuracy without acquiring locks.
// Must be called with at least a read lock held.
func (ri *RouterIntelligence) predictionAccuracyLocked() *PredictionAccuracyInsight {
	insight := &PredictionAccuracyInsight{
		TotalOutcomes: len(ri.outcomes),
	}

	if len(ri.outcomes) == 0 {
		return insight
	}

	var correct, successCount int
	for _, o := range ri.outcomes {
		if o.PredictedTier >= 0 && o.PredictedTier == o.ActualTier {
			correct++
		}
		if o.Success {
			successCount++
		}
	}

	insight.CorrectPredictions = correct
	if len(ri.outcomes) > 0 {
		insight.Accuracy = float64(correct) / float64(len(ri.outcomes))
		insight.SuccessRate = float64(successCount) / float64(len(ri.outcomes))
	}

	return insight
}

// generateSuggestions produces actionable optimization suggestions based on
// the current state of all subsystems.
// Must be called with at least a read lock held.
func (ri *RouterIntelligence) generateSuggestions() []string {
	var suggestions []string

	// Suggestion from cluster health
	if ri.patternAnalyzer != nil {
		patternCount := ri.patternAnalyzer.PatternCount()
		clusterCount := ri.patternAnalyzer.ClusterCount()

		if patternCount > 0 && clusterCount == 0 {
			suggestions = append(suggestions, "Pattern data exists but no clusters formed; consider lowering the cluster threshold")
		}
		if patternCount < 10 {
			suggestions = append(suggestions, "Insufficient pattern data for reliable recommendations; continue collecting data")
		}
	}

	// Suggestions from cost optimizer
	if ri.costOptimizer != nil {
		costSuggestions := ri.costOptimizer.GetOptimizationSuggestions()
		for _, cs := range costSuggestions {
			if cs.Confidence > 0.5 {
				suggestions = append(suggestions, cs.Description)
			}
		}
	}

	// Suggestion from prediction accuracy
	if len(ri.outcomes) >= 10 {
		accuracy := ri.predictionAccuracyLocked()
		if accuracy.Accuracy < 0.4 {
			suggestions = append(suggestions, "Prediction accuracy is low; auto-tune is increasing the confidence threshold")
		}
		if accuracy.SuccessRate < 0.7 {
			suggestions = append(suggestions, "Overall success rate is below 70%; check model availability and error rates")
		}
	}

	// Budget-related suggestion
	if ri.usagePredictor != nil && ri.dailyBudget > 0 {
		forecast := ri.usagePredictor.PredictBudgetExhaustion(ri.dailyBudget)
		if forecast != nil && forecast.WillExhaust {
			suggestions = append(suggestions, forecast.RecommendedAction)
		}
	}

	return suggestions
}

// --- Pure helper functions ---

// complexityLevelToScore maps a ComplexityLevel to a representative
// numeric score (0-100) for use with the pattern analyzer.
func complexityLevelToScore(level ComplexityLevel) int {
	switch level {
	case ComplexitySimple:
		return 10
	case ComplexityStandard:
		return 40
	case ComplexityComplex:
		return 80
	default:
		return 40
	}
}

// tierFromString converts a tier string to a ModelTier.
func tierFromString(s string) ModelTier {
	switch s {
	case "haiku":
		return TierHaiku
	case "sonnet":
		return TierSonnet
	case "opus":
		return TierOpus
	default:
		return TierSonnet
	}
}

// clampFloat64 constrains a value to [min, max].
func clampFloat64(v, minV, maxV float64) float64 {
	return math.Max(minV, math.Min(maxV, v))
}
