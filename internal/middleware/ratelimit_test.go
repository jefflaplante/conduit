package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestRateLimitMiddleware_AnonymousRequests(t *testing.T) {
	// Create rate limiter: 2 requests per minute for anonymous
	config := RateLimitConfig{
		Enabled: true,
		Anonymous: struct {
			WindowSeconds int `json:"windowSeconds"`
			MaxRequests   int `json:"maxRequests"`
		}{
			WindowSeconds: 60,
			MaxRequests:   2,
		},
		Authenticated: struct {
			WindowSeconds int `json:"windowSeconds"`
			MaxRequests   int `json:"maxRequests"`
		}{
			WindowSeconds: 60,
			MaxRequests:   10,
		},
		CleanupIntervalSeconds: 300,
	}

	middleware := NewRateLimitMiddleware(RateLimitMiddlewareConfig{Config: config})
	defer middleware.Stop()

	// Create test handler
	handler := middleware.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))

	// First request should succeed
	req1 := httptest.NewRequest("GET", "/test", nil)
	req1.RemoteAddr = "192.168.1.100:12345"
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)

	if w1.Code != http.StatusOK {
		t.Errorf("First request should succeed, got status %d", w1.Code)
	}

	// Check rate limit headers
	if limit := w1.Header().Get("X-RateLimit-Limit"); limit != "2" {
		t.Errorf("Expected X-RateLimit-Limit: 2, got: %s", limit)
	}
	if remaining := w1.Header().Get("X-RateLimit-Remaining"); remaining != "1" {
		t.Errorf("Expected X-RateLimit-Remaining: 1, got: %s", remaining)
	}

	// Second request should succeed
	req2 := httptest.NewRequest("GET", "/test", nil)
	req2.RemoteAddr = "192.168.1.100:12346"
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Errorf("Second request should succeed, got status %d", w2.Code)
	}
	if remaining := w2.Header().Get("X-RateLimit-Remaining"); remaining != "0" {
		t.Errorf("Expected X-RateLimit-Remaining: 0, got: %s", remaining)
	}

	// Third request should be rate limited
	req3 := httptest.NewRequest("GET", "/test", nil)
	req3.RemoteAddr = "192.168.1.100:12347"
	w3 := httptest.NewRecorder()
	handler.ServeHTTP(w3, req3)

	if w3.Code != http.StatusTooManyRequests {
		t.Errorf("Third request should be rate limited, got status %d", w3.Code)
	}

	// Check error response
	var errorResp struct {
		Error      string `json:"error"`
		Message    string `json:"message"`
		RetryAfter int    `json:"retry_after"`
	}
	if err := json.NewDecoder(w3.Body).Decode(&errorResp); err != nil {
		t.Fatalf("Failed to decode error response: %v", err)
	}
	if errorResp.Error != "rate_limit_exceeded" {
		t.Errorf("Expected error 'rate_limit_exceeded', got: %s", errorResp.Error)
	}
	if errorResp.RetryAfter <= 0 {
		t.Errorf("Expected positive retry after, got: %d", errorResp.RetryAfter)
	}

	// Check Retry-After header
	if retryAfter := w3.Header().Get("Retry-After"); retryAfter == "" {
		t.Error("Expected Retry-After header")
	}
}

