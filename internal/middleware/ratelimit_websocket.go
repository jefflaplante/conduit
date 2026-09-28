package middleware

import (
	"net/http"
)

// WrapWebSocket is the /ws stack: IP pre-auth limiter -> WS auth ->
// per-client limiter -> handler.
//
// wsAuth.Wrap validates the token (header, query or subprotocol) and, on
// success, sets the same auth context as the HTTP AuthMiddleware, so the
// per-client limiter applies the authenticated tier to valid clients
// (conduit-31jg.85; previously every /ws client was limited per IP at the
// anonymous tier). The handler still rejects failed auth with 401/403 before
// upgrading (WebSocketAuthenticator.RejectUpgrade, reusing the stored
// result), so WrapPreAuth sees those failures and charges the same per-IP
// auth-failure budget as the REST endpoints (conduit-31jg.73). A successful
// upgrade hijacks the connection through statusRecorder.Unwrap and is never
// counted as a failure. A nil wsAuth leaves every /ws client anonymous.
func (m *RateLimitMiddleware) WrapWebSocket(wsAuth *WebSocketAuthenticator, next http.Handler) http.Handler {
	inner := m.Wrap(next)
	if wsAuth != nil {
		inner = wsAuth.Wrap(inner)
	}
	return m.WrapPreAuth(inner)
}
