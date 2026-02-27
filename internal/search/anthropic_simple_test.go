package search

import (
	"context"
	"testing"
)

// TestNewAnthropicNativeSearch_Simple tests the constructor.
func TestNewAnthropicNativeSearch_Simple(t *testing.T) {
	tests := []struct {
		name      string
		config    AnthropicNativeSearchConfig
		expectErr bool
	}{
		{
			name: "valid config",
			config: AnthropicNativeSearchConfig{
				APIKey: "test-key",
			},
			expectErr: false,
		},
		{
			name:      "missing API key",
			config:    AnthropicNativeSearchConfig{},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			search, err := NewAnthropicNativeSearch(tt.config)
			if tt.expectErr {
				if err == nil {
					t.Error("expected error but got none")
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if search == nil {
					t.Fatal("expected non-nil search instance")
				}
			}
		})
	}
}

// TestAnthropicNativeSearch_Interface tests that AnthropicNativeSearch implements SearchStrategy.
func TestAnthropicNativeSearch_Interface(t *testing.T) {
	var _ SearchStrategy = (*AnthropicNativeSearch)(nil)
}

// TestAnthropicNativeSearch_Name tests the Name method.
func TestAnthropicNativeSearch_Name(t *testing.T) {
	search, err := NewAnthropicNativeSearch(AnthropicNativeSearchConfig{
		APIKey: "test-key",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	name := search.Name()
	if name != "Anthropic Native Web Search" {
		t.Errorf("expected 'Anthropic Native Web Search', got %s", name)
	}
}

// TestAnthropicNativeSearch_IsAvailable tests the IsAvailable method.
func TestAnthropicNativeSearch_IsAvailable(t *testing.T) {
	tests := []struct {
		name     string
		apiKey   string
		expected bool
	}{
		{
			name:     "valid API key",
			apiKey:   "sk-ant-api-test-key",
			expected: true,
		},
		{
			name:     "valid OAuth token",
			apiKey:   "sk-ant-oat-test-token",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			search, err := NewAnthropicNativeSearch(AnthropicNativeSearchConfig{
				APIKey: tt.apiKey,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			available := search.IsAvailable()
			if available != tt.expected {
				t.Errorf("expected IsAvailable to be %v, got %v", tt.expected, available)
			}
		})
	}
}

// TestAnthropicNativeSearch_Search tests basic search functionality.
func TestAnthropicNativeSearch_Search(t *testing.T) {
	search, err := NewAnthropicNativeSearch(AnthropicNativeSearchConfig{
		APIKey: "test-key",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Test with empty query (should fail validation)
	ctx := context.Background()
	params := SearchParameters{
		Query: "",
	}

	_, err = search.Search(ctx, params)
	if err == nil {
		t.Error("expected error for empty query")
	}

	// Test with valid query (will fail due to no mock, but should pass validation)
	params.Query = "test query"
	err = params.Validate()
	if err != nil {
		t.Errorf("expected params to be valid, got error: %v", err)
	}
}

// TestWebSearchToolRequest tests the tool request structure.
func TestWebSearchToolRequest(t *testing.T) {
	maxUses := 5
	request := WebSearchToolRequest{
		Query:        "test query",
		MaxUses:      &maxUses,
		UserLocation: "Seattle, WA, US",
	}

	if request.Query != "test query" {
		t.Errorf("expected query 'test query', got %s", request.Query)
	}
	if *request.MaxUses != 5 {
		t.Errorf("expected max uses 5, got %d", *request.MaxUses)
	}
	if request.UserLocation != "Seattle, WA, US" {
		t.Errorf("expected user location 'Seattle, WA, US', got %s", request.UserLocation)
	}
}

// TestMapAnthropicErrorToSearchError tests the error mapping function.
func TestMapAnthropicErrorToSearchError(t *testing.T) {
	tests := []struct {
		anthropicType string
		expectedErr   error
	}{
		{
			anthropicType: "rate_limit_error",
			expectedErr:   ErrAPIRateLimit,
		},
		{
			anthropicType: "authentication_error",
			expectedErr:   ErrAPIUnauthorized,
		},
		{
			anthropicType: "invalid_request_error",
			expectedErr:   ErrInvalidQuery,
		},
	}

	for _, tt := range tests {
		t.Run(tt.anthropicType, func(t *testing.T) {
			err := mapAnthropicErrorToSearchError(tt.anthropicType)
			if err != tt.expectedErr {
				t.Errorf("expected error %v, got %v", tt.expectedErr, err)
			}
		})
	}
}
