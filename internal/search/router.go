package search

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"conduit/internal/auth"
)

// SearchRouter intelligently routes search requests to the optimal strategy
// based on the model provider, with fallback mechanisms and monitoring.
type SearchRouter struct {
	strategies map[string]SearchStrategy
	config     SearchConfig
	cache      *SearchCache

	// Provider detection
	currentModel string

	// Metrics and monitoring
	usageStats map[string]*ProviderStats
	statsMutex sync.RWMutex
}

// ProviderStats tracks usage statistics for each search provider
type ProviderStats struct {
	RequestCount   int64         `json:"request_count"`
	SuccessCount   int64         `json:"success_count"`
	FailureCount   int64         `json:"failure_count"`
	TotalLatency   time.Duration `json:"total_latency"`
	AverageLatency time.Duration `json:"average_latency"`
	LastUsed       time.Time     `json:"last_used"`
	LastError      string        `json:"last_error,omitempty"`
}

// RouterConfig configures the search router
type RouterConfig struct {
	SearchConfig           SearchConfig                 `json:"search_config"`
	AnthropicConfig        *AnthropicNativeSearchConfig `json:"anthropic_config,omitempty"`
	BraveConfig            *BraveSearchConfig           `json:"brave_config,omitempty"`
	EnableFallback         bool                         `json:"enable_fallback"`
	FallbackTimeoutSeconds int                          `json:"fallback_timeout_seconds"`
	MetricsEnabled         bool                         `json:"metrics_enabled"`
}

// NewSearchRouter creates a new SearchRouter with the given configuration
func NewSearchRouter(config RouterConfig) (*SearchRouter, error) {
	router := &SearchRouter{
		strategies: make(map[string]SearchStrategy),
		config:     config.SearchConfig,
		usageStats: make(map[string]*ProviderStats),
	}

	// Initialize cache if enabled
	if config.SearchConfig.CacheEnabled {
		cacheConfig := config.SearchConfig.GetCacheConfig()
		router.cache = NewSearchCache(cacheConfig)
	}

	// Initialize Brave strategy if configured
	if config.BraveConfig != nil && router.config.IsBraveEnabled() {
		cacheConfig := router.config.GetCacheConfig()
		braveStrategy, err := NewBraveDirectSearch(*config.BraveConfig, cacheConfig)
		if err != nil {
			log.Printf("[SearchRouter] Failed to initialize Brave strategy: %v", err)
		} else {
			router.strategies["brave"] = braveStrategy
			router.initProviderStats("brave")
			log.Printf("[SearchRouter] Initialized Brave strategy")
		}
	}

	// Initialize Anthropic strategy if configured
	if config.AnthropicConfig != nil {
		anthConfig := *config.AnthropicConfig
		// Only initialize if we have an API key
		if anthConfig.APIKey != "" {
			anthStrategy, err := NewAnthropicNativeSearch(anthConfig)
			if err != nil {
				log.Printf("[SearchRouter] Failed to initialize Anthropic strategy: %v", err)
			} else {
				router.strategies["anthropic"] = anthStrategy
				router.initProviderStats("anthropic")
				log.Printf("[SearchRouter] Initialized Anthropic strategy")
			}
		} else {
			log.Printf("[SearchRouter] Anthropic strategy configured but no API key provided - will be initialized dynamically")
		}
	}

	return router, nil
}

// SetCurrentModel updates the current model for provider detection
func (r *SearchRouter) SetCurrentModel(model string) {
	r.currentModel = model
	log.Printf("[SearchRouter] Current model set to: %s", model)
}

