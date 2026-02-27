package monitoring

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"sync"
	"time"
)

// --- Source interfaces ---
// These interfaces define the data contract for each subsystem the dashboard
// can read from. All are optional; the dashboard degrades gracefully.

// UsageTrackerSource provides AI usage metrics.
type UsageTrackerSource interface {
	GetSnapshot() UsageSnapshot
	TotalCost() float64
}

// UsageSnapshot is duplicated here as a local type so the monitoring package
// does not import internal/ai. The dashboard consumer is responsible for
// adapting ai.UsageSnapshot to this type.
type UsageSnapshot struct {
	Providers map[string]*ProviderUsageRecord `json:"providers"`
	Models    map[string]*ModelUsageRecord    `json:"models"`
	Since     time.Time                       `json:"since"`
	Snapshot  time.Time                       `json:"snapshot"`
}

// ProviderUsageRecord tracks usage metrics for a single provider.
type ProviderUsageRecord struct {
	Provider          string    `json:"provider"`
	TotalRequests     int64     `json:"total_requests"`
	TotalInputTokens  int64     `json:"total_input_tokens"`
	TotalOutputTokens int64     `json:"total_output_tokens"`
	TotalCost         float64   `json:"total_cost"`
	TotalLatencyMs    int64     `json:"total_latency_ms"`
	LastUsed          time.Time `json:"last_used"`
	ErrorCount        int64     `json:"error_count"`
}

// ModelUsageRecord tracks usage metrics for a specific model.
type ModelUsageRecord struct {
	Model             string    `json:"model"`
	Provider          string    `json:"provider"`
	TotalRequests     int64     `json:"total_requests"`
	TotalInputTokens  int64     `json:"total_input_tokens"`
	TotalOutputTokens int64     `json:"total_output_tokens"`
	TotalCost         float64   `json:"total_cost"`
	TotalLatencyMs    int64     `json:"total_latency_ms"`
	AvgLatencyMs      float64   `json:"avg_latency_ms"`
	LastUsed          time.Time `json:"last_used"`
	ErrorCount        int64     `json:"error_count"`
}

// CostOptimizerSource provides cost analysis data.
type CostOptimizerSource interface {
	GetBreakdown(period time.Duration) *CostBreakdown
	GetSavingsEstimate() *SavingsEstimate
	GetOptimizationSuggestions() []CostSuggestion
}

