package logging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

// requestIDKey is the context key for the request ID
type requestIDKey struct{}

// WithRequestID adds a request ID to the context.
// If requestID is empty, a new random ID is generated.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	if requestID == "" {
		requestID = generateRequestID()
	}
	return context.WithValue(ctx, requestIDKey{}, requestID)
}

// RequestIDFromContext retrieves the request ID from the context.
// Returns an empty string if no request ID is set.
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(requestIDKey{}).(string); ok {
		return id
	}
	return ""
}

// generateRequestID creates a random 8-byte hex-encoded request ID
func generateRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// Fallback to a zero ID if random fails (extremely unlikely)
		return "00000000"
	}
	return hex.EncodeToString(b)
}

// ContextWithRequestID creates a new context with a request ID and returns
// both the context and the generated/provided request ID.
// This is useful when you need to both store and use the request ID.
func ContextWithRequestID(ctx context.Context, requestID string) (context.Context, string) {
	if requestID == "" {
		requestID = generateRequestID()
	}
	return context.WithValue(ctx, requestIDKey{}, requestID), requestID
}
