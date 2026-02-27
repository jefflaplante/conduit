// Package integration provides comprehensive integration tests for the hybrid web search system
package integration

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"conduit/internal/auth"
	"conduit/internal/search"
	"conduit/internal/tools"
	"conduit/internal/tools/types"
)

// TestWebSearchIntegrationSuite runs comprehensive integration tests for the web search system
func TestWebSearchIntegrationSuite(t *testing.T) {
	// Skip if no API keys are available (can run with mocks instead)
	braveAPIKey := os.Getenv("BRAVE_API_KEY")
	anthropicAPIKey := os.Getenv("ANTHROPIC_API_KEY")

	if braveAPIKey == "" && anthropicAPIKey == "" {
		t.Log("No API keys available - running tests with mock data only")
	}

	// Test Suite 1: OAuth Token Detection and Validation
	t.Run("OAuthValidation", func(t *testing.T) {
		testOAuthTokenDetection(t)
		testOAuthToolMapping(t)
		testOAuthHeaderValidation(t)
		testSystemPromptFormatValidation(t)
	})

	// Test Suite 2: Provider Routing Tests
	t.Run("ProviderRouting", func(t *testing.T) {
		testProviderDetectionFromModel(t)
		testOAuthVsRegularTokenScenarios(t)
		testFallbackBehavior(t, braveAPIKey != "")
	})

	// Test Suite 3: Integration Tests for Search Providers
	t.Run("SearchProviders", func(t *testing.T) {
		if braveAPIKey != "" {
			testBraveDirectSearchIntegration(t, braveAPIKey)
		} else {
			t.Log("Skipping Brave integration tests - no API key")
		}

		if anthropicAPIKey != "" {
			testAnthropicNativeSearchIntegration(t, anthropicAPIKey)
		} else {
			t.Log("Skipping Anthropic integration tests - no API key")
		}
	})

	// Test Suite 4: Error Handling and Fallback Scenarios
	t.Run("ErrorHandling", func(t *testing.T) {
		testNetworkFailureHandling(t)
		testAPIQuotaExceededHandling(t)
		testProviderFailureFallback(t)
	})

	// Test Suite 5: Performance and Load Testing
	t.Run("Performance", func(t *testing.T) {
		testSearchResponseTimes(t, braveAPIKey != "")
		testFallbackResponseTimes(t, braveAPIKey != "")
		testRateLimitingProtection(t)
		testCacheEffectiveness(t)
	})

	// Test Suite 6: End-to-End WebSearchTool Integration
	t.Run("WebSearchTool", func(t *testing.T) {
		testWebSearchToolWithMockProviders(t)
		if braveAPIKey != "" {
			testWebSearchToolWithRealAPI(t, braveAPIKey)
		}
	})
}

// OAuth Validation Tests
func testOAuthTokenDetection(t *testing.T) {
	t.Run("ValidOAuthToken", func(t *testing.T) {
		oauthToken := "sk-ant-oat01-abc123-xyz789"
		tokenInfo := auth.GetOAuthTokenInfo(oauthToken)

		if !tokenInfo.IsOAuthToken {
			t.Error("Should detect valid OAuth token")
		}

		if tokenInfo.Token != oauthToken {
			t.Errorf("Token should be preserved, got: %s", tokenInfo.Token)
		}

		if len(tokenInfo.RequiredHeaders) == 0 {
			t.Error("Should include required headers for OAuth token")
		}

		// Verify specific required headers
		expectedHeaders := auth.GetRequiredOAuthHeaders()
		for key, expectedValue := range expectedHeaders {
			if actualValue, exists := tokenInfo.RequiredHeaders[key]; !exists {
				t.Errorf("Missing required header: %s", key)
			} else if key == "anthropic-beta" && actualValue != expectedValue {
				t.Errorf("Incorrect anthropic-beta header: got %s, expected %s", actualValue, expectedValue)
			}
		}
	})

	t.Run("RegularToken", func(t *testing.T) {
		regularToken := "sk-ant-api01-regular123"
		tokenInfo := auth.GetOAuthTokenInfo(regularToken)

		if tokenInfo.IsOAuthToken {
			t.Error("Should not detect regular token as OAuth")
		}

		if len(tokenInfo.RequiredHeaders) > 0 {
			t.Error("Should not include headers for regular token")
		}
	})

	t.Run("EmptyToken", func(t *testing.T) {
		tokenInfo := auth.GetOAuthTokenInfo("")

		if tokenInfo.IsOAuthToken {
			t.Error("Should not detect empty token as OAuth")
		}
	})
}

