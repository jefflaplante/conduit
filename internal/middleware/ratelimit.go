package middleware

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"conduit/internal/logging"
	"conduit/internal/ratelimit"
)

// RateLimitConfig contains configuration for rate limiting middleware
type RateLimitConfig struct {
	// Anonymous rate limiting (IP-based)
	Anonymous struct {
		WindowSeconds int `json:"windowSeconds"` // Time window in seconds
		MaxRequests   int `json:"maxRequests"`   // Max requests in window
	} `json:"anonymous"`

	// Authenticated rate limiting (client-based)
	Authenticated struct {
		WindowSeconds int `json:"windowSeconds"` // Time window in seconds
		MaxRequests   int `json:"maxRequests"`   // Max requests in window
	} `json:"authenticated"`

	// Cleanup interval for expired buckets
	CleanupIntervalSeconds int `json:"cleanupIntervalSeconds"`

	// Enable/disable rate limiting
	Enabled bool `json:"enabled"`

	// TrustProxy controls how X-Forwarded-For headers are handled.
	// When false (default), only RemoteAddr is used - forwarded headers are ignored.
	// When true, the rightmost non-private IP from X-Forwarded-For is used.
	// Only enable this when running behind a trusted reverse proxy.
	TrustProxy bool `json:"trustProxy"`
}

// DefaultRateLimitConfig returns default rate limiting configuration
func DefaultRateLimitConfig() RateLimitConfig {
	return RateLimitConfig{
		Enabled: true,
		Anonymous: struct {
			WindowSeconds int `json:"windowSeconds"`
			MaxRequests   int `json:"maxRequests"`
		}{
			WindowSeconds: 60,  // 1 minute
			MaxRequests:   100, // 100 requests per minute for anonymous
		},
		Authenticated: struct {
			WindowSeconds int `json:"windowSeconds"`
			MaxRequests   int `json:"maxRequests"`
		}{
			WindowSeconds: 60,   // 1 minute
			MaxRequests:   1000, // 1000 requests per minute for authenticated
		},
		CleanupIntervalSeconds: 300, // 5 minutes
	}
}

// RateLimitMiddleware provides HTTP rate limiting
type RateLimitMiddleware struct {
	anonymousLimiter     *ratelimit.SlidingWindow
	authenticatedLimiter *ratelimit.SlidingWindow
	config               RateLimitConfig
	onRateLimitExceeded  func(r *http.Request, identifier string, isAnonymous bool)
	trustProxy           bool
	logger               *slog.Logger
}

// RateLimitMiddlewareConfig contains initialization options for rate limiting middleware
type RateLimitMiddlewareConfig struct {
	Config              RateLimitConfig
	OnRateLimitExceeded func(r *http.Request, identifier string, isAnonymous bool)
	// Logger is the structured logger; defaults to logging.Default() when nil.
	Logger *slog.Logger
}

// NewRateLimitMiddleware creates a new rate limiting middleware
func NewRateLimitMiddleware(config RateLimitMiddlewareConfig) *RateLimitMiddleware {
	logger := config.Logger
	if logger == nil {
		logger = logging.Default()
	}
	logger = logger.With("component", "ratelimit")

	if !config.Config.Enabled {
		// Return disabled middleware that allows all requests
		return &RateLimitMiddleware{
			config:              config.Config,
			onRateLimitExceeded: config.OnRateLimitExceeded,
			logger:              logger,
		}
	}

	// Create sliding window limiters
	anonymousWindow := time.Duration(config.Config.Anonymous.WindowSeconds) * time.Second
	authenticatedWindow := time.Duration(config.Config.Authenticated.WindowSeconds) * time.Second
	cleanupInterval := time.Duration(config.Config.CleanupIntervalSeconds) * time.Second

	// Default cleanup interval to 60 seconds if not specified
	if cleanupInterval <= 0 {
		cleanupInterval = 60 * time.Second
	}

	anonymousLimiter := ratelimit.NewSlidingWindow(
		anonymousWindow,
		config.Config.Anonymous.MaxRequests,
		cleanupInterval,
	)

	authenticatedLimiter := ratelimit.NewSlidingWindow(
		authenticatedWindow,
		config.Config.Authenticated.MaxRequests,
		cleanupInterval,
	)

	return &RateLimitMiddleware{
		anonymousLimiter:     anonymousLimiter,
		authenticatedLimiter: authenticatedLimiter,
		config:               config.Config,
		onRateLimitExceeded:  config.OnRateLimitExceeded,
		trustProxy:           config.Config.TrustProxy,
		logger:               logger,
	}
}