func TestRateLimitMiddleware_AuthenticatedRequests(t *testing.T) {
	// Create rate limiter: 3 requests per minute for authenticated
	config := RateLimitConfig{
		Enabled: true,
		Anonymous: struct {
			WindowSeconds int `json:"windowSeconds"`
			MaxRequests   int `json:"maxRequests"`
		}{
			WindowSeconds: 60,
			MaxRequests:   1,
		},
		Authenticated: struct {
			WindowSeconds int `json:"windowSeconds"`
			MaxRequests   int `json:"maxRequests"`
		}{
			WindowSeconds: 60,
			MaxRequests:   3,
		},
		CleanupIntervalSeconds: 300,
	}

	middleware := NewRateLimitMiddleware(RateLimitMiddlewareConfig{Config: config})
	defer middleware.Stop()

	// Create test handler
	handler := middleware.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))

	// Create authenticated context
	authInfo := &AuthInfo{
		ClientName: "test_client",
	}

	// First 3 requests should succeed
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "192.168.1.200:12345"
		ctx := context.WithValue(req.Context(), AuthContextKey, authInfo)
		req = req.WithContext(ctx)

		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Request %d should succeed, got status %d", i+1, w.Code)
		}

		// Check rate limit headers
		if limit := w.Header().Get("X-RateLimit-Limit"); limit != "3" {
			t.Errorf("Request %d: Expected X-RateLimit-Limit: 3, got: %s", i+1, limit)
		}
		expectedRemaining := strconv.Itoa(3 - (i + 1))
		if remaining := w.Header().Get("X-RateLimit-Remaining"); remaining != expectedRemaining {
			t.Errorf("Request %d: Expected X-RateLimit-Remaining: %s, got: %s", i+1, expectedRemaining, remaining)
		}
	}

	// Fourth request should be rate limited
	req4 := httptest.NewRequest("GET", "/test", nil)
	req4.RemoteAddr = "192.168.1.200:12346"
	ctx := context.WithValue(req4.Context(), AuthContextKey, authInfo)
	req4 = req4.WithContext(ctx)

	w4 := httptest.NewRecorder()
	handler.ServeHTTP(w4, req4)

	if w4.Code != http.StatusTooManyRequests {
		t.Errorf("Fourth request should be rate limited, got status %d", w4.Code)
	}
}

func TestRateLimitMiddleware_DifferentIPs(t *testing.T) {
	// Create rate limiter: 1 request per minute for anonymous
	config := RateLimitConfig{
		Enabled: true,
		Anonymous: struct {
			WindowSeconds int `json:"windowSeconds"`
			MaxRequests   int `json:"maxRequests"`
		}{
			WindowSeconds: 60,
			MaxRequests:   1,
		},
		Authenticated: struct {
			WindowSeconds int `json:"windowSeconds"`
			MaxRequests   int `json:"maxRequests"`
		}{
			WindowSeconds: 60,
			MaxRequests:   10,
		},
		CleanupIntervalSeconds: 300,
	}

	middleware := NewRateLimitMiddleware(RateLimitMiddlewareConfig{Config: config})
	defer middleware.Stop()

	// Create test handler
	handler := middleware.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))

	// Different IPs should have independent limits
	ips := []string{"192.168.1.1", "192.168.1.2", "192.168.1.3"}

	for _, ip := range ips {
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = ip + ":12345"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Request from IP %s should succeed, got status %d", ip, w.Code)
		}

		// Second request from same IP should be rate limited
		req2 := httptest.NewRequest("GET", "/test", nil)
		req2.RemoteAddr = ip + ":12346"
		w2 := httptest.NewRecorder()
		handler.ServeHTTP(w2, req2)

		if w2.Code != http.StatusTooManyRequests {
			t.Errorf("Second request from IP %s should be rate limited, got status %d", ip, w2.Code)
		}
	}
}

func TestRateLimitMiddleware_XForwardedFor(t *testing.T) {
	// Create rate limiter: 1 request per minute for anonymous
	config := RateLimitConfig{
		Enabled: true,
		Anonymous: struct {
			WindowSeconds int `json:"windowSeconds"`
			MaxRequests   int `json:"maxRequests"`
		}{
			WindowSeconds: 60,
			MaxRequests:   1,
		},
		Authenticated: struct {
			WindowSeconds int `json:"windowSeconds"`
			MaxRequests   int `json:"maxRequests"`
		}{
			WindowSeconds: 60,
			MaxRequests:   10,
		},
		CleanupIntervalSeconds: 300,
	}

	middleware := NewRateLimitMiddleware(RateLimitMiddlewareConfig{Config: config})
	defer middleware.Stop()

	// Create test handler
	handler := middleware.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))

	// Test X-Forwarded-For header handling
	req1 := httptest.NewRequest("GET", "/test", nil)
	req1.RemoteAddr = "10.0.0.1:12345" // Proxy IP
	req1.Header.Set("X-Forwarded-For", "203.0.113.1, 10.0.0.1")
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)

	if w1.Code != http.StatusOK {
		t.Errorf("First request should succeed, got status %d", w1.Code)
	}

	// Second request with same X-Forwarded-For should be rate limited
	req2 := httptest.NewRequest("GET", "/test", nil)
	req2.RemoteAddr = "10.0.0.2:12346" // Different proxy IP
	req2.Header.Set("X-Forwarded-For", "203.0.113.1, 10.0.0.2")
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)

	if w2.Code != http.StatusTooManyRequests {
		t.Errorf("Second request with same client IP should be rate limited, got status %d", w2.Code)
	}
}

