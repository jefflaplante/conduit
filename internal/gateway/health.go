package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"conduit/internal/monitoring"
	"conduit/internal/version"
)

// HealthResponse represents the basic health check response
type HealthResponse struct {
	Status    string    `json:"status"`
	Timestamp time.Time `json:"timestamp"`
	Version   string    `json:"version,omitempty"`
	Uptime    string    `json:"uptime"`
}

// MetricsResponse represents the detailed metrics response
type MetricsResponse struct {
	*monitoring.MetricsSnapshot
	Database struct {
		Connected bool   `json:"connected"`
		Error     string `json:"error,omitempty"`
	} `json:"database"`
	SystemHealth struct {
		MemoryPressure bool `json:"memory_pressure"`
		HighLoad       bool `json:"high_load"`
		QueueBacklog   bool `json:"queue_backlog"`
	} `json:"system_health"`
	Heartbeat struct {
		TotalJobs     int       `json:"total_jobs"`
		EnabledJobs   int       `json:"enabled_jobs"`
		LastExecution time.Time `json:"last_execution"`
		SuccessCount  int64     `json:"success_count"`
		ErrorCount    int64     `json:"error_count"`
		IsHealthy     bool      `json:"is_healthy"`
	} `json:"heartbeat"`
	LastActivity time.Time `json:"last_activity"`
	IsIdle       bool      `json:"is_idle"`
}

// DiagnosticEvent represents a real-time diagnostic event for the diagnostics endpoint
type DiagnosticEvent struct {
	Event     *monitoring.HeartbeatEvent `json:"event"`
	StreamID  string                     `json:"stream_id"`
	Timestamp time.Time                  `json:"timestamp"`
}