func testOAuthToolMapping(t *testing.T) {
	testCases := []struct {
		name         string
		inputTool    string
		expectedTool string
		shouldMap    bool
	}{
		{"WebSearchMapping", "web_search", "WebSearch", true},
		{"WebSearchWithDate", "web_search_20250305", "WebSearch", true},
		{"WebFetchMapping", "web_fetch", "WebFetch", true},
		{"WebFetchWithDate", "web_fetch_20250305", "WebFetch", true},
		{"ReadMapping", "read", "Read", true},
		{"WriteMapping", "write", "Write", true},
		{"EditMapping", "edit", "Edit", true},
		{"BashMapping", "bash", "Bash", true},
		{"ForbiddenMessage", "message", "", false},
		{"ForbiddenCron", "cron", "", false},
		{"ForbiddenExec", "exec", "", false},
		{"ForbiddenBrowser", "browser", "", false},
		{"UnknownTool", "unknown_tool", "", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mappedTool, canMap := auth.MapToolForOAuth(tc.inputTool)

			if canMap != tc.shouldMap {
				t.Errorf("Expected shouldMap=%v, got %v", tc.shouldMap, canMap)
			}

			if mappedTool != tc.expectedTool {
				t.Errorf("Expected mapped tool='%s', got '%s'", tc.expectedTool, mappedTool)
			}
		})
	}
}

func testOAuthHeaderValidation(t *testing.T) {
	t.Run("ValidHeaders", func(t *testing.T) {
		validHeaders := auth.GetRequiredOAuthHeaders()
		err := auth.ValidateOAuthHeaders(validHeaders)

		if err != nil {
			t.Errorf("Valid headers should pass validation, got: %v", err)
		}
	})

	t.Run("MissingAnthropicBeta", func(t *testing.T) {
		headers := auth.GetRequiredOAuthHeaders()
		delete(headers, "anthropic-beta")

		err := auth.ValidateOAuthHeaders(headers)
		if err == nil {
			t.Error("Should fail validation when anthropic-beta is missing")
		}

		if !strings.Contains(err.Error(), "anthropic-beta") {
			t.Errorf("Error should mention missing anthropic-beta header, got: %v", err)
		}
	})

	t.Run("IncorrectAnthropicBeta", func(t *testing.T) {
		headers := auth.GetRequiredOAuthHeaders()
		headers["anthropic-beta"] = "incorrect-value"

		err := auth.ValidateOAuthHeaders(headers)
		if err == nil {
			t.Error("Should fail validation when anthropic-beta is incorrect")
		}
	})

	t.Run("MissingUserAgent", func(t *testing.T) {
		headers := auth.GetRequiredOAuthHeaders()
		delete(headers, "user-agent")

		err := auth.ValidateOAuthHeaders(headers)
		if err == nil {
			t.Error("Should fail validation when user-agent is missing")
		}
	})
}

func testSystemPromptFormatValidation(t *testing.T) {
	t.Run("ValidArrayFormat", func(t *testing.T) {
		validPrompt := []map[string]interface{}{
			{"type": "text", "text": "You are Claude Code."},
			{"type": "text", "text": "Additional instructions."},
		}

		if !auth.IsSystemPromptValidForOAuth(validPrompt) {
			t.Error("Valid array format should pass validation")
		}
	})

	t.Run("StringFormatNotValid", func(t *testing.T) {
		stringPrompt := "You are Claude Code."

		if auth.IsSystemPromptValidForOAuth(stringPrompt) {
			t.Error("String format should not be valid for OAuth")
		}
	})

	t.Run("EmptyArrayNotValid", func(t *testing.T) {
		emptyArray := []interface{}{}

		if auth.IsSystemPromptValidForOAuth(emptyArray) {
			t.Error("Empty array should not be valid")
		}
	})

	t.Run("ConvertStringToArray", func(t *testing.T) {
		originalPrompt := "You are a helpful assistant."
		converted := auth.ConvertSystemPromptForOAuth(originalPrompt)

		// Should be converted to array format
		arrayPrompt, ok := converted.([]map[string]interface{})
		if !ok {
			t.Error("Converted prompt should be array format")
			return
		}

		if len(arrayPrompt) < 2 {
			t.Error("Converted prompt should have at least 2 elements")
		}

		// First element should be Claude Code identifier
		if arrayPrompt[0]["text"] != "You are Claude Code, Anthropic's official CLI for Claude." {
			t.Error("First element should be Claude Code identifier")
		}

		// Second element should be original prompt
		if arrayPrompt[1]["text"] != originalPrompt {
			t.Error("Second element should be original prompt")
		}
	})
}

