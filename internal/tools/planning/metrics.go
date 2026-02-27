package planning

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"
)

// MetricsCollector collects and analyzes tool execution metrics for optimization
type MetricsCollector struct {
	data                map[string]*ToolMetricData
	planMetrics         map[string]*PlanMetricData
	mu                  sync.RWMutex
	startTime           time.Time
	enabled             bool
	retentionDays       int
	aggregationInterval time.Duration
}

// ToolMetricData contains performance data for a specific tool
type ToolMetricData struct {
	ToolName         string            `json:"tool_name"`
	ExecutionCount   int64             `json:"execution_count"`
	SuccessCount     int64             `json:"success_count"`
	FailureCount     int64             `json:"failure_count"`
	TotalLatency     time.Duration     `json:"total_latency"`
	MinLatency       time.Duration     `json:"min_latency"`
	MaxLatency       time.Duration     `json:"max_latency"`
	CacheHits        int64             `json:"cache_hits"`
	CacheMisses      int64             `json:"cache_misses"`
	RetryCount       int64             `json:"retry_count"`
	FallbackCount    int64             `json:"fallback_count"`
	TotalCost        float64           `json:"total_cost"`
	ErrorTypes       map[string]int64  `json:"error_types"`
	LastExecution    time.Time         `json:"last_execution"`
	LatencyP50       time.Duration     `json:"latency_p50"`
	LatencyP95       time.Duration     `json:"latency_p95"`
	LatencyP99       time.Duration     `json:"latency_p99"`
	RecentExecutions []ExecutionRecord `json:"recent_executions"`
	CostPerCall      float64           `json:"cost_per_call"`
	ReliabilityScore float64           `json:"reliability_score"`
}

// PlanMetricData contains metrics for execution plan performance
type PlanMetricData struct {
	PlanID              string        `json:"plan_id"`
	TotalDuration       time.Duration `json:"total_duration"`
	ParallelGroups      int           `json:"parallel_groups"`
	MaxConcurrency      int           `json:"max_concurrency"`
	CacheHitRate        float64       `json:"cache_hit_rate"`
	SuccessRate         float64       `json:"success_rate"`
	OptimizationApplied []string      `json:"optimization_applied"`
	EstimatedDuration   time.Duration `json:"estimated_duration"`
	ActualDuration      time.Duration `json:"actual_duration"`
	AccuracyRatio       float64       `json:"accuracy_ratio"`
	TotalCost           float64       `json:"total_cost"`
	StepCount           int           `json:"step_count"`
	FailedSteps         []string      `json:"failed_steps"`
	CreatedAt           time.Time     `json:"created_at"`
}

// ExecutionRecord tracks individual tool executions for trend analysis
type ExecutionRecord struct {
	Timestamp    time.Time     `json:"timestamp"`
	Duration     time.Duration `json:"duration"`
	Success      bool          `json:"success"`
	CacheHit     bool          `json:"cache_hit"`
	Retries      int           `json:"retries"`
	FallbackUsed bool          `json:"fallback_used"`
	Cost         float64       `json:"cost"`
	Error        string        `json:"error,omitempty"`
}

// PerformanceSummary provides aggregated performance insights
type PerformanceSummary struct {
	TotalExecutions    int64                       `json:"total_executions"`
	TotalSuccesses     int64                       `json:"total_successes"`
	OverallSuccessRate float64                     `json:"overall_success_rate"`
	AverageLatency     time.Duration               `json:"average_latency"`
	CacheHitRate       float64                     `json:"cache_hit_rate"`
	TotalCost          float64                     `json:"total_cost"`
	TopPerformers      []ToolPerformanceRanking    `json:"top_performers"`
	BottomPerformers   []ToolPerformanceRanking    `json:"bottom_performers"`
	TrendData          map[string][]TrendPoint     `json:"trend_data"`
	Recommendations    []PerformanceRecommendation `json:"recommendations"`
	UpTime             time.Duration               `json:"uptime"`
	MetricsCollectedAt time.Time                   `json:"metrics_collected_at"`
}

// ToolPerformanceRanking ranks tools by performance
type ToolPerformanceRanking struct {
	ToolName       string        `json:"tool_name"`
	Score          float64       `json:"score"`
	ExecutionCount int64         `json:"execution_count"`
	AverageLatency time.Duration `json:"average_latency"`
	SuccessRate    float64       `json:"success_rate"`
	CacheHitRate   float64       `json:"cache_hit_rate"`
	CostEfficiency float64       `json:"cost_efficiency"`
}

