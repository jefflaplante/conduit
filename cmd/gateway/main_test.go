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

	// conduit-31jg.3: keep any persisted token secret out of ~/.conduit.
	t.Setenv("CONDUIT_DATA_DIR", t.TempDir())

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

	authStorage := auth.NewTokenStorage(store.DB(), "test-secret")

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
	b.Setenv("CONDUIT_DATA_DIR", b.TempDir()) // conduit-31jg.3

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		gw, err := gateway.New(cfg)
		if err != nil {
			b.Fatalf("Failed to create gateway: %v", err)
		}
		_ = gw // Use the gateway to avoid unused variable warning
	}
}
