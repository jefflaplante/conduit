package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"conduit/internal/config"
	"conduit/internal/middleware"
	"conduit/internal/monitoring"
	"conduit/internal/sessions"
)

// createTestGateway creates a minimal gateway for health endpoint testing
func createTestGateway(t *testing.T) *Gateway {
	// Create a test config
	cfg := &config.Config{
		Database: config.DatabaseConfig{
			Path: ":memory:", // Use in-memory SQLite for tests
		},
		RateLimiting: config.RateLimitingConfig{
			Enabled: false, // Disable rate limiting for tests
		},
	}

	// Create session store
	sessionStore, err := sessions.NewStore(cfg.Database.Path)
	if err != nil {
		t.Fatalf("Failed to create session store: %v", err)
	}

	// Create gateway metrics
	gatewayMetrics := monitoring.NewGatewayMetrics()
	gatewayMetrics.SetVersion("0.2.0")

	// Create metrics collector
	metricsCollector := monitoring.NewMetricsCollector(monitoring.CollectorDependencies{
		SessionStore:   sessionStore,
		GatewayMetrics: gatewayMetrics,
	})

	// Create event store
	eventStore := monitoring.NewMemoryEventStore(100)

	// Create a minimal rate limit middleware (disabled)
	rateLimitMiddleware := middleware.NewRateLimitMiddleware(middleware.RateLimitMiddlewareConfig{
		Config: middleware.RateLimitConfig{
			Enabled: false,
		},
	})

	return &Gateway{
		config:              cfg,
		sessions:            sessionStore,
		gatewayMetrics:      gatewayMetrics,
		metricsCollector:    metricsCollector,
		eventStore:          eventStore,
		rateLimitMiddleware: rateLimitMiddleware,
	}
}

func TestHandleHealthEnhanced(t *testing.T) {
	gw := createTestGateway(t)

	tests := []struct {
		name          string
		method        string
		expectStatus  int
		expectHealthy bool
		setupGateway  func(*Gateway)
	}{
		{
			name:          "GET health - healthy status",
			method:        "GET",
			expectStatus:  http.StatusOK,
			expectHealthy: true,
			setupGateway: func(g *Gateway) {
				g.gatewayMetrics.SetStatus("healthy")
			},
		},
		{
			name:          "GET health - degraded status",
			method:        "GET",
			expectStatus:  http.StatusServiceUnavailable,
			expectHealthy: false,
			setupGateway: func(g *Gateway) {
				g.gatewayMetrics.SetStatus("degraded")
			},
		},
		{
			name:          "POST health - method not supported",
			method:        "POST",
			expectStatus:  http.StatusOK, // Our endpoint doesn't check method
			expectHealthy: true,          // Status is still healthy
			setupGateway: func(g *Gateway) {
				g.gatewayMetrics.SetStatus("healthy")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setupGateway != nil {
				tt.setupGateway(gw)
			}

			req, err := http.NewRequest(tt.method, "/health", nil)
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}

			rr := httptest.NewRecorder()
			handler := http.HandlerFunc(gw.handleHealthEnhanced)
			handler.ServeHTTP(rr, req)

			// Check status code
			if rr.Code != tt.expectStatus {
				t.Errorf("Expected status %d, got %d", tt.expectStatus, rr.Code)
			}

			// Check content type
			if contentType := rr.Header().Get("Content-Type"); contentType != "application/json" {
				t.Errorf("Expected Content-Type application/json, got %s", contentType)
			}

			// Parse and check response
			var response HealthResponse
			if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
				t.Fatalf("Failed to decode response: %v", err)
			}

			// Check response structure
			if response.Timestamp.IsZero() {
				t.Error("Expected timestamp to be set")
			}

			if response.Version == "" {
				t.Error("Expected version to be set")
			}

			if response.Uptime == "" {
				t.Error("Expected uptime to be set")
			}

			// Check health status
			isHealthy := response.Status == "healthy"
			if isHealthy != tt.expectHealthy {
				t.Errorf("Expected healthy=%v, got status=%s", tt.expectHealthy, response.Status)
			}
		})
	}
}