// Enhanced health check endpoint with more detailed information
func (g *Gateway) handleHealthEnhanced(w http.ResponseWriter, r *http.Request) {
	// Always update system metrics first
	if g.gatewayMetrics != nil {
		g.gatewayMetrics.UpdateSystemMetrics()
	}

	uptime := "unknown"
	versionInfo := version.Info()
	status := "healthy"

	if g.gatewayMetrics != nil {
		uptime = g.gatewayMetrics.GetUptime().String()
		if g.gatewayMetrics.Version != "" {
			versionInfo = g.gatewayMetrics.Version
		}
		if !g.gatewayMetrics.IsHealthy() {
			status = "degraded"
		}
	}

	response := HealthResponse{
		Status:    status,
		Timestamp: time.Now(),
		Version:   versionInfo,
		Uptime:    uptime,
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

	if status != "healthy" {
		w.WriteHeader(http.StatusServiceUnavailable)
	} else {
		w.WriteHeader(http.StatusOK)
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("[Health] Failed to encode response: %v", err)
	}
}

// Detailed metrics endpoint
func (g *Gateway) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Collect current metrics
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if g.metricsCollector != nil {
		// Use the collector to gather fresh metrics
		if _, err := g.metricsCollector.CollectMetrics(ctx); err != nil {
			log.Printf("[Metrics] Error collecting metrics: %v", err)
		}
	}

	// Get the current metrics snapshot
	var metrics *monitoring.MetricsSnapshot
	if g.gatewayMetrics != nil {
		snapshot := g.gatewayMetrics.Snapshot()
		metrics = &snapshot
	} else {
		// Fallback to basic metrics
		fallback := monitoring.NewGatewayMetrics()
		fallback.UpdateSystemMetrics()
		snapshot := fallback.Snapshot()
		metrics = &snapshot
	}

	// Build enhanced response
	response := MetricsResponse{
		MetricsSnapshot: metrics,
		SystemHealth: struct {
			MemoryPressure bool `json:"memory_pressure"`
			HighLoad       bool `json:"high_load"`
			QueueBacklog   bool `json:"queue_backlog"`
		}{
			MemoryPressure: metrics.MemoryUsageMB > 512,   // Alert if >512MB
			HighLoad:       metrics.GoroutineCount > 1000, // Alert if >1000 goroutines
			QueueBacklog:   metrics.QueueDepth > 100,      // Alert if queue depth >100
		},
	}

	// Check database health
	response.Database.Connected = true
	if g.metricsCollector != nil {
		if err := g.metricsCollector.ValidateDatabase(ctx); err != nil {
			response.Database.Connected = false
			response.Database.Error = err.Error()
		}
	}

	// Get heartbeat metrics
	if g.metricsCollector != nil {
		heartbeatMetrics := g.metricsCollector.GetHeartbeatMetrics()
		response.Heartbeat.TotalJobs = heartbeatMetrics.TotalJobs
		response.Heartbeat.EnabledJobs = heartbeatMetrics.EnabledJobs
		response.Heartbeat.LastExecution = heartbeatMetrics.LastExecution
		response.Heartbeat.SuccessCount = heartbeatMetrics.SuccessCount
		response.Heartbeat.ErrorCount = heartbeatMetrics.ErrorCount
		response.Heartbeat.IsHealthy = heartbeatMetrics.IsHealthy
	}

	// Get last activity information
	if g.metricsCollector != nil {
		response.LastActivity = g.metricsCollector.GetLastActivityTime()
		response.IsIdle = g.metricsCollector.IsIdle(5 * time.Minute)
	} else {
		response.LastActivity = time.Now()
		response.IsIdle = false
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("[Metrics] Failed to encode response: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// Diagnostics endpoint for real-time events
func (g *Gateway) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse query parameters for filtering
	queryValues := r.URL.Query()

	filter := monitoring.EventFilter{}

	// Type filter
	if eventType := queryValues.Get("type"); eventType != "" {
		filter.Type = monitoring.HeartbeatEventType(eventType)
	}

	// Severity filter
	if severity := queryValues.Get("severity"); severity != "" {
		filter.Severity = monitoring.HeartbeatEventSeverity(severity)
	}

	// Source filter
	if source := queryValues.Get("source"); source != "" {
		filter.Source = source
	}

	// Time filters
	if sinceStr := queryValues.Get("since"); sinceStr != "" {
		if sinceTime, err := time.Parse(time.RFC3339, sinceStr); err == nil {
			filter.Since = &sinceTime
		}
	}

	if untilStr := queryValues.Get("until"); untilStr != "" {
		if untilTime, err := time.Parse(time.RFC3339, untilStr); err == nil {
			filter.Until = &untilTime
		}
	}

	// Limit filter
	if limitStr := queryValues.Get("limit"); limitStr != "" {
		if limit, err := strconv.Atoi(limitStr); err == nil && limit > 0 {
			filter.MaxResults = limit
		}
	}

	// Default limit
	if filter.MaxResults == 0 {
		filter.MaxResults = 100
	}

	// Query events from the event store
	var events []*monitoring.HeartbeatEvent
	var err error

	if g.eventStore != nil {
		events, err = g.eventStore.Query(filter)
		if err != nil {
			log.Printf("[Diagnostics] Error querying events: %v", err)
			http.Error(w, "Error querying events", http.StatusInternalServerError)
			return
		}
	} else {
		events = []*monitoring.HeartbeatEvent{}
	}

	// Build response
	response := struct {
		Events     []*monitoring.HeartbeatEvent `json:"events"`
		Count      int                          `json:"count"`
		Filter     monitoring.EventFilter       `json:"filter"`
		Timestamp  time.Time                    `json:"timestamp"`
		SystemInfo map[string]interface{}       `json:"system_info"`
	}{
		Events:    events,
		Count:     len(events),
		Filter:    filter,
		Timestamp: time.Now(),
		SystemInfo: map[string]interface{}{
			"gateway_version": version.Info(),
			"store_type":      "memory",
		},
	}

	// Add current system state to response
	if g.gatewayMetrics != nil {
		response.SystemInfo["status"] = g.gatewayMetrics.Status
		response.SystemInfo["uptime_seconds"] = g.gatewayMetrics.UptimeSeconds
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("[Diagnostics] Failed to encode response: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handlePrometheusMetrics provides Prometheus-compatible metrics (optional)
func (g *Gateway) handlePrometheusMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Collect current metrics
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if g.metricsCollector != nil {
		if _, err := g.metricsCollector.CollectMetrics(ctx); err != nil {
			log.Printf("[Prometheus] Error collecting metrics: %v", err)
		}
	}

	var metrics *monitoring.MetricsSnapshot
	if g.gatewayMetrics != nil {
		snapshot := g.gatewayMetrics.Snapshot()
		metrics = &snapshot
	} else {
		fallback := monitoring.NewGatewayMetrics()
		fallback.UpdateSystemMetrics()
		snapshot := fallback.Snapshot()
		metrics = &snapshot
	}

	// Generate Prometheus format
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.WriteHeader(http.StatusOK)

	// Write Prometheus metrics
	fmt.Fprintf(w, "# HELP conduit_uptime_seconds Total uptime in seconds\n")
	fmt.Fprintf(w, "# TYPE conduit_uptime_seconds counter\n")
	fmt.Fprintf(w, "conduit_uptime_seconds %d\n", metrics.UptimeSeconds)

	fmt.Fprintf(w, "# HELP conduit_memory_usage_bytes Memory usage in bytes\n")
	fmt.Fprintf(w, "# TYPE conduit_memory_usage_bytes gauge\n")
	fmt.Fprintf(w, "conduit_memory_usage_bytes %d\n", metrics.MemoryUsageBytes)

	fmt.Fprintf(w, "# HELP conduit_sessions_active Number of active sessions\n")
	fmt.Fprintf(w, "# TYPE conduit_sessions_active gauge\n")
	fmt.Fprintf(w, "conduit_sessions_active %d\n", metrics.ActiveSessions)

	fmt.Fprintf(w, "# HELP conduit_sessions_total Total number of sessions\n")
	fmt.Fprintf(w, "# TYPE conduit_sessions_total counter\n")
	fmt.Fprintf(w, "conduit_sessions_total %d\n", metrics.TotalSessions)

	fmt.Fprintf(w, "# HELP conduit_requests_completed_total Total completed requests\n")
	fmt.Fprintf(w, "# TYPE conduit_requests_completed_total counter\n")
	fmt.Fprintf(w, "conduit_requests_completed_total %d\n", metrics.CompletedRequests)

	fmt.Fprintf(w, "# HELP conduit_requests_failed_total Total failed requests\n")
	fmt.Fprintf(w, "# TYPE conduit_requests_failed_total counter\n")
	fmt.Fprintf(w, "conduit_requests_failed_total %d\n", metrics.FailedRequests)

	fmt.Fprintf(w, "# HELP conduit_goroutines Number of goroutines\n")
	fmt.Fprintf(w, "# TYPE conduit_goroutines gauge\n")
	fmt.Fprintf(w, "conduit_goroutines %d\n", metrics.GoroutineCount)

	fmt.Fprintf(w, "# HELP conduit_websocket_connections Number of WebSocket connections\n")
	fmt.Fprintf(w, "# TYPE conduit_websocket_connections gauge\n")
	fmt.Fprintf(w, "conduit_websocket_connections %d\n", metrics.WebhookConnections)

	fmt.Fprintf(w, "# HELP conduit_queue_depth Current queue depth\n")
	fmt.Fprintf(w, "# TYPE conduit_queue_depth gauge\n")
	fmt.Fprintf(w, "conduit_queue_depth %d\n", metrics.QueueDepth)

	// Add status as a labeled metric
	statusValue := 1
	if metrics.Status != "healthy" {
		statusValue = 0
	}
	fmt.Fprintf(w, "# HELP conduit_status Gateway status (1=healthy, 0=unhealthy)\n")
	fmt.Fprintf(w, "# TYPE conduit_status gauge\n")
	fmt.Fprintf(w, "conduit_status{status=\"%s\"} %d\n", metrics.Status, statusValue)
}
