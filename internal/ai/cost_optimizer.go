package ai

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
)

// CostPolicy defines how aggressively the optimizer manages costs.
type CostPolicy int

const (
	// PolicyStrict enforces a hard budget cap. Requests that would exceed the
	// budget are downgraded to the cheapest available model or rejected.
	PolicyStrict CostPolicy = iota

	// PolicyBestEffort prefers cheaper models when quality is comparable, but
	// allows upgrades for complex tasks. This is the default.
	PolicyBestEffort

	// PolicyQualityFirst minimizes downgrades; only suggests cheaper models
	// when the quality difference is negligible. Cost savings are secondary.
	PolicyQualityFirst
)

// String returns a human-readable name for the policy.
func (p CostPolicy) String() string {
	switch p {
	case PolicyStrict:
		return "strict"
	case PolicyBestEffort:
		return "best-effort"
	case PolicyQualityFirst:
		return "quality-first"
	default:
		return "unknown"
	}
}

// CostRecord captures a single request's cost data for analysis.
type CostRecord struct {
	Timestamp    time.Time       `json:"timestamp"`
	Model        string          `json:"model"`
	Tier         string          `json:"tier"`
	Complexity   ComplexityLevel `json:"complexity"`
	InputTokens  int             `json:"input_tokens"`
	OutputTokens int             `json:"output_tokens"`
	Cost         float64         `json:"cost"`
}

// CostBreakdown provides aggregated cost data over a time period.
type CostBreakdown struct {
	// Period is the time window covered by this breakdown.
	Period time.Duration `json:"period"`

	// TotalCost is the sum of all request costs in the window.
	TotalCost float64 `json:"total_cost"`

	// TotalRequests is the number of requests in the window.
	TotalRequests int `json:"total_requests"`

	// ByModel maps model ID to its aggregated cost and request count.
	ByModel map[string]*ModelCostEntry `json:"by_model"`

	// ByTier maps tier name to its aggregated cost and request count.
	ByTier map[string]*TierCostEntry `json:"by_tier"`

	// ByComplexity maps complexity level to its aggregated cost and request count.
	ByComplexity map[ComplexityLevel]*ComplexityCostEntry `json:"by_complexity"`
}

