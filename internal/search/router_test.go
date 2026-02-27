package search

import (
	"context"
	"errors"
	"testing"
	"time"

	"conduit/internal/auth"
)

// MockSearchStrategy implements SearchStrategy for testing
type MockSearchStrategy struct {
	name         string
	isAvailable  bool
	shouldFail   bool
	failError    error
	searchDelay  time.Duration
	capabilities SearchCapabilities
}

func NewMockSearchStrategy(name string) *MockSearchStrategy {
	return &MockSearchStrategy{
		name:        name,
		isAvailable: true,
		shouldFail:  false,
		searchDelay: 10 * time.Millisecond,
		capabilities: SearchCapabilities{
			MaxResults:     10,
			DefaultResults: 5,
			HasCaching:     false,
		},
	}
}

func (m *MockSearchStrategy) Name() string {
	return m.name
}

func (m *MockSearchStrategy) Search(ctx context.Context, params SearchParameters) (*SearchResponse, error) {
	if m.shouldFail {
		return nil, m.failError
	}

	// Simulate search delay
	time.Sleep(m.searchDelay)

	return &SearchResponse{
		Results: []SearchResult{
			{
				Title:       "Mock Result 1",
				URL:         "https://example.com/1",
				Description: "Mock search result 1",
			},
			{
				Title:       "Mock Result 2",
				URL:         "https://example.com/2",
				Description: "Mock search result 2",
			},
		},
		Query:     params.Query,
		Total:     2,
		Provider:  m.name,
		Timestamp: time.Now(),
	}, nil
}

func (m *MockSearchStrategy) IsAvailable() bool {
	return m.isAvailable
}

func (m *MockSearchStrategy) GetCapabilities() SearchCapabilities {
	return m.capabilities
}

func (m *MockSearchStrategy) SetAvailable(available bool) {
	m.isAvailable = available
}

func (m *MockSearchStrategy) SetShouldFail(shouldFail bool, err error) {
	m.shouldFail = shouldFail
	m.failError = err
}

// Test helper function to create a test router
func createTestRouter() *SearchRouter {
	config := RouterConfig{
		SearchConfig: SearchConfig{
			Enabled:                true,
			DefaultProvider:        "brave",
			CacheTTLMinutes:        15,
			CacheEnabled:           false, // Disable cache for testing
			EnableFallback:         true,
			FallbackTimeoutSeconds: 30,
			MetricsEnabled:         true,
		},
		EnableFallback: true,
		MetricsEnabled: true,
	}

	router := &SearchRouter{
		strategies: make(map[string]SearchStrategy),
		config:     config.SearchConfig,
		usageStats: make(map[string]*ProviderStats),
	}

	// Add mock strategies
	braveStrategy := NewMockSearchStrategy("brave")
	anthropicStrategy := NewMockSearchStrategy("anthropic")

	router.strategies["brave"] = braveStrategy
	router.strategies["anthropic"] = anthropicStrategy

	// Initialize stats
	router.initProviderStats("brave")
	router.initProviderStats("anthropic")

	return router
}

func TestSearchRouter_ProviderDetection(t *testing.T) {
	router := createTestRouter()

	tests := []struct {
		name             string
		model            string
		expectedProvider string
	}{
		{
			name:             "Anthropic model with anthropic prefix",
			model:            "anthropic/claude-opus-4-6",
			expectedProvider: "anthropic",
		},
		{
			name:             "Claude model name",
			model:            "claude-3-5-sonnet-20241022",
			expectedProvider: "anthropic",
		},
		{
			name:             "Claude in model name",
			model:            "claude-haiku-latest",
			expectedProvider: "anthropic",
		},
		{
			name:             "OpenAI model",
			model:            "openai/gpt-4",
			expectedProvider: "brave",
		},
		{
			name:             "Generic model",
			model:            "some-other-model",
			expectedProvider: "brave",
		},
		{
			name:             "Empty model",
			model:            "",
			expectedProvider: "brave",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := router.detectProviderFromModel(tt.model)
			if provider != tt.expectedProvider {
				t.Errorf("Expected provider %s, got %s for model %s",
					tt.expectedProvider, provider, tt.model)
			}
		})
	}
}

func TestSearchRouter_FallbackChain(t *testing.T) {
	router := createTestRouter()

	tests := []struct {
		name            string
		primaryProvider string
		expectedChain   []string
	}{
		{
			name:            "Anthropic fallback chain",
			primaryProvider: "anthropic",
			expectedChain:   []string{"anthropic", "brave"},
		},
		{
			name:            "Brave fallback chain",
			primaryProvider: "brave",
			expectedChain:   []string{"brave"},
		},
		{
			name:            "Unknown provider fallback chain",
			primaryProvider: "unknown",
			expectedChain:   []string{"brave"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chain := router.getFallbackChain(tt.primaryProvider)
			if len(chain) != len(tt.expectedChain) {
				t.Errorf("Expected chain length %d, got %d", len(tt.expectedChain), len(chain))
				return
			}

			for i, provider := range chain {
				if provider != tt.expectedChain[i] {
					t.Errorf("Expected provider %s at position %d, got %s",
						tt.expectedChain[i], i, provider)
				}
			}
		})
	}
}

