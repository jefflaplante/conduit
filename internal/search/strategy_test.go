package search

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultSearchParams(t *testing.T) {
	defaults := DefaultSearchParams()

	assert.Equal(t, 5, defaults.Count)
	assert.Equal(t, "US", defaults.Country)
	assert.Empty(t, defaults.Query) // Should be empty by default
}

func TestSearchParams_Validate(t *testing.T) {
	tests := []struct {
		name        string
		params      SearchParams
		expectError bool
		errorType   error
	}{
		{
			name:        "empty query",
			params:      SearchParameters{},
			expectError: true,
			errorType:   ErrInvalidQuery,
		},
		{
			name: "valid basic params",
			params: SearchParameters{
				Query: "test query",
			},
			expectError: false,
		},
		{
			name: "count too low gets corrected",
			params: SearchParameters{
				Query: "test query",
				Count: 0,
			},
			expectError: false,
		},
		{
			name: "count too high gets corrected",
			params: SearchParameters{
				Query: "test query",
				Count: 15,
			},
			expectError: false,
		},
		{
			name: "valid freshness parameter",
			params: SearchParameters{
				Query:     "test query",
				Freshness: "pd",
			},
			expectError: false,
		},
		{
			name: "another valid freshness",
			params: SearchParameters{
				Query:     "test query",
				Freshness: "pw",
			},
			expectError: false,
		},
		{
			name: "invalid freshness parameter",
			params: SearchParameters{
				Query:     "test query",
				Freshness: "invalid",
			},
			expectError: true,
			errorType:   ErrInvalidFreshness,
		},
		{
			name: "all valid parameters",
			params: SearchParameters{
				Query:      "comprehensive test query",
				Count:      8,
				Country:    "DE",
				SearchLang: "de",
				UILang:     "de",
				Freshness:  "pm",
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.params.Validate()

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorType != nil {
					assert.ErrorIs(t, err, tt.errorType)
				}
			} else {
				assert.NoError(t, err)

				// Verify count correction
				if tt.params.Count <= 0 || tt.params.Count > 10 {
					assert.Equal(t, 5, tt.params.Count)
				}
			}
		})
	}
}

func TestSearchParams_ValidateFreshness(t *testing.T) {
	validValues := []string{"pd", "pw", "pm", "py"}

	for _, value := range validValues {
		params := SearchParameters{
			Query:     "test",
			Freshness: value,
		}
		err := params.Validate()
		assert.NoError(t, err, "Freshness value %s should be valid", value)
	}

	invalidValues := []string{"invalid", "day", "week", "month", "year", "1d", "1w"}

	for _, value := range invalidValues {
		params := SearchParameters{
			Query:     "test",
			Freshness: value,
		}
		err := params.Validate()
		assert.Error(t, err, "Freshness value %s should be invalid", value)
		assert.ErrorIs(t, err, ErrInvalidFreshness)
	}
}

func TestSearchParams_CountValidation(t *testing.T) {
	tests := []struct {
		name          string
		inputCount    int
		expectedCount int
	}{
		{"negative count", -1, 5},
		{"zero count", 0, 5},
		{"valid low count", 1, 1},
		{"valid mid count", 5, 5},
		{"valid high count", 10, 10},
		{"too high count", 15, 5},
		{"way too high count", 100, 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := SearchParameters{
				Query: "test query",
				Count: tt.inputCount,
			}

			err := params.Validate()
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedCount, params.Count)
		})
	}
}
