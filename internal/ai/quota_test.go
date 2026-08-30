package ai

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsQuotaError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "random error",
			err:      errors.New("random error"),
			expected: false,
		},
		{
			name:     "400 with quota keyword",
			err:      errors.New("API error: 400 - quota exceeded"),
			expected: true,
		},
		{
			name:     "400 with credit keyword",
			err:      errors.New("API error: 400 - credit balance insufficient"),
			expected: true,
		},
		{
			name:     "400 with limit keyword",
			err:      errors.New("API error: 400 - rate limit exceeded"),
			expected: true,
		},
		{
			name:     "401 with quota keyword — NOT quota (auth propagates, bd-8dy: zero 401s in incident)",
			err:      errors.New("API error: 401 - quota limit reached"),
			expected: false,
		},
		{
			name:     "401 with exceeded keyword — NOT quota (auth propagates)",
			err:      errors.New("API error: 401 - request limit exceeded"),
			expected: false,
		},
		{
			name:     "400 without quota indicators",
			err:      errors.New("API error: 400 - bad request"),
			expected: false,
		},
		{
			name:     "401 without quota indicators",
			err:      errors.New("API error: 401 - unauthorized"),
			expected: false,
		},
		{
			name:     "429 rate limit",
			err:      errors.New("API error: 429 - too many requests"),
			expected: false,
		},
		{
			name:     "500 internal error",
			err:      errors.New("API error: 500 - internal server error"),
			expected: false,
		},
		{
			name:     "quota error with JSON",
			err:      errors.New(`API error: 400 - {"error":{"message":"quota exceeded","type":"quota_error"}}`),
			expected: true,
		},
		{
			name:     "400 with insufficient keyword",
			err:      errors.New("API error: 400 - insufficient credits"),
			expected: true,
		},
		{
			name:     "401 with balance keyword — NOT quota (auth propagates)",
			err:      errors.New("API error: 401 - account balance too low"),
			expected: false,
		},
		{
			name:     "generic error with 400 but no quota",
			err:      errors.New("400 bad request"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsQuotaError(tt.err)
			assert.Equal(t, tt.expected, result, "IsQuotaError(%v) should return %v", tt.err, tt.expected)
		})
	}
}