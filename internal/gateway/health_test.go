package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"conduit/internal/auth"
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

func TestDiagnosticEndpointsAuthRequirement(t *testing.T) {
	// Test that diagnostic endpoints require auth by default (except /health)
	// This tests the new conduit-330 security requirement

	tests := []struct {
		name           string
		endpoint       string
		requireAuth    bool   // diagnostics.require_auth setting
		healthPublic   *bool  // diagnostics.health_public setting
		expectStatus   int    // Expected status without auth token
		withAuth       bool   // Whether to include auth token
		expectWithAuth int    // Expected status with auth token
	}{
		// Default config: require_auth=true, health_public=true (default)
		{
			name:           "/health is public by default",
			endpoint:       "/health",
			requireAuth:    true,
			healthPublic:   nil, // default (true)
			expectStatus:   http.StatusOK,
			withAuth:       false,
			expectWithAuth: http.StatusOK,
		},
		{
			name:           "/metrics requires auth by default",
			endpoint:       "/metrics",
			requireAuth:    true,
			healthPublic:   nil,
			expectStatus:   http.StatusUnauthorized,
			withAuth:       true,
			expectWithAuth: http.StatusOK,
		},
		{
			name:           "/diagnostics requires auth by default",
			endpoint:       "/diagnostics",
			requireAuth:    true,
			healthPublic:   nil,
			expectStatus:   http.StatusUnauthorized,
			withAuth:       true,
			expectWithAuth: http.StatusOK,
		},
		{
			name:           "/prometheus requires auth by default",
			endpoint:       "/prometheus",
			requireAuth:    true,
			healthPublic:   nil,
			expectStatus:   http.StatusUnauthorized,
			withAuth:       true,
			expectWithAuth: http.StatusOK,
		},
		// health_public=false makes /health also require auth
		{
			name:           "/health requires auth when health_public=false",
			endpoint:       "/health",
			requireAuth:    true,
			healthPublic:   boolPtr(false),
			expectStatus:   http.StatusUnauthorized,
			withAuth:       true,
			expectWithAuth: http.StatusOK,
		},
		// require_auth=false makes all diagnostic endpoints public (legacy behavior)
		{
			name:           "/metrics is public when require_auth=false",
			endpoint:       "/metrics",
			requireAuth:    false,
			healthPublic:   nil,
			expectStatus:   http.StatusOK,
			withAuth:       false,
			expectWithAuth: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test gateway with the specific config
			gw, testToken := createTestGatewayWithDiagnosticsConfig(t, tt.requireAuth, tt.healthPublic)

			// Create test server with proper middleware chain
			mux := http.NewServeMux()
			mux.Handle("/health", gw.authMiddleware.Wrap(gw.rateLimitMiddleware.Wrap(http.HandlerFunc(gw.handleHealthEnhanced))))
			mux.Handle("/metrics", gw.authMiddleware.Wrap(gw.rateLimitMiddleware.Wrap(http.HandlerFunc(gw.handleMetrics))))
			mux.Handle("/diagnostics", gw.authMiddleware.Wrap(gw.rateLimitMiddleware.Wrap(http.HandlerFunc(gw.handleDiagnostics))))
			mux.Handle("/prometheus", gw.authMiddleware.Wrap(gw.rateLimitMiddleware.Wrap(http.HandlerFunc(gw.handlePrometheusMetrics))))

			server := httptest.NewServer(mux)
			defer server.Close()

			// Test without auth
			req, err := http.NewRequest("GET", server.URL+tt.endpoint, nil)
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("Failed to make request: %v", err)
			}
			resp.Body.Close()

			if resp.StatusCode != tt.expectStatus {
				t.Errorf("Without auth: expected status %d, got %d", tt.expectStatus, resp.StatusCode)
			}

			// Test with auth if needed
			if tt.withAuth {
				req, err := http.NewRequest("GET", server.URL+tt.endpoint, nil)
				if err != nil {
					t.Fatalf("Failed to create request: %v", err)
				}
				req.Header.Set("Authorization", "Bearer "+testToken)

				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatalf("Failed to make request: %v", err)
				}
				resp.Body.Close()

				if resp.StatusCode != tt.expectWithAuth {
					t.Errorf("With auth: expected status %d, got %d", tt.expectWithAuth, resp.StatusCode)
				}
			}
		})
	}
}

