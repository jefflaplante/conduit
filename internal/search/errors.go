package search

import (
	"errors"
)

var (
	// Parameter validation errors
	ErrInvalidQuery     = errors.New("query parameter is required and cannot be empty")
	ErrInvalidCount     = errors.New("count parameter must be between 1 and 10")
	ErrInvalidFreshness = errors.New("freshness parameter must be one of: pd, pw, pm, py")

	// API-related errors
	ErrAPIKeyMissing    = errors.New("API key is required but not configured")
	ErrAPIKeyInvalid    = errors.New("API key is invalid or expired")
	ErrAPIQuotaExceeded = errors.New("API quota exceeded")
	ErrAPIRateLimit     = errors.New("API rate limit exceeded")
	ErrAPIUnauthorized  = errors.New("API access unauthorized")
	ErrAPIServerError   = errors.New("API server error")

	// Network errors
	ErrNetworkTimeout = errors.New("network request timeout")
	ErrNetworkError   = errors.New("network error occurred")

	// Strategy errors
	ErrStrategyUnavailable   = errors.New("search strategy is not available")
	ErrNoStrategiesAvailable = errors.New("no search strategies are available")

	// Cache errors
	ErrCacheError = errors.New("cache operation failed")
)

// IsRetryableError returns true if the error might succeed on retry
func IsRetryableError(err error) bool {
	return errors.Is(err, ErrNetworkTimeout) ||
		errors.Is(err, ErrNetworkError) ||
		errors.Is(err, ErrAPIRateLimit) ||
		errors.Is(err, ErrAPIServerError)
}

// IsAPIQuotaError returns true if the error is related to API quota/billing
func IsAPIQuotaError(err error) bool {
	return errors.Is(err, ErrAPIQuotaExceeded) ||
		errors.Is(err, ErrAPIKeyInvalid) ||
		errors.Is(err, ErrAPIUnauthorized)
}
