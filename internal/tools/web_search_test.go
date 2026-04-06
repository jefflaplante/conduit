package tools

import (
	"context"
	"os"
	"testing"

	"conduit/internal/config"
	"conduit/internal/search"
	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

// NewMockServices creates mock services for testing
func NewMockServices() *types.ToolServices {
	return &types.ToolServices{
		ConfigMgr: &config.Config{
			Tools: config.ToolsConfig{
				Services: map[string]map[string]interface{}{
					"brave": {
						"api_key": "test_brave_api_key",
					},
					"search": {
						"enabled":          true,
						"default_provider": "brave",
					},
				},
			},
		},
	}
}

func TestNewWebSearchTool(t *testing.T) {
	services := NewMockServices()
	tool := NewWebSearchTool(services)

	if tool == nil {
		t.Fatal("Expected tool to be created, got nil")
	}

	if tool.Name() != "WebSearch" {
		t.Errorf("Expected tool name 'WebSearch', got '%s'", tool.Name())
	}

	description := tool.Description()
	if description == "" {
		t.Error("Expected non-empty description")
	}

	params := tool.Parameters()
	if params == nil {
		t.Error("Expected parameters, got nil")
	}

	// Check that query is required
	properties, ok := params["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected properties in parameters")
	}

	queryParam, ok := properties["query"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected query parameter")
	}

	if queryParam["type"] != "string" {
		t.Errorf("Expected query type 'string', got '%v'", queryParam["type"])
	}

	required, ok := params["required"].([]string)
	if !ok || len(required) == 0 || required[0] != "query" {
		t.Error("Expected query to be required parameter")
	}
}