// Wrap wraps an http.Handler with rate limiting
func (m *RateLimitMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !m.config.Enabled {
			// Rate limiting disabled, pass through
			next.ServeHTTP(w, r)
			return
		}

		// Get authentication info from context (set by auth middleware)
		authInfo := GetAuthInfo(r.Context())

		var allowed bool
		var remaining int
		var resetTime time.Time
		var retryAfter int
		var identifier string
		var isAnonymous bool

		if authInfo != nil {
			// Authenticated request - use client-based limiting
			identifier = authInfo.ClientName
			isAnonymous = false
			allowed, remaining, resetTime, retryAfter = m.authenticatedLimiter.Allow(identifier)
		} else {
			// Anonymous request - use IP-based limiting
			identifier = extractClientIP(r, m.trustProxy)
			isAnonymous = true
			allowed, remaining, resetTime, retryAfter = m.anonymousLimiter.Allow(identifier)
		}

		// Add rate limit headers to response
		m.setRateLimitHeaders(w, allowed, remaining, resetTime, retryAfter, isAnonymous)

		if !allowed {
			// Rate limit exceeded
			if m.onRateLimitExceeded != nil {
				m.onRateLimitExceeded(r, identifier, isAnonymous)
			}

			// Log rate limit exceeded (with sanitized identifier for privacy)
			sanitizedID := sanitizeIdentifier(identifier, isAnonymous)
			m.logger.Warn("rate limit exceeded",
				"request_id", logging.RequestIDFromContext(r.Context()),
				"remote_ip", extractClientIP(r, m.trustProxy),
				"method", r.Method,
				"path", r.URL.Path,
				"identifier", sanitizedID,
				"identifier_type", getIdentifierType(isAnonymous),
			)

			// Send 429 response
			m.sendRateLimitError(w, r, retryAfter)
			return
		}

		// Request allowed, continue to next middleware/handler
		next.ServeHTTP(w, r)
	})
}

// WrapFunc wraps an http.HandlerFunc with rate limiting
func (m *RateLimitMiddleware) WrapFunc(next http.HandlerFunc) http.HandlerFunc {
	return m.Wrap(next).ServeHTTP
}

// setRateLimitHeaders sets standard rate limiting HTTP headers
func (m *RateLimitMiddleware) setRateLimitHeaders(w http.ResponseWriter, allowed bool, remaining int, resetTime time.Time, retryAfter int, isAnonymous bool) {
	// Determine limit based on request type
	var limit int
	if isAnonymous {
		limit = m.config.Anonymous.MaxRequests
	} else {
		limit = m.config.Authenticated.MaxRequests
	}

	// Set standard rate limit headers
	w.Header().Set("X-RateLimit-Limit", strconv.Itoa(limit))
	w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
	w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetTime.Unix(), 10))

	// Set Retry-After header only if rate limited
	if !allowed && retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	}
}

// sendRateLimitError sends a 429 Too Many Requests response
func (m *RateLimitMiddleware) sendRateLimitError(w http.ResponseWriter, r *http.Request, retryAfter int) {
	// Set content type and security headers
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	// Set status code
	w.WriteHeader(http.StatusTooManyRequests)

	// Create error response
	errorResponse := struct {
		Error      string `json:"error"`
		Message    string `json:"message"`
		RetryAfter int    `json:"retry_after"`
	}{
		Error:      "rate_limit_exceeded",
		Message:    "Rate limit exceeded. Try again later.",
		RetryAfter: retryAfter,
	}

	// Encode and send response
	if err := json.NewEncoder(w).Encode(errorResponse); err != nil {
		// If JSON encoding fails, we've already set the status code
		// so we can't do much more than log the error
		m.logger.Error("failed to encode rate limit error response", "error", err)
	}
}

// extractClientIP extracts the client IP from the request.
//
// When trustProxy is false (default), only RemoteAddr is used. This prevents
// attackers from spoofing their IP via X-Forwarded-For headers.
//
// When trustProxy is true, the function uses the rightmost non-private IP from
// X-Forwarded-For, which is the IP added by the trusted proxy. This is more
// secure than using the leftmost IP (which can be spoofed by the client).
//
// Security: Only set trustProxy=true when running behind a trusted reverse proxy
// that sets X-Forwarded-For correctly.
func extractClientIP(r *http.Request, trustProxy bool) string {
	// Always extract RemoteAddr as the fallback
	remoteIP := extractRemoteAddr(r.RemoteAddr)

	// If not trusting proxy headers, use RemoteAddr directly
	if !trustProxy {
		return remoteIP
	}

	// Check X-Forwarded-For header (can contain multiple IPs)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Parse IPs from right to left, looking for the rightmost non-private IP.
		// The rightmost IP is the one added by our trusted proxy, which is the
		// actual client IP (or the last external hop if behind multiple proxies).
		ips := strings.Split(xff, ",")
		for i := len(ips) - 1; i >= 0; i-- {
			ip := strings.TrimSpace(ips[i])
			if isValidIP(ip) && !isPrivateIP(ip) {
				return ip
			}
		}
		// If all IPs are private (internal network), use the leftmost
		// as it's likely the actual internal client
		if len(ips) > 0 {
			ip := strings.TrimSpace(ips[0])
			if isValidIP(ip) {
				return ip
			}
		}
	}

	// Check X-Real-IP header (single IP set by proxy)
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		ip := strings.TrimSpace(xri)
		if isValidIP(ip) {
			return ip
		}
	}

	// Fallback to RemoteAddr
	return remoteIP
}