func TestSearchRouter_SuccessfulSearch(t *testing.T) {
	router := createTestRouter()
	router.SetCurrentModel("anthropic/claude-opus-4-6")

	ctx := context.Background()
	params := SearchParameters{
		Query: "test query",
		Count: 5,
	}

	response, err := router.Search(ctx, params)
	if err != nil {
		t.Fatalf("Expected successful search, got error: %v", err)
	}

	if response == nil {
		t.Fatal("Expected response, got nil")
	}

	if response.Provider != "anthropic" {
		t.Errorf("Expected provider 'anthropic', got '%s'", response.Provider)
	}

	if len(response.Results) == 0 {
		t.Error("Expected results, got empty array")
	}

	// Check that metrics were updated
	stats := router.GetUsageStats()
	if stats["anthropic"].RequestCount != 1 {
		t.Errorf("Expected request count 1, got %d", stats["anthropic"].RequestCount)
	}
	if stats["anthropic"].SuccessCount != 1 {
		t.Errorf("Expected success count 1, got %d", stats["anthropic"].SuccessCount)
	}
}

func TestSearchRouter_FallbackOnFailure(t *testing.T) {
	router := createTestRouter()
	router.SetCurrentModel("anthropic/claude-opus-4-6")

	// Make anthropic strategy fail
	anthropicStrategy := router.strategies["anthropic"].(*MockSearchStrategy)
	anthropicStrategy.SetShouldFail(true, errors.New("anthropic search failed"))

	ctx := context.Background()
	params := SearchParameters{
		Query: "test query",
		Count: 5,
	}

	response, err := router.Search(ctx, params)
	if err != nil {
		t.Fatalf("Expected successful fallback search, got error: %v", err)
	}

	if response.Provider != "brave" {
		t.Errorf("Expected fallback to 'brave', got '%s'", response.Provider)
	}

	// Check that metrics were updated for both providers
	stats := router.GetUsageStats()
	if stats["anthropic"].RequestCount != 1 {
		t.Errorf("Expected anthropic request count 1, got %d", stats["anthropic"].RequestCount)
	}
	if stats["anthropic"].FailureCount != 1 {
		t.Errorf("Expected anthropic failure count 1, got %d", stats["anthropic"].FailureCount)
	}
	if stats["brave"].RequestCount != 1 {
		t.Errorf("Expected brave request count 1, got %d", stats["brave"].RequestCount)
	}
	if stats["brave"].SuccessCount != 1 {
		t.Errorf("Expected brave success count 1, got %d", stats["brave"].SuccessCount)
	}
}

func TestSearchRouter_AllProvidersFail(t *testing.T) {
	router := createTestRouter()
	router.SetCurrentModel("anthropic/claude-opus-4-6")

	// Make both strategies fail
	anthropicStrategy := router.strategies["anthropic"].(*MockSearchStrategy)
	anthropicStrategy.SetShouldFail(true, errors.New("anthropic search failed"))

	braveStrategy := router.strategies["brave"].(*MockSearchStrategy)
	braveStrategy.SetShouldFail(true, errors.New("brave search failed"))

	ctx := context.Background()
	params := SearchParameters{
		Query: "test query",
		Count: 5,
	}

	response, err := router.Search(ctx, params)
	if err == nil {
		t.Fatal("Expected error when all providers fail, got nil")
	}

	if response != nil {
		t.Error("Expected nil response when all providers fail")
	}

	// Check that failure counts were updated
	stats := router.GetUsageStats()
	if stats["anthropic"].FailureCount != 1 {
		t.Errorf("Expected anthropic failure count 1, got %d", stats["anthropic"].FailureCount)
	}
	if stats["brave"].FailureCount != 1 {
		t.Errorf("Expected brave failure count 1, got %d", stats["brave"].FailureCount)
	}
}

func TestSearchRouter_UnavailableProviders(t *testing.T) {
	router := createTestRouter()
	router.SetCurrentModel("anthropic/claude-opus-4-6")

	// Make anthropic strategy unavailable
	anthropicStrategy := router.strategies["anthropic"].(*MockSearchStrategy)
	anthropicStrategy.SetAvailable(false)

	ctx := context.Background()
	params := SearchParameters{
		Query: "test query",
		Count: 5,
	}

	response, err := router.Search(ctx, params)
	if err != nil {
		t.Fatalf("Expected successful fallback search, got error: %v", err)
	}

	if response.Provider != "brave" {
		t.Errorf("Expected fallback to 'brave', got '%s'", response.Provider)
	}
}

func TestSearchRouter_InvalidParameters(t *testing.T) {
	router := createTestRouter()

	ctx := context.Background()
	params := SearchParameters{
		Query: "", // Invalid empty query
		Count: 5,
	}

	response, err := router.Search(ctx, params)
	if err == nil {
		t.Fatal("Expected error for invalid parameters, got nil")
	}

	if response != nil {
		t.Error("Expected nil response for invalid parameters")
	}
}