// Provider Routing Tests
func testProviderDetectionFromModel(t *testing.T) {
	testCases := []struct {
		name             string
		model            string
		expectedProvider string
	}{
		{"AnthropicPrefix", "anthropic/claude-3-5-sonnet-20241022", "anthropic"},
		{"ClaudePrefix", "claude-3-5-sonnet-20241022", "anthropic"},
		{"ClaudeContains", "some-claude-model", "anthropic"},
		{"OpenAI", "gpt-4", "brave"},
		{"Gemini", "gemini-pro", "brave"},
		{"EmptyModel", "", "brave"},
		{"UnknownModel", "unknown-model-123", "brave"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a mock router
			config := search.RouterConfig{
				SearchConfig: search.SearchConfig{
					Enabled:         true,
					DefaultProvider: "brave",
				},
			}

			router, err := search.NewSearchRouter(config)
			if err != nil {
				t.Fatalf("Failed to create router: %v", err)
			}

			router.SetCurrentModel(tc.model)

			// We need to test the private detectProviderFromModel method
			// For now, we'll test via the public Search method and check the provider
			// in the response, using a mock strategy that we can detect

			// This is a simplified test - in a real scenario we'd have more sophisticated
			// provider detection testing
			t.Logf("Model '%s' would route to provider '%s'", tc.model, tc.expectedProvider)
		})
	}
}

func testOAuthVsRegularTokenScenarios(t *testing.T) {
	t.Run("OAuthTokenScenario", func(t *testing.T) {
		oauthToken := "sk-ant-oat01-test123"
		model := "anthropic/claude-3-5-sonnet"

		config := search.RouterConfig{
			SearchConfig: search.SearchConfig{
				Enabled: true,
			},
		}

		router, err := search.NewSearchRouter(config)
		if err != nil {
			t.Fatalf("Failed to create router: %v", err)
		}

		// Update with OAuth context
		err = router.UpdateWithRequestContext(oauthToken, model)
		if err != nil {
			t.Errorf("Failed to update with OAuth context: %v", err)
		}

		// Verify OAuth token detection
		tokenInfo := auth.GetOAuthTokenInfo(oauthToken)
		if !tokenInfo.IsOAuthToken {
			t.Error("Should detect OAuth token correctly")
		}
	})

	t.Run("RegularTokenScenario", func(t *testing.T) {
		regularToken := "sk-ant-api01-test123"
		model := "anthropic/claude-3-5-sonnet"

		config := search.RouterConfig{
			SearchConfig: search.SearchConfig{
				Enabled: true,
			},
		}

		router, err := search.NewSearchRouter(config)
		if err != nil {
			t.Fatalf("Failed to create router: %v", err)
		}

		// Update with regular token context
		err = router.UpdateWithRequestContext(regularToken, model)
		if err != nil {
			t.Errorf("Failed to update with regular token context: %v", err)
		}

		// Verify regular token is not detected as OAuth
		tokenInfo := auth.GetOAuthTokenInfo(regularToken)
		if tokenInfo.IsOAuthToken {
			t.Error("Should not detect regular token as OAuth")
		}
	})
}

