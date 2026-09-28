package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"conduit/internal/auth"
	"conduit/internal/logging"
)

// WebSocketAuthenticator handles authentication for WebSocket connections
// WebSocket auth happens during the HTTP upgrade handshake, not as middleware
type WebSocketAuthenticator struct {
	storage   *auth.TokenStorage
	extractor *auth.TokenExtractor
	logger    *slog.Logger
}

// WebSocketAuthResult contains the result of WebSocket authentication
type WebSocketAuthResult struct {
	// Authenticated indicates if authentication succeeded
	Authenticated bool
	// AuthInfo contains auth details if authenticated
	AuthInfo *AuthInfo
	// ResponseProtocol is the protocol to include in upgrade response
	// (should echo back "conduit-auth" if used)
	ResponseProtocol string
	// Error describes the auth failure (nil if authenticated)
	Error *AuthError
}

// WebSocketCloseCode constants for authentication failures
// Using standard WebSocket close codes where appropriate
const (
	// CloseUnauthorized is used when no valid token is provided
	// 4401 maps to HTTP 401 in the 4000-4999 private use range
	CloseUnauthorized = 4401
	// CloseForbidden is used when token is invalid/expired
	// 4403 maps to HTTP 403 in the 4000-4999 private use range
	CloseForbidden = 4403
)

// NewWebSocketAuthenticator creates a new WebSocket authenticator.
// An optional *slog.Logger may be provided; pass nil to use the default logger.
func NewWebSocketAuthenticator(storage *auth.TokenStorage, loggers ...*slog.Logger) *WebSocketAuthenticator {
	var logger *slog.Logger
	if len(loggers) > 0 && loggers[0] != nil {
		logger = loggers[0]
	} else {
		logger = logging.Default()
	}
	return &WebSocketAuthenticator{
		storage:   storage,
		extractor: auth.NewWebSocketTokenExtractor(),
		logger:    logger.With("component", "ws_auth"),
	}
}

// wsAuthResultKey is the context key under which Wrap stores the
// WebSocketAuthResult for the request.
type wsAuthResultKey struct{}

// Wrap authenticates the /ws upgrade request once, up front, and passes it
// on with the result in its context (conduit-31jg.85). On success it also
// sets AuthContextKey — the same context the HTTP AuthMiddleware sets — so a
// RateLimitMiddleware.Wrap placed after it applies the authenticated
// (per-client) tier instead of the anonymous (per-IP) one.
//
// Wrap never rejects: failures pass through unauthenticated, and the handler
// rejects them via Authenticate (which returns the stored result, so the
// token is validated only once) + RejectUpgrade. Their 401/403 still reaches
// WrapPreAuth and charges the per-IP auth-failure budget.
func (a *WebSocketAuthenticator) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := a.authenticate(r)
		ctx := context.WithValue(r.Context(), wsAuthResultKey{}, &result)
		if result.Authenticated && result.AuthInfo != nil {
			ctx = context.WithValue(ctx, AuthContextKey, result.AuthInfo)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Authenticate validates a WebSocket upgrade request
// This should be called before upgrading the connection
// Returns auth result with protocol to echo if using subprotocol auth.
// If the request already went through Wrap, the stored result is returned
// without validating the token again.
func (a *WebSocketAuthenticator) Authenticate(r *http.Request) WebSocketAuthResult {
	if res, ok := r.Context().Value(wsAuthResultKey{}).(*WebSocketAuthResult); ok && res != nil {
		return *res
	}
	return a.authenticate(r)
}

func (a *WebSocketAuthenticator) authenticate(r *http.Request) WebSocketAuthResult {
	// Extract token from request (supports all sources including WS subprotocol)
	extracted := a.extractor.Extract(r)

	// Handle missing or malformed token
	if extracted.Token == "" {
		if extracted.IsMalformed {
			a.logger.Warn("auth failed",
				"request_id", logging.RequestIDFromContext(r.Context()),
				"remote_ip", r.RemoteAddr,
				"source", extracted.Source,
				"reason", "malformed_token",
			)
			return WebSocketAuthResult{
				Error: &ErrMalformedToken,
			}
		}
		return WebSocketAuthResult{
			Error: &ErrMissingToken,
		}
	}

	// Validate token against database
	tokenInfo, err := a.storage.ValidateToken(extracted.Token)
	if err != nil {
		a.logger.Warn("auth failed",
			"request_id", logging.RequestIDFromContext(r.Context()),
			"remote_ip", r.RemoteAddr,
			"source", extracted.Source,
			"reason", sanitizeError(err),
		)

		if isExpiredError(err) {
			return WebSocketAuthResult{Error: &ErrExpiredToken}
		}
		return WebSocketAuthResult{Error: &ErrInvalidToken}
	}

	// Build auth info
	authInfo := &AuthInfo{
		TokenID:         tokenInfo.TokenID,
		ClientName:      tokenInfo.ClientName,
		ExpiresAt:       tokenInfo.ExpiresAt,
		Metadata:        tokenInfo.Metadata,
		Source:          extracted.Source,
		AuthenticatedAt: time.Now(),
	}

	a.logger.Info("connection authenticated",
		"request_id", logging.RequestIDFromContext(r.Context()),
		"client", tokenInfo.ClientName,
		"source", extracted.Source,
	)

	// Determine response protocol
	var responseProtocol string
	if extracted.Source == auth.TokenSourceWebSocketProtocol {
		// Echo back the auth protocol to confirm it's accepted
		responseProtocol = "conduit-auth"
	}

	return WebSocketAuthResult{
		Authenticated:    true,
		AuthInfo:         authInfo,
		ResponseProtocol: responseProtocol,
	}
}

// RejectConnection sends a WebSocket close frame with appropriate error
// This should be called after Upgrade if authentication fails
func (a *WebSocketAuthenticator) RejectConnection(conn *websocket.Conn, authErr *AuthError) {
	closeCode := CloseUnauthorized
	if authErr.Code == http.StatusForbidden {
		closeCode = CloseForbidden
	}

	// Send close message with reason
	message := websocket.FormatCloseMessage(closeCode, authErr.Message)
	conn.WriteControl(websocket.CloseMessage, message, time.Now().Add(time.Second))
	conn.Close()
}

// RejectUpgrade rejects a WebSocket upgrade request before the upgrade happens
// This sends a standard HTTP error response
func (a *WebSocketAuthenticator) RejectUpgrade(w http.ResponseWriter, authErr *AuthError) {
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("WWW-Authenticate", `Bearer realm="conduit"`)
	http.Error(w, authErr.Message, authErr.Code)
}