func TestSearchRouter_MetricsTracking(t *testing.T) {
	router := createTestRouter()
	router.SetCurrentModel("brave-model")

	ctx := context.Background()
	params := SearchParameters{
		Query: "test query",
		Count: 5,
	}

	// Perform multiple searches
	for i := 0; i < 3; i++ {
		_, err := router.Search(ctx, params)
		if err != nil {
			t.Fatalf("Search %d failed: %v", i+1, err)
		}
	}

	// Check metrics
	stats := router.GetUsageStats()
	braveStats := stats["brave"]

	if braveStats.RequestCount != 3 {
		t.Errorf("Expected request count 3, got %d", braveStats.RequestCount)
	}
	if braveStats.SuccessCount != 3 {
		t.Errorf("Expected success count 3, got %d", braveStats.SuccessCount)
	}
	if braveStats.FailureCount != 0 {
		t.Errorf("Expected failure count 0, got %d", braveStats.FailureCount)
	}
	if braveStats.AverageLatency == 0 {
		t.Error("Expected non-zero average latency")
	}
	if braveStats.LastUsed.IsZero() {
		t.Error("Expected LastUsed to be set")
	}
}

func TestSearchRouter_OAuthDetection(t *testing.T) {
	router := createTestRouter()

	// Test OAuth token detection
	oauthToken := "sk-ant-oat-example-oauth-token"
	regularToken := "sk-ant-regular-token"

	// Test OAuth token
	tokenInfo := auth.GetOAuthTokenInfo(oauthToken)
	if !tokenInfo.IsOAuthToken {
		t.Error("Expected OAuth token to be detected")
	}

	// Test regular token
	tokenInfo = auth.GetOAuthTokenInfo(regularToken)
	if tokenInfo.IsOAuthToken {
		t.Error("Expected regular token not to be detected as OAuth")
	}

	// Test updating router with OAuth context
	err := router.UpdateWithRequestContext(oauthToken, "anthropic/claude-opus-4-6")
	if err != nil {
		t.Errorf("Expected no error updating router context, got: %v", err)
	}

	if router.currentModel != "anthropic/claude-opus-4-6" {
		t.Errorf("Expected current model to be set to 'anthropic/claude-opus-4-6', got '%s'", router.currentModel)
	}
}

func TestSearchRouter_AvailableProviders(t *testing.T) {
	router := createTestRouter()

	providers := router.GetAvailableProviders()
	expectedProviders := []string{"anthropic", "brave"}

	if len(providers) != len(expectedProviders) {
		t.Errorf("Expected %d providers, got %d", len(expectedProviders), len(providers))
	}

	// Make anthropic unavailable
	anthropicStrategy := router.strategies["anthropic"].(*MockSearchStrategy)
	anthropicStrategy.SetAvailable(false)

	providers = router.GetAvailableProviders()
	if len(providers) != 1 || providers[0] != "brave" {
		t.Errorf("Expected only 'brave' provider to be available, got %v", providers)
	}
}

func TestSearchRouter_ProviderSelection(t *testing.T) {
	router := createTestRouter()

	testCases := []struct {
		model            string
		expectedProvider string
		description      string
	}{
		{"anthropic/claude-3-opus", "anthropic", "Anthropic model with provider prefix"},
		{"claude-3-5-sonnet-20241022", "anthropic", "Claude model name"},
		{"CLAUDE-HAIKU", "anthropic", "Case insensitive Claude model"},
		{"gpt-4", "brave", "Non-Claude model"},
		{"unknown-model", "brave", "Unknown model"},
		{"", "brave", "Empty model"},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			router.SetCurrentModel(tc.model)

			ctx := context.Background()
			params := SearchParameters{
				Query: "test query",
				Count: 3,
			}

			response, err := router.Search(ctx, params)
			if err != nil {
				t.Fatalf("Search failed: %v", err)
			}

			if response.Provider != tc.expectedProvider {
				t.Errorf("Expected provider %s for model %s, got %s",
					tc.expectedProvider, tc.model, response.Provider)
			}
		})
	}
}

// Benchmark tests
func BenchmarkSearchRouter_Search(b *testing.B) {
	router := createTestRouter()
	router.SetCurrentModel("brave-model")

	ctx := context.Background()
	params := SearchParameters{
		Query: "benchmark test",
		Count: 5,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := router.Search(ctx, params)
		if err != nil {
			b.Fatalf("Search failed: %v", err)
		}
	}
}

func BenchmarkSearchRouter_ProviderDetection(b *testing.B) {
	router := createTestRouter()

	models := []string{
		"anthropic/claude-opus-4-6",
		"claude-3-5-sonnet-20241022",
		"gpt-4",
		"unknown-model",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		model := models[i%len(models)]
		router.detectProviderFromModel(model)
	}
}