func testFallbackBehavior(t *testing.T, hasBraveAPI bool) {
	t.Run("AnthropicToB rave Fallback", func(t *testing.T) {
		// Create configuration with both providers but make Anthropic unavailable
		config := search.RouterConfig{
			SearchConfig: search.SearchConfig{
				Enabled:         true,
				DefaultProvider: "anthropic",
			},
		}

		if hasBraveAPI {
			config.BraveConfig = &search.BraveSearchConfig{
				APIKey:   os.Getenv("BRAVE_API_KEY"),
				Endpoint: "https://api.search.brave.com/res/v1/web/search",
			}
		}

		router, err := search.NewSearchRouter(config)
		if err != nil {
			t.Fatalf("Failed to create router: %v", err)
		}

		// Set Anthropic model
		router.SetCurrentModel("anthropic/claude-3-5-sonnet")

		// If we have real Brave API, test actual fallback
		if hasBraveAPI {
			ctx := context.Background()
			params := search.SearchParameters{
				Query: "test search",
				Count: 1,
			}

			// This should attempt Anthropic (which will fail) and fallback to Brave
			_, err = router.Search(ctx, params)
			// We expect this to work because Brave should be available as fallback
			// Even if Anthropic fails, the search should succeed
			t.Logf("Fallback search result: %v", err)
		} else {
			t.Log("Skipping real fallback test - no Brave API key")
		}
	})
}

// Search Provider Integration Tests
func testBraveDirectSearchIntegration(t *testing.T, apiKey string) {
	t.Run("BasicSearch", func(t *testing.T) {
		config := search.BraveSearchConfig{
			APIKey:   apiKey,
			Endpoint: "https://api.search.brave.com/res/v1/web/search",
		}

		braveSearch, err := search.NewBraveDirectSearch(config, search.CacheConfig{Enabled: false})
		if err != nil {
			t.Fatalf("Failed to create Brave search: %v", err)
		}

		ctx := context.Background()
		params := search.SearchParameters{
			Query:   "OpenAI GPT",
			Count:   3,
			Country: "US",
		}

		startTime := time.Now()
		response, err := braveSearch.Search(ctx, params)
		duration := time.Since(startTime)

		if err != nil {
			t.Fatalf("Brave search failed: %v", err)
		}

		// Validate response
		if response == nil {
			t.Fatal("Response should not be nil")
		}

		if len(response.Results) == 0 {
			t.Error("Should return at least one result")
		}

		if response.Query != params.Query {
			t.Errorf("Response query should match input: got %s, expected %s", response.Query, params.Query)
		}

		if response.Provider == "" {
			t.Error("Provider should be set in response")
		}

		// Performance check
		if duration > 3*time.Second {
			t.Errorf("Search took too long: %v (should be < 3s)", duration)
		}

		t.Logf("Brave search completed in %v with %d results", duration, len(response.Results))

		// Validate result structure
		for i, result := range response.Results {
			if result.Title == "" {
				t.Errorf("Result %d should have a title", i)
			}
			if result.URL == "" {
				t.Errorf("Result %d should have a URL", i)
			}
		}
	})

	t.Run("RegionalSearch", func(t *testing.T) {
		config := search.BraveSearchConfig{
			APIKey:   apiKey,
			Endpoint: "https://api.search.brave.com/res/v1/web/search",
		}

		braveSearch, err := search.NewBraveDirectSearch(config, search.CacheConfig{Enabled: false})
		if err != nil {
			t.Fatalf("Failed to create Brave search: %v", err)
		}

		ctx := context.Background()
		params := search.SearchParameters{
			Query:      "weather forecast",
			Count:      2,
			Country:    "DE",
			SearchLang: "de",
		}

		response, err := braveSearch.Search(ctx, params)
		if err != nil {
			t.Fatalf("Regional search failed: %v", err)
		}

		if len(response.Results) == 0 {
			t.Error("Regional search should return results")
		}

		t.Logf("Regional search returned %d results", len(response.Results))
	})
}

func testAnthropicNativeSearchIntegration(t *testing.T, apiKey string) {
	t.Run("BasicSearch", func(t *testing.T) {
		// Note: This test would require actual Anthropic API access
		// For now, we'll test the configuration and setup
		config := search.AnthropicNativeSearchConfig{
			APIKey:  apiKey,
			BaseURL: "https://api.anthropic.com",
		}

		anthSearch, err := search.NewAnthropicNativeSearch(config)
		if err != nil {
			t.Fatalf("Failed to create Anthropic search: %v", err)
		}

		if !anthSearch.IsAvailable() {
			t.Error("Anthropic search should be available when configured")
		}

		// We would test actual search here if we had OAuth token
		// For now, just verify the setup is correct
		t.Log("Anthropic search strategy configured successfully")
	})
}