func TestRateLimitMiddleware_Disabled(t *testing.T) {
	// Create disabled rate limiter
	config := RateLimitConfig{
		Enabled: false,
		Anonymous: struct {
			WindowSeconds int `json:"windowSeconds"`
			MaxRequests   int `json:"maxRequests"`
		}{
			WindowSeconds: 60,
			MaxRequests:   1,
		},
		Authenticated: struct {
			WindowSeconds int `json:"windowSeconds"`
			MaxRequests   int `json:"maxRequests"`
		}{
			WindowSeconds: 60,
			MaxRequests:   1,
		},
		CleanupIntervalSeconds: 300,
	}

	middleware := NewRateLimitMiddleware(RateLimitMiddlewareConfig{Config: config})
	defer middleware.Stop()

	// Create test handler
	handler := middleware.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))

	// Multiple requests should all succeed when rate limiting is disabled
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "192.168.1.100:12345"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Request %d should succeed when rate limiting disabled, got status %d", i+1, w.Code)
		}

		// Should not have rate limit headers when disabled
		if limit := w.Header().Get("X-RateLimit-Limit"); limit != "" {
			t.Errorf("Should not have X-RateLimit-Limit header when disabled, got: %s", limit)
		}
	}
}

func TestRateLimitMiddleware_ErrorCallback(t *testing.T) {
	callbackCalled := false
	var callbackIdentifier string
	var callbackIsAnonymous bool

	config := RateLimitConfig{
		Enabled: true,
		Anonymous: struct {
			WindowSeconds int `json:"windowSeconds"`
			MaxRequests   int `json:"maxRequests"`
		}{
			WindowSeconds: 60,
			MaxRequests:   1,
		},
		Authenticated: struct {
			WindowSeconds int `json:"windowSeconds"`
			MaxRequests   int `json:"maxRequests"`
		}{
			WindowSeconds: 60,
			MaxRequests:   1,
		},
		CleanupIntervalSeconds: 300,
	}

	middleware := NewRateLimitMiddleware(RateLimitMiddlewareConfig{
		Config: config,
		OnRateLimitExceeded: func(r *http.Request, identifier string, isAnonymous bool) {
			callbackCalled = true
			callbackIdentifier = identifier
			callbackIsAnonymous = isAnonymous
		},
	})
	defer middleware.Stop()

	// Create test handler
	handler := middleware.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))

	// First request should succeed
	req1 := httptest.NewRequest("GET", "/test", nil)
	req1.RemoteAddr = "192.168.1.100:12345"
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)

	if callbackCalled {
		t.Error("Callback should not be called for successful request")
	}

	// Second request should trigger callback
	req2 := httptest.NewRequest("GET", "/test", nil)
	req2.RemoteAddr = "192.168.1.100:12346"
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)

	if !callbackCalled {
		t.Error("Callback should be called when rate limit exceeded")
	}
	if callbackIdentifier != "192.168.1.100" {
		t.Errorf("Expected callback identifier '192.168.1.100', got: %s", callbackIdentifier)
	}
	if !callbackIsAnonymous {
		t.Error("Expected callback isAnonymous to be true for anonymous request")
	}
}

