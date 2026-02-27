package monitoring

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// --- Mock sources ---

type mockUsageTracker struct {
	snapshot  UsageSnapshot
	totalCost float64
}

func (m *mockUsageTracker) GetSnapshot() UsageSnapshot { return m.snapshot }
func (m *mockUsageTracker) TotalCost() float64         { return m.totalCost }

type mockCostOptimizer struct {
	breakdown   *CostBreakdown
	savings     *SavingsEstimate
	suggestions []CostSuggestion
}

func (m *mockCostOptimizer) GetBreakdown(period time.Duration) *CostBreakdown {
	return m.breakdown
}
func (m *mockCostOptimizer) GetSavingsEstimate() *SavingsEstimate {
	return m.savings
}
func (m *mockCostOptimizer) GetOptimizationSuggestions() []CostSuggestion {
	return m.suggestions
}

type mockUsagePredictor struct {
	forecast      *UsageForecast
	trend         string
	snapshotCount int
}

func (m *mockUsagePredictor) PredictUsage(horizon time.Duration) *UsageForecast {
	return m.forecast
}
func (m *mockUsagePredictor) GetTrend() string   { return m.trend }
func (m *mockUsagePredictor) SnapshotCount() int { return m.snapshotCount }

type mockPatternAnalyzer struct {
	patternCount int
	clusterCount int
	clusters     []PatternClusterInfo
}

func (m *mockPatternAnalyzer) PatternCount() int                 { return m.patternCount }
func (m *mockPatternAnalyzer) ClusterCount() int                 { return m.clusterCount }
func (m *mockPatternAnalyzer) GetClusters() []PatternClusterInfo { return m.clusters }

// --- Tests ---

func TestNewDashboardCollector(t *testing.T) {
	dc := NewDashboardCollector()
	if dc == nil {
		t.Fatal("expected non-nil DashboardCollector")
	}
	if dc.startTime.IsZero() {
		t.Error("expected startTime to be set")
	}
}

func TestDashboardCollectorSetters(t *testing.T) {
	dc := NewDashboardCollector()

	gm := NewGatewayMetrics()
	dc.SetGatewayMetrics(gm)
	dc.mu.RLock()
	if dc.gatewayMetrics != gm {
		t.Error("SetGatewayMetrics did not set the field")
	}
	dc.mu.RUnlock()

	ut := &mockUsageTracker{}
	dc.SetUsageTracker(ut)
	dc.mu.RLock()
	if dc.usageTracker != ut {
		t.Error("SetUsageTracker did not set the field")
	}
	dc.mu.RUnlock()

	co := &mockCostOptimizer{}
	dc.SetCostOptimizer(co)
	dc.mu.RLock()
	if dc.costOptimizer != co {
		t.Error("SetCostOptimizer did not set the field")
	}
	dc.mu.RUnlock()

	up := &mockUsagePredictor{}
	dc.SetUsagePredictor(up)
	dc.mu.RLock()
	if dc.usagePredictor != up {
		t.Error("SetUsagePredictor did not set the field")
	}
	dc.mu.RUnlock()

	pa := &mockPatternAnalyzer{}
	dc.SetPatternAnalyzer(pa)
	dc.mu.RLock()
	if dc.patternAnalyzer != pa {
		t.Error("SetPatternAnalyzer did not set the field")
	}
	dc.mu.RUnlock()
}

func TestPrometheusEndpointEmpty(t *testing.T) {
	dc := NewDashboardCollector()
	handler := dc.Handler()

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("expected text/plain content type, got %q", ct)
	}

	body, _ := io.ReadAll(resp.Body)
	text := string(body)

	// Should always have system metrics even with no sources
	if !strings.Contains(text, "conduit_uptime_seconds") {
		t.Error("expected conduit_uptime_seconds in prometheus output")
	}
	if !strings.Contains(text, "conduit_goroutines") {
		t.Error("expected conduit_goroutines in prometheus output")
	}
	if !strings.Contains(text, "conduit_memory_bytes") {
		t.Error("expected conduit_memory_bytes in prometheus output")
	}
}