// CostBreakdown mirrors ai.CostBreakdown without importing ai.
type CostBreakdown struct {
	Period        time.Duration              `json:"period"`
	TotalCost     float64                    `json:"total_cost"`
	TotalRequests int                        `json:"total_requests"`
	ByModel       map[string]*ModelCostEntry `json:"by_model"`
	ByTier        map[string]*TierCostEntry  `json:"by_tier"`
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

// SavingsEstimate mirrors ai.SavingsEstimate without importing ai.
type SavingsEstimate struct {
	CurrentSpend     float64          `json:"current_spend"`
	OptimalSpend     float64          `json:"optimal_spend"`
	PotentialSavings float64          `json:"potential_savings"`
	SavingsPct       float64          `json:"savings_pct"`
	Suggestions      []CostSuggestion `json:"suggestions"`
}

// CostSuggestion mirrors ai.CostSuggestion without importing ai.
type CostSuggestion struct {
	Description      string  `json:"description"`
	EstimatedSavings float64 `json:"estimated_savings"`
	AffectedPct      float64 `json:"affected_pct"`
	Confidence       float64 `json:"confidence"`
}

// UsagePredictorSource provides usage forecasting data.
type UsagePredictorSource interface {
	PredictUsage(horizon time.Duration) *UsageForecast
	GetTrend() string
	SnapshotCount() int
}

// UsageForecast mirrors ai.UsageForecast without importing ai.
type UsageForecast struct {
	Horizon           time.Duration `json:"horizon"`
	PredictedTokens   int64         `json:"predicted_tokens"`
	PredictedCost     float64       `json:"predicted_cost"`
	PredictedRequests int64         `json:"predicted_requests"`
	Confidence        float64       `json:"confidence"`
	Trend             string        `json:"trend"`
}

// PatternAnalyzerSource provides cluster statistics.
type PatternAnalyzerSource interface {
	PatternCount() int
	ClusterCount() int
	GetClusters() []PatternClusterInfo
}

// PatternClusterInfo mirrors a subset of ai.PatternCluster.
type PatternClusterInfo struct {
	ID             string  `json:"id"`
	Description    string  `json:"description"`
	MemberCount    int     `json:"member_count"`
	DominantModel  string  `json:"dominant_model"`
	AvgSuccessRate float64 `json:"avg_success_rate"`
	AvgLatencyMs   float64 `json:"avg_latency_ms"`
	AvgComplexity  float64 `json:"avg_complexity"`
}

// --- Dashboard Metrics Collector ---

// DashboardCollector aggregates metrics from multiple optional sources and
// exposes them through HTTP handlers. Thread-safe for concurrent reads.
type DashboardCollector struct {
	mu sync.RWMutex

	gatewayMetrics  *GatewayMetrics
	usageTracker    UsageTrackerSource
	costOptimizer   CostOptimizerSource
	usagePredictor  UsagePredictorSource
	patternAnalyzer PatternAnalyzerSource

	startTime time.Time
}

// NewDashboardCollector creates a new metrics dashboard collector.
func NewDashboardCollector() *DashboardCollector {
	return &DashboardCollector{
		startTime: time.Now(),
	}
}

// SetGatewayMetrics sets the gateway metrics source.
func (dc *DashboardCollector) SetGatewayMetrics(m *GatewayMetrics) {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	dc.gatewayMetrics = m
}

// SetUsageTracker sets the usage tracker source.
func (dc *DashboardCollector) SetUsageTracker(ut UsageTrackerSource) {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	dc.usageTracker = ut
}

// SetCostOptimizer sets the cost optimizer source.
func (dc *DashboardCollector) SetCostOptimizer(co CostOptimizerSource) {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	dc.costOptimizer = co
}

// SetUsagePredictor sets the usage predictor source.
func (dc *DashboardCollector) SetUsagePredictor(up UsagePredictorSource) {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	dc.usagePredictor = up
}

// SetPatternAnalyzer sets the pattern analyzer source.
func (dc *DashboardCollector) SetPatternAnalyzer(pa PatternAnalyzerSource) {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	dc.patternAnalyzer = pa
}

// Handler returns an http.Handler that routes metrics endpoints.
func (dc *DashboardCollector) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", dc.handlePrometheus)
	mux.HandleFunc("/metrics/json", dc.handleJSON)
	mux.HandleFunc("/metrics/health", dc.handleHealth)
	mux.HandleFunc("/metrics/usage", dc.handleUsage)
	mux.HandleFunc("/metrics/costs", dc.handleCosts)
	mux.HandleFunc("/metrics/routing", dc.handleRouting)
	return mux
}

// --- Prometheus text format ---