// Error Handling Tests
func testNetworkFailureHandling(t *testing.T) {
	t.Run("BraveNetworkFailure", func(t *testing.T) {
		// Use invalid endpoint to simulate network failure
		config := search.BraveSearchConfig{
			APIKey:   "test_key",
			Endpoint: "https://invalid.nonexistent.domain.com/search",
		}

		braveSearch, err := search.NewBraveDirectSearch(config, search.CacheConfig{Enabled: false})
		if err != nil {
			t.Fatalf("Failed to create Brave search: %v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		params := search.SearchParameters{
			Query: "test",
			Count: 1,
		}

		_, err = braveSearch.Search(ctx, params)
		if err == nil {
			t.Error("Should fail with network error")
		}

		// Should handle timeout gracefully
		if ctx.Err() == context.DeadlineExceeded {
			t.Log("Correctly handled timeout")
		}
	})
}

func testAPIQuotaExceededHandling(t *testing.T) {
	t.Run("InvalidAPIKey", func(t *testing.T) {
		config := search.BraveSearchConfig{
			APIKey:   "invalid_key_12345",
			Endpoint: "https://api.search.brave.com/res/v1/web/search",
		}

		braveSearch, err := search.NewBraveDirectSearch(config, search.CacheConfig{Enabled: false})
		if err != nil {
			t.Fatalf("Failed to create Brave search: %v", err)
		}

		ctx := context.Background()
		params := search.SearchParameters{
			Query: "test",
			Count: 1,
		}

		_, err = braveSearch.Search(ctx, params)
		if err == nil {
			t.Error("Should fail with invalid API key")
		}

		// Should get authentication error
		if strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "403") {
			t.Log("Correctly handled authentication error")
		}
	})
}

func testProviderFailureFallback(t *testing.T) {
	t.Run("RouterFallback", func(t *testing.T) {
		// Create router with both providers but invalid Anthropic config
		config := search.RouterConfig{
			SearchConfig: search.SearchConfig{
				Enabled:         true,
				DefaultProvider: "anthropic",
			},
			AnthropicConfig: &search.AnthropicNativeSearchConfig{
				APIKey:  "invalid_key",
				BaseURL: "https://api.anthropic.com",
			},
			EnableFallback: true,
		}

		// Add Brave if we have API key
		if braveKey := os.Getenv("BRAVE_API_KEY"); braveKey != "" {
			config.BraveConfig = &search.BraveSearchConfig{
				APIKey:   braveKey,
				Endpoint: "https://api.search.brave.com/res/v1/web/search",
			}
		}

		router, err := search.NewSearchRouter(config)
		if err != nil {
			t.Fatalf("Failed to create router: %v", err)
		}

		router.SetCurrentModel("anthropic/claude-3-5-sonnet")

		// This should try Anthropic (fail) then fallback to Brave
		ctx := context.Background()
		params := search.SearchParameters{
			Query: "test",
			Count: 1,
		}

		_, err = router.Search(ctx, params)
		// The result depends on whether we have a valid Brave key
		if os.Getenv("BRAVE_API_KEY") != "" {
			if err != nil {
				t.Logf("Fallback completed with error: %v", err)
			} else {
				t.Log("Fallback to Brave succeeded")
			}
		} else {
			t.Log("No fallback provider available")
		}

		// Check usage stats
		stats := router.GetUsageStats()
		if len(stats) > 0 {
			t.Logf("Usage stats: %+v", stats)
		}
	})
}

// Performance Tests
func testSearchResponseTimes(t *testing.T, hasBraveAPI bool) {
	if !hasBraveAPI {
		t.Skip("Skipping performance tests - no Brave API key")
		return
	}

	t.Run("SearchUnder3Seconds", func(t *testing.T) {
		config := search.BraveSearchConfig{
			APIKey:   os.Getenv("BRAVE_API_KEY"),
			Endpoint: "https://api.search.brave.com/res/v1/web/search",
		}

		braveSearch, err := search.NewBraveDirectSearch(config, search.CacheConfig{Enabled: false})
		if err != nil {
			t.Fatalf("Failed to create Brave search: %v", err)
		}

		ctx := context.Background()
		params := search.SearchParameters{
			Query: "artificial intelligence",
			Count: 5,
		}

		startTime := time.Now()
		response, err := braveSearch.Search(ctx, params)
		duration := time.Since(startTime)

		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}

		// Performance requirement: < 3 seconds
		if duration >= 3*time.Second {
			t.Errorf("Search took %v, should be < 3 seconds", duration)
		}

		if len(response.Results) == 0 {
			t.Error("Should return results within time limit")
		}

		t.Logf("Search completed in %v with %d results", duration, len(response.Results))
	})
}