func TestHandleMetrics(t *testing.T) {
	gw := createTestGateway(t)

	// Create actual sessions in the store - some active (processing/waiting), some idle
	for i := 0; i < 5; i++ {
		sess, _ := gw.sessions.GetOrCreateSession(fmt.Sprintf("user%d", i), "test_channel")
		if i < 3 {
			// First 3 sessions are processing (active)
			gw.sessions.UpdateSessionState(sess.Key, sessions.SessionStateProcessing, nil)
		} else if i < 4 {
			// 1 session is waiting (active)
			gw.sessions.UpdateSessionState(sess.Key, sessions.SessionStateWaiting, nil)
		}
		// Last session stays idle (not active)
	}

	// Setup queue metrics (these aren't overwritten by collector)
	gw.metricsCollector.UpdateQueueDepth(3)
	gw.gatewayMetrics.IncrementCompleted()
	gw.gatewayMetrics.IncrementFailed()
	gw.metricsCollector.UpdateWebSocketConnections(4)

	tests := []struct {
		name         string
		method       string
		expectStatus int
	}{
		{
			name:         "GET metrics - success",
			method:       "GET",
			expectStatus: http.StatusOK,
		},
		{
			name:         "POST metrics - not allowed",
			method:       "POST",
			expectStatus: http.StatusMethodNotAllowed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(tt.method, "/metrics", nil)
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}

			rr := httptest.NewRecorder()
			handler := http.HandlerFunc(gw.handleMetrics)
			handler.ServeHTTP(rr, req)

			// Check status code
			if rr.Code != tt.expectStatus {
				t.Errorf("Expected status %d, got %d", tt.expectStatus, rr.Code)
			}

			if tt.expectStatus == http.StatusOK {
				// Check content type
				if contentType := rr.Header().Get("Content-Type"); contentType != "application/json" {
					t.Errorf("Expected Content-Type application/json, got %s", contentType)
				}

				// Parse and validate response
				var response MetricsResponse
				if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
					t.Fatalf("Failed to decode response: %v", err)
				}

				// Check that metrics are present
				if response.MetricsSnapshot == nil {
					t.Error("Expected GatewayMetrics to be present")
				}

				if response.MetricsSnapshot.ActiveSessions != 4 {
					t.Errorf("Expected 4 active sessions, got %d", response.MetricsSnapshot.ActiveSessions)
				}

				if response.MetricsSnapshot.QueueDepth != 3 {
					t.Errorf("Expected queue depth 3, got %d", response.MetricsSnapshot.QueueDepth)
				}

				if response.MetricsSnapshot.CompletedRequests != 1 {
					t.Errorf("Expected 1 completed request, got %d", response.MetricsSnapshot.CompletedRequests)
				}

				// Check database health
				if !response.Database.Connected {
					t.Error("Expected database to be connected")
				}

				// Check last activity
				if response.LastActivity.IsZero() {
					t.Error("Expected last activity to be set")
				}

				// Check system health flags exist
				// (We don't check specific values as they depend on runtime conditions)
			}
		})
	}
}

