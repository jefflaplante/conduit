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
	srv := httptest.NewServer(rl.WrapWebSocket(wsAuth, h))
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

// conduit-31jg.85: a valid /ws client is limited per client at the
// authenticated tier, not per IP at the anonymous tier.
func TestWebSocket_AuthenticatedClientGetsAuthenticatedTier(t *testing.T) {
	srv, storage, rl := newWSTestServer(t, 3) // anonymous 3/min, authenticated 50/min
	tok := createTestToken(t, storage, "owner", nil)
	const dials = 10 // well past the anonymous limit
	for i := 0; i < dials; i++ {
		if code := dialWS(t, srv, tok); code != http.StatusSwitchingProtocols {
			t.Fatalf("dial %d: status %d, want 101 (authenticated tier)", i, code)
		}
	}
	if used, _, _, _ := rl.authenticatedLimiter.PeekIdentifier("owner"); used != dials {
		t.Errorf("authenticated limiter used=%d for client, want %d", used, dials)
	}
	if used, _, _, _ := rl.anonymousLimiter.PeekIdentifier("127.0.0.1"); used != 0 {
		t.Errorf("anonymous limiter charged for authenticated client: used=%d", used)
	}
}

// Unauthenticated /ws requests stay on the anonymous (per-IP) tier and
// still charge the pre-auth failure budget.
func TestWebSocket_UnauthenticatedClientGetsAnonymousTier(t *testing.T) {
	srv, _, rl := newWSTestServer(t, 5)
	for i := 0; i < 2; i++ {
		if code := dialWS(t, srv, ""); code != http.StatusUnauthorized {
			t.Fatalf("dial %d: status %d, want 401", i, code)
		}
	}
	if used, _, _, _ := rl.anonymousLimiter.PeekIdentifier("127.0.0.1"); used != 2 {
		t.Errorf("anonymous limiter used=%d, want 2", used)
	}
	if used, _, _, _ := rl.authFailLimiter.PeekIdentifier("127.0.0.1"); used != 2 {
		t.Errorf("auth-failure budget used=%d, want 2", used)
	}
}

// A request that went through WebSocketAuthenticator.Wrap carries the HTTP
// auth context, and Authenticate reuses the stored result (no second token
// validation: deleting the token after Wrap must not change the outcome).
func TestWebSocketAuthenticator_WrapSetsAuthContext(t *testing.T) {
	db, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	storage := auth.NewTokenStorage(db, "test-secret")
	tok := createTestToken(t, storage, "owner", nil)
	wsAuth := NewWebSocketAuthenticator(storage)

	var ctxInfo *AuthInfo
	var res WebSocketAuthResult
	h := wsAuth.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctxInfo = GetAuthInfo(r.Context())
		if _, err := db.Exec(`DELETE FROM auth_tokens`); err != nil {
			t.Fatal(err)
		}
		res = wsAuth.Authenticate(r)
	}))
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if ctxInfo == nil || ctxInfo.ClientName != "owner" {
		t.Fatalf("auth context not set: %+v", ctxInfo)
	}
	if !res.Authenticated || res.AuthInfo != ctxInfo {
		t.Fatalf("Authenticate did not reuse the Wrap result: %+v", res)
	}
}