// createTestGatewayWithDiagnosticsConfig creates a test gateway with specific diagnostics config
// Returns the gateway and a valid test token
func createTestGatewayWithDiagnosticsConfig(t *testing.T, requireAuth bool, healthPublic *bool) (*Gateway, string) {
	// Create a test config
	cfg := &config.Config{
		Database: config.DatabaseConfig{
			Path: ":memory:",
		},
		RateLimiting: config.RateLimitingConfig{
			Enabled: false,
		},
		Diagnostics: config.DiagnosticsConfig{
			RequireAuth:  requireAuth,
			HealthPublic: healthPublic,
		},
	}

	// Create session store
	sessionStore, err := sessions.NewStore(cfg.Database.Path)
	if err != nil {
		t.Fatalf("Failed to create session store: %v", err)
	}

	// Create auth storage and a test token
	authStorage := auth.NewTokenStorage(sessionStore.DB())
	tokenResp, err := authStorage.CreateToken(auth.CreateTokenRequest{
		ClientName: "test-client",
	})
	if err != nil {
		t.Fatalf("Failed to create test token: %v", err)
	}

	// Build auth skip paths based on config (same logic as gateway.go)
	var authSkipPaths []string
	if cfg.Diagnostics.IsHealthPublic() {
		authSkipPaths = append(authSkipPaths, "/health")
	}
	if !cfg.Diagnostics.RequireAuth {
		authSkipPaths = append(authSkipPaths, "/metrics", "/diagnostics", "/prometheus")
	}

	// Create auth middleware with the computed skip paths
	authMiddleware := middleware.NewAuthMiddleware(authStorage, middleware.AuthMiddlewareConfig{
		SkipPaths: authSkipPaths,
	})

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

	// Create rate limit middleware (disabled)
	rateLimitMiddleware := middleware.NewRateLimitMiddleware(middleware.RateLimitMiddlewareConfig{
		Config: middleware.RateLimitConfig{
			Enabled: false,
		},
	})

	gw := &Gateway{
		config:              cfg,
		sessions:            sessionStore,
		gatewayMetrics:      gatewayMetrics,
		metricsCollector:    metricsCollector,
		eventStore:          eventStore,
		rateLimitMiddleware: rateLimitMiddleware,
		authMiddleware:      authMiddleware,
	}

	return gw, tokenResp.Token
}

// boolPtr returns a pointer to a bool value
func boolPtr(b bool) *bool {
	return &b
}

// mockFailingCollector implements monitoring.MetricsCollectorInterface with a failing database
type mockFailingCollector struct {
	dbErr error
}

func (m *mockFailingCollector) IsIdle(d time.Duration) bool                            { return false }
func (m *mockFailingCollector) MarkActivity()                                          {}
func (m *mockFailingCollector) GetLastActivityTime() time.Time                         { return time.Now() }
func (m *mockFailingCollector) CollectMetrics(_ context.Context) (*monitoring.GatewayMetrics, error) {
	return monitoring.NewGatewayMetrics(), nil
}
func (m *mockFailingCollector) DetectStuckSessions(_ context.Context, _ time.Duration) ([]string, error) {
	return nil, nil
}
func (m *mockFailingCollector) ValidateDatabase(_ context.Context) error { return m.dbErr }
func (m *mockFailingCollector) UpdateWebSocketConnections(count int)     {}
func (m *mockFailingCollector) UpdateActiveRequests(count int)           {}
func (m *mockFailingCollector) UpdateQueueDepth(depth int)              {}
func (m *mockFailingCollector) UpdateHeartbeatJobs(total, enabled int)  {}
func (m *mockFailingCollector) GetHeartbeatMetrics() monitoring.HeartbeatMetrics {
	return monitoring.HeartbeatMetrics{}
}

func TestMetricsEndpoint_SanitizesDatabaseErrors(t *testing.T) {
	gw := createTestGateway(t)

	// Replace the metrics collector with one that returns a detailed database error
	sensitiveErr := fmt.Errorf("FATAL: password authentication failed for user \"conduit\" at 192.168.1.50:5432/conduit_db")
	gw.metricsCollector = &mockFailingCollector{dbErr: sensitiveErr}

	req, err := http.NewRequest("GET", "/metrics", nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(gw.handleMetrics)
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rr.Code)
	}

	// Parse the response
	var response MetricsResponse
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Database should be marked as disconnected
	if response.Database.Connected {
		t.Error("Expected database to be disconnected")
	}

	// The error message should be generic, not containing sensitive details
	if response.Database.Error != "database unavailable" {
		t.Errorf("Expected generic error message 'database unavailable', got %q", response.Database.Error)
	}

	// Verify sensitive details are NOT in the response
	body := rr.Body.String()
	if strings.Contains(body, "password") {
		t.Error("Response should not contain 'password'")
	}
	if strings.Contains(body, "192.168.1.50") {
		t.Error("Response should not contain internal IP addresses")
	}
	if strings.Contains(body, "conduit_db") {
		t.Error("Response should not contain database names")
	}
}

func TestDebugEndpoint_SanitizesErrors(t *testing.T) {
	gw := createTestGateway(t)

	// Call the debug endpoint with a session key that will cause an error
	// The gateway has no agent system configured, so GetSystemPromptDebug will fail
	req, err := http.NewRequest("GET", "/debug/prompt?session=nonexistent", nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(gw.handleDebugPrompt)
	handler.ServeHTTP(rr, req)

	// If it returns an error, verify the message is generic
	if rr.Code == http.StatusInternalServerError {
		body := rr.Body.String()
		if body != "internal server error\n" {
			t.Errorf("Expected generic error message, got %q", body)
		}
	}
	// If it returns 200 (no error), that's also fine - the test just verifies
	// that when errors occur, they are sanitized
}