func TestPrometheusEndpointWithSources(t *testing.T) {
	dc := NewDashboardCollector()

	gm := NewGatewayMetrics()
	gm.UpdateSessionCount(3, 1, 1, 1, 5)
	gm.IncrementCompleted()
	gm.IncrementCompleted()
	gm.IncrementFailed()
	dc.SetGatewayMetrics(gm)

	ut := &mockUsageTracker{
		totalCost: 1.23,
		snapshot: UsageSnapshot{
			Models: map[string]*ModelUsageRecord{
				"claude-sonnet-4": {
					Model:             "claude-sonnet-4",
					TotalRequests:     10,
					TotalInputTokens:  5000,
					TotalOutputTokens: 2000,
					TotalCost:         0.045,
					AvgLatencyMs:      350,
					ErrorCount:        1,
				},
			},
		},
	}
	dc.SetUsageTracker(ut)

	handler := dc.Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body, _ := io.ReadAll(rec.Result().Body)
	text := string(body)

	// Gateway metrics
	if !strings.Contains(text, "conduit_sessions_active 3") {
		t.Error("expected active sessions in prometheus output")
	}
	if !strings.Contains(text, "conduit_requests_completed_total 2") {
		t.Error("expected completed requests in prometheus output")
	}
	if !strings.Contains(text, "conduit_requests_failed_total 1") {
		t.Error("expected failed requests in prometheus output")
	}

	// AI metrics
	if !strings.Contains(text, "conduit_ai_cost_total_usd 1.230000") {
		t.Error("expected total cost in prometheus output")
	}
	if !strings.Contains(text, `conduit_ai_requests_total{model="claude-sonnet-4"} 10`) {
		t.Error("expected per-model requests in prometheus output")
	}
	if !strings.Contains(text, `conduit_ai_errors_total{model="claude-sonnet-4"} 1`) {
		t.Error("expected per-model errors in prometheus output")
	}
}

func TestPrometheusRejectsNonGET(t *testing.T) {
	dc := NewDashboardCollector()
	handler := dc.Handler()

	req := httptest.NewRequest(http.MethodPost, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Result().StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for POST, got %d", rec.Result().StatusCode)
	}
}

func TestJSONEndpointEmpty(t *testing.T) {
	dc := NewDashboardCollector()
	handler := dc.Handler()

	req := httptest.NewRequest(http.MethodGet, "/metrics/json", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected application/json, got %q", ct)
	}

	var payload fullMetricsPayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("failed to decode JSON: %v", err)
	}

	if payload.Uptime < 0 {
		t.Error("expected non-negative uptime")
	}
	if payload.System.GoroutineCount == 0 {
		t.Error("expected goroutine count > 0")
	}
	if payload.Gateway != nil {
		t.Error("expected nil gateway when no source set")
	}
	if payload.Usage != nil {
		t.Error("expected nil usage when no source set")
	}
}

func TestJSONEndpointWithSources(t *testing.T) {
	dc := NewDashboardCollector()

	gm := NewGatewayMetrics()
	gm.UpdateSessionCount(2, 1, 0, 1, 4)
	dc.SetGatewayMetrics(gm)

	ut := &mockUsageTracker{
		totalCost: 0.5,
		snapshot: UsageSnapshot{
			Providers: map[string]*ProviderUsageRecord{
				"anthropic": {Provider: "anthropic", TotalRequests: 5},
			},
			Models: map[string]*ModelUsageRecord{
				"claude-haiku-4": {Model: "claude-haiku-4", TotalRequests: 5},
			},
			Since:    time.Now().Add(-1 * time.Hour),
			Snapshot: time.Now(),
		},
	}
	dc.SetUsageTracker(ut)

	handler := dc.Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics/json", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var payload fullMetricsPayload
	if err := json.NewDecoder(rec.Result().Body).Decode(&payload); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}

	if payload.Gateway == nil {
		t.Fatal("expected non-nil gateway")
	}
	if payload.Gateway.ActiveSessions != 2 {
		t.Errorf("expected 2 active sessions, got %d", payload.Gateway.ActiveSessions)
	}

	if payload.Usage == nil {
		t.Fatal("expected non-nil usage")
	}
	if len(payload.Usage.Models) != 1 {
		t.Errorf("expected 1 model, got %d", len(payload.Usage.Models))
	}
}

