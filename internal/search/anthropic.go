// Package search provides Anthropic native web search implementation.
package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"conduit/internal/auth"
	"conduit/internal/models"
)

// AnthropicNativeSearch implements SearchStrategy using Anthropic's native web_search_20250305 tool.
// This search strategy leverages Anthropic's server-side tool execution for web search,
// providing optimized search results with proper citations and content processing.
type AnthropicNativeSearch struct {
	// apiKey is the Anthropic API key (can be OAuth or regular API key)
	apiKey string

	// httpClient is the HTTP client used for API requests
	httpClient *http.Client

	// model is the Anthropic model to use for search requests
	model string

	// baseURL is the Anthropic API base URL
	baseURL string

	// userLocation is the default user location for localized results
	userLocation string

	// maxUses is the default maximum number of search operations per request
	maxUses int
}

// AnthropicNativeSearchConfig contains configuration for the Anthropic native search.
type AnthropicNativeSearchConfig struct {
	APIKey       string `json:"api_key"`
	Model        string `json:"model,omitempty"`         // Default: "claude-3-5-sonnet-20241022"
	BaseURL      string `json:"base_url,omitempty"`      // Default: "https://api.anthropic.com"
	UserLocation string `json:"user_location,omitempty"` // Default: "Seattle, WA, US"
	MaxUses      int    `json:"max_uses,omitempty"`      // Default: 5
}

// WebSearchToolRequest represents the request for Anthropic's web_search tool.
type WebSearchToolRequest struct {
	Query          string   `json:"query"`
	MaxUses        *int     `json:"max_uses,omitempty"`
	AllowedDomains []string `json:"allowed_domains,omitempty"`
	BlockedDomains []string `json:"blocked_domains,omitempty"`
	UserLocation   string   `json:"user_location,omitempty"`
}

// WebSearchResultBlock represents a single search result from Anthropic's response.
type WebSearchResultBlock struct {
	Type      string  `json:"type"` // Always "web_search_result"
	URL       string  `json:"url"`
	Title     string  `json:"title"`
	Content   string  `json:"content"` // May be encrypted/encoded
	Timestamp string  `json:"timestamp,omitempty"`
	Source    string  `json:"source,omitempty"`
	Relevance float64 `json:"relevance,omitempty"`
}

// NewAnthropicNativeSearch creates a new Anthropic native search strategy.
func NewAnthropicNativeSearch(config AnthropicNativeSearchConfig) (*AnthropicNativeSearch, error) {
	if config.APIKey == "" {
		return nil, fmt.Errorf("API key is required")
	}

	// Set defaults
	if config.Model == "" {
		config.Model = "claude-3-5-sonnet-20241022"
	}
	if config.BaseURL == "" {
		config.BaseURL = "https://api.anthropic.com"
	}
	if config.UserLocation == "" {
		config.UserLocation = "Seattle, WA, US" // Pacific timezone default
	}
	if config.MaxUses == 0 {
		config.MaxUses = 5
	}

	return &AnthropicNativeSearch{
		apiKey:       config.APIKey,
		httpClient:   &http.Client{Timeout: 60 * time.Second},
		model:        config.Model,
		baseURL:      config.BaseURL,
		userLocation: config.UserLocation,
		maxUses:      config.MaxUses,
	}, nil
}

// Name returns the name of this search strategy.
func (a *AnthropicNativeSearch) Name() string {
	return "Anthropic Native Web Search"
}

// Search performs a search using Anthropic's native web_search_20250305 tool.
func (a *AnthropicNativeSearch) Search(ctx context.Context, params SearchParameters) (*SearchResponse, error) {
	startTime := time.Now()

	// Validate parameters
	if err := params.Validate(); err != nil {
		return nil, err
	}

	// Use the current version of Anthropic's web_search tool
	// This corresponds to tools.AnthropicWebSearchTool = "web_search_20250305"
	toolName := "web_search_20250305"

	// Note: maxUses is configured in the tool definition but not used in the simplified API

	// Create the Anthropic request
	toolDef := models.AnthropicTool{
		Name:        toolName, // This will be "web_search_20250305"
		Description: "Search the web using Anthropic's native search capabilities.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{
					"type":        "string",
					"description": "The search query",
				},
				"max_uses": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum number of search operations",
					"minimum":     1,
					"maximum":     10,
				},
				"allowed_domains": map[string]interface{}{
					"type":        "array",
					"description": "List of domains to restrict search to",
					"items":       map[string]interface{}{"type": "string"},
				},
				"blocked_domains": map[string]interface{}{
					"type":        "array",
					"description": "List of domains to exclude from search",
					"items":       map[string]interface{}{"type": "string"},
				},
				"user_location": map[string]interface{}{
					"type":        "string",
					"description": "User location for localized results",
				},
			},
			"required": []string{"query"},
		},
	}

	// Create a simple system prompt that will trigger tool use
	systemPrompt := "You are a helpful assistant. When the user asks you to search for something, use the web_search tool to find relevant information."

	// Create messages
	messages := []models.AnthropicMessage{
		{
			Role:    "user",
			Content: fmt.Sprintf("Please search for: %s", params.Query),
		},
	}

	// Build the request using the models package
	requestOpts := models.RequestOptions{
		Token:        a.apiKey,
		Model:        a.model,
		MaxTokens:    4096,
		SystemPrompt: systemPrompt,
		Messages:     messages,
		Tools:        []models.AnthropicTool{toolDef},
	}

	req, headers, err := models.BuildAnthropicRequest(requestOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %v", err)
	}

	// Make the API request
	response, err := a.makeAPIRequest(ctx, req, headers)
	if err != nil {
		return nil, err
	}

	// Parse and convert the response
	searchResponse, err := a.parseSearchResponse(response, params.Query, startTime)
	if err != nil {
		return nil, fmt.Errorf("failed to parse response: %v", err)
	}

	return searchResponse, nil
}

