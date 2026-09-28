package middleware

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
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
	// conduit-31jg.4: IP-keyed limiters that run in front of auth (see
	// WrapPreAuth). preAuthLimiter caps total requests per IP at the larger
	// of the two tier limits; authFailLimiter counts auth failures per IP at
	// the anonymous-tier rate so credential floods are rejected before any
	// token lookup hits SQLite.
	preAuthLimiter      *ratelimit.SlidingWindow
	authFailLimiter     *ratelimit.SlidingWindow
	config              RateLimitConfig
	onRateLimitExceeded func(r *http.Request, identifier string, isAnonymous bool)
	trustProxy          bool
	logger              *slog.Logger
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

	preAuthMax := config.Config.Anonymous.MaxRequests
	preAuthWindow := anonymousWindow
	if config.Config.Authenticated.MaxRequests > preAuthMax {
		preAuthMax = config.Config.Authenticated.MaxRequests
	}
	if authenticatedWindow > preAuthWindow {
		preAuthWindow = authenticatedWindow
	}

	return &RateLimitMiddleware{
		anonymousLimiter:     anonymousLimiter,
		authenticatedLimiter: authenticatedLimiter,
		preAuthLimiter:       ratelimit.NewSlidingWindow(preAuthWindow, preAuthMax, cleanupInterval),
		authFailLimiter: ratelimit.NewSlidingWindow(
			anonymousWindow, config.Config.Anonymous.MaxRequests, cleanupInterval),
		config:              config.Config,
		onRateLimitExceeded: config.OnRateLimitExceeded,
		trustProxy:          config.Config.TrustProxy,
		logger:              logger,
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
			// Anonymous request - use IP-based limiting (IPv6 keyed by /64,
			// conduit-31jg.4)
			identifier = rateLimitKey(extractClientIP(r, m.trustProxy))
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

// Stop stops the rate limiting middleware and cleans up resources
func (m *RateLimitMiddleware) Stop() {
	if m.anonymousLimiter != nil {
		m.anonymousLimiter.Stop()
	}
	if m.authenticatedLimiter != nil {
		m.authenticatedLimiter.Stop()
	}
	if m.preAuthLimiter != nil {
		m.preAuthLimiter.Stop()
	}
	if m.authFailLimiter != nil {
		m.authFailLimiter.Stop()
	}
}