// TrendPoint represents a data point in performance trends
type TrendPoint struct {
	Timestamp time.Time `json:"timestamp"`
	Value     float64   `json:"value"`
}

// PerformanceRecommendation suggests optimizations
type PerformanceRecommendation struct {
	Type        string `json:"type"`        // "caching", "parallelization", "timeout", "retry"
	Tool        string `json:"tool"`        // Tool name or "general"
	Priority    string `json:"priority"`    // "high", "medium", "low"
	Description string `json:"description"` // Human-readable recommendation
	Impact      string `json:"impact"`      // Expected improvement
	Effort      string `json:"effort"`      // Implementation effort
}

// NewMetricsCollector creates a new metrics collector
func NewMetricsCollector() *MetricsCollector {
	return &MetricsCollector{
		data:                make(map[string]*ToolMetricData),
		planMetrics:         make(map[string]*PlanMetricData),
		startTime:           time.Now(),
		enabled:             true,
		retentionDays:       30,
		aggregationInterval: time.Minute * 5,
	}
}

// RecordExecution records a tool execution for metrics
func (mc *MetricsCollector) RecordExecution(toolName string, duration time.Duration, success bool, cacheHit bool, retries int, fallbackUsed bool, cost float64, err error) {
	if !mc.enabled {
		return
	}

	mc.mu.Lock()
	defer mc.mu.Unlock()

	// Get or create tool metrics
	if _, exists := mc.data[toolName]; !exists {
		mc.data[toolName] = &ToolMetricData{
			ToolName:         toolName,
			ErrorTypes:       make(map[string]int64),
			RecentExecutions: []ExecutionRecord{},
			MinLatency:       duration, // Initialize with first duration
			MaxLatency:       duration,
		}
	}

	metrics := mc.data[toolName]

	// Update counters
	metrics.ExecutionCount++
	if success {
		metrics.SuccessCount++
	} else {
		metrics.FailureCount++
	}

	// Update latency statistics
	metrics.TotalLatency += duration
	if duration < metrics.MinLatency {
		metrics.MinLatency = duration
	}
	if duration > metrics.MaxLatency {
		metrics.MaxLatency = duration
	}

	// Update cache statistics
	if cacheHit {
		metrics.CacheHits++
	} else {
		metrics.CacheMisses++
	}

	// Update retry and fallback counts
	metrics.RetryCount += int64(retries)
	if fallbackUsed {
		metrics.FallbackCount++
	}

	// Update cost tracking
	metrics.TotalCost += cost
	metrics.CostPerCall = metrics.TotalCost / float64(metrics.ExecutionCount)

	// Track error types
	if err != nil {
		errorType := mc.categorizeError(err)
		metrics.ErrorTypes[errorType]++
	}

	// Update recent executions (keep last 100)
	record := ExecutionRecord{
		Timestamp:    time.Now(),
		Duration:     duration,
		Success:      success,
		CacheHit:     cacheHit,
		Retries:      retries,
		FallbackUsed: fallbackUsed,
		Cost:         cost,
	}
	if err != nil {
		record.Error = err.Error()
	}

	metrics.RecentExecutions = append(metrics.RecentExecutions, record)
	if len(metrics.RecentExecutions) > 100 {
		metrics.RecentExecutions = metrics.RecentExecutions[1:]
	}

	// Update calculated fields
	metrics.LastExecution = time.Now()
	metrics.ReliabilityScore = float64(metrics.SuccessCount) / float64(metrics.ExecutionCount)

	// Update percentiles from recent executions
	mc.updatePercentiles(metrics)
}

