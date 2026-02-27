package middleware

import (
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"conduit/internal/auth"
)

// WebSocketAuthenticator handles authentication for WebSocket connections
// WebSocket auth happens during the HTTP upgrade handshake, not as middleware
type WebSocketAuthenticator struct {
	storage   *auth.TokenStorage
	extractor *auth.TokenExtractor
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

// NewWebSocketAuthenticator creates a new WebSocket authenticator
func NewWebSocketAuthenticator(storage *auth.TokenStorage) *WebSocketAuthenticator {
	return &WebSocketAuthenticator{
		storage:   storage,
		extractor: auth.NewWebSocketTokenExtractor(),
	}
}

// Authenticate validates a WebSocket upgrade request
// This should be called before upgrading the connection
// Returns auth result with protocol to echo if using subprotocol auth
func (a *WebSocketAuthenticator) Authenticate(r *http.Request) WebSocketAuthResult {
	// Extract token from request (supports all sources including WS subprotocol)
	extracted := a.extractor.Extract(r)

	// Handle missing or malformed token
	if extracted.Token == "" {
		if extracted.IsMalformed {
			log.Printf("[WS Auth] Malformed token from %s (source: %s)",
				r.RemoteAddr, extracted.Source)
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
		log.Printf("[WS Auth] Token validation failed from %s (source: %s): %v",
			r.RemoteAddr, extracted.Source, sanitizeError(err))

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

	log.Printf("[WS Auth] Connection authenticated: client=%s source=%s",
		tokenInfo.ClientName, extracted.Source)

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

// AuthenticatedUpgrader wraps websocket.Upgrader with authentication
type AuthenticatedUpgrader struct {
	*websocket.Upgrader
	auth *WebSocketAuthenticator
}

// NewAuthenticatedUpgrader creates an upgrader that requires authentication
func NewAuthenticatedUpgrader(storage *auth.TokenStorage, upgrader *websocket.Upgrader) *AuthenticatedUpgrader {
	return &AuthenticatedUpgrader{
		Upgrader: upgrader,
		auth:     NewWebSocketAuthenticator(storage),
	}
}

// UpgradeWithAuth upgrades an HTTP connection to WebSocket with authentication
// Returns the connection, auth info, and any error
// If authentication fails, the connection is nil and error describes the failure
func (u *AuthenticatedUpgrader) UpgradeWithAuth(w http.ResponseWriter, r *http.Request) (*websocket.Conn, *AuthInfo, error) {
	// Authenticate first
	result := u.auth.Authenticate(r)
	if !result.Authenticated {
		u.auth.RejectUpgrade(w, result.Error)
		return nil, nil, &AuthenticationError{AuthError: *result.Error}
	}

	// Build response header for protocol negotiation
	var responseHeader http.Header
	if result.ResponseProtocol != "" {
		responseHeader = http.Header{
			"Sec-WebSocket-Protocol": []string{result.ResponseProtocol},
		}
	}

	// Upgrade the connection
	conn, err := u.Upgrader.Upgrade(w, r, responseHeader)
	if err != nil {
		return nil, nil, err
	}

	return conn, result.AuthInfo, nil
}

// AuthenticationError wraps AuthError as an error interface
type AuthenticationError struct {
	AuthError
}

func (e *AuthenticationError) Error() string {
	return e.Message
}

// GetRequestedProtocols extracts the list of requested WebSocket subprotocols
func GetRequestedProtocols(r *http.Request) []string {
	header := r.Header.Get("Sec-WebSocket-Protocol")
	if header == "" {
		return nil
	}

	var protocols []string
	for _, p := range strings.Split(header, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			protocols = append(protocols, p)
		}
	}
	return protocols
}

// HasAuthProtocol checks if the request includes the conduit-auth protocol
func HasAuthProtocol(r *http.Request) bool {
	for _, p := range GetRequestedProtocols(r) {
		if p == "conduit-auth" {
			return true
		}
	}
	return false
}
