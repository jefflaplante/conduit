package tools

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"conduit/internal/search"
	"conduit/internal/tools/types"
)

// WebSearchTool implements intelligent web search using the SearchRouter
// with automatic provider selection and fallback mechanisms.
type WebSearchTool struct {
	services *types.ToolServices
	router   *search.SearchRouter
	config   search.SearchConfig
}

// NewWebSearchTool creates a new WebSearchTool with intelligent routing
func NewWebSearchTool(services *types.ToolServices) *WebSearchTool {
	tool := &WebSearchTool{
		services: services,
	}

	// Load search configuration from environment and services
	tool.config = search.LoadSearchConfigFromEnv()

	// Override with configuration from services if available
	if services != nil && services.ConfigMgr != nil {
		tool.loadConfigFromServices()
	}

	// Initialize the search router
	if err := tool.initializeRouter(); err != nil {
		log.Printf("[WebSearchTool] Failed to initialize search router: %v", err)
		// Continue with basic initialization - router will be nil but tool can still function
	}

	log.Printf("[WebSearchTool] Initialized with router and %d available providers",
		len(tool.getAvailableProviders()))

	return tool
}

// loadConfigFromServices loads configuration from the services config manager
func (t *WebSearchTool) loadConfigFromServices() {
	// Override Brave API key from services configuration
	if braveConfig, exists := t.services.ConfigMgr.Tools.Services["brave"]; exists {
		if apiKey, keyExists := braveConfig["api_key"]; keyExists {
			if apiKeyStr, ok := apiKey.(string); ok {
				// Update Brave provider config
				if braveProvider, exists := t.config.Providers["brave"]; exists {
					braveProvider.APIKey = apiKeyStr
					t.config.Providers["brave"] = braveProvider
				}
			}
		}
	}

	// Check for search-specific configuration
	if searchConfig, exists := t.services.ConfigMgr.Tools.Services["search"]; exists {
		// Handle search configuration overrides
		if enabled, exists := searchConfig["enabled"]; exists {
			if enabledBool, ok := enabled.(bool); ok {
				t.config.Enabled = enabledBool
			}
		}
		if defaultProvider, exists := searchConfig["default_provider"]; exists {
			if providerStr, ok := defaultProvider.(string); ok {
				t.config.DefaultProvider = providerStr
			}
		}
	}
}

// initializeRouter creates and configures the search router
func (t *WebSearchTool) initializeRouter() error {
	// Validate configuration
	if err := t.config.Validate(); err != nil {
		return fmt.Errorf("invalid search configuration: %w", err)
	}

	// Get router configuration
	routerConfig := t.config.GetRouterConfig()

	// Create the router
	router, err := search.NewSearchRouter(routerConfig)
	if err != nil {
		return fmt.Errorf("failed to create search router: %w", err)
	}

	t.router = router
	return nil
}

// Name returns the tool name
func (t *WebSearchTool) Name() string {
	return "WebSearch"
}

// Description returns the tool description
func (t *WebSearchTool) Description() string {
	providers := t.getAvailableProviders()
	if len(providers) > 0 {
		return fmt.Sprintf("Search the web intelligently using multiple providers (%s) with automatic provider selection and fallback",
			strings.Join(providers, ", "))
	}
	return "Search the web using Brave Search API with support for region-specific results"
}

// Parameters returns the tool parameters schema
func (t *WebSearchTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Search query string",
			},
			"count": map[string]interface{}{
				"type":        "integer",
				"description": "Number of results to return (1-10)",
				"minimum":     1,
				"maximum":     10,
				"default":     5,
			},
			"country": map[string]interface{}{
				"type":        "string",
				"description": "2-letter country code for region-specific results (e.g., 'DE', 'US', 'ALL')",
				"default":     "US",
			},
			"freshness": map[string]interface{}{
				"type":        "string",
				"description": "Filter results by discovery time ('pd'=past day, 'pw'=past week, 'pm'=past month, 'py'=past year)",
				"enum":        []string{"pd", "pw", "pm", "py"},
			},
			"search_lang": map[string]interface{}{
				"type":        "string",
				"description": "ISO language code for search results (e.g., 'de', 'en', 'fr')",
			},
			"ui_lang": map[string]interface{}{
				"type":        "string",
				"description": "ISO language code for UI elements",
			},
		},
		"required": []string{"query"},
	}
}

// Execute performs the web search using the intelligent router
func (t *WebSearchTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	// Extract and validate parameters
	params, err := t.extractSearchParameters(args)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("invalid parameters: %v", err),
		}, nil
	}

	// Check if router is available
	if t.router == nil {
		// Fallback to direct Brave search if router initialization failed
		return t.fallbackToBraveSearch(ctx, params)
	}

	// Update router with current request context if available
	if err := t.updateRouterContext(ctx); err != nil {
		log.Printf("[WebSearchTool] Failed to update router context: %v", err)
	}

	// Perform search using the router
	startTime := time.Now()
	response, err := t.router.Search(ctx, *params)
	searchDuration := time.Since(startTime)

	if err != nil {
		log.Printf("[WebSearchTool] Search failed after %v: %v", searchDuration, err)
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("search failed: %v", err),
		}, nil
	}

	// Format results for output
	content := t.formatSearchResults(response)

	// Include provider information and metrics in the response
	responseData := map[string]interface{}{
		"results":     response.Results,
		"query":       response.Query,
		"total":       response.Total,
		"provider":    response.Provider,
		"cached":      response.Cached,
		"search_time": searchDuration.String(),
		"timestamp":   response.Timestamp,
		"metadata":    response.Metadata,
	}

	// Add usage stats if available
	if stats := t.router.GetUsageStats(); len(stats) > 0 {
		responseData["usage_stats"] = stats
	}

	log.Printf("[WebSearchTool] Successfully found %d results using %s in %v",
		len(response.Results), response.Provider, searchDuration)

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data:    responseData,
	}, nil
}