// RecordPlanExecution records execution plan metrics
func (mc *MetricsCollector) RecordPlanExecution(planResult *PlanResult, estimated EstimatedMetrics, optimizations []string) {
	if !mc.enabled {
		return
	}

	mc.mu.Lock()
	defer mc.mu.Unlock()

	// Calculate metrics
	successCount := 0
	cacheHits := 0
	totalCost := 0.0

	for _, stepResult := range planResult.StepResults {
		if stepResult.Success {
			successCount++
		}
		if stepResult.CacheHit {
			cacheHits++
		}
		totalCost += stepResult.Duration.Seconds() * 0.001 // Rough cost calculation
	}

	successRate := float64(successCount) / float64(len(planResult.StepResults))
	cacheHitRate := float64(cacheHits) / float64(len(planResult.StepResults))

	// Calculate accuracy of estimates
	accuracyRatio := 1.0
	if estimated.Duration > 0 {
		accuracyRatio = float64(planResult.Duration) / float64(estimated.Duration)
	}

	planMetrics := &PlanMetricData{
		PlanID:              planResult.PlanID,
		TotalDuration:       planResult.Duration,
		ParallelGroups:      planResult.CacheHits,        // Reusing field for groups count
		MaxConcurrency:      len(planResult.StepResults), // Max possible concurrent
		CacheHitRate:        cacheHitRate,
		SuccessRate:         successRate,
		OptimizationApplied: optimizations,
		EstimatedDuration:   estimated.Duration,
		ActualDuration:      planResult.Duration,
		AccuracyRatio:       accuracyRatio,
		TotalCost:           totalCost,
		StepCount:           len(planResult.StepResults),
		FailedSteps:         planResult.FailedSteps,
		CreatedAt:           planResult.StartTime,
	}

	mc.planMetrics[planResult.PlanID] = planMetrics

	log.Printf("Recorded plan execution metrics: %s (duration: %v, success rate: %.2f)",
		planResult.PlanID, planResult.Duration, successRate)
}

// GetToolMetrics returns metrics for a specific tool
func (mc *MetricsCollector) GetToolMetrics(toolName string) *ToolMetricData {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	if metrics, exists := mc.data[toolName]; exists {
		// Return copy to avoid race conditions
		copy := *metrics
		return &copy
	}
	return nil
}

// GetAllToolMetrics returns metrics for all tools
func (mc *MetricsCollector) GetAllToolMetrics() map[string]*ToolMetricData {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	result := make(map[string]*ToolMetricData)
	for name, metrics := range mc.data {
		copy := *metrics
		result[name] = &copy
	}
	return result
}

// GetPerformanceSummary returns aggregated performance insights
func (mc *MetricsCollector) GetPerformanceSummary() *PerformanceSummary {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	summary := &PerformanceSummary{
		MetricsCollectedAt: time.Now(),
		UpTime:             time.Since(mc.startTime),
		TrendData:          make(map[string][]TrendPoint),
	}

	// Aggregate across all tools
	var totalExecutions, totalSuccesses, totalCacheHits, totalCacheRequests int64
	var totalLatency time.Duration
	var totalCost float64

	for _, metrics := range mc.data {
		totalExecutions += metrics.ExecutionCount
		totalSuccesses += metrics.SuccessCount
		totalLatency += metrics.TotalLatency
		totalCost += metrics.TotalCost
		totalCacheHits += metrics.CacheHits
		totalCacheRequests += metrics.CacheHits + metrics.CacheMisses
	}

	summary.TotalExecutions = totalExecutions
	summary.TotalSuccesses = totalSuccesses
	summary.TotalCost = totalCost

	if totalExecutions > 0 {
		summary.OverallSuccessRate = float64(totalSuccesses) / float64(totalExecutions)
		summary.AverageLatency = time.Duration(int64(totalLatency) / totalExecutions)
	}

	if totalCacheRequests > 0 {
		summary.CacheHitRate = float64(totalCacheHits) / float64(totalCacheRequests)
	}

	// Generate performance rankings
	summary.TopPerformers = mc.calculateTopPerformers(5)
	summary.BottomPerformers = mc.calculateBottomPerformers(5)

	// Generate recommendations
	summary.Recommendations = mc.generateRecommendations()

	return summary
}

// updatePercentiles calculates latency percentiles from recent executions
func (mc *MetricsCollector) updatePercentiles(metrics *ToolMetricData) {
	if len(metrics.RecentExecutions) < 10 {
		return // Need more data for meaningful percentiles
	}

	// Extract latencies and sort
	latencies := make([]time.Duration, len(metrics.RecentExecutions))
	for i, record := range metrics.RecentExecutions {
		latencies[i] = record.Duration
	}

	sort.Slice(latencies, func(i, j int) bool {
		return latencies[i] < latencies[j]
	})

	// Calculate percentiles
	p50Index := len(latencies) * 50 / 100
	p95Index := len(latencies) * 95 / 100
	p99Index := len(latencies) * 99 / 100

	metrics.LatencyP50 = latencies[p50Index]
	metrics.LatencyP95 = latencies[p95Index]
	metrics.LatencyP99 = latencies[p99Index]
}

