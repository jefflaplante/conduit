package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"conduit/internal/auth"

	"github.com/gorilla/websocket"
)

// newWSTestServer mirrors gateway.handleWebSocket behind the production
// /ws stack (WrapWebSocket): authenticate, RejectUpgrade on failure,
// otherwise upgrade.
func newWSTestServer(t *testing.T, anonMax int) (*httptest.Server, *auth.TokenStorage, *RateLimitMiddleware) {
	t.Helper()
	db, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	storage := auth.NewTokenStorage(db, "test-secret")

	cfg := DefaultRateLimitConfig()
	cfg.Anonymous.MaxRequests = anonMax
	cfg.Authenticated.MaxRequests = 50
	rl := NewRateLimitMiddleware(RateLimitMiddlewareConfig{Config: cfg})
	t.Cleanup(rl.Stop)

	wsAuth := NewWebSocketAuthenticator(storage)
	upgrader := &websocket.Upgrader{}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		res := wsAuth.Authenticate(r)
		if !res.Authenticated {
			wsAuth.RejectUpgrade(w, res.Error)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = conn.Close()
	})
	srv := httptest.NewServer(rl.WrapWebSocket(h))
	t.Cleanup(srv.Close)
	return srv, storage, rl
}

func dialWS(t *testing.T, srv *httptest.Server, token string) int {
	t.Helper()
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	hdr := http.Header{}
	if token != "" {
		hdr.Set("Authorization", "Bearer "+token)
	}
	conn, resp, err := websocket.DefaultDialer.Dial(u, hdr)
	if err == nil {
		_ = conn.Close()
		return http.StatusSwitchingProtocols
	}
	if resp == nil {
		t.Fatalf("dial: %v", err)
	}
	return resp.StatusCode
}

// conduit-31jg.73: /ws auth failures must consume the per-IP pre-auth
// failure budget; once exhausted, further attempts get 429 without reaching
// token validation.
func TestWebSocket_AuthFailuresFeedPreAuthBudget(t *testing.T) {
	srv, _, _ := newWSTestServer(t, 3)

	var rejected, limited int
	for i := 0; i < 8; i++ {
		switch code := dialWS(t, srv, "conduit_bogus"); code {
		case http.StatusUnauthorized, http.StatusForbidden:
			rejected++
		case http.StatusTooManyRequests:
			limited++
		default:
			t.Fatalf("attempt %d: unexpected status %d", i, code)
		}
	}
	if rejected != 3 || limited != 5 {
		t.Fatalf("rejected=%d limited=%d, want 3/5", rejected, limited)
	}
}

// A valid token still upgrades through the pre-auth wrapper (hijack goes
// through statusRecorder.Unwrap) and is not counted as a failure.
func TestWebSocket_ValidTokenUpgradesThroughPreAuth(t *testing.T) {
	srv, storage, rl := newWSTestServer(t, 5)
	tok := createTestToken(t, storage, "owner", nil)
	for i := 0; i < 3; i++ {
		if code := dialWS(t, srv, tok); code != http.StatusSwitchingProtocols {
			t.Fatalf("dial %d: status %d, want 101", i, code)
		}
	}
	if used, _, _, _ := rl.authFailLimiter.PeekIdentifier("127.0.0.1"); used != 0 {
		t.Fatalf("successful upgrades charged the auth-failure budget: used=%d", used)
	}
}