func (dc *DashboardCollector) handlePrometheus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	dc.mu.RLock()
	gm := dc.gatewayMetrics
	ut := dc.usageTracker
	dc.mu.RUnlock()

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	// System metrics
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	uptimeSeconds := int64(time.Since(dc.startTime).Seconds())

	fmt.Fprintf(w, "# HELP conduit_uptime_seconds Gateway uptime in seconds.\n")
	fmt.Fprintf(w, "# TYPE conduit_uptime_seconds gauge\n")
	fmt.Fprintf(w, "conduit_uptime_seconds %d\n", uptimeSeconds)

	fmt.Fprintf(w, "# HELP conduit_goroutines Number of running goroutines.\n")
	fmt.Fprintf(w, "# TYPE conduit_goroutines gauge\n")
	fmt.Fprintf(w, "conduit_goroutines %d\n", runtime.NumGoroutine())

	fmt.Fprintf(w, "# HELP conduit_memory_bytes Current memory allocation in bytes.\n")
	fmt.Fprintf(w, "# TYPE conduit_memory_bytes gauge\n")
	fmt.Fprintf(w, "conduit_memory_bytes %d\n", memStats.Alloc)

	// Gateway metrics (sessions, requests)
	if gm != nil {
		snap := gm.Snapshot()

		fmt.Fprintf(w, "# HELP conduit_sessions_active Active session count.\n")
		fmt.Fprintf(w, "# TYPE conduit_sessions_active gauge\n")
		fmt.Fprintf(w, "conduit_sessions_active %d\n", snap.ActiveSessions)

		fmt.Fprintf(w, "# HELP conduit_sessions_total Total session count.\n")
		fmt.Fprintf(w, "# TYPE conduit_sessions_total gauge\n")
		fmt.Fprintf(w, "conduit_sessions_total %d\n", snap.TotalSessions)

		fmt.Fprintf(w, "# HELP conduit_requests_completed_total Completed requests counter.\n")
		fmt.Fprintf(w, "# TYPE conduit_requests_completed_total counter\n")
		fmt.Fprintf(w, "conduit_requests_completed_total %d\n", snap.CompletedRequests)

		fmt.Fprintf(w, "# HELP conduit_requests_failed_total Failed requests counter.\n")
		fmt.Fprintf(w, "# TYPE conduit_requests_failed_total counter\n")
		fmt.Fprintf(w, "conduit_requests_failed_total %d\n", snap.FailedRequests)

		fmt.Fprintf(w, "# HELP conduit_websocket_connections Current WebSocket connections.\n")
		fmt.Fprintf(w, "# TYPE conduit_websocket_connections gauge\n")
		fmt.Fprintf(w, "conduit_websocket_connections %d\n", snap.WebhookConnections)
	}

	// Usage tracker metrics (per-model)
	if ut != nil {
		usageSnap := ut.GetSnapshot()
		totalCost := ut.TotalCost()

		fmt.Fprintf(w, "# HELP conduit_ai_cost_total_usd Total AI cost in USD.\n")
		fmt.Fprintf(w, "# TYPE conduit_ai_cost_total_usd gauge\n")
		fmt.Fprintf(w, "conduit_ai_cost_total_usd %.6f\n", totalCost)

		fmt.Fprintf(w, "# HELP conduit_ai_requests_total Total AI requests by model.\n")
		fmt.Fprintf(w, "# TYPE conduit_ai_requests_total counter\n")
		for name, mr := range usageSnap.Models {
			fmt.Fprintf(w, "conduit_ai_requests_total{model=%q} %d\n", name, mr.TotalRequests)
		}

		fmt.Fprintf(w, "# HELP conduit_ai_tokens_input_total Total input tokens by model.\n")
		fmt.Fprintf(w, "# TYPE conduit_ai_tokens_input_total counter\n")
		for name, mr := range usageSnap.Models {
			fmt.Fprintf(w, "conduit_ai_tokens_input_total{model=%q} %d\n", name, mr.TotalInputTokens)
		}

		fmt.Fprintf(w, "# HELP conduit_ai_tokens_output_total Total output tokens by model.\n")
		fmt.Fprintf(w, "# TYPE conduit_ai_tokens_output_total counter\n")
		for name, mr := range usageSnap.Models {
			fmt.Fprintf(w, "conduit_ai_tokens_output_total{model=%q} %d\n", name, mr.TotalOutputTokens)
		}

		fmt.Fprintf(w, "# HELP conduit_ai_errors_total Total AI errors by model.\n")
		fmt.Fprintf(w, "# TYPE conduit_ai_errors_total counter\n")
		for name, mr := range usageSnap.Models {
			fmt.Fprintf(w, "conduit_ai_errors_total{model=%q} %d\n", name, mr.ErrorCount)
		}

		fmt.Fprintf(w, "# HELP conduit_ai_latency_avg_ms Average latency by model in ms.\n")
		fmt.Fprintf(w, "# TYPE conduit_ai_latency_avg_ms gauge\n")
		for name, mr := range usageSnap.Models {
			fmt.Fprintf(w, "conduit_ai_latency_avg_ms{model=%q} %.1f\n", name, mr.AvgLatencyMs)
		}
	}
}

// --- JSON full metrics ---

// fullMetricsPayload is the structure returned by GET /metrics/json.
type fullMetricsPayload struct {
	Timestamp time.Time        `json:"timestamp"`
	Uptime    int64            `json:"uptime_seconds"`
	System    systemMetrics    `json:"system"`
	Gateway   *MetricsSnapshot `json:"gateway,omitempty"`
	Usage     *UsageSnapshot   `json:"usage,omitempty"`
	Costs     *costsPayload    `json:"costs,omitempty"`
	Routing   *routingPayload  `json:"routing,omitempty"`
}

type systemMetrics struct {
	MemoryBytes    uint64  `json:"memory_bytes"`
	MemoryMB       float64 `json:"memory_mb"`
	GoroutineCount int     `json:"goroutine_count"`
}