// calculateTopPerformers ranks tools by performance score
func (mc *MetricsCollector) calculateTopPerformers(count int) []ToolPerformanceRanking {
	var rankings []ToolPerformanceRanking

	for toolName, metrics := range mc.data {
		if metrics.ExecutionCount < 5 {
			continue // Need minimum executions for ranking
		}

		ranking := ToolPerformanceRanking{
			ToolName:       toolName,
			ExecutionCount: metrics.ExecutionCount,
			SuccessRate:    metrics.ReliabilityScore,
		}

		if metrics.ExecutionCount > 0 {
			ranking.AverageLatency = time.Duration(int64(metrics.TotalLatency) / metrics.ExecutionCount)
		}

		if metrics.CacheHits+metrics.CacheMisses > 0 {
			ranking.CacheHitRate = float64(metrics.CacheHits) / float64(metrics.CacheHits+metrics.CacheMisses)
		}

		// Calculate cost efficiency (lower cost per success = higher efficiency)
		if metrics.SuccessCount > 0 {
			ranking.CostEfficiency = 1.0 / (metrics.TotalCost / float64(metrics.SuccessCount))
		}

		// Calculate overall performance score
		ranking.Score = mc.calculatePerformanceScore(metrics)

		rankings = append(rankings, ranking)
	}

	// Sort by score (descending)
	sort.Slice(rankings, func(i, j int) bool {
		return rankings[i].Score > rankings[j].Score
	})

	// Return top performers
	if len(rankings) > count {
		rankings = rankings[:count]
	}

	return rankings
}

// calculateBottomPerformers ranks worst performing tools
func (mc *MetricsCollector) calculateBottomPerformers(count int) []ToolPerformanceRanking {
	rankings := mc.calculateTopPerformers(len(mc.data))

	// Reverse to get bottom performers
	for i, j := 0, len(rankings)-1; i < j; i, j = i+1, j-1 {
		rankings[i], rankings[j] = rankings[j], rankings[i]
	}

	if len(rankings) > count {
		rankings = rankings[:count]
	}

	return rankings
}

// calculatePerformanceScore calculates overall performance score for a tool
func (mc *MetricsCollector) calculatePerformanceScore(metrics *ToolMetricData) float64 {
	if metrics.ExecutionCount == 0 {
		return 0
	}

	// Weighted score components
	successWeight := 0.4
	latencyWeight := 0.3
	costWeight := 0.2
	cacheWeight := 0.1

	// Success rate score (0-1)
	successScore := metrics.ReliabilityScore

	// Latency score (lower is better, normalized)
	avgLatency := float64(metrics.TotalLatency) / float64(metrics.ExecutionCount)
	latencyScore := 1.0 / (1.0 + avgLatency/1e9) // Convert to seconds and normalize

	// Cost score (lower is better, normalized)
	costScore := 1.0 / (1.0 + metrics.CostPerCall*100)

	// Cache hit score
	var cacheScore float64
	if metrics.CacheHits+metrics.CacheMisses > 0 {
		cacheScore = float64(metrics.CacheHits) / float64(metrics.CacheHits+metrics.CacheMisses)
	}

	// Weighted total
	totalScore := successWeight*successScore + latencyWeight*latencyScore +
		costWeight*costScore + cacheWeight*cacheScore

	return totalScore
}