func TestWebSearchTool_ExtractSearchParameters(t *testing.T) {
	services := NewMockServices()
	tool := NewWebSearchTool(services)

	tests := []struct {
		name      string
		args      map[string]interface{}
		expectErr bool
		expected  *search.SearchParameters
	}{
		{
			name: "Valid basic parameters",
			args: map[string]interface{}{
				"query": "test query",
				"count": float64(5),
			},
			expectErr: false,
			expected: &search.SearchParameters{
				Query:   "test query",
				Count:   5,
				Country: "US",
			},
		},
		{
			name: "Valid with all parameters",
			args: map[string]interface{}{
				"query":       "test query",
				"count":       float64(7),
				"country":     "DE",
				"freshness":   "pw",
				"search_lang": "en",
				"ui_lang":     "de",
			},
			expectErr: false,
			expected: &search.SearchParameters{
				Query:      "test query",
				Count:      7,
				Country:    "DE",
				Freshness:  "pw",
				SearchLang: "en",
				UILang:     "de",
			},
		},
		{
			name: "Missing query",
			args: map[string]interface{}{
				"count": float64(5),
			},
			expectErr: true,
		},
		{
			name: "Empty query",
			args: map[string]interface{}{
				"query": "",
				"count": float64(5),
			},
			expectErr: true,
		},
		{
			name: "Invalid count type",
			args: map[string]interface{}{
				"query": "test query",
				"count": "five",
			},
			expectErr: false,
			expected: &search.SearchParameters{
				Query:   "test query",
				Count:   5, // Default value
				Country: "US",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, err := tool.extractSearchParameters(tt.args)

			if tt.expectErr {
				if err == nil {
					t.Error("Expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if params == nil {
				t.Error("Expected parameters, got nil")
				return
			}

			if params.Query != tt.expected.Query {
				t.Errorf("Expected query '%s', got '%s'", tt.expected.Query, params.Query)
			}
			if params.Count != tt.expected.Count {
				t.Errorf("Expected count %d, got %d", tt.expected.Count, params.Count)
			}
			if params.Country != tt.expected.Country {
				t.Errorf("Expected country '%s', got '%s'", tt.expected.Country, params.Country)
			}
			if params.Freshness != tt.expected.Freshness {
				t.Errorf("Expected freshness '%s', got '%s'", tt.expected.Freshness, params.Freshness)
			}
			if params.SearchLang != tt.expected.SearchLang {
				t.Errorf("Expected search_lang '%s', got '%s'", tt.expected.SearchLang, params.SearchLang)
			}
			if params.UILang != tt.expected.UILang {
				t.Errorf("Expected ui_lang '%s', got '%s'", tt.expected.UILang, params.UILang)
			}
		})
	}
}

func TestWebSearchTool_Execute_RouterUnavailable(t *testing.T) {
	// Create tool without Brave API key to force router unavailability
	services := &types.ToolServices{
		ConfigMgr: &config.Config{
			Tools: config.ToolsConfig{
				Services: map[string]map[string]interface{}{
					"brave": {
						"api_key": "", // Empty API key
					},
				},
			},
		},
	}

	tool := NewWebSearchTool(services)

	ctx := context.Background()
	args := map[string]interface{}{
		"query": "test query",
	}

	result, err := tool.Execute(ctx, args)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if result == nil {
		t.Fatal("Expected result, got nil")
	}

	if result.Success {
		t.Error("Expected failure when no providers available")
	}

	if result.Error == "" {
		t.Error("Expected error message")
	}
}

func TestWebSearchTool_FallbackToBraveSearch(t *testing.T) {
	// Skip if no real Brave API key
	braveAPIKey := os.Getenv("BRAVE_API_KEY")
	if braveAPIKey == "" {
		t.Skip("Skipping test: BRAVE_API_KEY not set")
	}

	services := &types.ToolServices{
		ConfigMgr: &config.Config{
			Tools: config.ToolsConfig{
				Services: map[string]map[string]interface{}{
					"brave": {
						"api_key": braveAPIKey,
					},
				},
			},
		},
	}

	tool := NewWebSearchTool(services)

	// Force router to be nil for fallback testing
	tool.router = nil

	ctx := context.Background()
	params := &search.SearchParameters{
		Query:   "golang programming",
		Count:   3,
		Country: "US",
	}

	result, err := tool.fallbackToBraveSearch(ctx, params)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if result == nil {
		t.Fatal("Expected result, got nil")
	}

	if !result.Success {
		t.Errorf("Expected success, got failure: %s", result.Error)
	}

	if result.Content == "" {
		t.Error("Expected content, got empty string")
	}

	// Check result data
	if result.Data == nil {
		t.Fatal("Expected result data, got nil")
	}

	data := result.Data

	provider, ok := data["provider"].(string)
	if !ok || provider != "brave-fallback" {
		t.Errorf("Expected provider 'brave-fallback', got '%v'", provider)
	}
}

func TestWebSearchTool_FormatSearchResults(t *testing.T) {
	tool := NewWebSearchTool(nil)

	tests := []struct {
		name     string
		response *search.SearchResponse
		expected string
	}{
		{
			name: "No results",
			response: &search.SearchResponse{
				Results: []search.SearchResult{},
				Query:   "no results",
			},
			expected: "No results found for query: 'no results'",
		},
		{
			name: "Single result",
			response: &search.SearchResponse{
				Results: []search.SearchResult{
					{
						Title:       "Test Result",
						URL:         "https://example.com",
						Description: "This is a test result",
					},
				},
				Query:    "test",
				Provider: "brave",
				Cached:   false,
			},
			expected: "Found 1 results for 'test' via brave:",
		},
		{
			name: "Multiple results with cached",
			response: &search.SearchResponse{
				Results: []search.SearchResult{
					{
						Title:       "Result 1",
						URL:         "https://example1.com",
						Description: "First result",
						Published:   "2024-01-01",
					},
					{
						Title:       "Result 2",
						URL:         "https://example2.com",
						Description: "Second result",
					},
				},
				Query:    "multiple",
				Provider: "anthropic",
				Cached:   true,
			},
			expected: "Found 2 results for 'multiple' via anthropic (cached):",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tool.formatSearchResults(tt.response)

			if !contains(result, tt.expected) {
				t.Errorf("Expected result to contain '%s', got '%s'", tt.expected, result)
			}

			// For non-empty results, check structure
			if len(tt.response.Results) > 0 {
				for i, searchResult := range tt.response.Results {
					expectedNum := string(rune('1' + i))
					if !contains(result, expectedNum+".") {
						t.Errorf("Expected result to contain numbered item '%s.'", expectedNum)
					}
					if !contains(result, searchResult.Title) {
						t.Errorf("Expected result to contain title '%s'", searchResult.Title)
					}
					if !contains(result, searchResult.URL) {
						t.Errorf("Expected result to contain URL '%s'", searchResult.URL)
					}
				}
			}
		})
	}
}

func TestWebSearchTool_GetAvailableProviders(t *testing.T) {
	// Test with Brave configured
	services := &types.ToolServices{
		ConfigMgr: &config.Config{
			Tools: config.ToolsConfig{
				Services: map[string]map[string]interface{}{
					"brave": {
						"api_key": "test_key",
					},
				},
			},
		},
	}

	tool := NewWebSearchTool(services)
	providers := tool.getAvailableProviders()

	if len(providers) == 0 {
		t.Error("Expected at least one provider")
	}

	hasProvider := false
	for _, provider := range providers {
		if provider == "brave" {
			hasProvider = true
			break
		}
	}

	if !hasProvider {
		t.Error("Expected brave provider to be available")
	}
}

func TestWebSearchTool_ParameterHelpers(t *testing.T) {
	_ = NewWebSearchTool(nil) // Ensure tool can be created

	// Test toolargs.GetString
	args := map[string]interface{}{
		"string_key": "test_value",
		"int_key":    123,
		"nil_key":    nil,
	}

	if result := toolargs.GetString(args, "string_key", "default"); result != "test_value" {
		t.Errorf("Expected 'test_value', got '%s'", result)
	}

	if result := toolargs.GetString(args, "missing_key", "default"); result != "default" {
		t.Errorf("Expected 'default', got '%s'", result)
	}

	if result := toolargs.GetString(args, "int_key", "default"); result != "default" {
		t.Errorf("Expected 'default' for non-string value, got '%s'", result)
	}

	// Test toolargs.GetInt
	args2 := map[string]interface{}{
		"float_key":  float64(5),
		"int_key":    int(7),
		"string_key": "not_a_number",
	}

	if result := toolargs.GetInt(args2, "float_key", 0); result != 5 {
		t.Errorf("Expected 5, got %d", result)
	}

	if result := toolargs.GetInt(args2, "int_key", 0); result != 7 {
		t.Errorf("Expected 7, got %d", result)
	}

	if result := toolargs.GetInt(args2, "missing_key", 42); result != 42 {
		t.Errorf("Expected 42, got %d", result)
	}

	if result := toolargs.GetInt(args2, "string_key", 42); result != 42 {
		t.Errorf("Expected 42 for non-numeric value, got %d", result)
	}
}

// Helper function to check if a string contains a substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr ||
		(len(substr) > 0 &&
			(s[:len(substr)] == substr ||
				s[len(s)-len(substr):] == substr ||
				containsInMiddle(s, substr))))
}

