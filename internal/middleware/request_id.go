package middleware

import (
	"net/http"

	"conduit/internal/logging"
)

// RequestIDMiddleware injects a request_id into every request's context and
// sets the X-Request-ID response header so callers can correlate logs.
type RequestIDMiddleware struct{}

// NewRequestIDMiddleware creates a new RequestIDMiddleware.
func NewRequestIDMiddleware() *RequestIDMiddleware {
	return &RequestIDMiddleware{}
}

// Wrap returns an http.Handler that adds a request_id to the context.
// If the incoming request already carries an X-Request-ID header that value
// is reused; otherwise a new random ID is generated.
func (m *RequestIDMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		incoming := r.Header.Get("X-Request-ID")
		ctx, reqID := logging.ContextWithRequestID(r.Context(), incoming)
		w.Header().Set("X-Request-ID", reqID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Handler is an alias for Wrap to satisfy common middleware chain conventions.
func (m *RequestIDMiddleware) Handler(next http.Handler) http.Handler {
	return m.Wrap(next)
}
