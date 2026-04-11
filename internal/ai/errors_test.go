package ai

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUserFriendlyError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: "",
		},
		{
			name:     "context size exceeded",
			err:      fmt.Errorf(`API error: 400 - {"error":{"code":400,"message":"request (32598 tokens) exceeds the available context size (16384 tokens), try increasing it","type":"exceed_context_size_error"}}`),
			expected: "request (32598 tokens) exceeds the available context size (16384 tokens), try increasing it",
		},
		{
			name:     "generic API error with message",
			err:      fmt.Errorf(`API error: 500 - {"error":{"message":"internal server error","type":"server_error"}}`),
			expected: "internal server error",
		},
		{
			name:     "non-JSON error",
			err:      fmt.Errorf("request failed: connection refused"),
			expected: "request failed: connection refused",
		},
		{
			name:     "API error with non-JSON body",
			err:      fmt.Errorf("API error: 502 - Bad Gateway"),
			expected: "API error: 502 - Bad Gateway",
		},
		{
			name:     "API error with empty message field",
			err:      fmt.Errorf(`API error: 400 - {"error":{"type":"bad_request"}}`),
			expected: `API error: 400 - {"error":{"type":"bad_request"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := UserFriendlyError(tt.err)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected AIErrorCategory
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: CategoryUnknown,
		},
		{
			name:     "context deadline exceeded",
			err:      errors.New("context deadline exceeded"),
			expected: CategoryTimeout,
		},
		{
			name:     "Client.Timeout exceeded",
			err:      errors.New("Client.Timeout exceeded while awaiting headers"),
			expected: CategoryTimeout,
		},
		{
			name:     "rate limit exceeded",
			err:      errors.New("rate limit exceeded"),
			expected: CategoryRateLimit,
		},
		{
			name:     "HTTP 429",
			err:      errors.New("HTTP 429: too many requests"),
			expected: CategoryRateLimit,
		},
		{
			name:     "overloaded",
			err:      errors.New("API is overloaded, please try again later"),
			expected: CategoryRateLimit,
		},
		{
			name:     "503 service unavailable",
			err:      errors.New("503 service unavailable"),
			expected: CategoryServiceUnavailable,
		},
		{
			name:     "502 bad gateway",
			err:      errors.New("502 bad gateway"),
			expected: CategoryServiceUnavailable,
		},
		{
			name:     "401 unauthorized",
			err:      errors.New("401 unauthorized"),
			expected: CategoryAuthentication,
		},
		{
			name:     "invalid api key",
			err:      errors.New("invalid api key provided"),
			expected: CategoryAuthentication,
		},
		{
			name:     "context length exceeded",
			err:      errors.New("context length exceeded: maximum is 100000 tokens"),
			expected: CategoryContextExceeded,
		},
		{
			name:     "token limit exceeded",
			err:      errors.New("token limit exceeded"),
			expected: CategoryContextExceeded,
		},
		{
			name:     "random error",
			err:      errors.New("random error"),
			expected: CategoryUnknown,
		},
		{
			name:     "RateLimitError type",
			err:      &RateLimitError{StatusCode: 429, RetryAfterMs: 60000, Message: "rate limited"},
			expected: CategoryRateLimit,
		},
		{
			name:     "CategorizedError authentication",
			err:      &CategorizedError{Category: CategoryAuthentication, Msg: "auth failed"},
			expected: CategoryAuthentication,
		},
		{
			name:     "CategorizedError rate limit",
			err:      &CategorizedError{Category: CategoryRateLimit, Msg: "rate limited"},
			expected: CategoryRateLimit,
		},
		{
			name:     "CategorizedError timeout",
			err:      &CategorizedError{Category: CategoryTimeout, Msg: "timed out"},
			expected: CategoryTimeout,
		},
		{
			name:     "CategorizedError service unavailable",
			err:      &CategorizedError{Category: CategoryServiceUnavailable, Msg: "overloaded"},
			expected: CategoryServiceUnavailable,
		},
		{
			name:     "CategorizedError unknown",
			err:      &CategorizedError{Category: CategoryUnknown, Msg: "something went wrong"},
			expected: CategoryUnknown,
		},
		{
			name:     "too many requests",
			err:      errors.New("error: too many requests"),
			expected: CategoryRateLimit,
		},
		{
			name:     "504 gateway timeout classified as timeout due to timeout keyword",
			err:      errors.New("504 gateway timeout"),
			expected: CategoryTimeout,
		},
		{
			name:     "403 forbidden",
			err:      errors.New("403 forbidden"),
			expected: CategoryAuthentication,
		},
		{
			name:     "authentication failed",
			err:      errors.New("authentication failed"),
			expected: CategoryAuthentication,
		},
		{
			name:     "message too long",
			err:      errors.New("message too long for context window"),
			expected: CategoryContextExceeded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ClassifyError(tt.err)
			assert.Equal(t, tt.expected, result, "ClassifyError(%v) should return %v", tt.err, tt.expected)
		})
	}
}

func TestGetUserMessage(t *testing.T) {
	tests := []struct {
		name            string
		err             error
		expectedContain string
	}{
		{
			name:            "timeout error contains took too long",
			err:             errors.New("context deadline exceeded"),
			expectedContain: "took too long",
		},
		{
			name:            "rate limit error contains temporarily busy",
			err:             errors.New("rate limit exceeded"),
			expectedContain: "temporarily busy",
		},
		{
			name:            "auth error contains configuration issue",
			err:             errors.New("401 unauthorized"),
			expectedContain: "configuration issue",
		},
		{
			name:            "unknown error contains Sorry",
			err:             errors.New("random error"),
			expectedContain: "Sorry",
		},
		{
			name:            "service unavailable contains temporarily unavailable",
			err:             errors.New("503 service unavailable"),
			expectedContain: "temporarily unavailable",
		},
		{
			name:            "context exceeded suggests new conversation",
			err:             errors.New("token limit exceeded"),
			expectedContain: "too long",
		},
		{
			name:            "nil error returns generic sorry message",
			err:             nil,
			expectedContain: "Sorry",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetUserMessage(tt.err)
			assert.Contains(t, result, tt.expectedContain, "GetUserMessage(%v) should contain %q", tt.err, tt.expectedContain)
		})
	}
}