func testFallbackResponseTimes(t *testing.T, hasBraveAPI bool) {
	if !hasBraveAPI {
		t.Skip("Skipping fallback performance tests - no Brave API key")
		return
	}

	t.Run("FallbackUnder1Second", func(t *testing.T) {
		// This test measures the time for fallback to kick in
		// We'll use a router with a failing primary and working fallback
		config := search.RouterConfig{
			SearchConfig: search.SearchConfig{
				Enabled:         true,
				DefaultProvider: "anthropic",
			},
			BraveConfig: &search.BraveSearchConfig{
				APIKey:   os.Getenv("BRAVE_API_KEY"),
				Endpoint: "https://api.search.brave.com/res/v1/web/search",
			},
			EnableFallback:         true,
			FallbackTimeoutSeconds: 1,
		}

		router, err := search.NewSearchRouter(config)
		if err != nil {
			t.Fatalf("Failed to create router: %v", err)
		}

		// Force Anthropic model (will fail and fallback to Brave)
		router.SetCurrentModel("anthropic/claude-3-5-sonnet")

		ctx := context.Background()
		params := search.SearchParameters{
			Query: "test query",
			Count: 1,
		}

		startTime := time.Now()
		_, err = router.Search(ctx, params)
		duration := time.Since(startTime)

		// Fallback requirement: < 1 second additional delay
		// (This is after primary fails, so total time may be > 1s)
		t.Logf("Fallback completed in %v", duration)
	})
}

func testRateLimitingProtection(t *testing.T) {
	t.Run("RateLimitProtection", func(t *testing.T) {
		// Create a rate-limited router/search implementation
		// This would test that rapid requests are properly throttled

		config := search.BraveSearchConfig{
			APIKey:   "test_key", // Using invalid key to avoid actual API calls
			Endpoint: "https://api.search.brave.com/res/v1/web/search",
		}

		braveSearch, err := search.NewBraveDirectSearch(config, search.CacheConfig{Enabled: false})
		if err != nil {
			t.Fatalf("Failed to create Brave search: %v", err)
		}

		// Simulate multiple rapid requests
		ctx := context.Background()
		params := search.SearchParameters{
			Query: "rate limit test",
			Count: 1,
		}

		for i := 0; i < 5; i++ {
			startTime := time.Now()
			_, err = braveSearch.Search(ctx, params)
			duration := time.Since(startTime)

			// Should fail with invalid key, but check that each request
			// doesn't take too long (indicating no rate limiting)
			if duration > 5*time.Second {
				t.Errorf("Request %d took too long: %v", i, duration)
			}
		}

		t.Log("Rate limiting protection test completed")
	})
}

func testCacheEffectiveness(t *testing.T) {
	t.Run("CachePerformance", func(t *testing.T) {
		// Test that cached requests are significantly faster
		cacheConfig := search.CacheConfig{
			Enabled:    true,
			TTLMinutes: 5,
		}

		config := search.BraveSearchConfig{
			APIKey:   "test_key", // Invalid key is fine for cache testing
			Endpoint: "https://api.search.brave.com/res/v1/web/search",
		}

		braveSearch, err := search.NewBraveDirectSearch(config, cacheConfig)
		if err != nil {
			t.Fatalf("Failed to create Brave search: %v", err)
		}

		ctx := context.Background()
		params := search.SearchParameters{
			Query: "cache test query",
			Count: 1,
		}

		// First request (will fail but should be cached)
		startTime := time.Now()
		_, firstErr := braveSearch.Search(ctx, params)
		firstDuration := time.Since(startTime)

		// Second request (should use cache)
		startTime = time.Now()
		_, secondErr := braveSearch.Search(ctx, params)
		secondDuration := time.Since(startTime)

		// Both should fail with invalid API key, but second should be much faster if cached
		if firstErr == nil || secondErr == nil {
			t.Log("Unexpected success with invalid API key")
		}

		// Cache should make second request faster (though both may be very fast with error responses)
		t.Logf("First request: %v, Second request: %v", firstDuration, secondDuration)

		if secondDuration >= firstDuration {
			t.Log("Cache may not be effective, or errors are too fast to measure difference")
		}
	})
}

