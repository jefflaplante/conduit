package loadtest

import (
	"fmt"
	"sort"
	"time"
)

// OptimizationCategory classifies the type of optimization suggestion.
type OptimizationCategory string

const (
	CategoryConnectionPool OptimizationCategory = "connection_pool"
	CategoryRateLimit      OptimizationCategory = "rate_limit"
	CategoryMemory         OptimizationCategory = "memory"
	CategoryLatency        OptimizationCategory = "latency"
	CategoryThroughput     OptimizationCategory = "throughput"
	CategoryErrorRate      OptimizationCategory = "error_rate"
)

// ImpactLevel describes the estimated severity/benefit of an optimization.
type ImpactLevel string

const (
	ImpactCritical ImpactLevel = "critical"
	ImpactHigh     ImpactLevel = "high"
	ImpactMedium   ImpactLevel = "medium"
	ImpactLow      ImpactLevel = "low"
)

// Optimization represents a single optimization recommendation derived from
// load test results.
type Optimization struct {
	Category    OptimizationCategory
	Description string
	Impact      ImpactLevel
	Action      string
}

// Optimizer analyzes load test results and produces actionable optimization
// recommendations. Each detection function examines a specific dimension of
// the results (error rates, latency distribution, throughput, etc.).
type Optimizer struct {
	// Thresholds — callers can override these before calling Analyze.
	ErrorRateThreshold          float64       // fraction [0,1]; default 0.05 (5%)
	P99LatencyThreshold         time.Duration // default 5s
	P95P50RatioThreshold        float64       // default 10.0
	ThroughputDropThreshold     float64       // fraction of target RPS; default 0.8
	ConnectionPoolErrorKeywords []string      // substrings to match in errors
	RateLimitErrorKeywords      []string
}

// NewOptimizer creates an Optimizer with sensible default thresholds.
func NewOptimizer() *Optimizer {
	return &Optimizer{
		ErrorRateThreshold:      0.05,
		P99LatencyThreshold:     5 * time.Second,
		P95P50RatioThreshold:    10.0,
		ThroughputDropThreshold: 0.8,
		ConnectionPoolErrorKeywords: []string{
			"connection refused",
			"too many open",
			"connection pool",
			"dial tcp",
			"connection reset",
		},
		RateLimitErrorKeywords: []string{
			"rate limit",
			"429",
			"too many requests",
			"throttl",
		},
	}
}

// AnalyzeResults examines load test results and returns a list of optimizations
// sorted by impact (critical first).
func AnalyzeResults(results *LoadTestResult) []Optimization {
	return NewOptimizer().Analyze(results)
}

// Analyze examines load test results and returns a list of optimizations
// sorted by impact (critical first).
func (o *Optimizer) Analyze(results *LoadTestResult) []Optimization {
	if results == nil || results.TotalRequests == 0 {
		return nil
	}

	var opts []Optimization

	opts = append(opts, o.detectHighErrorRate(results)...)
	opts = append(opts, o.detectConnectionPoolExhaustion(results)...)
	opts = append(opts, o.detectRateLimitSaturation(results)...)
	opts = append(opts, o.detectLatencySpikes(results)...)
	opts = append(opts, o.detectThroughputBottleneck(results)...)
	opts = append(opts, o.detectMemoryPressure(results)...)
	opts = append(opts, o.detectSlowQueryPatterns(results)...)

	// Sort by impact severity
	impactOrder := map[ImpactLevel]int{
		ImpactCritical: 0,
		ImpactHigh:     1,
		ImpactMedium:   2,
		ImpactLow:      3,
	}
	sort.Slice(opts, func(i, j int) bool {
		return impactOrder[opts[i].Impact] < impactOrder[opts[j].Impact]
	})

	return opts
}