// extractSearchParameters extracts and validates search parameters from the tool arguments
func (t *WebSearchTool) extractSearchParameters(args map[string]interface{}) (*search.SearchParameters, error) {
	query, ok := args["query"].(string)
	if !ok || query == "" {
		return nil, fmt.Errorf("query parameter is required and must be a non-empty string")
	}

	params := &search.SearchParameters{
		Query:      query,
		Count:      t.getIntArg(args, "count", 5),
		Country:    t.getStringArg(args, "country", "US"),
		Freshness:  t.getStringArg(args, "freshness", ""),
		SearchLang: t.getStringArg(args, "search_lang", ""),
		UILang:     t.getStringArg(args, "ui_lang", ""),
	}

	// Validate parameters
	if err := params.Validate(); err != nil {
		return nil, err
	}

	return params, nil
}

// updateRouterContext updates the search router with current request context
func (t *WebSearchTool) updateRouterContext(ctx context.Context) error {
	// Extract API key and model from context if available
	// This would typically come from the request metadata

	// For now, we'll implement a basic version that tries to get information from services
	apiKey := ""
	model := ""

	// Try to extract from context values (this would be set by middleware)
	if ctxAPIKey := ctx.Value("api_key"); ctxAPIKey != nil {
		if keyStr, ok := ctxAPIKey.(string); ok {
			apiKey = keyStr
		}
	}

	if ctxModel := ctx.Value("model"); ctxModel != nil {
		if modelStr, ok := ctxModel.(string); ok {
			model = modelStr
		}
	}

	// Update router with context information
	return t.router.UpdateWithRequestContext(apiKey, model)
}

// fallbackToBraveSearch provides a fallback when the router is unavailable
func (t *WebSearchTool) fallbackToBraveSearch(ctx context.Context, params *search.SearchParameters) (*types.ToolResult, error) {
	log.Printf("[WebSearchTool] Using Brave fallback search for query: %s", params.Query)

	// Check if we have Brave configuration
	if !t.config.IsBraveEnabled() {
		return &types.ToolResult{
			Success: false,
			Error:   "No search providers available: router unavailable and Brave not configured",
		}, nil
	}

	// Create a direct Brave search instance
	braveConfig := t.config.GetBraveConfig()
	cacheConfig := t.config.GetCacheConfig()
	braveSearch, err := search.NewBraveDirectSearch(braveConfig, cacheConfig)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("fallback search failed: %v", err),
		}, nil
	}

	// Perform search
	response, err := braveSearch.Search(ctx, *params)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("fallback search failed: %v", err),
		}, nil
	}

	// Format results
	content := t.formatSearchResults(response)

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data: map[string]interface{}{
			"results":  response.Results,
			"query":    response.Query,
			"total":    response.Total,
			"provider": "brave-fallback",
		},
	}, nil
}

// formatSearchResults formats search results for display
func (t *WebSearchTool) formatSearchResults(response *search.SearchResponse) string {
	if len(response.Results) == 0 {
		return fmt.Sprintf("No results found for query: '%s'", response.Query)
	}

	var builder strings.Builder

	// Include provider information
	providerInfo := response.Provider
	if response.Cached {
		providerInfo += " (cached)"
	}

	builder.WriteString(fmt.Sprintf("Found %d results for '%s' via %s:\n\n",
		len(response.Results), response.Query, providerInfo))

	for i, result := range response.Results {
		builder.WriteString(fmt.Sprintf("%d. **%s**\n", i+1, result.Title))
		builder.WriteString(fmt.Sprintf("   %s\n", result.URL))
		if result.Description != "" {
			// Truncate description if too long
			desc := result.Description
			if len(desc) > 200 {
				desc = desc[:197] + "..."
			}
			builder.WriteString(fmt.Sprintf("   %s\n", desc))
		}
		if result.Published != "" {
			builder.WriteString(fmt.Sprintf("   Published: %s\n", result.Published))
		}
		builder.WriteString("\n")
	}

	return builder.String()
}

// getAvailableProviders returns a list of available search providers
func (t *WebSearchTool) getAvailableProviders() []string {
	if t.router != nil {
		return t.router.GetAvailableProviders()
	}

	// Fallback: check configuration
	var providers []string
	if t.config.IsBraveEnabled() {
		providers = append(providers, "brave")
	}
	if t.config.IsAnthropicEnabled() {
		providers = append(providers, "anthropic")
	}
	return providers
}

// Helper methods for parameter extraction
func (t *WebSearchTool) getStringArg(args map[string]interface{}, key, defaultVal string) string {
	if val, ok := args[key].(string); ok {
		return val
	}
	return defaultVal
}

func (t *WebSearchTool) getIntArg(args map[string]interface{}, key string, defaultVal int) int {
	if val, ok := args[key].(float64); ok {
		return int(val)
	}
	if val, ok := args[key].(int); ok {
		return val
	}
	return defaultVal
}
