// Package middleware provides HTTP middleware for the Conduit gateway.
package middleware

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"conduit/internal/auth"
)

// contextKey is a type for context keys to avoid collisions
type contextKey string

const (
	// AuthContextKey is the context key for storing authentication info
	AuthContextKey contextKey = "auth"
)

// AuthInfo contains authenticated client information stored in request context
type AuthInfo struct {
	// TokenID is the unique identifier of the token
	TokenID string
	// ClientName is the name of the authenticated client
	ClientName string
	// ExpiresAt is when the token expires (nil for non-expiring tokens)
	ExpiresAt *time.Time
	// Metadata contains additional token metadata
	Metadata map[string]string
	// Source indicates where the token was extracted from
	Source auth.TokenSource
	// AuthenticatedAt is when this request was authenticated
	AuthenticatedAt time.Time
}

// GetAuthInfo retrieves authentication info from the request context
// Returns nil if the request is not authenticated
func GetAuthInfo(ctx context.Context) *AuthInfo {
	if info, ok := ctx.Value(AuthContextKey).(*AuthInfo); ok {
		return info
	}
	return nil
}

// IsAuthenticated checks if the request context contains valid auth info
func IsAuthenticated(ctx context.Context) bool {
	return GetAuthInfo(ctx) != nil
}

// AuthError represents an authentication error
type AuthError struct {
	// Code is the HTTP status code
	Code int `json:"-"`
	// Error is the error identifier (e.g., "unauthorized", "forbidden")
	Error string `json:"error"`
	// Message is a human-readable description (generic, no details)
	Message string `json:"message"`
}

// Standard auth errors - generic messages to avoid information leakage
var (
	ErrMissingToken = AuthError{
		Code:    http.StatusUnauthorized,
		Error:   "unauthorized",
		Message: "Authentication required",
	}
	ErrMalformedToken = AuthError{
		Code:    http.StatusUnauthorized,
		Error:   "unauthorized",
		Message: "Authentication required",
	}
	ErrInvalidToken = AuthError{
		Code:    http.StatusForbidden,
		Error:   "forbidden",
		Message: "Access denied",
	}
	ErrExpiredToken = AuthError{
		Code:    http.StatusForbidden,
		Error:   "forbidden",
		Message: "Access denied",
	}
)

// AuthMiddleware provides HTTP authentication middleware
type AuthMiddleware struct {
	storage     *auth.TokenStorage
	extractor   *auth.TokenExtractor
	skipPaths   map[string]bool
	onAuthError func(r *http.Request, err AuthError)
}

// AuthMiddlewareConfig contains configuration for AuthMiddleware
type AuthMiddlewareConfig struct {
	// SkipPaths is a list of paths that don't require authentication
	SkipPaths []string
	// OnAuthError is called when authentication fails (for logging/metrics)
	OnAuthError func(r *http.Request, err AuthError)
}

// NewAuthMiddleware creates a new authentication middleware
func NewAuthMiddleware(storage *auth.TokenStorage, config AuthMiddlewareConfig) *AuthMiddleware {
	skipPaths := make(map[string]bool)
	for _, path := range config.SkipPaths {
		skipPaths[path] = true
	}

	return &AuthMiddleware{
		storage:     storage,
		extractor:   auth.NewTokenExtractor(),
		skipPaths:   skipPaths,
		onAuthError: config.OnAuthError,
	}
}

// Wrap wraps an http.Handler with authentication
func (m *AuthMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if this path should skip authentication
		if m.skipPaths[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}

		// Extract token from request
		extracted := m.extractor.Extract(r)

		// Handle missing or malformed token
		if extracted.Token == "" {
			if extracted.IsMalformed {
				m.sendError(w, r, ErrMalformedToken)
			} else {
				m.sendError(w, r, ErrMissingToken)
			}
			return
		}

		// Validate token against database
		tokenInfo, err := m.storage.ValidateToken(extracted.Token)
		if err != nil {
			// Log the error for debugging (without exposing token)
			log.Printf("[Auth] Token validation failed from %s (source: %s): %v",
				r.RemoteAddr, extracted.Source, sanitizeError(err))

			// Check if it's an expiration error vs invalid token
			if isExpiredError(err) {
				m.sendError(w, r, ErrExpiredToken)
			} else {
				m.sendError(w, r, ErrInvalidToken)
			}
			return
		}

		// Create auth info for context
		authInfo := &AuthInfo{
			TokenID:         tokenInfo.TokenID,
			ClientName:      tokenInfo.ClientName,
			ExpiresAt:       tokenInfo.ExpiresAt,
			Metadata:        tokenInfo.Metadata,
			Source:          extracted.Source,
			AuthenticatedAt: time.Now(),
		}

		// Log successful authentication (without token details)
		log.Printf("[Auth] Request authenticated: client=%s path=%s source=%s",
			tokenInfo.ClientName, r.URL.Path, extracted.Source)

		// Add auth info to context and continue
		ctx := context.WithValue(r.Context(), AuthContextKey, authInfo)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// WrapFunc wraps an http.HandlerFunc with authentication
func (m *AuthMiddleware) WrapFunc(next http.HandlerFunc) http.HandlerFunc {
	return m.Wrap(next).ServeHTTP
}

// Handler returns the middleware as an http.Handler chain function
func (m *AuthMiddleware) Handler(next http.Handler) http.Handler {
	return m.Wrap(next)
}

// sendError sends an authentication error response
func (m *AuthMiddleware) sendError(w http.ResponseWriter, r *http.Request, authErr AuthError) {
	// Call error callback if configured
	if m.onAuthError != nil {
		m.onAuthError(r, authErr)
	}

	// Set security headers
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", `Bearer realm="conduit"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")

	// Prepare for rate limiting integration (OCGO-005)
	// These headers will be set by the rate limiter middleware
	// w.Header().Set("X-RateLimit-Remaining", "...")
	// w.Header().Set("Retry-After", "...")

	w.WriteHeader(authErr.Code)

	response := struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}{
		Error:   authErr.Error,
		Message: authErr.Message,
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		// If JSON encoding fails, we've already written the status code
		log.Printf("[Auth] Failed to encode error response: %v", err)
	}
}

// RequireAuth is a standalone middleware function for simple use cases
func RequireAuth(storage *auth.TokenStorage, next http.Handler) http.Handler {
	m := NewAuthMiddleware(storage, AuthMiddlewareConfig{})
	return m.Wrap(next)
}

// RequireAuthFunc is a standalone middleware function for http.HandlerFunc
func RequireAuthFunc(storage *auth.TokenStorage, next http.HandlerFunc) http.HandlerFunc {
	m := NewAuthMiddleware(storage, AuthMiddlewareConfig{})
	return m.WrapFunc(next)
}

// sanitizeError removes any token-related information from error messages
func sanitizeError(err error) string {
	if err == nil {
		return "unknown error"
	}

	errStr := err.Error()

	// Generic error descriptions for logging (no token values)
	switch {
	case contains(errStr, "expired"):
		return "token expired"
	case contains(errStr, "invalid"):
		return "invalid token"
	case contains(errStr, "required"):
		return "token required"
	default:
		return "validation failed"
	}
}

// isExpiredError checks if an error indicates token expiration
func isExpiredError(err error) bool {
	if err == nil {
		return false
	}
	return contains(err.Error(), "expired")
}

// contains is a simple case-insensitive substring check
func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			sc := s[i+j]
			pc := substr[j]
			// Simple ASCII case-insensitive
			if sc >= 'A' && sc <= 'Z' {
				sc += 32
			}
			if pc >= 'A' && pc <= 'Z' {
				pc += 32
			}
			if sc != pc {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