func TestHealthEndpoint(t *testing.T) {
	dc := NewDashboardCollector()

	gm := NewGatewayMetrics()
	gm.SetStatus("healthy")
	gm.UpdateSessionCount(1, 0, 0, 1, 2)
	dc.SetGatewayMetrics(gm)

	handler := dc.Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var payload healthPayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if payload.Status != "healthy" {
		t.Errorf("expected healthy status, got %q", payload.Status)
	}
	if payload.UptimeSeconds < 0 {
		t.Error("expected non-negative uptime")
	}
	if payload.Sessions == nil {
		t.Fatal("expected sessions info")
	}
	if payload.Sessions.Total != 2 {
		t.Errorf("expected 2 total sessions, got %d", payload.Sessions.Total)
	}
}

func TestHealthEndpointDegraded(t *testing.T) {
	dc := NewDashboardCollector()

	// High error rate should degrade status
	ut := &mockUsageTracker{
		snapshot: UsageSnapshot{
			Models: map[string]*ModelUsageRecord{
				"model-a": {
					TotalRequests: 10,
					ErrorCount:    8, // 80% error rate
				},
			},
		},
	}
	dc.SetUsageTracker(ut)

	handler := dc.Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var payload healthPayload
	json.NewDecoder(rec.Result().Body).Decode(&payload)

	if payload.Status != "degraded" {
		t.Errorf("expected degraded status with 80%% error rate, got %q", payload.Status)
	}
	if payload.ErrorRate == nil || *payload.ErrorRate < 0.5 {
		t.Error("expected error rate > 0.5")
	}
}

func TestHealthEndpointNoSources(t *testing.T) {
	dc := NewDashboardCollector()

	handler := dc.Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var payload healthPayload
	json.NewDecoder(rec.Result().Body).Decode(&payload)

	if payload.Status != "healthy" {
		t.Errorf("expected healthy when no sources, got %q", payload.Status)
	}
	if payload.Sessions != nil {
		t.Error("expected nil sessions when no gateway metrics")
	}
}

func TestUsageEndpoint(t *testing.T) {
	dc := NewDashboardCollector()

	now := time.Now()
	ut := &mockUsageTracker{
		totalCost: 2.5,
		snapshot: UsageSnapshot{
			Providers: map[string]*ProviderUsageRecord{
				"anthropic": {Provider: "anthropic", TotalRequests: 20, TotalCost: 2.5},
			},
			Models: map[string]*ModelUsageRecord{
				"claude-sonnet-4": {Model: "claude-sonnet-4", TotalRequests: 15, TotalCost: 2.0},
				"claude-haiku-4":  {Model: "claude-haiku-4", TotalRequests: 5, TotalCost: 0.5},
			},
			Since:    now.Add(-2 * time.Hour),
			Snapshot: now,
		},
	}
	dc.SetUsageTracker(ut)

	up := &mockUsagePredictor{
		forecast: &UsageForecast{
			Horizon:           24 * time.Hour,
			PredictedTokens:   100000,
			PredictedCost:     5.0,
			PredictedRequests: 50,
			Confidence:        0.8,
			Trend:             "increasing",
		},
		trend:         "increasing",
		snapshotCount: 10,
	}
	dc.SetUsagePredictor(up)

	handler := dc.Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics/usage", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var payload usagePayload
	if err := json.NewDecoder(rec.Result().Body).Decode(&payload); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if payload.TotalCost != 2.5 {
		t.Errorf("expected total cost 2.5, got %f", payload.TotalCost)
	}
	if len(payload.Models) != 2 {
		t.Errorf("expected 2 models, got %d", len(payload.Models))
	}
	if payload.Trend != "increasing" {
		t.Errorf("expected trend 'increasing', got %q", payload.Trend)
	}
	if payload.Forecast == nil {
		t.Fatal("expected non-nil forecast")
	}
	if payload.Forecast.PredictedCost != 5.0 {
		t.Errorf("expected predicted cost 5.0, got %f", payload.Forecast.PredictedCost)
	}
}

func TestUsageEndpointNoSources(t *testing.T) {
	dc := NewDashboardCollector()

	handler := dc.Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics/usage", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var payload usagePayload
	json.NewDecoder(rec.Result().Body).Decode(&payload)

	if payload.TotalCost != 0 {
		t.Errorf("expected 0 cost with no sources, got %f", payload.TotalCost)
	}
}