// ModelCostEntry holds cost data for a single model.
type ModelCostEntry struct {
	Model        string  `json:"model"`
	TotalCost    float64 `json:"total_cost"`
	RequestCount int     `json:"request_count"`
	AvgCost      float64 `json:"avg_cost"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
}

// TierCostEntry holds cost data for a model tier.
type TierCostEntry struct {
	Tier         string  `json:"tier"`
	TotalCost    float64 `json:"total_cost"`
	RequestCount int     `json:"request_count"`
	AvgCost      float64 `json:"avg_cost"`
}

// ComplexityCostEntry holds cost data for a complexity level.
type ComplexityCostEntry struct {
	Complexity   ComplexityLevel `json:"complexity"`
	TotalCost    float64         `json:"total_cost"`
	RequestCount int             `json:"request_count"`
	AvgCost      float64         `json:"avg_cost"`
}

// CostSuggestion describes a potential cost optimization.
type CostSuggestion struct {
	// Description is a human-readable explanation of the optimization.
	Description string `json:"description"`

	// EstimatedSavings is the estimated dollar savings if this suggestion is applied.
	EstimatedSavings float64 `json:"estimated_savings"`

	// AffectedPct is the percentage of requests affected by this suggestion.
	AffectedPct float64 `json:"affected_pct"`

	// Confidence is how confident the system is in this suggestion (0.0-1.0).
	Confidence float64 `json:"confidence"`
}

// SavingsEstimate summarizes the total potential savings from all suggestions.
type SavingsEstimate struct {
	// CurrentSpend is the total spend in the analysis window.
	CurrentSpend float64 `json:"current_spend"`

	// OptimalSpend is the estimated spend if every request used the cheapest
	// viable model for its complexity level.
	OptimalSpend float64 `json:"optimal_spend"`

	// PotentialSavings is CurrentSpend - OptimalSpend.
	PotentialSavings float64 `json:"potential_savings"`

	// SavingsPct is the percentage of current spend that could be saved.
	SavingsPct float64 `json:"savings_pct"`

	// Suggestions is the list of individual suggestions contributing to savings.
	Suggestions []CostSuggestion `json:"suggestions"`
}

// CostOptimizerConfig holds configuration for the CostOptimizer.
type CostOptimizerConfig struct {
	// Policy determines the cost management strategy.
	Policy CostPolicy

	// DailyBudget is the hard daily budget cap in USD. 0 means unlimited.
	DailyBudget float64

	// WindowSize is the rolling window for cost analysis. Defaults to 24h.
	WindowSize time.Duration

	// MaxRecords is the maximum number of cost records to retain.
	// Older records are evicted when this limit is reached. Defaults to 10000.
	MaxRecords int

	// TierModels maps tier names to their model IDs, used for optimal-cost
	// calculations. If nil, uses default tier model mapping.
	TierModels map[string]string
}

// CostOptimizer tracks request costs and provides optimization intelligence.
// It maintains a rolling window of cost records and analyzes them to identify
// savings opportunities, generate breakdowns, and make downgrade recommendations.
//
// Thread-safe for concurrent recording and querying.
type CostOptimizer struct {
	mu      sync.RWMutex
	records []CostRecord
	config  CostOptimizerConfig

	// tierModelMap maps each complexity level to the cheapest viable model ID,
	// used for optimal-spend calculations in savings estimates.
	tierModelMap map[ComplexityLevel]string
}

// NewCostOptimizer creates a new CostOptimizer with the given configuration.
// If cfg is nil, defaults are used (PolicyBestEffort, 24h window, 10000 records).
func NewCostOptimizer(cfg *CostOptimizerConfig) *CostOptimizer {
	c := CostOptimizerConfig{
		Policy:     PolicyBestEffort,
		WindowSize: 24 * time.Hour,
		MaxRecords: 10000,
	}

	if cfg != nil {
		c.Policy = cfg.Policy
		c.DailyBudget = cfg.DailyBudget
		if cfg.WindowSize > 0 {
			c.WindowSize = cfg.WindowSize
		}
		if cfg.MaxRecords > 0 {
			c.MaxRecords = cfg.MaxRecords
		}
		c.TierModels = cfg.TierModels
	}

	co := &CostOptimizer{
		records: make([]CostRecord, 0, 256),
		config:  c,
	}
	co.tierModelMap = co.buildTierModelMap()

	return co
}

// RecordCost records the cost of a completed request.
func (co *CostOptimizer) RecordCost(result *SmartRoutingResult, inputTokens, outputTokens int) {
	if result == nil {
		return
	}

	cost := CalculateCost(result.SelectedModel, inputTokens, outputTokens)

	record := CostRecord{
		Timestamp:    time.Now(),
		Model:        result.SelectedModel,
		Tier:         result.Tier.String(),
		Complexity:   result.Complexity.Level,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		Cost:         cost,
	}

	co.mu.Lock()
	defer co.mu.Unlock()

	co.records = append(co.records, record)

	// Evict oldest records if we exceed the limit
	if len(co.records) > co.config.MaxRecords {
		excess := len(co.records) - co.config.MaxRecords
		co.records = co.records[excess:]
	}
}

// GetBreakdown returns a cost breakdown for the specified time period.
// Only records within the most recent `period` duration are included.
func (co *CostOptimizer) GetBreakdown(period time.Duration) *CostBreakdown {
	co.mu.RLock()
	defer co.mu.RUnlock()

	cutoff := time.Now().Add(-period)
	breakdown := &CostBreakdown{
		Period:       period,
		ByModel:      make(map[string]*ModelCostEntry),
		ByTier:       make(map[string]*TierCostEntry),
		ByComplexity: make(map[ComplexityLevel]*ComplexityCostEntry),
	}

	for _, r := range co.records {
		if r.Timestamp.Before(cutoff) {
			continue
		}

		breakdown.TotalCost += r.Cost
		breakdown.TotalRequests++

		// By model
		me, ok := breakdown.ByModel[r.Model]
		if !ok {
			me = &ModelCostEntry{Model: r.Model}
			breakdown.ByModel[r.Model] = me
		}
		me.TotalCost += r.Cost
		me.RequestCount++
		me.InputTokens += r.InputTokens
		me.OutputTokens += r.OutputTokens

		// By tier
		te, ok := breakdown.ByTier[r.Tier]
		if !ok {
			te = &TierCostEntry{Tier: r.Tier}
			breakdown.ByTier[r.Tier] = te
		}
		te.TotalCost += r.Cost
		te.RequestCount++

		// By complexity
		ce, ok := breakdown.ByComplexity[r.Complexity]
		if !ok {
			ce = &ComplexityCostEntry{Complexity: r.Complexity}
			breakdown.ByComplexity[r.Complexity] = ce
		}
		ce.TotalCost += r.Cost
		ce.RequestCount++
	}

	// Calculate averages
	for _, me := range breakdown.ByModel {
		if me.RequestCount > 0 {
			me.AvgCost = me.TotalCost / float64(me.RequestCount)
		}
	}
	for _, te := range breakdown.ByTier {
		if te.RequestCount > 0 {
			te.AvgCost = te.TotalCost / float64(te.RequestCount)
		}
	}
	for _, ce := range breakdown.ByComplexity {
		if ce.RequestCount > 0 {
			ce.AvgCost = ce.TotalCost / float64(ce.RequestCount)
		}
	}

	return breakdown
}

// GetOptimizationSuggestions analyzes cost records and returns actionable
// suggestions for reducing spend. The analysis window is the optimizer's
// configured WindowSize.
func (co *CostOptimizer) GetOptimizationSuggestions() []CostSuggestion {
	co.mu.RLock()
	defer co.mu.RUnlock()

	cutoff := time.Now().Add(-co.config.WindowSize)
	var windowRecords []CostRecord
	for _, r := range co.records {
		if !r.Timestamp.Before(cutoff) {
			windowRecords = append(windowRecords, r)
		}
	}

	if len(windowRecords) == 0 {
		return nil
	}

	var suggestions []CostSuggestion

	// Suggestion 1: Simple requests routed to expensive models
	s1 := co.analyzeSimpleOnExpensive(windowRecords)
	if s1 != nil {
		suggestions = append(suggestions, *s1)
	}

	// Suggestion 2: Standard requests routed to opus
	s2 := co.analyzeStandardOnOpus(windowRecords)
	if s2 != nil {
		suggestions = append(suggestions, *s2)
	}

	// Suggestion 3: Cost spike detection
	s3 := co.analyzeCostSpike(windowRecords)
	if s3 != nil {
		suggestions = append(suggestions, *s3)
	}

	// Suggestion 4: High-cost model concentration
	s4 := co.analyzeModelConcentration(windowRecords)
	if s4 != nil {
		suggestions = append(suggestions, *s4)
	}

	// Sort by estimated savings (highest first)
	sort.Slice(suggestions, func(i, j int) bool {
		return suggestions[i].EstimatedSavings > suggestions[j].EstimatedSavings
	})

	return suggestions
}

// ShouldDowngrade evaluates whether the given request should use a cheaper
// model based on the current cost policy, spend rate, and complexity.
// Returns true if a downgrade is recommended, along with the suggested
// tier name.
func (co *CostOptimizer) ShouldDowngrade(complexity ComplexityLevel, currentTier string) (bool, string) {
	co.mu.RLock()
	defer co.mu.RUnlock()

	switch co.config.Policy {
	case PolicyQualityFirst:
		// Only downgrade under extreme budget pressure
		if co.config.DailyBudget > 0 {
			spend := co.currentWindowSpend()
			if spend >= co.config.DailyBudget {
				return true, "haiku"
			}
		}
		return false, ""

	case PolicyStrict:
		// Aggressively downgrade based on budget utilization
		if co.config.DailyBudget > 0 {
			spend := co.currentWindowSpend()
			utilization := spend / co.config.DailyBudget

			if utilization >= 1.0 {
				return true, "haiku"
			}
			if utilization >= 0.7 && currentTier == "opus" {
				return true, "sonnet"
			}
			if utilization >= 0.9 && currentTier == "sonnet" {
				return true, "haiku"
			}
		}

		// Also downgrade if complexity doesn't warrant the tier
		if complexity == ComplexitySimple && (currentTier == "sonnet" || currentTier == "opus") {
			return true, "haiku"
		}
		if complexity == ComplexityStandard && currentTier == "opus" {
			return true, "sonnet"
		}
		return false, ""

	default: // PolicyBestEffort
		// Moderate downgrade: respect complexity but look for easy wins
		if co.config.DailyBudget > 0 {
			spend := co.currentWindowSpend()
			utilization := spend / co.config.DailyBudget

			if utilization >= 1.0 {
				return true, "haiku"
			}
			if utilization >= 0.8 && currentTier == "opus" {
				return true, "sonnet"
			}
		}

		// Downgrade simple requests on expensive tiers
		if complexity == ComplexitySimple && currentTier == "opus" {
			return true, "haiku"
		}
		if complexity == ComplexitySimple && currentTier == "sonnet" {
			return true, "haiku"
		}
		return false, ""
	}
}

// GetSavingsEstimate calculates the potential savings if all requests in the
// analysis window had used the cheapest viable model for their complexity.
func (co *CostOptimizer) GetSavingsEstimate() *SavingsEstimate {
	co.mu.RLock()
	defer co.mu.RUnlock()

	cutoff := time.Now().Add(-co.config.WindowSize)
	var windowRecords []CostRecord
	for _, r := range co.records {
		if !r.Timestamp.Before(cutoff) {
			windowRecords = append(windowRecords, r)
		}
	}

	if len(windowRecords) == 0 {
		return &SavingsEstimate{}
	}

	var currentSpend, optimalSpend float64
	for _, r := range windowRecords {
		currentSpend += r.Cost

		// Calculate optimal cost: use the cheapest viable model for this complexity
		optimalModel, ok := co.tierModelMap[r.Complexity]
		if !ok {
			// Unknown complexity, assume current cost is optimal
			optimalSpend += r.Cost
			continue
		}
		optimalCost := CalculateCost(optimalModel, r.InputTokens, r.OutputTokens)
		optimalSpend += optimalCost
	}

	potentialSavings := currentSpend - optimalSpend
	if potentialSavings < 0 {
		potentialSavings = 0
	}

	savingsPct := 0.0
	if currentSpend > 0 {
		savingsPct = (potentialSavings / currentSpend) * 100.0
	}

	suggestions := co.getOptimizationSuggestionsLocked(windowRecords)

	return &SavingsEstimate{
		CurrentSpend:     currentSpend,
		OptimalSpend:     optimalSpend,
		PotentialSavings: potentialSavings,
		SavingsPct:       savingsPct,
		Suggestions:      suggestions,
	}
}

// RecordCount returns the current number of records stored.
func (co *CostOptimizer) RecordCount() int {
	co.mu.RLock()
	defer co.mu.RUnlock()
	return len(co.records)
}

// Policy returns the currently configured cost policy.
func (co *CostOptimizer) Policy() CostPolicy {
	co.mu.RLock()
	defer co.mu.RUnlock()
	return co.config.Policy
}

// PruneOlderThan removes records older than the given duration.
// Returns the number of records removed.
func (co *CostOptimizer) PruneOlderThan(age time.Duration) int {
	co.mu.Lock()
	defer co.mu.Unlock()

	cutoff := time.Now().Add(-age)
	kept := co.records[:0]
	for _, r := range co.records {
		if !r.Timestamp.Before(cutoff) {
			kept = append(kept, r)
		}
	}
	removed := len(co.records) - len(kept)
	co.records = kept
	return removed
}

// --- Internal analysis helpers ---

// currentWindowSpend returns total spend in the configured analysis window.
// Must be called with at least a read lock held.
func (co *CostOptimizer) currentWindowSpend() float64 {
	cutoff := time.Now().Add(-co.config.WindowSize)
	var total float64
	for _, r := range co.records {
		if !r.Timestamp.Before(cutoff) {
			total += r.Cost
		}
	}
	return total
}

// analyzeSimpleOnExpensive checks what percentage of simple-complexity requests
// are going to sonnet or opus models. If significant, suggests routing them
// to haiku instead.
func (co *CostOptimizer) analyzeSimpleOnExpensive(records []CostRecord) *CostSuggestion {
	var simpleTotal, simpleOnExpensive int
	var wasteCost float64

	for _, r := range records {
		if r.Complexity == ComplexitySimple {
			simpleTotal++
			if r.Tier == "sonnet" || r.Tier == "opus" {
				simpleOnExpensive++
				// Estimate what it would cost on haiku
				haikuCost := co.estimateCostOnTier(r, "haiku")
				wasteCost += r.Cost - haikuCost
			}
		}
	}

	if simpleTotal == 0 || simpleOnExpensive == 0 {
		return nil
	}

	pct := float64(simpleOnExpensive) / float64(simpleTotal) * 100.0
	if pct < 10 {
		return nil // Not significant enough
	}

	confidence := math.Min(pct/100.0+0.3, 0.95)

	return &CostSuggestion{
		Description:      fmt.Sprintf("%.0f%% of simple requests are routed to expensive models (sonnet/opus) — route them to haiku", pct),
		EstimatedSavings: wasteCost,
		AffectedPct:      pct,
		Confidence:       confidence,
	}
}

// analyzeStandardOnOpus checks for standard-complexity requests going to opus.
func (co *CostOptimizer) analyzeStandardOnOpus(records []CostRecord) *CostSuggestion {
	var standardTotal, standardOnOpus int
	var wasteCost float64

	for _, r := range records {
		if r.Complexity == ComplexityStandard {
			standardTotal++
			if r.Tier == "opus" {
				standardOnOpus++
				sonnetCost := co.estimateCostOnTier(r, "sonnet")
				wasteCost += r.Cost - sonnetCost
			}
		}
	}

	if standardTotal == 0 || standardOnOpus == 0 {
		return nil
	}

	pct := float64(standardOnOpus) / float64(standardTotal) * 100.0
	if pct < 10 {
		return nil
	}

	confidence := math.Min(pct/100.0+0.2, 0.90)

	return &CostSuggestion{
		Description:      fmt.Sprintf("%.0f%% of standard-complexity requests are routed to opus — sonnet may suffice", pct),
		EstimatedSavings: wasteCost,
		AffectedPct:      pct,
		Confidence:       confidence,
	}
}

// analyzeCostSpike detects if recent spending is significantly above the
// average rate over the window.
func (co *CostOptimizer) analyzeCostSpike(records []CostRecord) *CostSuggestion {
	if len(records) < 10 {
		return nil // Not enough data for spike detection
	}

	// Split into two halves and compare
	mid := len(records) / 2
	var firstHalfCost, secondHalfCost float64
	for i, r := range records {
		if i < mid {
			firstHalfCost += r.Cost
		} else {
			secondHalfCost += r.Cost
		}
	}

	if firstHalfCost == 0 {
		return nil
	}

	ratio := secondHalfCost / firstHalfCost
	if ratio <= 1.5 {
		return nil // No significant spike
	}

	excess := secondHalfCost - firstHalfCost

	return &CostSuggestion{
		Description:      fmt.Sprintf("Recent spending is %.0f%% higher than the previous period — potential cost spike detected", (ratio-1)*100),
		EstimatedSavings: excess,
		AffectedPct:      100.0,
		Confidence:       math.Min(0.5+(ratio-1.5)*0.2, 0.85),
	}
}

// analyzeModelConcentration checks if a single high-cost model handles most
// requests, suggesting diversification.
func (co *CostOptimizer) analyzeModelConcentration(records []CostRecord) *CostSuggestion {
	if len(records) < 5 {
		return nil
	}

	modelCosts := make(map[string]float64)
	modelCounts := make(map[string]int)
	var totalCost float64

	for _, r := range records {
		modelCosts[r.Model] += r.Cost
		modelCounts[r.Model]++
		totalCost += r.Cost
	}

	if totalCost == 0 {
		return nil
	}

	// Find the most expensive model by total cost
	var topModel string
	var topCost float64
	for model, cost := range modelCosts {
		if cost > topCost {
			topCost = cost
			topModel = model
		}
	}

	costShare := topCost / totalCost * 100.0
	requestShare := float64(modelCounts[topModel]) / float64(len(records)) * 100.0

	// Only flag if model accounts for >70% of cost with >50% of requests
	if costShare < 70 || requestShare < 50 {
		return nil
	}

	// Check if this model is an expensive tier
	pricing := PricingForModel(topModel)
	if pricing.InputPerMToken < 5.0 {
		return nil // Already a cheap model
	}

	return &CostSuggestion{
		Description:      fmt.Sprintf("%s accounts for %.0f%% of cost (%.0f%% of requests) — consider routing some requests to cheaper models", topModel, costShare, requestShare),
		EstimatedSavings: topCost * 0.3, // Conservative estimate: 30% of that cost is saveable
		AffectedPct:      requestShare,
		Confidence:       0.6,
	}
}

// estimateCostOnTier estimates what a record would have cost on a different tier.
func (co *CostOptimizer) estimateCostOnTier(r CostRecord, targetTier string) float64 {
	// Look up a representative model for the target tier
	model := co.tierModel(targetTier)
	if model == "" {
		return r.Cost // Can't estimate, return original
	}
	return CalculateCost(model, r.InputTokens, r.OutputTokens)
}

// tierModel returns a representative model ID for a tier name.
func (co *CostOptimizer) tierModel(tier string) string {
	if co.config.TierModels != nil {
		if m, ok := co.config.TierModels[tier]; ok {
			return m
		}
	}
	// Default tier model mapping
	switch tier {
	case "haiku":
		return "claude-haiku-4"
	case "sonnet":
		return "claude-sonnet-4"
	case "opus":
		return "claude-opus-4"
	default:
		return ""
	}
}

// buildTierModelMap maps each complexity level to its cheapest viable model ID.
func (co *CostOptimizer) buildTierModelMap() map[ComplexityLevel]string {
	m := make(map[ComplexityLevel]string)
	m[ComplexitySimple] = co.tierModel("haiku")
	m[ComplexityStandard] = co.tierModel("sonnet")
	m[ComplexityComplex] = co.tierModel("opus")
	return m
}

// getOptimizationSuggestionsLocked runs the suggestion analysis without
// acquiring the lock. Used by GetSavingsEstimate which already holds the lock.
func (co *CostOptimizer) getOptimizationSuggestionsLocked(records []CostRecord) []CostSuggestion {
	if len(records) == 0 {
		return nil
	}

	var suggestions []CostSuggestion

	if s := co.analyzeSimpleOnExpensive(records); s != nil {
		suggestions = append(suggestions, *s)
	}
	if s := co.analyzeStandardOnOpus(records); s != nil {
		suggestions = append(suggestions, *s)
	}
	if s := co.analyzeCostSpike(records); s != nil {
		suggestions = append(suggestions, *s)
	}
	if s := co.analyzeModelConcentration(records); s != nil {
		suggestions = append(suggestions, *s)
	}

	sort.Slice(suggestions, func(i, j int) bool {
		return suggestions[i].EstimatedSavings > suggestions[j].EstimatedSavings
	})

	return suggestions
}