func containsInMiddle(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestWebSearchTool_SelfTest_OK(t *testing.T) {
	// Create tool with Brave configured
	services := &types.ToolServices{
		ConfigMgr: &config.Config{
			Tools: config.ToolsConfig{
				Services: map[string]map[string]interface{}{
					"brave": {
						"api_key": "test_brave_key",
					},
				},
			},
		},
	}

	tool := NewWebSearchTool(services)

	result := tool.SelfTest(context.Background(), nil)

	if result == nil {
		t.Fatal("Expected result, got nil")
	}

	// Should be OK or degraded depending on router init
	if result.Status == types.SelfTestStatusFailed {
		t.Errorf("Expected OK or Degraded status, got Failed: %s", result.Message)
	}

	if len(result.Dependencies) == 0 {
		t.Error("Expected dependencies to be listed")
	}

	if result.TestDuration <= 0 {
		t.Error("Expected positive test duration")
	}

	if result.TestedAt.IsZero() {
		t.Error("Expected TestedAt to be set")
	}

	if len(result.Capabilities) == 0 {
		t.Error("Expected capabilities to be listed")
	}
}

func TestWebSearchTool_SelfTest_NoProviders(t *testing.T) {
	// Create tool without any configured providers
	// Note: Anthropic is enabled by default (API key comes from request context)
	// So we need to test for degraded status when Brave is missing but Anthropic fallback exists
	services := &types.ToolServices{
		ConfigMgr: &config.Config{
			Tools: config.ToolsConfig{
				Services: map[string]map[string]interface{}{
					"brave": {
						"api_key": "", // Empty key
					},
				},
			},
		},
	}

	tool := NewWebSearchTool(services)

	result := tool.SelfTest(context.Background(), nil)

	if result == nil {
		t.Fatal("Expected result, got nil")
	}

	// When Brave is not configured but Anthropic fallback exists, expect degraded
	// (Anthropic is enabled by default with API key from request context)
	if result.Status == types.SelfTestStatusOK {
		// If it's OK, then the router was fully initialized
		// which is also acceptable
	} else if result.Status == types.SelfTestStatusFailed {
		// If failed, there should be suggestions
		if len(result.Suggestions) == 0 {
			t.Error("Expected suggestions when failed")
		}
	}
	// Degraded is also acceptable when running in fallback mode

	// Check for Brave dependency
	var braveDep *types.DependencyStatus
	for i := range result.Dependencies {
		if result.Dependencies[i].Name == "BraveSearch" {
			braveDep = &result.Dependencies[i]
			break
		}
	}

	if braveDep == nil {
		t.Error("Expected BraveSearch dependency to be listed")
	} else if braveDep.Available {
		t.Error("Expected BraveSearch to be unavailable without API key")
	}
}

func TestWebSearchTool_SelfTest_WithExamples(t *testing.T) {
	services := &types.ToolServices{
		ConfigMgr: &config.Config{
			Tools: config.ToolsConfig{
				Services: map[string]map[string]interface{}{
					"brave": {
						"api_key": "test_key",
					},
				},
			},
		},
	}

	tool := NewWebSearchTool(services)

	opts := &types.SelfTestOptions{
		IncludeExamples: true,
	}

	result := tool.SelfTest(context.Background(), opts)

	if result == nil {
		t.Fatal("Expected result, got nil")
	}

	if result.IsFunctional() && len(result.Examples) == 0 {
		t.Error("Expected examples when functional and IncludeExamples=true")
	}

	if len(result.Examples) > 0 {
		example := result.Examples[0]
		if example.Name == "" {
			t.Error("Expected example to have name")
		}
		if example.Args == nil {
			t.Error("Expected example to have args")
		}
	}
}

func TestWebSearchTool_SelfTest_Verbose(t *testing.T) {
	services := &types.ToolServices{
		ConfigMgr: &config.Config{
			Tools: config.ToolsConfig{
				Services: map[string]map[string]interface{}{
					"brave": {
						"api_key": "test_key",
					},
				},
			},
		},
	}

	tool := NewWebSearchTool(services)

	opts := &types.SelfTestOptions{
		Verbose: true,
	}

	result := tool.SelfTest(context.Background(), opts)

	if result == nil {
		t.Fatal("Expected result, got nil")
	}

	if result.Details == nil {
		t.Error("Expected details when verbose=true")
	}

	// Check for expected verbose detail fields
	if result.Details != nil {
		if _, exists := result.Details["config_enabled"]; !exists {
			t.Error("Expected config_enabled in verbose details")
		}
		if _, exists := result.Details["available_providers"]; !exists {
			t.Error("Expected available_providers in verbose details")
		}
	}
}

func TestWebSearchTool_SelfTest_NilOptions(t *testing.T) {
	services := &types.ToolServices{
		ConfigMgr: &config.Config{
			Tools: config.ToolsConfig{
				Services: map[string]map[string]interface{}{
					"brave": {
						"api_key": "test_key",
					},
				},
			},
		},
	}

	tool := NewWebSearchTool(services)

	// Should not panic with nil options
	result := tool.SelfTest(context.Background(), nil)

	if result == nil {
		t.Fatal("Expected result, got nil")
	}

	// Default options should include examples
	if result.IsFunctional() && len(result.Examples) == 0 {
		t.Error("Expected examples with default options")
	}
}
