package ai

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// AIErrorCategory represents the classification of an AI provider error.
type AIErrorCategory int

const (
	CategoryUnknown AIErrorCategory = iota
	CategoryTimeout
	CategoryRateLimit
	CategoryServiceUnavailable
	CategoryAuthentication
	CategoryContextExceeded
)

// CategorizedError carries an explicit error category alongside the message,
// eliminating the need for string-matching re-classification.
type CategorizedError struct {
	Category AIErrorCategory
	Msg      string
}

func (e *CategorizedError) Error() string {
	return e.Msg
}

// userFriendlyMessages maps error categories to human-readable messages.
var userFriendlyMessages = map[AIErrorCategory]string{
	CategoryTimeout:            "The AI service took too long to respond. Please try again.",
	CategoryRateLimit:          "The AI service is temporarily busy. Please wait a moment and try again.",
	CategoryServiceUnavailable: "The AI service is temporarily unavailable. Please try again in a few minutes.",
	CategoryAuthentication:     "There's a configuration issue with the AI service. Please contact the administrator.",
	CategoryContextExceeded:    "Your conversation is too long. Try starting a new conversation or use /clear.",
	CategoryUnknown:            "Sorry, I encountered an error processing your message. Please try again.",
}

// ClassifyError analyzes an error and returns its category.
func ClassifyError(err error) AIErrorCategory {
	if err == nil {
		return CategoryUnknown
	}

	// Check for typed errors first (fast path, no string matching).
	if ce, ok := err.(*CategorizedError); ok {
		return ce.Category
	}
	if _, ok := err.(*RateLimitError); ok {
		return CategoryRateLimit
	}

	msg := strings.ToLower(err.Error())

	// Check for timeout patterns
	if strings.Contains(msg, "context deadline exceeded") ||
		strings.Contains(msg, "client.timeout") ||
		strings.Contains(msg, "timeout") {
		return CategoryTimeout
	}
	// Check for rate limit patterns
	if strings.Contains(msg, "rate limit") ||
		strings.Contains(msg, "429") ||
		strings.Contains(msg, "too many requests") ||
		strings.Contains(msg, "overloaded") {
		return CategoryRateLimit
	}

	// Check for 5xx / service unavailable patterns
	if strings.Contains(msg, "502") ||
		strings.Contains(msg, "503") ||
		strings.Contains(msg, "504") ||
		strings.Contains(msg, "service unavailable") ||
		strings.Contains(msg, "bad gateway") {
		return CategoryServiceUnavailable
	}

	// Check for authentication patterns
	if strings.Contains(msg, "401") ||
		strings.Contains(msg, "403") ||
		strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "forbidden") ||
		strings.Contains(msg, "api key") ||
		strings.Contains(msg, "authentication") {
		return CategoryAuthentication
	}

	// Check for context exceeded patterns
	if (strings.Contains(msg, "context") && strings.Contains(msg, "exceed")) ||
		strings.Contains(msg, "token limit") ||
		strings.Contains(msg, "too long") {
		return CategoryContextExceeded
	}

	return CategoryUnknown
}

// IsTransientTimeoutError reports whether err is a transient timeout worth
// retrying once. Matches context.DeadlineExceeded (including wrapped forms via
// errors.Is and the string form injected by net/http: "context deadline
// exceeded (Client.Timeout exceeded while awaiting headers)").
//
// Deliberately EXCLUDES context.Canceled: cancellation is intentional (user
// abort, session teardown, shutdown), never transient.
//
// Used by the retry-once logic in the router (bd-13p): z.ai calls were dying
// with "context deadline exceeded" on post-tool completion turns, killing
// sub-agent sessions silently (3 occurrences, root-caused via gateway.db).
func IsTransientTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "context deadline exceeded")
}

// GetUserMessage returns a user-friendly message for an error.
func GetUserMessage(err error) string {
	category := ClassifyError(err)
	if msg, ok := userFriendlyMessages[category]; ok {
		return msg
	}
	return userFriendlyMessages[CategoryUnknown]
}

// UserFriendlyError extracts a human-readable message from an AI provider error.
// API errors typically have the format "API error: 400 - {json body}" where the
// JSON body contains an error.message field with a descriptive message.
// Falls back to the original error string if parsing fails.
func UserFriendlyError(err error) string {
	if err == nil {
		return ""
	}

	errStr := err.Error()

	// Try to extract JSON body from "API error: NNN - {json}" format
	idx := strings.Index(errStr, " - {")
	if idx == -1 {
		return errStr
	}

	jsonBody := errStr[idx+3:]
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(jsonBody), &parsed) == nil && parsed.Error.Message != "" {
		return parsed.Error.Message
	}

	return errStr
}

