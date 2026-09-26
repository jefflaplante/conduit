package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"conduit/internal/auth"
)

// newPreAuthTestStack builds the production ordering used by
// gateway_lifecycle.go: IP pre-auth limiter -> auth -> per-client limiter.
func newPreAuthTestStack(t *testing.T, anonMax, authMax int) (http.Handler, *auth.TokenStorage, *RateLimitMiddleware) {
	t.Helper()
	db, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	storage := auth.NewTokenStorage(db, "test-secret")

	cfg := DefaultRateLimitConfig()
	cfg.Anonymous.MaxRequests = anonMax
	cfg.Authenticated.MaxRequests = authMax
	rl := NewRateLimitMiddleware(RateLimitMiddlewareConfig{Config: cfg})
	t.Cleanup(rl.Stop)

	am := NewAuthMiddleware(storage, AuthMiddlewareConfig{})
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	return rl.WrapPreAuth(am.Wrap(rl.Wrap(ok))), storage, rl
}

func doReq(h http.Handler, remote, bearer string) int {
	req := httptest.NewRequest("GET", "/api/channels/status", nil)
	req.RemoteAddr = remote
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// conduit-31jg.4: an unauthenticated flood (bad tokens) must be rate
// limited. Before the fix, auth rejected every request with 403 before the
// limiter ever ran, so the flood was never counted.
func TestPreAuth_UnauthenticatedFloodIsLimited(t *testing.T) {
	h, _, _ := newPreAuthTestStack(t, 3, 50)

	var forbidden, limited int
	for i := 0; i < 20; i++ {
		switch code := doReq(h, "203.0.113.7:4000", "conduit_bogus"); code {
		case http.StatusForbidden:
			forbidden++
		case http.StatusTooManyRequests:
			limited++
		default:
			t.Fatalf("unexpected status %d", code)
		}
	}
	if forbidden != 3 || limited != 17 {
		t.Fatalf("forbidden=%d limited=%d, want 3/17", forbidden, limited)
	}

	// Missing-token flood is limited too.
	for i := 0; i < 5; i++ {
		doReq(h, "203.0.113.8:4000", "")
	}
	if code := doReq(h, "203.0.113.8:4000", ""); code != http.StatusTooManyRequests {
		t.Fatalf("missing-token flood not limited: %d", code)
	}
}

// conduit-31jg.4: legitimate authenticated clients must keep their
// per-client (authenticated tier) budget, not be squeezed to the anonymous
// tier by the new IP gate.
func TestPreAuth_AuthenticatedClientKeepsAuthTier(t *testing.T) {
	h, storage, _ := newPreAuthTestStack(t, 2, 8)
	tok := createTestToken(t, storage, "owner", nil)

	for i := 0; i < 8; i++ {
		if code := doReq(h, "198.51.100.1:5000", tok); code != http.StatusOK {
			t.Fatalf("request %d: status %d, want 200", i, code)
		}
	}
	if code := doReq(h, "198.51.100.1:5000", tok); code != http.StatusTooManyRequests {
		t.Fatalf("9th request: status %d, want 429", code)
	}
}

// conduit-31jg.4: failures from one IP must not lock out other IPs.
func TestPreAuth_FloodIsPerIP(t *testing.T) {
	h, storage, _ := newPreAuthTestStack(t, 2, 8)
	tok := createTestToken(t, storage, "owner", nil)
	for i := 0; i < 10; i++ {
		doReq(h, "203.0.113.9:1", "bad")
	}
	if code := doReq(h, "198.51.100.2:1", tok); code != http.StatusOK {
		t.Fatalf("other IP blocked: %d", code)
	}
}

// conduit-31jg.4: IPv6 clients are keyed by /64 so a single host cannot
// rotate through its prefix to evade the limiter.
func TestPreAuth_IPv6KeyedBySlash64(t *testing.T) {
	h, _, _ := newPreAuthTestStack(t, 2, 50)
	doReq(h, "[2001:db8:1:2::1]:1", "bad")
	doReq(h, "[2001:db8:1:2::2]:1", "bad")
	if code := doReq(h, "[2001:db8:1:2:ffff::3]:1", "bad"); code != http.StatusTooManyRequests {
		t.Fatalf("same /64 not shared: %d", code)
	}
	// A different /64 is independent.
	if code := doReq(h, "[2001:db8:1:3::1]:1", "bad"); code != http.StatusForbidden {
		t.Fatalf("different /64 affected: %d", code)
	}
}

func TestRateLimitKey(t *testing.T) {
	cases := map[string]string{
		"203.0.113.7":          "203.0.113.7",
		"2001:db8:1:2::1":      "2001:db8:1:2::/64",
		"2001:db8:1:2:aaaa::9": "2001:db8:1:2::/64",
		"::ffff:203.0.113.7":   "203.0.113.7",
		"not-an-ip":            "not-an-ip",
	}
	for in, want := range cases {
		if got := rateLimitKey(in); got != want {
			t.Errorf("rateLimitKey(%q)=%q want %q", in, got, want)
		}
	}
}

// conduit-31jg.4: the ?token= query parameter is no longer accepted on
// plain HTTP routes (it leaks into proxy/access logs); WebSocket upgrades
// still accept it (browsers cannot set headers on WS).
func TestAuthMiddleware_QueryParamRejectedOnHTTP(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	storage := auth.NewTokenStorage(db, "test-secret")
	token := createTestToken(t, storage, "query-client", nil)

	m := NewAuthMiddleware(storage, AuthMiddlewareConfig{})
	h := m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	req := httptest.NewRequest("GET", "/metrics?token="+token, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", rec.Code)
	}
}