func TestHandleDiagnostics(t *testing.T) {
	gw := createTestGateway(t)

	// Add some test events
	event1 := monitoring.NewHeartbeatEvent(monitoring.EventTypeHeartbeat, monitoring.SeverityInfo, "Test heartbeat", "test")
	event2 := monitoring.NewHeartbeatEvent(monitoring.EventTypeStatusChange, monitoring.SeverityWarning, "Status changed", "test")
	event3 := monitoring.NewHeartbeatEvent(monitoring.EventTypeMetricAlert, monitoring.SeverityError, "High memory usage", "test")

	gw.eventStore.Store(event1)
	gw.eventStore.Store(event2)
	gw.eventStore.Store(event3)

	tests := []struct {
		name         string
		method       string
		url          string
		expectStatus int
		expectCount  int
	}{
		{
			name:         "GET all events",
			method:       "GET",
			url:          "/diagnostics",
			expectStatus: http.StatusOK,
			expectCount:  3,
		},
		{
			name:         "GET filtered by severity",
			method:       "GET",
			url:          "/diagnostics?severity=warning",
			expectStatus: http.StatusOK,
			expectCount:  1,
		},
		{
			name:         "GET filtered by type",
			method:       "GET",
			url:          "/diagnostics?type=heartbeat",
			expectStatus: http.StatusOK,
			expectCount:  1,
		},
		{
			name:         "GET with limit",
			method:       "GET",
			url:          "/diagnostics?limit=2",
			expectStatus: http.StatusOK,
			expectCount:  2,
		},
		{
			name:         "POST not allowed",
			method:       "POST",
			url:          "/diagnostics",
			expectStatus: http.StatusMethodNotAllowed,
			expectCount:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(tt.method, tt.url, nil)
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}

			rr := httptest.NewRecorder()
			handler := http.HandlerFunc(gw.handleDiagnostics)
			handler.ServeHTTP(rr, req)

			// Check status code
			if rr.Code != tt.expectStatus {
				t.Errorf("Expected status %d, got %d", tt.expectStatus, rr.Code)
			}

			if tt.expectStatus == http.StatusOK {
				// Check content type
				if contentType := rr.Header().Get("Content-Type"); contentType != "application/json" {
					t.Errorf("Expected Content-Type application/json, got %s", contentType)
				}

				// Parse response
				var response struct {
					Events     []*monitoring.HeartbeatEvent `json:"events"`
					Count      int                          `json:"count"`
					Filter     monitoring.EventFilter       `json:"filter"`
					Timestamp  time.Time                    `json:"timestamp"`
					SystemInfo map[string]interface{}       `json:"system_info"`
				}

				if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
					t.Fatalf("Failed to decode response: %v", err)
				}

				// Check event count
				if response.Count != tt.expectCount {
					t.Errorf("Expected %d events, got %d", tt.expectCount, response.Count)
				}

				if len(response.Events) != tt.expectCount {
					t.Errorf("Expected %d events in array, got %d", tt.expectCount, len(response.Events))
				}

				// Check system info
				if response.SystemInfo == nil {
					t.Error("Expected system info to be present")
				}

				if _, ok := response.SystemInfo["gateway_version"]; !ok {
					t.Error("Expected gateway_version to be present in system info")
				}
			}
		})
	}
}

func TestHandlePrometheusMetrics(t *testing.T) {
	gw := createTestGateway(t)

	// Create actual sessions - 5 processing + 2 waiting = 7 active, total 10
	for i := 0; i < 10; i++ {
		sess, _ := gw.sessions.GetOrCreateSession(fmt.Sprintf("prometheus_user%d", i), "test_channel")
		if i < 5 {
			// First 5 are processing
			gw.sessions.UpdateSessionState(sess.Key, sessions.SessionStateProcessing, nil)
		} else if i < 7 {
			// Next 2 are waiting
			gw.sessions.UpdateSessionState(sess.Key, sessions.SessionStateWaiting, nil)
		}
		// Last 3 stay idle
	}

	// Setup queue metrics
	gw.metricsCollector.UpdateQueueDepth(7)
	gw.gatewayMetrics.IncrementCompleted()
	gw.gatewayMetrics.IncrementCompleted()
	gw.gatewayMetrics.IncrementFailed()

	tests := []struct {
		name         string
		method       string
		expectStatus int
		checkContent bool
	}{
		{
			name:         "GET prometheus metrics - success",
			method:       "GET",
			expectStatus: http.StatusOK,
			checkContent: true,
		},
		{
			name:         "POST prometheus - not allowed",
			method:       "POST",
			expectStatus: http.StatusMethodNotAllowed,
			checkContent: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(tt.method, "/prometheus", nil)
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}

			rr := httptest.NewRecorder()
			handler := http.HandlerFunc(gw.handlePrometheusMetrics)
			handler.ServeHTTP(rr, req)

			// Check status code
			if rr.Code != tt.expectStatus {
				t.Errorf("Expected status %d, got %d", tt.expectStatus, rr.Code)
			}

			if tt.checkContent && tt.expectStatus == http.StatusOK {
				// Check content type
				if contentType := rr.Header().Get("Content-Type"); !strings.Contains(contentType, "text/plain") {
					t.Errorf("Expected Content-Type text/plain, got %s", contentType)
				}

				body := rr.Body.String()

				// Check that Prometheus format metrics are present
				requiredMetrics := []string{
					"conduit_uptime_seconds",
					"conduit_memory_usage_bytes",
					"conduit_sessions_active",
					"conduit_sessions_total",
					"conduit_requests_completed_total",
					"conduit_requests_failed_total",
					"conduit_goroutines",
					"conduit_websocket_connections",
					"conduit_queue_depth",
					"conduit_status",
				}

				for _, metric := range requiredMetrics {
					if !strings.Contains(body, metric) {
						t.Errorf("Expected metric %s to be present in response", metric)
					}
				}

				// Check that HELP and TYPE comments are present
				if !strings.Contains(body, "# HELP") {
					t.Error("Expected HELP comments in Prometheus format")
				}

				if !strings.Contains(body, "# TYPE") {
					t.Error("Expected TYPE comments in Prometheus format")
				}

				// Check specific values from our test data (7 active = 5 processing + 2 waiting)
				if !strings.Contains(body, "conduit_sessions_active 7") {
					t.Error("Expected active sessions count to match test data")
				}

				if !strings.Contains(body, "conduit_queue_depth 7") {
					t.Error("Expected queue depth to match test data")
				}

				if !strings.Contains(body, "conduit_requests_completed_total 2") {
					t.Error("Expected completed requests to match test data")
				}
			}
		})
	}
}

