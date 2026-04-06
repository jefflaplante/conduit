package tools

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"conduit/internal/search"
	toolargs "conduit/internal/tools/args"
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
		Count:      toolargs.GetInt(args, "count", 5),
		Country:    toolargs.GetString(args, "country", "US"),
		Freshness:  toolargs.GetString(args, "freshness", ""),
		SearchLang: toolargs.GetString(args, "search_lang", ""),
		UILang:     toolargs.GetString(args, "ui_lang", ""),
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

// SelfTest implements types.SelfTester for WebSearchTool.
func (t *WebSearchTool) SelfTest(ctx context.Context, opts *types.SelfTestOptions) *types.SelfTestResult {
	start := time.Now()

	if opts == nil {
		opts = types.DefaultSelfTestOptions()
	}

	result := &types.SelfTestResult{
		Status:       types.SelfTestStatusOK,
		Message:      "WebSearch tool is functional",
		Capabilities: []string{"web_search", "region_filtering", "freshness_filtering", "language_filtering"},
		TestedAt:     time.Now(),
	}

	deps := []types.DependencyStatus{}

	// Check search router
	routerStatus := types.DependencyStatus{
		Name:     "SearchRouter",
		Required: false, // Not strictly required - tool has fallback
	}

	if t.router != nil {
		routerStatus.Available = true
		routerStatus.Status = "initialized"
		providers := t.router.GetAvailableProviders()
		if len(providers) > 0 {
			routerStatus.Message = fmt.Sprintf("providers: %s", strings.Join(providers, ", "))
		}
		result.Capabilities = append(result.Capabilities, "intelligent_routing", "provider_fallback", "result_caching")
	} else {
		routerStatus.Available = false
		routerStatus.Status = "not_initialized"
		routerStatus.Message = "using fallback mode"
	}
	deps = append(deps, routerStatus)

	// Check Brave provider
	braveStatus := types.DependencyStatus{
		Name:     "BraveSearch",
		Required: false, // At least one provider needed
	}

	if t.config.IsBraveEnabled() {
		braveStatus.Available = true
		braveStatus.Status = "configured"
	} else {
		braveStatus.Available = false
		if braveProvider, exists := t.config.Providers["brave"]; exists && braveProvider.Enabled {
			braveStatus.Status = "missing_api_key"
		} else {
			braveStatus.Status = "disabled"
		}
	}
	deps = append(deps, braveStatus)

	// Check Anthropic provider
	anthropicStatus := types.DependencyStatus{
		Name:     "AnthropicSearch",
		Required: false, // At least one provider needed
	}

	if t.config.IsAnthropicEnabled() {
		anthropicStatus.Available = true
		anthropicStatus.Status = "enabled"
		anthropicStatus.Message = "API key set dynamically from request context"
	} else {
		anthropicStatus.Available = false
		anthropicStatus.Status = "disabled"
	}
	deps = append(deps, anthropicStatus)

	// Determine overall status based on provider availability
	availableProviders := t.getAvailableProviders()
	if len(availableProviders) == 0 {
		result.Status = types.SelfTestStatusFailed
		result.Message = "No search providers available"
		result.Suggestions = []string{
			"Set BRAVE_API_KEY environment variable for Brave Search",
			"Or configure tools.services.brave.api_key in config",
			"Enable Anthropic search provider for fallback",
		}
	} else if t.router == nil {
		result.Status = types.SelfTestStatusDegraded
		result.Message = fmt.Sprintf("WebSearch operational in fallback mode (providers: %s)", strings.Join(availableProviders, ", "))
		result.UnavailableCapabilities = []string{"intelligent_routing", "provider_fallback", "result_caching"}
		result.Suggestions = []string{"Check search router initialization logs for errors"}
	} else {
		result.Message = fmt.Sprintf("WebSearch fully functional with %d providers (%s)",
			len(availableProviders), strings.Join(availableProviders, ", "))
	}

	result.Dependencies = deps
	result.TestDuration = time.Since(start)

	// Add verbose details if requested
	if opts.Verbose {
		result.Details = map[string]interface{}{
			"config_enabled":     t.config.Enabled,
			"default_provider":   t.config.DefaultProvider,
			"cache_enabled":      t.config.CacheEnabled,
			"cache_ttl_minutes":  t.config.CacheTTLMinutes,
			"enable_fallback":    t.config.EnableFallback,
			"available_providers": availableProviders,
		}
		if t.router != nil {
			result.Details["usage_stats"] = t.router.GetUsageStats()
		}
	}

	if opts.IncludeExamples && result.IsFunctional() {
		result.Examples = []types.ToolExample{
			{
				Name:        "Basic search",
				Description: "Search the web for a query",
				Args: map[string]interface{}{
					"query": "golang best practices",
					"count": 5,
				},
				Expected: "Returns up to 5 search results with intelligent provider selection",
			},
			{
				Name:        "Region-specific search",
				Description: "Search with region filtering",
				Args: map[string]interface{}{
					"query":   "local news",
					"country": "DE",
					"count":   10,
				},
				Expected: "Returns results localized to Germany",
			},
			{
				Name:        "Recent results search",
				Description: "Search for recent content only",
				Args: map[string]interface{}{
					"query":     "breaking news",
					"freshness": "pd",
				},
				Expected: "Returns only results from the past day",
			},
		}
	}

	return result
}