// generateRecommendations creates performance improvement recommendations
func (mc *MetricsCollector) generateRecommendations() []PerformanceRecommendation {
	var recommendations []PerformanceRecommendation

	for toolName, metrics := range mc.data {
		if metrics.ExecutionCount < 10 {
			continue // Need enough data for recommendations
		}

		// Caching recommendations
		if metrics.CacheMisses > metrics.CacheHits*3 && metrics.ExecutionCount > 20 {
			recommendations = append(recommendations, PerformanceRecommendation{
				Type:        "caching",
				Tool:        toolName,
				Priority:    "high",
				Description: fmt.Sprintf("Tool %s has low cache hit rate (%.1f%%). Consider implementing or improving caching.", toolName, float64(metrics.CacheHits)/float64(metrics.CacheHits+metrics.CacheMisses)*100),
				Impact:      "30-50% latency reduction",
				Effort:      "medium",
			})
		}

		// Reliability recommendations
		if metrics.ReliabilityScore < 0.9 {
			recommendations = append(recommendations, PerformanceRecommendation{
				Type:        "reliability",
				Tool:        toolName,
				Priority:    "high",
				Description: fmt.Sprintf("Tool %s has low success rate (%.1f%%). Consider improving error handling and retries.", toolName, metrics.ReliabilityScore*100),
				Impact:      "Reduce failed executions",
				Effort:      "medium",
			})
		}

		// Timeout recommendations
		avgLatency := time.Duration(int64(metrics.TotalLatency) / metrics.ExecutionCount)
		if avgLatency > time.Second*5 && metrics.RetryCount > metrics.SuccessCount/2 {
			recommendations = append(recommendations, PerformanceRecommendation{
				Type:        "timeout",
				Tool:        toolName,
				Priority:    "medium",
				Description: fmt.Sprintf("Tool %s has high latency (%.2fs avg) and retry rate. Consider optimizing timeouts.", toolName, avgLatency.Seconds()),
				Impact:      "Faster failure detection",
				Effort:      "low",
			})
		}

		// Cost recommendations
		if metrics.CostPerCall > 0.01 && metrics.SuccessCount > 50 {
			recommendations = append(recommendations, PerformanceRecommendation{
				Type:        "cost",
				Tool:        toolName,
				Priority:    "medium",
				Description: fmt.Sprintf("Tool %s has high cost per call ($%.4f). Consider cost optimization strategies.", toolName, metrics.CostPerCall),
				Impact:      "20-40% cost reduction",
				Effort:      "medium",
			})
		}
	}

	// General recommendations based on overall metrics
	if len(mc.data) > 3 {
		totalParallelizable := 0
		for toolName := range mc.data {
			if mc.isParallelizable(toolName) {
				totalParallelizable++
			}
		}

		if totalParallelizable > 1 {
			recommendations = append(recommendations, PerformanceRecommendation{
				Type:        "parallelization",
				Tool:        "general",
				Priority:    "high",
				Description: fmt.Sprintf("%d tools are parallelizable. Ensure optimal parallel execution strategy.", totalParallelizable),
				Impact:      "40-70% overall speedup",
				Effort:      "low",
			})
		}
	}

	// Sort by priority
	sort.Slice(recommendations, func(i, j int) bool {
		priorities := map[string]int{"high": 3, "medium": 2, "low": 1}
		return priorities[recommendations[i].Priority] > priorities[recommendations[j].Priority]
	})

	return recommendations
}

// Helper functions

func (mc *MetricsCollector) categorizeError(err error) string {
	errStr := err.Error()

	if contains(errStr, "timeout") || contains(errStr, "deadline") {
		return "timeout"
	}
	if contains(errStr, "network") || contains(errStr, "connection") {
		return "network"
	}
	if contains(errStr, "permission") || contains(errStr, "forbidden") {
		return "permission"
	}
	if contains(errStr, "not found") || contains(errStr, "404") {
		return "not_found"
	}
	if contains(errStr, "rate limit") || contains(errStr, "429") {
		return "rate_limit"
	}

	return "unknown"
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr ||
		(len(s) > len(substr) &&
			(s[:len(substr)] == substr || s[len(s)-len(substr):] == substr ||
				findSubstring(s, substr) != -1)))
}

func findSubstring(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func (mc *MetricsCollector) isParallelizable(toolName string) bool {
	parallelTools := []string{"web_search", "web_fetch", "memory_search", "read_file"}
	for _, tool := range parallelTools {
		if toolName == tool {
			return true
		}
	}
	return false
}

// Export exports metrics to JSON format
func (mc *MetricsCollector) Export() ([]byte, error) {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	exportData := map[string]interface{}{
		"tool_metrics": mc.data,
		"plan_metrics": mc.planMetrics,
		"summary":      mc.GetPerformanceSummary(),
		"exported_at":  time.Now(),
	}

	return json.MarshalIndent(exportData, "", "  ")
}

// Reset clears all metrics data
func (mc *MetricsCollector) Reset() {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	mc.data = make(map[string]*ToolMetricData)
	mc.planMetrics = make(map[string]*PlanMetricData)
	mc.startTime = time.Now()

	log.Printf("Metrics collector reset")
}

// Enable/disable metrics collection
func (mc *MetricsCollector) SetEnabled(enabled bool) {
	mc.enabled = enabled
	log.Printf("Metrics collection enabled: %v", enabled)
}