func (dc *DashboardCollector) handleJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	dc.mu.RLock()
	gm := dc.gatewayMetrics
	ut := dc.usageTracker
	co := dc.costOptimizer
	pa := dc.patternAnalyzer
	dc.mu.RUnlock()

	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	payload := fullMetricsPayload{
		Timestamp: time.Now(),
		Uptime:    int64(time.Since(dc.startTime).Seconds()),
		System: systemMetrics{
			MemoryBytes:    memStats.Alloc,
			MemoryMB:       float64(memStats.Alloc) / 1024 / 1024,
			GoroutineCount: runtime.NumGoroutine(),
		},
	}

	if gm != nil {
		snap := gm.Snapshot()
		payload.Gateway = &snap
	}

	if ut != nil {
		usageSnap := ut.GetSnapshot()
		payload.Usage = &usageSnap
	}

	if co != nil {
		payload.Costs = dc.buildCostsPayload(co)
	}

	if pa != nil {
		payload.Routing = dc.buildRoutingPayload(pa, ut)
	}

	writeJSON(w, http.StatusOK, payload)
}

// --- Health endpoint ---

type healthPayload struct {
	Status        string             `json:"status"`
	Timestamp     time.Time          `json:"timestamp"`
	UptimeSeconds int64              `json:"uptime_seconds"`
	Sessions      *sessionHealthInfo `json:"sessions,omitempty"`
	ErrorRate     *float64           `json:"error_rate,omitempty"`
	System        systemMetrics      `json:"system"`
}

type sessionHealthInfo struct {
	Active     int `json:"active"`
	Processing int `json:"processing"`
	Waiting    int `json:"waiting"`
	Idle       int `json:"idle"`
	Total      int `json:"total"`
}

func (dc *DashboardCollector) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	dc.mu.RLock()
	gm := dc.gatewayMetrics
	ut := dc.usageTracker
	dc.mu.RUnlock()

	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	payload := healthPayload{
		Status:        "healthy",
		Timestamp:     time.Now(),
		UptimeSeconds: int64(time.Since(dc.startTime).Seconds()),
		System: systemMetrics{
			MemoryBytes:    memStats.Alloc,
			MemoryMB:       float64(memStats.Alloc) / 1024 / 1024,
			GoroutineCount: runtime.NumGoroutine(),
		},
	}

	if gm != nil {
		snap := gm.Snapshot()
		payload.Sessions = &sessionHealthInfo{
			Active:     snap.ActiveSessions,
			Processing: snap.ProcessingSessions,
			Waiting:    snap.WaitingSessions,
			Idle:       snap.IdleSessions,
			Total:      snap.TotalSessions,
		}
		payload.Status = snap.Status
	}

	// Compute error rate from usage tracker
	if ut != nil {
		usageSnap := ut.GetSnapshot()
		var totalRequests, totalErrors int64
		for _, mr := range usageSnap.Models {
			totalRequests += mr.TotalRequests
			totalErrors += mr.ErrorCount
		}
		if totalRequests > 0 {
			rate := float64(totalErrors) / float64(totalRequests)
			payload.ErrorRate = &rate
		}
	}

	// Degrade status if error rate is high
	if payload.ErrorRate != nil && *payload.ErrorRate > 0.5 {
		payload.Status = "degraded"
	}

	writeJSON(w, http.StatusOK, payload)
}

// --- Usage endpoint ---

type usagePayload struct {
	Timestamp time.Time                       `json:"timestamp"`
	TotalCost float64                         `json:"total_cost"`
	Providers map[string]*ProviderUsageRecord `json:"providers,omitempty"`
	Models    map[string]*ModelUsageRecord    `json:"models,omitempty"`
	Since     time.Time                       `json:"since"`
	Forecast  *UsageForecast                  `json:"forecast,omitempty"`
	Trend     string                          `json:"trend,omitempty"`
}

func (dc *DashboardCollector) handleUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	dc.mu.RLock()
	ut := dc.usageTracker
	up := dc.usagePredictor
	dc.mu.RUnlock()

	payload := usagePayload{
		Timestamp: time.Now(),
	}

	if ut != nil {
		usageSnap := ut.GetSnapshot()
		payload.TotalCost = ut.TotalCost()
		payload.Providers = usageSnap.Providers
		payload.Models = usageSnap.Models
		payload.Since = usageSnap.Since
	}

	if up != nil {
		forecast := up.PredictUsage(24 * time.Hour)
		if forecast != nil {
			payload.Forecast = forecast
		}
		payload.Trend = up.GetTrend()
	}

	writeJSON(w, http.StatusOK, payload)
}

// --- Costs endpoint ---

