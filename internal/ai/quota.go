package ai

import (
	"strings"
)

// IsQuotaError determines if an error is a quota-exhaustion error.
// Used for fallback retry logic in bd-6tb.
//
// Deliberately NARROW: Claude Max quota exhaustion presents as HTTP 400 with
// "out of extra usage" (bd-8dy: 514 occurrences, zero 401s). Auth failures
// (401/403) must propagate so callers see credential problems instead of
// silently falling back to another provider — see
// TestGenerateResponseSmart_NoFallbackOnAuthError for the contract.
func IsQuotaError(err error) bool {
	if err == nil {
		return false
	}

	msg := strings.ToLower(err.Error())

	// Claude Max (and similar) send HTTP 400 with quota indicators in the body.
	if strings.Contains(msg, "400") {
		if strings.Contains(msg, "out of extra usage") ||
			strings.Contains(msg, "quota") ||
			strings.Contains(msg, "credit") ||
			strings.Contains(msg, "balance") ||
			strings.Contains(msg, "limit") ||
			strings.Contains(msg, "exceeded") ||
			strings.Contains(msg, "insufficient") {
			return true
		}
	}

	return false
}