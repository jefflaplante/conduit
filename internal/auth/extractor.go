// Package auth provides authentication token management and extraction.
package auth

import (
	"net/http"
	"strings"
)

// TokenSource indicates where a token was extracted from
type TokenSource int

const (
	// TokenSourceNone indicates no token was found
	TokenSourceNone TokenSource = iota
	// TokenSourceBearerHeader indicates token from Authorization: Bearer header
	TokenSourceBearerHeader
	// TokenSourceAPIKeyHeader indicates token from X-API-Key header
	TokenSourceAPIKeyHeader
	// TokenSourceQueryParam indicates token from ?token= query parameter
	TokenSourceQueryParam
	// TokenSourceWebSocketProtocol indicates token from Sec-WebSocket-Protocol header
	TokenSourceWebSocketProtocol
)

// String returns the human-readable name of the token source
func (s TokenSource) String() string {
	switch s {
	case TokenSourceBearerHeader:
		return "bearer_header"
	case TokenSourceAPIKeyHeader:
		return "api_key_header"
	case TokenSourceQueryParam:
		return "query_param"
	case TokenSourceWebSocketProtocol:
		return "websocket_protocol"
	default:
		return "none"
	}
}

// ExtractedToken contains the extracted token and metadata about extraction
type ExtractedToken struct {
	// Token is the raw token value (may be empty if not found)
	Token string
	// Source indicates where the token was extracted from
	Source TokenSource
	// IsMalformed indicates the token location was found but format was wrong
	// (e.g., "Authorization: Bearer" with no actual token)
	IsMalformed bool
}

// TokenExtractor handles extraction of authentication tokens from HTTP requests
type TokenExtractor struct {
	// extractors is the ordered list of extraction functions to try
	extractors []func(*http.Request) ExtractedToken
}

// NewTokenExtractor creates a new TokenExtractor with default extraction sources
// Extraction is attempted in priority order:
// 1. Authorization: Bearer <token>
// 2. X-API-Key: <token>
// 3. ?token=<token>
func NewTokenExtractor() *TokenExtractor {
	return &TokenExtractor{
		extractors: []func(*http.Request) ExtractedToken{
			extractFromBearerHeader,
			extractFromAPIKeyHeader,
			extractFromQueryParam,
		},
	}
}

// NewWebSocketTokenExtractor creates an extractor for WebSocket connections
// that also checks the Sec-WebSocket-Protocol header
// Priority order:
// 1. Authorization: Bearer <token>
// 2. X-API-Key: <token>
// 3. Sec-WebSocket-Protocol: conduit-auth, <token>
// 4. ?token=<token>
func NewWebSocketTokenExtractor() *TokenExtractor {
	return &TokenExtractor{
		extractors: []func(*http.Request) ExtractedToken{
			extractFromBearerHeader,
			extractFromAPIKeyHeader,
			extractFromWebSocketProtocol,
			extractFromQueryParam,
		},
	}
}

// Extract attempts to extract a token from the request using all configured sources
// Returns the first successful extraction or an empty result if no token found
func (e *TokenExtractor) Extract(r *http.Request) ExtractedToken {
	for _, extractor := range e.extractors {
		result := extractor(r)
		// Return on first token found (even if malformed - for proper error messaging)
		if result.Token != "" || result.IsMalformed {
			return result
		}
	}
	return ExtractedToken{Source: TokenSourceNone}
}

// extractFromBearerHeader extracts token from Authorization: Bearer <token>
func extractFromBearerHeader(r *http.Request) ExtractedToken {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return ExtractedToken{}
	}

	// Must start with "Bearer " (case-insensitive per RFC 7235)
	const bearerPrefix = "Bearer "
	if len(auth) < len(bearerPrefix) {
		return ExtractedToken{}
	}

	// Case-insensitive comparison for "Bearer "
	if !strings.EqualFold(auth[:len(bearerPrefix)], bearerPrefix) {
		return ExtractedToken{}
	}

	token := strings.TrimSpace(auth[len(bearerPrefix):])
	if token == "" {
		// "Authorization: Bearer " was present but no token followed
		return ExtractedToken{
			Source:      TokenSourceBearerHeader,
			IsMalformed: true,
		}
	}

	return ExtractedToken{
		Token:  token,
		Source: TokenSourceBearerHeader,
	}
}

// extractFromAPIKeyHeader extracts token from X-API-Key header
func extractFromAPIKeyHeader(r *http.Request) ExtractedToken {
	apiKey := r.Header.Get("X-API-Key")
	if apiKey == "" {
		return ExtractedToken{}
	}

	token := strings.TrimSpace(apiKey)
	if token == "" {
		return ExtractedToken{
			Source:      TokenSourceAPIKeyHeader,
			IsMalformed: true,
		}
	}

	return ExtractedToken{
		Token:  token,
		Source: TokenSourceAPIKeyHeader,
	}
}

// extractFromQueryParam extracts token from ?token= query parameter
func extractFromQueryParam(r *http.Request) ExtractedToken {
	token := r.URL.Query().Get("token")
	if token == "" {
		return ExtractedToken{}
	}

	token = strings.TrimSpace(token)
	if token == "" {
		return ExtractedToken{
			Source:      TokenSourceQueryParam,
			IsMalformed: true,
		}
	}

	return ExtractedToken{
		Token:  token,
		Source: TokenSourceQueryParam,
	}
}

// extractFromWebSocketProtocol extracts token from Sec-WebSocket-Protocol header
// Expected format: "conduit-auth, <token>" or "conduit-auth,<token>"
// The protocol "conduit-auth" signals this is an authenticated connection
func extractFromWebSocketProtocol(r *http.Request) ExtractedToken {
	protocols := r.Header.Get("Sec-WebSocket-Protocol")
	if protocols == "" {
		return ExtractedToken{}
	}

	// Split protocols (comma-separated per WebSocket spec)
	parts := strings.Split(protocols, ",")

	// Look for conduit-auth protocol and token
	hasAuthProtocol := false
	var token string

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		if part == "conduit-auth" {
			hasAuthProtocol = true
		} else if hasAuthProtocol && token == "" {
			// The part after conduit-auth is the token
			token = part
		}
	}

	if !hasAuthProtocol {
		return ExtractedToken{}
	}

	if token == "" {
		// conduit-auth protocol was present but no token followed
		return ExtractedToken{
			Source:      TokenSourceWebSocketProtocol,
			IsMalformed: true,
		}
	}

	return ExtractedToken{
		Token:  token,
		Source: TokenSourceWebSocketProtocol,
	}
}

// SanitizeTokenForLogging returns a safe version of a token for logging purposes
// Shows prefix and last 4 chars, masks the middle
func SanitizeTokenForLogging(token string) string {
	if token == "" {
		return "<empty>"
	}

	// For very short tokens, just show asterisks
	if len(token) < 12 {
		return "****"
	}

	// Show first 8 chars (typically includes prefix) and last 4
	return token[:8] + "****" + token[len(token)-4:]
}

// MaskToken completely masks a token for display
func MaskToken(token string) string {
	if token == "" {
		return "<none>"
	}
	return "<redacted>"
}
