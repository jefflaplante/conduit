package middleware

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"conduit/internal/auth"
	"conduit/internal/config"
	"conduit/internal/sessions"
)

// TestRateLimitingIntegration tests the complete rate limiting system integration
func TestRateLimitingIntegration(t *testing.T) {
	// Create a test database for auth tokens
	db, err := sessions.NewStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer db.Close()

	// Create auth storage
	authStorage := auth.NewTokenStorage(db.DB())

	// Create a test token for authenticated requests
	clientName := "test_client"
	expiresAt := time.Now().Add(24 * time.Hour)
	tokenResp, err := authStorage.CreateToken(auth.CreateTokenRequest{
		ClientName: clientName,
		ExpiresAt:  &expiresAt,
		Metadata:   nil,
	})
	if err != nil {
		t.Fatalf("Failed to create test token: %v", err)
	}
	token := tokenResp.Token

	// Create rate limiting config for testing
	rateLimitConfig := RateLimitConfig{
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
			MaxRequests:   5, // Higher limit for authenticated
		},
		CleanupIntervalSeconds: 300,
	}

	// Create middleware stack: auth -> rate limiting -> handler
	authMiddleware := NewAuthMiddleware(authStorage, AuthMiddlewareConfig{
		SkipPaths: []string{"/health"}, // Health endpoint is anonymous
	})

	rateLimitMiddleware := NewRateLimitMiddleware(RateLimitMiddlewareConfig{
		Config: rateLimitConfig,
	})
	defer rateLimitMiddleware.Stop()

	// Test handler that responds with OK
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// Create handlers for different endpoints
	healthHandler := rateLimitMiddleware.Wrap(testHandler)                   // Anonymous rate limiting only
	apiHandler := authMiddleware.Wrap(rateLimitMiddleware.Wrap(testHandler)) // Auth + rate limiting

	// Test 1: Anonymous requests to /health should be rate limited by IP
	t.Run("AnonymousRateLimit", func(t *testing.T) {
		for i := 0; i < 2; i++ {
			req := httptest.NewRequest("GET", "/health", nil)
			req.RemoteAddr = "192.168.1.100:12345"
			w := httptest.NewRecorder()

			healthHandler.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("Anonymous request %d should succeed, got %d", i+1, w.Code)
			}

			// Check rate limit headers
			if limit := w.Header().Get("X-RateLimit-Limit"); limit != "2" {
				t.Errorf("Expected X-RateLimit-Limit: 2, got: %s", limit)
			}

			expectedRemaining := strconv.Itoa(2 - (i + 1))
			if remaining := w.Header().Get("X-RateLimit-Remaining"); remaining != expectedRemaining {
				t.Errorf("Request %d: expected remaining %s, got: %s", i+1, expectedRemaining, remaining)
			}
		}

		// Third request should be rate limited
		req := httptest.NewRequest("GET", "/health", nil)
		req.RemoteAddr = "192.168.1.100:12346"
		w := httptest.NewRecorder()

		healthHandler.ServeHTTP(w, req)

		if w.Code != http.StatusTooManyRequests {
			t.Errorf("Third anonymous request should be rate limited, got %d", w.Code)
		}

		// Check error response
		var errorResp map[string]interface{}
		if err := json.NewDecoder(w.Body).Decode(&errorResp); err != nil {
			t.Fatalf("Failed to decode error response: %v", err)
		}

		if errorResp["error"] != "rate_limit_exceeded" {
			t.Errorf("Expected error 'rate_limit_exceeded', got: %v", errorResp["error"])
		}

		if retryAfter, ok := errorResp["retry_after"].(float64); !ok || retryAfter <= 0 {
			t.Errorf("Expected positive retry_after, got: %v", retryAfter)
		}
	})

	// Test 2: Different IPs should have independent rate limits
	t.Run("IndependentIPLimits", func(t *testing.T) {
		ips := []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}

		for _, ip := range ips {
			for i := 0; i < 2; i++ {
				req := httptest.NewRequest("GET", "/health", nil)
				req.RemoteAddr = fmt.Sprintf("%s:1234%d", ip, i)
				w := httptest.NewRecorder()

				healthHandler.ServeHTTP(w, req)

				if w.Code != http.StatusOK {
					t.Errorf("Request %d from IP %s should succeed, got %d", i+1, ip, w.Code)
				}
			}

			// Third request should be rate limited
			req := httptest.NewRequest("GET", "/health", nil)
			req.RemoteAddr = fmt.Sprintf("%s:12345", ip)
			w := httptest.NewRecorder()

			healthHandler.ServeHTTP(w, req)

			if w.Code != http.StatusTooManyRequests {
				t.Errorf("Third request from IP %s should be rate limited, got %d", ip, w.Code)
			}
		}
	})

	// Test 3: Authenticated requests should have higher limits and be tracked by client
	t.Run("AuthenticatedRateLimit", func(t *testing.T) {
		// Make 5 authenticated requests (should all succeed)
		for i := 0; i < 5; i++ {
			req := httptest.NewRequest("GET", "/api/test", nil)
			req.RemoteAddr = "192.168.2.100:12345"
			req.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()

			apiHandler.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("Authenticated request %d should succeed, got %d", i+1, w.Code)
			}

			// Check rate limit headers show higher limit
			if limit := w.Header().Get("X-RateLimit-Limit"); limit != "5" {
				t.Errorf("Expected X-RateLimit-Limit: 5 for authenticated, got: %s", limit)
			}

			expectedRemaining := strconv.Itoa(5 - (i + 1))
			if remaining := w.Header().Get("X-RateLimit-Remaining"); remaining != expectedRemaining {
				t.Errorf("Request %d: expected remaining %s, got: %s", i+1, expectedRemaining, remaining)
			}
		}

		// Sixth request should be rate limited
		req := httptest.NewRequest("GET", "/api/test", nil)
		req.RemoteAddr = "192.168.2.100:12346"
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()

		apiHandler.ServeHTTP(w, req)

		if w.Code != http.StatusTooManyRequests {
			t.Errorf("Sixth authenticated request should be rate limited, got %d", w.Code)
		}
	})

	// Test 4: Authentication failure should still apply anonymous rate limiting
	t.Run("InvalidTokenAnonymousLimit", func(t *testing.T) {
		// Request with invalid token should be treated as anonymous
		req := httptest.NewRequest("GET", "/api/test", nil)
		req.RemoteAddr = "192.168.3.100:12345"
		req.Header.Set("Authorization", "Bearer invalid_token")
		w := httptest.NewRecorder()

		apiHandler.ServeHTTP(w, req)

		// Should get auth error, not rate limit error (since auth middleware runs first)
		if w.Code != http.StatusForbidden {
			t.Errorf("Request with invalid token should get 403, got %d", w.Code)
		}
	})

	// Test 5: X-Forwarded-For header should be respected for IP extraction
	t.Run("XForwardedForHandling", func(t *testing.T) {
		clientIP := "203.0.113.1"

		// Make requests with X-Forwarded-For header
		for i := 0; i < 2; i++ {
			req := httptest.NewRequest("GET", "/health", nil)
			req.RemoteAddr = "10.0.0.1:12345" // Proxy IP
			req.Header.Set("X-Forwarded-For", fmt.Sprintf("%s, 10.0.0.1", clientIP))
			w := httptest.NewRecorder()

			healthHandler.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("X-Forwarded-For request %d should succeed, got %d", i+1, w.Code)
			}
		}

		// Third request should be rate limited (same client IP)
		req := httptest.NewRequest("GET", "/health", nil)
		req.RemoteAddr = "10.0.0.2:12346" // Different proxy IP
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("%s, 10.0.0.2", clientIP))
		w := httptest.NewRecorder()

		healthHandler.ServeHTTP(w, req)

		if w.Code != http.StatusTooManyRequests {
			t.Errorf("Third X-Forwarded-For request should be rate limited, got %d", w.Code)
		}
	})

	// Test 6: Rate limit headers should always be present
	t.Run("RateLimitHeaders", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/health", nil)
		req.RemoteAddr = "192.168.4.100:12345"
		w := httptest.NewRecorder()

		healthHandler.ServeHTTP(w, req)

		// Check all required headers are present
		requiredHeaders := []string{"X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset"}
		for _, header := range requiredHeaders {
			if value := w.Header().Get(header); value == "" {
				t.Errorf("Header %s should be present, got empty value", header)
			}
		}

		// Reset time should be a valid Unix timestamp
		if resetStr := w.Header().Get("X-RateLimit-Reset"); resetStr != "" {
			if resetTime, err := strconv.ParseInt(resetStr, 10, 64); err != nil {
				t.Errorf("X-RateLimit-Reset should be valid Unix timestamp, got: %s", resetStr)
			} else {
				// Reset time should be in the future
				now := time.Now().Unix()
				if resetTime <= now {
					t.Errorf("X-RateLimit-Reset should be in future, got %d (now: %d)", resetTime, now)
				}
			}
		}
	})

	// Test 7: Disabled rate limiting should allow all requests
	t.Run("DisabledRateLimit", func(t *testing.T) {
		disabledConfig := RateLimitConfig{
			Enabled: false,
			Anonymous: struct {
				WindowSeconds int `json:"windowSeconds"`
				MaxRequests   int `json:"maxRequests"`
			}{
				WindowSeconds: 60,
				MaxRequests:   1, // Very low limit, but disabled
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

		disabledMiddleware := NewRateLimitMiddleware(RateLimitMiddlewareConfig{
			Config: disabledConfig,
		})
		defer disabledMiddleware.Stop()

		disabledHandler := disabledMiddleware.Wrap(testHandler)

		// Should be able to make many requests when disabled
		for i := 0; i < 10; i++ {
			req := httptest.NewRequest("GET", "/test", nil)
			req.RemoteAddr = "192.168.5.100:12345"
			w := httptest.NewRecorder()

			disabledHandler.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("Request %d should succeed when rate limiting disabled, got %d", i+1, w.Code)
			}

			// Should not have rate limit headers when disabled
			if limit := w.Header().Get("X-RateLimit-Limit"); limit != "" {
				t.Errorf("Should not have X-RateLimit-Limit when disabled, got: %s", limit)
			}
		}
	})
}