// SetAPIKey sets the API key for a specific provider (useful for OAuth detection)
func (r *SearchRouter) SetAPIKey(provider, apiKey string) error {
	switch provider {
	case "anthropic":
		// Try to update existing strategy first
		if strategy, exists := r.strategies[provider]; exists {
			if anthStrategy, ok := strategy.(*AnthropicNativeSearch); ok {
				anthStrategy.apiKey = apiKey
				log.Printf("[SearchRouter] Updated existing Anthropic API key")
				return nil
			}
		}

		// If strategy doesn't exist, create it dynamically
		anthConfig := r.config.GetAnthropicConfig()
		anthConfig.APIKey = apiKey

		anthStrategy, err := NewAnthropicNativeSearch(anthConfig)
		if err != nil {
			return fmt.Errorf("failed to create Anthropic strategy: %w", err)
		}

		r.strategies[provider] = anthStrategy
		r.initProviderStats(provider)
		log.Printf("[SearchRouter] Created new Anthropic strategy with API key")
		return nil

	case "brave":
		// Brave API key is set during initialization
		log.Printf("[SearchRouter] Brave API key cannot be updated after initialization")
		return fmt.Errorf("brave API key cannot be updated after initialization")

	default:
		return fmt.Errorf("unsupported provider: %s", provider)
	}
}

// detectProviderFromModel determines which search provider to use based on the model name
func (r *SearchRouter) detectProviderFromModel(model string) string {
	if model == "" {
		model = r.currentModel
	}

	// Anthropic model patterns: anthropic/*, claude-*, and direct model names
	lowerModel := strings.ToLower(model)
	if strings.HasPrefix(lowerModel, "anthropic/") ||
		strings.HasPrefix(lowerModel, "claude-") ||
		strings.Contains(lowerModel, "claude") {
		// Check if Anthropic strategy is available
		if strategy, exists := r.strategies["anthropic"]; exists && strategy.IsAvailable() {
			return "anthropic"
		}
	}

	// Default to Brave for all other models
	return "brave"
}

// getFallbackChain returns the fallback chain for a given primary provider
func (r *SearchRouter) getFallbackChain(primaryProvider string) []string {
	switch primaryProvider {
	case "anthropic":
		// Anthropic → Brave → Error
		fallback := []string{"anthropic"}
		if _, exists := r.strategies["brave"]; exists {
			fallback = append(fallback, "brave")
		}
		return fallback
	case "brave":
		// Brave only (no fallback for direct API calls)
		return []string{"brave"}
	default:
		// Unknown provider, try Brave as default
		if _, exists := r.strategies["brave"]; exists {
			return []string{"brave"}
		}
		return []string{}
	}
}

// Search performs an intelligent search with provider selection and fallback
func (r *SearchRouter) Search(ctx context.Context, params SearchParameters) (*SearchResponse, error) {
	// Validate parameters
	if err := params.Validate(); err != nil {
		return nil, fmt.Errorf("invalid search parameters: %w", err)
	}

	// Detect OAuth and update Anthropic strategy if needed
	if r.currentModel != "" {
		if err := r.handleOAuthDetection(); err != nil {
			log.Printf("[SearchRouter] OAuth detection error: %v", err)
		}
	}

	// Determine primary provider
	primaryProvider := r.detectProviderFromModel(r.currentModel)
	log.Printf("[SearchRouter] Selected primary provider: %s for model: %s", primaryProvider, r.currentModel)

	// Get fallback chain
	fallbackChain := r.getFallbackChain(primaryProvider)

	var lastError error

	// Try each provider in the fallback chain
	for i, providerName := range fallbackChain {
		strategy, exists := r.strategies[providerName]
		if !exists {
			lastError = fmt.Errorf("provider %s not available", providerName)
			continue
		}

		if !strategy.IsAvailable() {
			lastError = fmt.Errorf("provider %s not available", providerName)
			continue
		}

		// Log attempt
		if i > 0 {
			log.Printf("[SearchRouter] Falling back to provider: %s", providerName)
		}

		// Track metrics
		startTime := time.Now()
		r.incrementRequestCount(providerName)

		// Perform search
		result, err := strategy.Search(ctx, params)
		latency := time.Since(startTime)

		if err != nil {
			lastError = err
			r.incrementFailureCount(providerName)
			r.updateLastError(providerName, err.Error())
			log.Printf("[SearchRouter] Provider %s failed: %v", providerName, err)
			continue
		}

		// Success!
		r.incrementSuccessCount(providerName)
		r.updateLatency(providerName, latency)
		r.updateLastUsed(providerName)

		// Ensure provider is set in response
		if result != nil {
			result.Provider = providerName
			result.Timestamp = time.Now()
		}

		log.Printf("[SearchRouter] Successfully searched with %s in %v", providerName, latency)
		return result, nil
	}

	// All providers failed
	return nil, fmt.Errorf("all search providers failed, last error: %w", lastError)
}