func TestCostsEndpoint(t *testing.T) {
	dc := NewDashboardCollector()

	co := &mockCostOptimizer{
		breakdown: &CostBreakdown{
			Period:        24 * time.Hour,
			TotalCost:     3.0,
			TotalRequests: 30,
			ByModel: map[string]*ModelCostEntry{
				"claude-sonnet-4": {Model: "claude-sonnet-4", TotalCost: 2.5, RequestCount: 25},
			},
			ByTier: map[string]*TierCostEntry{
				"sonnet": {Tier: "sonnet", TotalCost: 2.5, RequestCount: 25},
			},
		},
		savings: &SavingsEstimate{
			CurrentSpend:     3.0,
			OptimalSpend:     1.5,
			PotentialSavings: 1.5,
			SavingsPct:       50.0,
		},
		suggestions: []CostSuggestion{
			{
				Description:      "Route simple requests to haiku",
				EstimatedSavings: 1.0,
				AffectedPct:      40.0,
				Confidence:       0.8,
			},
		},
	}
	dc.SetCostOptimizer(co)

	handler := dc.Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics/costs", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var payload costsPayload
	if err := json.NewDecoder(rec.Result().Body).Decode(&payload); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if payload.Breakdown == nil {
		t.Fatal("expected non-nil breakdown")
	}
	if payload.Breakdown.TotalCost != 3.0 {
		t.Errorf("expected total cost 3.0, got %f", payload.Breakdown.TotalCost)
	}
	if payload.Savings == nil {
		t.Fatal("expected non-nil savings")
	}
	if payload.Savings.SavingsPct != 50.0 {
		t.Errorf("expected 50%% savings, got %f", payload.Savings.SavingsPct)
	}
	if len(payload.Suggestions) != 1 {
		t.Errorf("expected 1 suggestion, got %d", len(payload.Suggestions))
	}
}

func TestCostsEndpointNoSources(t *testing.T) {
	dc := NewDashboardCollector()

	handler := dc.Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics/costs", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var payload costsPayload
	json.NewDecoder(rec.Result().Body).Decode(&payload)

	if payload.Breakdown != nil {
		t.Error("expected nil breakdown with no sources")
	}
}

func TestRoutingEndpoint(t *testing.T) {
	dc := NewDashboardCollector()

	ut := &mockUsageTracker{
		snapshot: UsageSnapshot{
			Models: map[string]*ModelUsageRecord{
				"claude-sonnet-4": {
					Model:         "claude-sonnet-4",
					TotalRequests: 60,
					ErrorCount:    2,
					AvgLatencyMs:  400,
					TotalCost:     1.8,
				},
				"claude-haiku-4": {
					Model:         "claude-haiku-4",
					TotalRequests: 40,
					ErrorCount:    1,
					AvgLatencyMs:  150,
					TotalCost:     0.2,
				},
			},
		},
	}
	dc.SetUsageTracker(ut)

	pa := &mockPatternAnalyzer{
		patternCount: 100,
		clusterCount: 3,
		clusters: []PatternClusterInfo{
			{
				ID:             "cluster-0",
				Description:    "simple requests routed to haiku",
				MemberCount:    40,
				DominantModel:  "claude-haiku-4",
				AvgSuccessRate: 0.975,
				AvgLatencyMs:   150,
				AvgComplexity:  10,
			},
		},
	}
	dc.SetPatternAnalyzer(pa)

	handler := dc.Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics/routing", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var payload routingPayload
	if err := json.NewDecoder(rec.Result().Body).Decode(&payload); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if payload.TotalRequests != 100 {
		t.Errorf("expected 100 total requests, got %d", payload.TotalRequests)
	}
	if payload.TotalErrors != 3 {
		t.Errorf("expected 3 total errors, got %d", payload.TotalErrors)
	}
	if payload.ErrorRate == 0 {
		t.Error("expected non-zero error rate")
	}

	sonnet, ok := payload.ModelDistribution["claude-sonnet-4"]
	if !ok {
		t.Fatal("expected claude-sonnet-4 in model distribution")
	}
	if sonnet.Requests != 60 {
		t.Errorf("expected 60 sonnet requests, got %d", sonnet.Requests)
	}
	if sonnet.RequestShare != 0.6 {
		t.Errorf("expected 0.6 request share, got %f", sonnet.RequestShare)
	}

	if payload.PatternCount != 100 {
		t.Errorf("expected 100 patterns, got %d", payload.PatternCount)
	}
	if payload.ClusterCount != 3 {
		t.Errorf("expected 3 clusters, got %d", payload.ClusterCount)
	}
	if len(payload.Clusters) != 1 {
		t.Errorf("expected 1 cluster info, got %d", len(payload.Clusters))
	}
}