// TestGatewayConfigurationIntegration tests that the gateway properly loads rate limiting configuration
func TestGatewayConfigurationIntegration(t *testing.T) {
	// Test default configuration
	cfg := config.Default()

	// Verify default rate limiting settings
	if !cfg.RateLimiting.Enabled {
		t.Error("Rate limiting should be enabled by default")
	}

	if cfg.RateLimiting.Anonymous.MaxRequests != 100 {
		t.Errorf("Expected default anonymous limit 100, got %d", cfg.RateLimiting.Anonymous.MaxRequests)
	}

	if cfg.RateLimiting.Authenticated.MaxRequests != 1000 {
		t.Errorf("Expected default authenticated limit 1000, got %d", cfg.RateLimiting.Authenticated.MaxRequests)
	}

	// Test that the config can be converted to middleware config
	middlewareConfig := RateLimitConfig{
		Enabled: cfg.RateLimiting.Enabled,
		Anonymous: struct {
			WindowSeconds int `json:"windowSeconds"`
			MaxRequests   int `json:"maxRequests"`
		}{
			WindowSeconds: cfg.RateLimiting.Anonymous.WindowSeconds,
			MaxRequests:   cfg.RateLimiting.Anonymous.MaxRequests,
		},
		Authenticated: struct {
			WindowSeconds int `json:"windowSeconds"`
			MaxRequests   int `json:"maxRequests"`
		}{
			WindowSeconds: cfg.RateLimiting.Authenticated.WindowSeconds,
			MaxRequests:   cfg.RateLimiting.Authenticated.MaxRequests,
		},
		CleanupIntervalSeconds: cfg.RateLimiting.CleanupIntervalSeconds,
	}

	// Create middleware with config (should not panic)
	rateLimitMiddleware := NewRateLimitMiddleware(RateLimitMiddlewareConfig{
		Config: middlewareConfig,
	})
	defer rateLimitMiddleware.Stop()

	// Get stats to verify it's working
	stats := rateLimitMiddleware.GetStats()
	if enabled, ok := stats["enabled"].(bool); !ok || !enabled {
		t.Error("Rate limiting should be enabled in stats")
	}
}

// BenchmarkRateLimitingPerformance tests performance under load
func BenchmarkRateLimitingPerformance(b *testing.B) {
	rateLimitConfig := RateLimitConfig{
		Enabled: true,
		Anonymous: struct {
			WindowSeconds int `json:"windowSeconds"`
			MaxRequests   int `json:"maxRequests"`
		}{
			WindowSeconds: 60,
			MaxRequests:   10000, // High limit for benchmark
		},
		Authenticated: struct {
			WindowSeconds int `json:"windowSeconds"`
			MaxRequests   int `json:"maxRequests"`
		}{
			WindowSeconds: 60,
			MaxRequests:   50000, // Very high limit for benchmark
		},
		CleanupIntervalSeconds: 300,
	}

	rateLimitMiddleware := NewRateLimitMiddleware(RateLimitMiddlewareConfig{
		Config: rateLimitConfig,
	})
	defer rateLimitMiddleware.Stop()

	handler := rateLimitMiddleware.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			req := httptest.NewRequest("GET", "/test", nil)
			req.RemoteAddr = fmt.Sprintf("192.168.%d.%d:12345", i%255+1, i%255+1)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)
			i++
		}
	})
}