// handleOAuthDetection detects OAuth tokens and configures Anthropic strategy accordingly
func (r *SearchRouter) handleOAuthDetection() error {
	// This would typically be called with the actual API key/token from the request context
	// For now, we'll implement the logic structure

	// Note: In a real implementation, this would extract the API key from the current request context
	// and use auth.GetOAuthTokenInfo to detect OAuth tokens

	return nil // Placeholder implementation
}

// UpdateWithRequestContext updates the router with information from the current request
func (r *SearchRouter) UpdateWithRequestContext(apiKey, model string) error {
	// Update current model
	r.SetCurrentModel(model)

	// Detect and handle OAuth
	if apiKey != "" {
		tokenInfo := auth.GetOAuthTokenInfo(apiKey)
		if tokenInfo.IsOAuthToken {
			log.Printf("[SearchRouter] Detected OAuth token, updating Anthropic strategy")
			if err := r.SetAPIKey("anthropic", apiKey); err != nil {
				log.Printf("[SearchRouter] Failed to set Anthropic API key: %v", err)
			}
		}
	}

	return nil
}

// GetUsageStats returns current usage statistics for all providers
func (r *SearchRouter) GetUsageStats() map[string]*ProviderStats {
	r.statsMutex.RLock()
	defer r.statsMutex.RUnlock()

	// Create a deep copy to avoid concurrent access issues
	statsCopy := make(map[string]*ProviderStats)
	for provider, stats := range r.usageStats {
		statsCopy[provider] = &ProviderStats{
			RequestCount:   stats.RequestCount,
			SuccessCount:   stats.SuccessCount,
			FailureCount:   stats.FailureCount,
			TotalLatency:   stats.TotalLatency,
			AverageLatency: stats.AverageLatency,
			LastUsed:       stats.LastUsed,
			LastError:      stats.LastError,
		}
	}

	return statsCopy
}

// GetAvailableProviders returns a list of currently available search providers
func (r *SearchRouter) GetAvailableProviders() []string {
	var providers []string
	for name, strategy := range r.strategies {
		if strategy.IsAvailable() {
			providers = append(providers, name)
		}
	}
	return providers
}

// Helper methods for metrics tracking

func (r *SearchRouter) initProviderStats(provider string) {
	r.statsMutex.Lock()
	defer r.statsMutex.Unlock()
	r.usageStats[provider] = &ProviderStats{}
}

func (r *SearchRouter) incrementRequestCount(provider string) {
	r.statsMutex.Lock()
	defer r.statsMutex.Unlock()
	if stats, exists := r.usageStats[provider]; exists {
		stats.RequestCount++
	}
}

func (r *SearchRouter) incrementSuccessCount(provider string) {
	r.statsMutex.Lock()
	defer r.statsMutex.Unlock()
	if stats, exists := r.usageStats[provider]; exists {
		stats.SuccessCount++
	}
}

func (r *SearchRouter) incrementFailureCount(provider string) {
	r.statsMutex.Lock()
	defer r.statsMutex.Unlock()
	if stats, exists := r.usageStats[provider]; exists {
		stats.FailureCount++
	}
}

func (r *SearchRouter) updateLatency(provider string, latency time.Duration) {
	r.statsMutex.Lock()
	defer r.statsMutex.Unlock()
	if stats, exists := r.usageStats[provider]; exists {
		stats.TotalLatency += latency
		if stats.RequestCount > 0 {
			stats.AverageLatency = stats.TotalLatency / time.Duration(stats.RequestCount)
		}
	}
}

func (r *SearchRouter) updateLastUsed(provider string) {
	r.statsMutex.Lock()
	defer r.statsMutex.Unlock()
	if stats, exists := r.usageStats[provider]; exists {
		stats.LastUsed = time.Now()
	}
}

func (r *SearchRouter) updateLastError(provider string, errorMsg string) {
	r.statsMutex.Lock()
	defer r.statsMutex.Unlock()
	if stats, exists := r.usageStats[provider]; exists {
		stats.LastError = errorMsg
	}
}