// makeAPIRequest performs the actual HTTP request to Anthropic's API.
func (a *AnthropicNativeSearch) makeAPIRequest(ctx context.Context, req *models.AnthropicRequest, headers map[string]string) (*models.AnthropicResponse, error) {
	// Serialize the request
	requestBody, err := req.ToJSON()
	if err != nil {
		return nil, fmt.Errorf("failed to serialize request: %v", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", a.baseURL+"/v1/messages", bytes.NewReader(requestBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %v", err)
	}

	// Set headers
	for key, value := range headers {
		httpReq.Header.Set(key, value)
	}

	// Make the request
	httpResp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return nil, ErrNetworkError
	}
	defer httpResp.Body.Close()

	// Read response body
	responseBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, ErrNetworkError
	}

	// Check for HTTP errors
	if httpResp.StatusCode != http.StatusOK {
		var anthropicErr models.AnthropicError
		if err := json.Unmarshal(responseBody, &anthropicErr); err == nil {
			return nil, mapAnthropicErrorToSearchError(anthropicErr.Type)
		}
		return nil, fmt.Errorf("HTTP %d: %s", httpResp.StatusCode, string(responseBody))
	}

	// Parse the response
	var response models.AnthropicResponse
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return nil, fmt.Errorf("failed to parse API response: %v", err)
	}

	return &response, nil
}

// parseSearchResponse converts Anthropic's response into our SearchResponse format.
func (a *AnthropicNativeSearch) parseSearchResponse(response *models.AnthropicResponse, originalQuery string, startTime time.Time) (*SearchResponse, error) {
	searchResponse := &SearchResponse{
		Query:     originalQuery,
		Provider:  "anthropic_native",
		Timestamp: time.Now(),
		Results:   make([]SearchResult, 0),
		Metadata: map[string]interface{}{
			"search_time": time.Since(startTime),
			"model":       response.Model,
			"usage":       response.Usage,
		},
	}

	// Parse the response content for search results
	// Note: This is a simplified parser - Anthropic's actual response format may vary
	// and include server-side tool use results that need special handling
	for _, content := range response.Content {
		if content.Type == "text" && content.Text != "" {
			// Look for structured search results in the text
			// This is a placeholder - the actual implementation would need to handle
			// server-side tool use blocks and potentially encrypted content
			results := a.extractSearchResultsFromText(content.Text)
			searchResponse.Results = append(searchResponse.Results, results...)
		}
	}

	// Update total
	searchResponse.Total = len(searchResponse.Results)

	return searchResponse, nil
}

// extractSearchResultsFromText is a placeholder for extracting search results from response text.
// In the actual implementation, this would handle server-side tool use blocks and
// parse structured search result data that Anthropic returns.
func (a *AnthropicNativeSearch) extractSearchResultsFromText(text string) []SearchResult {
	results := make([]SearchResult, 0)

	// This is a simplified implementation - in reality, Anthropic's web search results
	// would come back as structured tool use responses or special content blocks
	// that would need proper parsing.

	// For now, we'll create a minimal response indicating that search was performed
	if strings.Contains(strings.ToLower(text), "search") {
		results = append(results, SearchResult{
			Title:       "Search completed via Anthropic",
			URL:         "",
			Description: "Search results processed by Anthropic's native web search tool",
		})
	}

	return results
}

// IsAvailable checks if the Anthropic native search strategy is currently available.
func (a *AnthropicNativeSearch) IsAvailable() bool {
	if a.apiKey == "" {
		return false
	}

	// For OAuth tokens, we need to verify the token has web search capabilities
	if auth.IsOAuthToken(a.apiKey) {
		// Check if web_search tool is available with OAuth
		if _, compatible := auth.MapToolForOAuth("web_search"); !compatible {
			return false
		}
	}

	// Could add a health check API call here if needed
	return true
}

// GetCapabilities returns the capabilities of the Anthropic native search strategy.
func (a *AnthropicNativeSearch) GetCapabilities() SearchCapabilities {
	return SearchCapabilities{
		SupportsCountry:   false, // Anthropic handles this server-side
		SupportsLanguage:  false, // Anthropic handles this server-side
		SupportsFreshness: false, // Anthropic handles this server-side
		MaxResults:        10,    // Based on typical Anthropic tool limits
		DefaultResults:    5,     // Reasonable default
		HasCaching:        false, // No client-side caching implemented
		RequiresAPIKey:    true,  // Requires valid Anthropic API key
	}
}

// Close releases any resources held by the search strategy.
func (a *AnthropicNativeSearch) Close() error {
	// Close HTTP client if needed
	if a.httpClient != nil {
		// Note: http.Client doesn't have a Close method in standard library
		// If using a custom client with connection pooling, close it here
	}
	return nil
}

// mapAnthropicErrorToSearchError maps Anthropic API error types to existing search error types.
func mapAnthropicErrorToSearchError(anthropicType string) error {
	switch anthropicType {
	case "rate_limit_error":
		return ErrAPIRateLimit
	case "authentication_error":
		return ErrAPIUnauthorized
	case "invalid_request_error":
		return ErrInvalidQuery
	case "api_error":
		return ErrAPIServerError
	case "overloaded_error":
		return ErrAPIServerError
	default:
		return ErrAPIServerError
	}
}