func (o *Optimizer) detectHighErrorRate(r *LoadTestResult) []Optimization {
	if r.TotalRequests == 0 {
		return nil
	}
	errorRate := float64(r.Failures) / float64(r.TotalRequests)
	if errorRate < o.ErrorRateThreshold {
		return nil
	}

	impact := ImpactMedium
	if errorRate > 0.20 {
		impact = ImpactCritical
	} else if errorRate > 0.10 {
		impact = ImpactHigh
	}

	return []Optimization{{
		Category:    CategoryErrorRate,
		Description: fmt.Sprintf("High error rate detected: %.1f%% of requests failed (%d/%d)", errorRate*100, r.Failures, r.TotalRequests),
		Impact:      impact,
		Action:      "Investigate the most common error types below. Consider adding retry logic with exponential backoff, or reducing concurrency to stay within provider limits.",
	}}
}

func (o *Optimizer) detectConnectionPoolExhaustion(r *LoadTestResult) []Optimization {
	if len(r.ErrorCounts) == 0 {
		return nil
	}

	poolErrors := 0
	for errMsg, count := range r.ErrorCounts {
		for _, kw := range o.ConnectionPoolErrorKeywords {
			if containsCI(errMsg, kw) {
				poolErrors += count
				break
			}
		}
	}

	if poolErrors == 0 {
		return nil
	}

	return []Optimization{{
		Category:    CategoryConnectionPool,
		Description: fmt.Sprintf("Connection pool exhaustion detected: %d errors related to connection limits", poolErrors),
		Impact:      ImpactHigh,
		Action:      "Increase the HTTP client connection pool size (MaxIdleConns, MaxIdleConnsPerHost). Consider using persistent connections and adjusting idle timeout. Current SQLite pool is max 4 open connections — increase if database-bound.",
	}}
}

func (o *Optimizer) detectRateLimitSaturation(r *LoadTestResult) []Optimization {
	if len(r.ErrorCounts) == 0 {
		return nil
	}

	rlErrors := 0
	for errMsg, count := range r.ErrorCounts {
		for _, kw := range o.RateLimitErrorKeywords {
			if containsCI(errMsg, kw) {
				rlErrors += count
				break
			}
		}
	}

	if rlErrors == 0 {
		return nil
	}

	pct := float64(rlErrors) / float64(r.TotalRequests) * 100
	impact := ImpactMedium
	if pct > 20 {
		impact = ImpactCritical
	} else if pct > 10 {
		impact = ImpactHigh
	}

	return []Optimization{{
		Category:    CategoryRateLimit,
		Description: fmt.Sprintf("Rate limit saturation: %d requests (%.1f%%) hit rate limits", rlErrors, pct),
		Impact:      impact,
		Action:      "Reduce requests-per-second, implement client-side rate limiting with token bucket, or increase the provider's rate limit tier. Consider request queuing with backpressure.",
	}}
}

func (o *Optimizer) detectLatencySpikes(r *LoadTestResult) []Optimization {
	var opts []Optimization

	// Check absolute P99
	if r.P99Latency > o.P99LatencyThreshold {
		opts = append(opts, Optimization{
			Category:    CategoryLatency,
			Description: fmt.Sprintf("P99 latency is high: %v (threshold: %v)", r.P99Latency.Round(time.Millisecond), o.P99LatencyThreshold),
			Impact:      ImpactHigh,
			Action:      "Reduce max_tokens for simple requests, implement request timeouts, or use a faster model for low-complexity queries. Consider smart routing to delegate simple requests to lighter models.",
		})
	}

	// Check P95/P50 ratio (indicates tail latency problems)
	if r.P50Latency > 0 {
		ratio := float64(r.P95Latency) / float64(r.P50Latency)
		if ratio > o.P95P50RatioThreshold {
			opts = append(opts, Optimization{
				Category:    CategoryLatency,
				Description: fmt.Sprintf("Large latency spread: P95/P50 ratio is %.1fx (P50=%v, P95=%v)", ratio, r.P50Latency.Round(time.Millisecond), r.P95Latency.Round(time.Millisecond)),
				Impact:      ImpactMedium,
				Action:      "Tail latency may indicate resource contention or GC pauses. Profile the application under load, check for lock contention, and consider request prioritization.",
			})
		}
	}

	return opts
}

