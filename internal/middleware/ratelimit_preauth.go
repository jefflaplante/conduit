package middleware

import (
	"net/http"
	"time"

	"conduit/internal/logging"
)

// WrapPreAuth wraps an authentication handler with IP-keyed rate limiting
// that runs BEFORE auth. conduit-31jg.4: previously the only limiter ran
// after auth, so requests with missing/invalid tokens were rejected by auth
// (each costing up to two SQLite lookups) without ever being counted.
//
// Intended ordering:
//
//	rl.WrapPreAuth(authMiddleware.Wrap(rl.Wrap(handler)))
//
// Two checks are applied per client IP (IPv6 keyed by /64):
//   - auth-failure budget: once an IP has produced Anonymous.MaxRequests
//     401/403 responses within the anonymous window, further requests are
//     rejected with 429 before auth runs.
//   - overall flood cap: max(Anonymous, Authenticated).MaxRequests per IP,
//     so a legitimate authenticated client still gets its full per-client
//     budget from the inner Wrap.
func (m *RateLimitMiddleware) WrapPreAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !m.config.Enabled || m.preAuthLimiter == nil {
			next.ServeHTTP(w, r)
			return
		}

		ip := rateLimitKey(extractClientIP(r, m.trustProxy))

		// Auth-failure budget exhausted: reject without touching auth.
		if _, remaining, resetAt, _ := m.authFailLimiter.PeekIdentifier(ip); remaining <= 0 {
			retryAfter := int(time.Until(resetAt).Seconds())
			if retryAfter <= 0 {
				retryAfter = 1
			}
			m.rejectPreAuth(w, r, ip, 0, resetAt, retryAfter)
			return
		}

		allowed, remaining, resetAt, retryAfter := m.preAuthLimiter.Allow(ip)
		if !allowed {
			m.rejectPreAuth(w, r, ip, remaining, resetAt, retryAfter)
			return
		}

		sr := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(sr, r)
		if sr.status == http.StatusUnauthorized || sr.status == http.StatusForbidden {
			m.authFailLimiter.Allow(ip)
		}
	})
}

func (m *RateLimitMiddleware) rejectPreAuth(w http.ResponseWriter, r *http.Request, ip string, remaining int, resetAt time.Time, retryAfter int) {
	m.setRateLimitHeaders(w, false, remaining, resetAt, retryAfter, true)
	if m.onRateLimitExceeded != nil {
		m.onRateLimitExceeded(r, ip, true)
	}
	m.logger.Warn("pre-auth rate limit exceeded",
		"request_id", logging.RequestIDFromContext(r.Context()),
		"method", r.Method,
		"path", r.URL.Path,
		"identifier", sanitizeIdentifier(ip, true),
		"identifier_type", "preauth_ip",
	)
	m.sendRateLimitError(w, r, retryAfter)
}

// statusRecorder captures the status code written by the wrapped handler.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach Flush/Hijack on the underlying
// writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