// extractRemoteAddr extracts the IP from RemoteAddr (host:port format)
func extractRemoteAddr(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		// If SplitHostPort fails, return the raw RemoteAddr
		return remoteAddr
	}
	return host
}

// isValidIP checks if a string is a valid IP address
func isValidIP(ip string) bool {
	return net.ParseIP(ip) != nil
}

// isPrivateIP checks if an IP address is in a private/reserved range.
// This includes RFC1918 private networks, loopback, link-local, and other reserved ranges.
func isPrivateIP(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}

	// Check for IPv4 private ranges
	if ip4 := ip.To4(); ip4 != nil {
		// 10.0.0.0/8
		if ip4[0] == 10 {
			return true
		}
		// 172.16.0.0/12
		if ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31 {
			return true
		}
		// 192.168.0.0/16
		if ip4[0] == 192 && ip4[1] == 168 {
			return true
		}
		// 127.0.0.0/8 (loopback)
		if ip4[0] == 127 {
			return true
		}
		// 169.254.0.0/16 (link-local)
		if ip4[0] == 169 && ip4[1] == 254 {
			return true
		}
		// 100.64.0.0/10 (Carrier-grade NAT)
		if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
			return true
		}
		return false
	}

	// Check for IPv6 private ranges
	// ::1 (loopback)
	if ip.Equal(net.IPv6loopback) {
		return true
	}
	// fc00::/7 (Unique Local Addresses)
	if ip[0] == 0xfc || ip[0] == 0xfd {
		return true
	}
	// fe80::/10 (Link-Local)
	if ip[0] == 0xfe && (ip[1]&0xc0) == 0x80 {
		return true
	}

	return false
}

// sanitizeIdentifier sanitizes identifiers for logging (privacy protection)
func sanitizeIdentifier(identifier string, isAnonymous bool) string {
	if !isAnonymous {
		// For authenticated clients, show the client name as-is
		return identifier
	}

	// For IP addresses, show only partial information for privacy
	if ip := net.ParseIP(identifier); ip != nil {
		if ip.To4() != nil {
			// IPv4: show only first two octets
			parts := strings.Split(identifier, ".")
			if len(parts) >= 2 {
				return fmt.Sprintf("%s.%s.*.* ", parts[0], parts[1])
			}
		} else if ip.To16() != nil {
			// IPv6: show only first part
			parts := strings.Split(identifier, ":")
			if len(parts) >= 1 {
				return fmt.Sprintf("%s::*", parts[0])
			}
		}
	}

	// Fallback: just indicate it's an IP
	return "IP_ADDR"
}

// getIdentifierType returns a string describing the identifier type
func getIdentifierType(isAnonymous bool) string {
	if isAnonymous {
		return "anonymous_ip"
	}
	return "authenticated_client"
}

// Stop stops the rate limiting middleware and cleans up resources
func (m *RateLimitMiddleware) Stop() {
	if m.anonymousLimiter != nil {
		m.anonymousLimiter.Stop()
	}
	if m.authenticatedLimiter != nil {
		m.authenticatedLimiter.Stop()
	}
}

// GetStats returns statistics about rate limiting
func (m *RateLimitMiddleware) GetStats() map[string]interface{} {
	if !m.config.Enabled {
		return map[string]interface{}{
			"enabled": false,
		}
	}

	stats := map[string]interface{}{
		"enabled": true,
		"config": map[string]interface{}{
			"anonymous": map[string]interface{}{
				"window_seconds": m.config.Anonymous.WindowSeconds,
				"max_requests":   m.config.Anonymous.MaxRequests,
			},
			"authenticated": map[string]interface{}{
				"window_seconds": m.config.Authenticated.WindowSeconds,
				"max_requests":   m.config.Authenticated.MaxRequests,
			},
			"cleanup_interval_seconds": m.config.CleanupIntervalSeconds,
		},
	}

	if m.anonymousLimiter != nil {
		anonymousStats := m.anonymousLimiter.GetStats()
		stats["anonymous_stats"] = map[string]interface{}{
			"active_buckets":   anonymousStats.ActiveBuckets,
			"total_timestamps": anonymousStats.TotalTimestamps,
		}
	}

	if m.authenticatedLimiter != nil {
		authenticatedStats := m.authenticatedLimiter.GetStats()
		stats["authenticated_stats"] = map[string]interface{}{
			"active_buckets":   authenticatedStats.ActiveBuckets,
			"total_timestamps": authenticatedStats.TotalTimestamps,
		}
	}

	return stats
}