func TestRateLimitingOnHealthEndpoints(t *testing.T) {
	// This test verifies that rate limiting is properly applied to health endpoints
	cfg := &config.Config{
		Database: config.DatabaseConfig{
			Path: ":memory:",
		},
		RateLimiting: config.RateLimitingConfig{
			Enabled: true,
			Anonymous: struct {
				WindowSeconds int `json:"windowSeconds"`
				MaxRequests   int `json:"maxRequests"`
			}{
				WindowSeconds: 60,
				MaxRequests:   2, // Very low limit for testing
			},
		},
	}

	sessionStore, err := sessions.NewStore(cfg.Database.Path)
	if err != nil {
		t.Fatalf("Failed to create session store: %v", err)
	}

	rateLimitMiddleware := middleware.NewRateLimitMiddleware(middleware.RateLimitMiddlewareConfig{
		Config: middleware.RateLimitConfig{
			Enabled: true,
			Anonymous: struct {
				WindowSeconds int `json:"windowSeconds"`
				MaxRequests   int `json:"maxRequests"`
			}{
				WindowSeconds: 60,
				MaxRequests:   2, // Low limit for testing
			},
			Authenticated: struct {
				WindowSeconds int `json:"windowSeconds"`
				MaxRequests   int `json:"maxRequests"`
			}{
				WindowSeconds: 60,
				MaxRequests:   10, // Higher limit for authenticated
			},
		},
	})

	gw := &Gateway{
		config:         cfg,
		sessions:       sessionStore,
		gatewayMetrics: monitoring.NewGatewayMetrics(),
		metricsCollector: monitoring.NewMetricsCollector(monitoring.CollectorDependencies{
			SessionStore:   sessionStore,
			GatewayMetrics: monitoring.NewGatewayMetrics(),
		}),
		eventStore:          monitoring.NewMemoryEventStore(100),
		rateLimitMiddleware: rateLimitMiddleware,
	}

	// Create handler with rate limiting
	handler := gw.rateLimitMiddleware.Wrap(http.HandlerFunc(gw.handleHealthEnhanced))

	// Make requests up to the limit
	for i := 0; i < 2; i++ {
		req, err := http.NewRequest("GET", "/health", nil)
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}
		req.RemoteAddr = "127.0.0.1:12345" // Set a consistent IP for rate limiting

		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("Request %d: expected status 200, got %d", i+1, rr.Code)
		}
	}

	// Next request should be rate limited
	req, err := http.NewRequest("GET", "/health", nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req.RemoteAddr = "127.0.0.1:12345"

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("Expected rate limit to trigger (429), got %d", rr.Code)
	}

	// Cleanup
	rateLimitMiddleware.Stop()
}