func TestRoutingEndpointNoSources(t *testing.T) {
	dc := NewDashboardCollector()

	handler := dc.Handler()
	req := httptest.NewRequest(http.MethodGet, "/metrics/routing", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var payload routingPayload
	json.NewDecoder(rec.Result().Body).Decode(&payload)

	if payload.TotalRequests != 0 {
		t.Errorf("expected 0 requests with no sources, got %d", payload.TotalRequests)
	}
}

func TestAllEndpointsRejectNonGET(t *testing.T) {
	dc := NewDashboardCollector()
	handler := dc.Handler()

	endpoints := []string{
		"/metrics",
		"/metrics/json",
		"/metrics/health",
		"/metrics/usage",
		"/metrics/costs",
		"/metrics/routing",
	}

	for _, endpoint := range endpoints {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			req := httptest.NewRequest(method, endpoint, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Result().StatusCode != http.StatusMethodNotAllowed {
				t.Errorf("%s %s: expected 405, got %d", method, endpoint, rec.Result().StatusCode)
			}
		}
	}
}

func TestGracefulDegradationPartialSources(t *testing.T) {
	// Only gateway metrics set, everything else nil
	dc := NewDashboardCollector()
	gm := NewGatewayMetrics()
	gm.UpdateSessionCount(1, 1, 0, 0, 1)
	dc.SetGatewayMetrics(gm)

	handler := dc.Handler()

	// /metrics/json should work with partial data
	req := httptest.NewRequest(http.MethodGet, "/metrics/json", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Result().StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with partial sources, got %d", rec.Result().StatusCode)
	}

	var payload fullMetricsPayload
	if err := json.NewDecoder(rec.Result().Body).Decode(&payload); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if payload.Gateway == nil {
		t.Error("expected gateway data to be present")
	}
	if payload.Usage != nil {
		t.Error("expected nil usage with no tracker")
	}
	if payload.Costs != nil {
		t.Error("expected nil costs with no optimizer")
	}
	if payload.Routing != nil {
		t.Error("expected nil routing with no pattern analyzer")
	}
}

func TestConcurrentAccess(t *testing.T) {
	dc := NewDashboardCollector()
	gm := NewGatewayMetrics()
	dc.SetGatewayMetrics(gm)

	ut := &mockUsageTracker{
		totalCost: 1.0,
		snapshot: UsageSnapshot{
			Models: map[string]*ModelUsageRecord{
				"m": {Model: "m", TotalRequests: 5},
			},
		},
	}
	dc.SetUsageTracker(ut)

	handler := dc.Handler()
	done := make(chan bool, 10)

	// Hammer all endpoints concurrently
	endpoints := []string{
		"/metrics",
		"/metrics/json",
		"/metrics/health",
		"/metrics/usage",
		"/metrics/costs",
		"/metrics/routing",
	}

	for _, ep := range endpoints {
		go func(endpoint string) {
			for i := 0; i < 20; i++ {
				req := httptest.NewRequest(http.MethodGet, endpoint, nil)
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				if rec.Result().StatusCode != http.StatusOK {
					t.Errorf("%s returned %d", endpoint, rec.Result().StatusCode)
				}
			}
			done <- true
		}(ep)
	}

	// Also set sources concurrently
	go func() {
		for i := 0; i < 20; i++ {
			dc.SetGatewayMetrics(gm)
			dc.SetUsageTracker(ut)
		}
		done <- true
	}()

	for i := 0; i < len(endpoints)+1; i++ {
		<-done
	}
}

func TestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, map[string]string{"key": "value"})

	resp := rec.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if resp.Header.Get("Content-Type") != "application/json" {
		t.Errorf("expected application/json, got %q", resp.Header.Get("Content-Type"))
	}

	var data map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if data["key"] != "value" {
		t.Errorf("expected key=value, got %q", data["key"])
	}
}
