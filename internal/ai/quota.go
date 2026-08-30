package ai

import (
	"strings"
)

// IsQuotaError determines if an error is a quota-exhaustion error (400/401 with quota indicators).
// Used for fallback retry logic in bd-6tb.
func IsQuotaError(err error) bool {
	if err == nil {
		return false
	}

	msg := strings.ToLower(err.Error())

	// Check for 400/401 HTTP codes with quota indicators
	if strings.Contains(msg, "400") || strings.Contains(msg, "401") {
		if strings.Contains(msg, "quota") ||
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