type costsPayload struct {
	Timestamp   time.Time        `json:"timestamp"`
	Breakdown   *CostBreakdown   `json:"breakdown,omitempty"`
	Savings     *SavingsEstimate `json:"savings,omitempty"`
	Suggestions []CostSuggestion `json:"suggestions,omitempty"`
}

func (dc *DashboardCollector) handleCosts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	dc.mu.RLock()
	co := dc.costOptimizer
	dc.mu.RUnlock()

	payload := costsPayload{
		Timestamp: time.Now(),
	}

	if co != nil {
		cp := dc.buildCostsPayload(co)
		if cp != nil {
			payload = *cp
		}
	}

	writeJSON(w, http.StatusOK, payload)
}

func (dc *DashboardCollector) buildCostsPayload(co CostOptimizerSource) *costsPayload {
	breakdown := co.GetBreakdown(24 * time.Hour)
	savings := co.GetSavingsEstimate()
	suggestions := co.GetOptimizationSuggestions()

	return &costsPayload{
		Timestamp:   time.Now(),
		Breakdown:   breakdown,
		Savings:     savings,
		Suggestions: suggestions,
	}
}

// --- Routing endpoint ---

type routingPayload struct {
	Timestamp         time.Time                    `json:"timestamp"`
	ModelDistribution map[string]*modelRoutingStat `json:"model_distribution,omitempty"`
	TotalRequests     int64                        `json:"total_requests"`
	TotalErrors       int64                        `json:"total_errors"`
	ErrorRate         float64                      `json:"error_rate"`
	AvgLatencyMs      float64                      `json:"avg_latency_ms"`
	PatternCount      int                          `json:"pattern_count"`
	ClusterCount      int                          `json:"cluster_count"`
	Clusters          []PatternClusterInfo         `json:"clusters,omitempty"`
}

type modelRoutingStat struct {
	Model        string  `json:"model"`
	Requests     int64   `json:"requests"`
	RequestShare float64 `json:"request_share"`
	ErrorCount   int64   `json:"error_count"`
	ErrorRate    float64 `json:"error_rate"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
	TotalCost    float64 `json:"total_cost"`
}

func (dc *DashboardCollector) handleRouting(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	dc.mu.RLock()
	ut := dc.usageTracker
	pa := dc.patternAnalyzer
	dc.mu.RUnlock()

	payload := dc.buildRoutingPayload(pa, ut)
	if payload == nil {
		payload = &routingPayload{Timestamp: time.Now()}
	}

	writeJSON(w, http.StatusOK, payload)
}

func (dc *DashboardCollector) buildRoutingPayload(pa PatternAnalyzerSource, ut UsageTrackerSource) *routingPayload {
	payload := &routingPayload{
		Timestamp:         time.Now(),
		ModelDistribution: make(map[string]*modelRoutingStat),
	}

	if ut != nil {
		usageSnap := ut.GetSnapshot()
		var totalRequests, totalErrors int64
		var totalLatencyWeighted float64

		for name, mr := range usageSnap.Models {
			totalRequests += mr.TotalRequests
			totalErrors += mr.ErrorCount
			totalLatencyWeighted += mr.AvgLatencyMs * float64(mr.TotalRequests)

			stat := &modelRoutingStat{
				Model:        name,
				Requests:     mr.TotalRequests,
				ErrorCount:   mr.ErrorCount,
				AvgLatencyMs: mr.AvgLatencyMs,
				TotalCost:    mr.TotalCost,
			}
			if mr.TotalRequests > 0 {
				stat.ErrorRate = float64(mr.ErrorCount) / float64(mr.TotalRequests)
			}
			payload.ModelDistribution[name] = stat
		}

		payload.TotalRequests = totalRequests
		payload.TotalErrors = totalErrors
		if totalRequests > 0 {
			payload.ErrorRate = float64(totalErrors) / float64(totalRequests)
			payload.AvgLatencyMs = totalLatencyWeighted / float64(totalRequests)
		}

		// Calculate request share
		for _, stat := range payload.ModelDistribution {
			if totalRequests > 0 {
				stat.RequestShare = float64(stat.Requests) / float64(totalRequests)
			}
		}
	}

	if pa != nil {
		payload.PatternCount = pa.PatternCount()
		payload.ClusterCount = pa.ClusterCount()
		payload.Clusters = pa.GetClusters()
	}

	return payload
}

// --- Helper ---

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v) //nolint:errcheck
}
