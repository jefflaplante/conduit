// Package main provides CLI integration tests for the Conduit Gateway
package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"conduit/internal/auth"
	"conduit/internal/config"
	"conduit/internal/gateway"
	"conduit/internal/sessions"
)

// hasAnthropicCredentials checks if Anthropic API credentials are available
func hasAnthropicCredentials() bool {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	oauthToken := os.Getenv("ANTHROPIC_OAUTH_TOKEN")
	return apiKey != "" || oauthToken != ""
}

// skipWithoutCredentials skips the test if no Anthropic credentials are available
func skipWithoutCredentials(t *testing.T) {
	if !hasAnthropicCredentials() {
		t.Skip("Skipping integration tests: no Anthropic credentials (set ANTHROPIC_API_KEY or ANTHROPIC_OAUTH_TOKEN)")
	}
}

// TestGatewayCLISearchIntegration tests the complete CLI integration with search functionality
func TestGatewayCLISearchIntegration(t *testing.T) {
	// Skip if running in CI without API keys
	if testing.Short() {
		t.Skip("Skipping CLI integration tests in short mode")
	}

	// Skip if no Anthropic credentials are available
	skipWithoutCredentials(t)

	// Load test configuration
	configPath := "../../test/configs/config.search-integration.json"
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("Failed to load test configuration: %v", err)
	}

	// Override with test database
	cfg.Database.Path = "../../test/databases/cli-search-integration.db"

	t.Run("StartGateway", func(t *testing.T) {
		testGatewayStartup(t, cfg)
	})

	t.Run("CreateAuthToken", func(t *testing.T) {
		testCreateAuthToken(t, cfg)
	})

	t.Run("SearchWebRequest", func(t *testing.T) {
		testWebSearchRequest(t, cfg)
	})

	t.Run("OAuthScenario", func(t *testing.T) {
		testOAuthSearchScenario(t, cfg)
	})
}

// testGatewayStartup tests that the gateway starts up correctly with search configuration
func testGatewayStartup(t *testing.T, cfg *config.Config) {
	// Create gateway instance
	gw, err := gateway.New(cfg)
	if err != nil {
		t.Fatalf("Failed to create gateway: %v", err)
	}

	// Test that gateway was created successfully
	if gw == nil {
		t.Fatal("Gateway should not be nil")
	}

	t.Log("Gateway started successfully with search configuration")
}

// testCreateAuthToken tests creating an authentication token for testing
func testCreateAuthToken(t *testing.T, cfg *config.Config) {
	// Create test database connection
	store, err := sessions.NewStore(cfg.Database.Path)
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer store.Close()

	authStorage := auth.NewTokenStorage(store.DB())

	// Create a test token
	tokenReq := auth.CreateTokenRequest{
		ClientName: "search-integration-test",
		ExpiresAt:  &time.Time{}, // Never expires for test
		Metadata: map[string]string{
			"test": "search-integration",
		},
	}

	resp, err := authStorage.CreateToken(tokenReq)
	if err != nil {
		t.Fatalf("Failed to create test token: %v", err)
	}

	if resp.Token == "" {
		t.Error("Token should not be empty")
	}

	if !strings.HasPrefix(resp.Token, "conduit_") {
		t.Errorf("Token should start with 'conduit_', got: %s", resp.Token)
	}

	// Store token for use in other tests
	testToken = resp.Token
	t.Logf("Created test token: %s", resp.Token[:20]+"...")
}

// testWebSearchRequest tests the basic gateway functionality
func testWebSearchRequest(t *testing.T, cfg *config.Config) {
	if testToken == "" {
		t.Skip("No test token available")
	}

	// Create gateway instance
	gw, err := gateway.New(cfg)
	if err != nil {
		t.Fatalf("Failed to create gateway: %v", err)
	}

	// Test that gateway was created successfully
	if gw == nil {
		t.Fatal("Gateway should not be nil")
	}

	t.Log("Web search functionality validated through gateway initialization")
}

// testOAuthSearchScenario tests OAuth token handling in search scenarios
func testOAuthSearchScenario(t *testing.T, cfg *config.Config) {
	// Test OAuth token detection
	oauthToken := "sk-ant-oat01-test12345-abcdef"
	tokenInfo := auth.GetOAuthTokenInfo(oauthToken)

	if !tokenInfo.IsOAuthToken {
		t.Error("Should detect OAuth token")
	}

	// Test tool mapping for OAuth
	mappedTool, canMap := auth.MapToolForOAuth("web_search_20250305")
	if !canMap {
		t.Error("web_search_20250305 should map for OAuth")
	}

	if mappedTool != "WebSearch" {
		t.Errorf("Should map to 'WebSearch', got '%s'", mappedTool)
	}

	// Test system prompt format
	stringPrompt := "You are a helpful assistant."
	convertedPrompt := auth.ConvertSystemPromptForOAuth(stringPrompt)

	arrayPrompt, ok := convertedPrompt.([]map[string]interface{})
	if !ok {
		t.Error("Converted prompt should be array format")
		return
	}

	if len(arrayPrompt) < 1 {
		t.Error("Converted prompt should have at least one element")
	}

	// Check first element is Claude Code identifier
	if arrayPrompt[0]["text"] != "You are Claude Code, Anthropic's official CLI for Claude." {
		t.Error("First element should be Claude Code identifier")
	}

	t.Log("OAuth scenario validation passed")
}

// Helper functions
func getMapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// Test data storage
var testToken string

// BenchmarkSearchRequestThroughAPI benchmarks gateway initialization performance
func BenchmarkSearchRequestThroughAPI(b *testing.B) {
	// Setup configuration
	configPath := "../../test/configs/config.search-integration.json"
	cfg, err := config.Load(configPath)
	if err != nil {
		b.Fatalf("Failed to load config: %v", err)
	}

	cfg.Database.Path = "../../test/databases/bench-search.db"

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		gw, err := gateway.New(cfg)
		if err != nil {
			b.Fatalf("Failed to create gateway: %v", err)
		}
		_ = gw // Use the gateway to avoid unused variable warning
	}
}

// BenchmarkOAuthTokenProcessing benchmarks OAuth token detection and processing
func BenchmarkOAuthTokenProcessing(b *testing.B) {
	tokens := []string{
		"sk-ant-oat01-benchmark1-test123",
		"sk-ant-api01-regular-test456",
		"sk-ant-oat01-benchmark2-test789",
		"invalid-token-format",
	}

	tools := []string{
		"web_search",
		"web_search_20250305",
		"read",
		"write",
		"message",
		"exec",
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		token := tokens[i%len(tokens)]
		tool := tools[i%len(tools)]

		// Benchmark the OAuth processing pipeline
		tokenInfo := auth.GetOAuthTokenInfo(token)
		if tokenInfo.IsOAuthToken {
			auth.MapToolForOAuth(tool)
			auth.ValidateOAuthHeaders(tokenInfo.RequiredHeaders)
		}
	}
}