func (o *Optimizer) detectThroughputBottleneck(r *LoadTestResult) []Optimization {
	// Only flag if we have per-generator stats showing imbalanced performance
	if len(r.GeneratorStats) < 2 {
		return nil
	}

	var avgLatencies []time.Duration
	for _, gs := range r.GeneratorStats {
		if gs.Requests > 0 {
			avgLatencies = append(avgLatencies, gs.TotalLatency/time.Duration(gs.Requests))
		}
	}

	if len(avgLatencies) < 2 {
		return nil
	}

	sort.Slice(avgLatencies, func(i, j int) bool { return avgLatencies[i] < avgLatencies[j] })
	fastest := avgLatencies[0]
	slowest := avgLatencies[len(avgLatencies)-1]

	if fastest > 0 && float64(slowest)/float64(fastest) > 5.0 {
		return []Optimization{{
			Category:    CategoryThroughput,
			Description: fmt.Sprintf("Generator throughput imbalance: slowest generator avg is %.1fx slower than fastest (fastest=%v, slowest=%v)", float64(slowest)/float64(fastest), fastest.Round(time.Millisecond), slowest.Round(time.Millisecond)),
			Impact:      ImpactMedium,
			Action:      "Consider routing complex requests to separate worker pools, or reduce the weight of slow generators in the request mix to improve overall throughput.",
		}}
	}

	return nil
}

func (o *Optimizer) detectMemoryPressure(r *LoadTestResult) []Optimization {
	// Heuristic: if we see a pattern of increasing latencies over time, it may
	// indicate GC pressure or memory growth. We analyze the results timeline.
	if len(r.Results) < 20 {
		return nil
	}

	// Split results into first quarter and last quarter by time
	sorted := make([]RequestResult, len(r.Results))
	copy(sorted, r.Results)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].StartTime.Before(sorted[j].StartTime) })

	quarter := len(sorted) / 4
	if quarter == 0 {
		return nil
	}

	firstQ := sorted[:quarter]
	lastQ := sorted[len(sorted)-quarter:]

	avgFirst := avgLatency(firstQ)
	avgLast := avgLatency(lastQ)

	if avgFirst > 0 && avgLast > 0 {
		ratio := float64(avgLast) / float64(avgFirst)
		if ratio > 2.0 {
			return []Optimization{{
				Category:    CategoryMemory,
				Description: fmt.Sprintf("Latency degradation over time: last quarter avg is %.1fx slower than first quarter (first=%v, last=%v)", ratio, avgFirst.Round(time.Millisecond), avgLast.Round(time.Millisecond)),
				Impact:      ImpactHigh,
				Action:      "Progressive latency increase suggests memory pressure or resource leaks. Profile heap allocations under load, check for goroutine leaks, and verify connection cleanup. Consider setting GOMEMLIMIT.",
			}}
		}
	}

	return nil
}

func (o *Optimizer) detectSlowQueryPatterns(r *LoadTestResult) []Optimization {
	// Check if specific generator types are disproportionately slow
	for name, gs := range r.GeneratorStats {
		if gs.Requests < 5 {
			continue
		}
		avg := gs.TotalLatency / time.Duration(gs.Requests)
		if avg > 3*time.Second && gs.Failures > gs.Successes {
			return []Optimization{{
				Category:    CategoryLatency,
				Description: fmt.Sprintf("Generator '%s' shows poor performance: avg %v with %d failures out of %d requests", name, avg.Round(time.Millisecond), gs.Failures, gs.Requests),
				Impact:      ImpactMedium,
				Action:      fmt.Sprintf("The '%s' request pattern is causing excessive failures. Consider simplifying the prompt, reducing max_tokens, or adding circuit breaker logic for this request type.", name),
			}}
		}
	}
	return nil
}

// avgLatency computes average latency across a slice of results.
func avgLatency(results []RequestResult) time.Duration {
	if len(results) == 0 {
		return 0
	}
	var total time.Duration
	for _, r := range results {
		total += r.Latency
	}
	return total / time.Duration(len(results))
}

// containsCI checks if s contains substr (case-insensitive).
func containsCI(s, substr string) bool {
	sLower := toLower(s)
	subLower := toLower(substr)
	return len(sLower) >= len(subLower) && containsString(sLower, subLower)
}

func toLower(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

func containsString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