func TestRateLimitMiddleware_GetStats(t *testing.T) {
	// Test disabled middleware stats
	disabledConfig := RateLimitConfig{Enabled: false}
	disabledMiddleware := NewRateLimitMiddleware(RateLimitMiddlewareConfig{Config: disabledConfig})

	disabledStats := disabledMiddleware.GetStats()
	if enabled, ok := disabledStats["enabled"].(bool); !ok || enabled {
		t.Error("Expected disabled stats to show enabled: false")
	}

	// Test enabled middleware stats
	config := RateLimitConfig{
		Enabled: true,
		Anonymous: struct {
			WindowSeconds int `json:"windowSeconds"`
			MaxRequests   int `json:"maxRequests"`
		}{
			WindowSeconds: 60,
			MaxRequests:   100,
		},
		Authenticated: struct {
			WindowSeconds int `json:"windowSeconds"`
			MaxRequests   int `json:"maxRequests"`
		}{
			WindowSeconds: 60,
			MaxRequests:   1000,
		},
		CleanupIntervalSeconds: 300,
	}

	middleware := NewRateLimitMiddleware(RateLimitMiddlewareConfig{Config: config})
	defer middleware.Stop()

	stats := middleware.GetStats()

	if enabled, ok := stats["enabled"].(bool); !ok || !enabled {
		t.Error("Expected enabled stats to show enabled: true")
	}

	// Check config section
	if configStats, ok := stats["config"].(map[string]interface{}); !ok {
		t.Error("Expected config section in stats")
	} else {
		if anonymousConfig, ok := configStats["anonymous"].(map[string]interface{}); !ok {
			t.Error("Expected anonymous config in stats")
		} else {
			if maxReq, ok := anonymousConfig["max_requests"].(int); !ok || maxReq != 100 {
				t.Errorf("Expected anonymous max_requests: 100, got: %v", maxReq)
			}
		}
	}
}

func TestExtractClientIP(t *testing.T) {
	tests := []struct {
		name          string
		remoteAddr    string
		xForwardedFor string
		xRealIP       string
		expectedIP    string
	}{
		{
			name:       "Remote address only",
			remoteAddr: "192.168.1.100:12345",
			expectedIP: "192.168.1.100",
		},
		{
			name:          "X-Forwarded-For single IP",
			remoteAddr:    "10.0.0.1:12345",
			xForwardedFor: "203.0.113.1",
			expectedIP:    "203.0.113.1",
		},
		{
			name:          "X-Forwarded-For multiple IPs",
			remoteAddr:    "10.0.0.1:12345",
			xForwardedFor: "203.0.113.1, 10.0.0.2, 10.0.0.1",
			expectedIP:    "203.0.113.1",
		},
		{
			name:       "X-Real-IP",
			remoteAddr: "10.0.0.1:12345",
			xRealIP:    "203.0.113.2",
			expectedIP: "203.0.113.2",
		},
		{
			name:          "X-Forwarded-For takes precedence over X-Real-IP",
			remoteAddr:    "10.0.0.1:12345",
			xForwardedFor: "203.0.113.1",
			xRealIP:       "203.0.113.2",
			expectedIP:    "203.0.113.1",
		},
		{
			name:       "IPv6",
			remoteAddr: "[2001:db8::1]:12345",
			expectedIP: "2001:db8::1",
		},
		{
			name:       "Malformed address",
			remoteAddr: "not-an-ip",
			expectedIP: "not-an-ip",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/test", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.xForwardedFor != "" {
				req.Header.Set("X-Forwarded-For", tt.xForwardedFor)
			}
			if tt.xRealIP != "" {
				req.Header.Set("X-Real-IP", tt.xRealIP)
			}

			ip := extractClientIP(req)
			if ip != tt.expectedIP {
				t.Errorf("Expected IP: %s, got: %s", tt.expectedIP, ip)
			}
		})
	}
}

func TestSanitizeIdentifier(t *testing.T) {
	tests := []struct {
		identifier  string
		isAnonymous bool
		expected    string
	}{
		{"client_name", false, "client_name"},
		{"192.168.1.100", true, "192.168.*.* "},
		{"2001:db8::1", true, "2001::*"},
		{"not_an_ip", true, "IP_ADDR"},
	}

	for _, tt := range tests {
		result := sanitizeIdentifier(tt.identifier, tt.isAnonymous)
		if result != tt.expected {
			t.Errorf("sanitizeIdentifier(%s, %v) = %s, expected %s",
				tt.identifier, tt.isAnonymous, result, tt.expected)
		}
	}
}