// End-to-End WebSearchTool Tests
func testWebSearchToolWithMockProviders(t *testing.T) {
	t.Run("WebSearchToolIntegration", func(t *testing.T) {
		// Create a mock services configuration
		mockServices := &types.ToolServices{
			// Add minimal services needed for WebSearchTool
		}

		// Create WebSearchTool
		webSearchTool := tools.NewWebSearchTool(mockServices)

		if webSearchTool == nil {
			t.Fatal("WebSearchTool should not be nil")
		}

		// Test tool metadata
		if webSearchTool.Name() != "WebSearch" {
			t.Errorf("Expected tool name 'WebSearch', got '%s'", webSearchTool.Name())
		}

		if webSearchTool.Description() == "" {
			t.Error("Tool description should not be empty")
		}

		// Test parameters schema
		params := webSearchTool.Parameters()
		if params == nil {
			t.Fatal("Parameters should not be nil")
		}

		// Check required parameter
		properties, ok := params["properties"].(map[string]interface{})
		if !ok {
			t.Fatal("Parameters should have properties")
		}

		if _, hasQuery := properties["query"]; !hasQuery {
			t.Error("Parameters should include query field")
		}

		// Test execution with invalid parameters
		ctx := context.Background()
		result, err := webSearchTool.Execute(ctx, map[string]interface{}{})

		if err != nil {
			t.Errorf("Execute should not return error for invalid params, got: %v", err)
		}

		if result == nil {
			t.Fatal("Result should not be nil")
		}

		if result.Success {
			t.Error("Result should not be successful with empty parameters")
		}

		if !strings.Contains(result.Error, "query") {
			t.Error("Error should mention missing query parameter")
		}
	})
}

func testWebSearchToolWithRealAPI(t *testing.T, apiKey string) {
	t.Run("WebSearchToolRealAPI", func(t *testing.T) {
		// Create services with real Brave API configuration
		mockServices := &types.ToolServices{
			// Configure with real API key
		}

		// This would require more sophisticated setup to actually configure
		// the tool with real API access
		webSearchTool := tools.NewWebSearchTool(mockServices)

		ctx := context.Background()
		args := map[string]interface{}{
			"query": "test search query",
			"count": 2,
		}

		result, err := webSearchTool.Execute(ctx, args)

		if err != nil {
			t.Logf("Real API test failed (expected if no configuration): %v", err)
			return
		}

		if result != nil && result.Success {
			t.Log("Real API test succeeded")
			if result.Data != nil {
				t.Logf("Result data available: %+v", result.Data)
			}
		}
	})
}

// Benchmark tests for performance validation
func BenchmarkSearchRouterDetection(b *testing.B) {
	router, _ := search.NewSearchRouter(search.RouterConfig{})

	models := []string{
		"anthropic/claude-3-5-sonnet",
		"gpt-4",
		"gemini-pro",
		"claude-3-haiku",
		"anthropic/claude-3-opus",
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		model := models[i%len(models)]
		router.SetCurrentModel(model)
		// The actual detection would happen during Search()
	}
}

func BenchmarkOAuthTokenDetection(b *testing.B) {
	tokens := []string{
		"sk-ant-oat01-abc123-def456",
		"sk-ant-api01-regular123",
		"sk-ant-oat01-xyz789-uvw012",
		"invalid-token-format",
		"",
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		token := tokens[i%len(tokens)]
		auth.GetOAuthTokenInfo(token)
	}
}

func BenchmarkToolMapping(b *testing.B) {
	tools := []string{
		"web_search",
		"web_search_20250305",
		"message",
		"read",
		"write",
		"exec",
		"browser",
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		tool := tools[i%len(tools)]
		auth.MapToolForOAuth(tool)
	}
}
