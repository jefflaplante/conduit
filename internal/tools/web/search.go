package web

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"conduit/internal/tools/types"
)

// WebSearchTool implements web search using Brave Search API
type WebSearchTool struct {
	services    *types.ToolServices
	httpClient  *http.Client
	braveAPIKey string
}

// BraveSearchResult represents a single search result
type BraveSearchResult struct {
	Title       string      `json:"title"`
	URL         string      `json:"url"`
	Description string      `json:"description"`
	Published   string      `json:"published,omitempty"`
	Thumbnail   interface{} `json:"thumbnail,omitempty"` // Can be string or object
}

// BraveSearchResponse represents the API response from Brave Search
type BraveSearchResponse struct {
	Type string `json:"type"`
	Web  struct {
		Type    string              `json:"type"`
		Results []BraveSearchResult `json:"results"`
	} `json:"web"`
	Query struct {
		Original string `json:"original"`
		Altered  string `json:"altered,omitempty"`
	} `json:"query"`
}

func NewWebSearchTool(services *types.ToolServices) *WebSearchTool {
	tool := &WebSearchTool{
		services: services,
	}

	// Get HTTP client from services
	if services != nil && services.WebClient != nil {
		tool.httpClient = services.WebClient

		// Get API key from configuration
		if services.ConfigMgr != nil {
			// Get Brave API key from tools.services.brave.api_key
			if braveConfig, exists := services.ConfigMgr.Tools.Services["brave"]; exists {
				if apiKey, keyExists := braveConfig["api_key"]; keyExists {
					if apiKeyStr, ok := apiKey.(string); ok {
						tool.braveAPIKey = apiKeyStr
					}
				}
			}
		}
	}

	// Fallback to default client if not provided
	if tool.httpClient == nil {
		tool.httpClient = &http.Client{
			Timeout: 30 * time.Second,
		}
	}

	return tool
}

func (t *WebSearchTool) Name() string {
	return "WebSearch"
}

func (t *WebSearchTool) Description() string {
	return "Search the web using Brave Search API with support for region-specific results"
}

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
				"default":     10,
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
		},
		"required": []string{"query"},
	}
}

func (t *WebSearchTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	query, ok := args["query"].(string)
	if !ok {
		return &types.ToolResult{
			Success: false,
			Error:   "query parameter is required and must be a string",
		}, nil
	}

	count := t.getIntArg(args, "count", 10)
	country := t.getStringArg(args, "country", "US")
	freshness := t.getStringArg(args, "freshness", "")
	searchLang := t.getStringArg(args, "search_lang", "")

	// Validate count
	if count < 1 || count > 10 {
		count = 10
	}

	// Check for API key
	if t.braveAPIKey == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "Brave Search API key not configured",
		}, nil
	}

	// Debug logging
	log.Printf("[WebSearch] API key loaded: %s", t.braveAPIKey[:8]+"...")

	// Perform search
	results, err := t.searchBrave(ctx, query, count, country, freshness, searchLang)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("search failed: %v", err),
		}, nil
	}

	// Format results
	content := t.formatSearchResults(results, query)

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data: map[string]interface{}{
			"results": results,
			"query":   query,
			"total":   len(results),
			"country": country,
		},
	}, nil
}

// searchBrave performs the actual search using Brave Search API
func (t *WebSearchTool) searchBrave(ctx context.Context, query string, count int, country, freshness, searchLang string) ([]BraveSearchResult, error) {
	// Construct the API URL
	baseURL := "https://api.search.brave.com/res/v1/web/search"
	params := url.Values{}
	params.Add("q", query)
	params.Add("count", fmt.Sprintf("%d", count))
	params.Add("country", country)

	if freshness != "" {
		params.Add("freshness", freshness)
	}
	if searchLang != "" {
		params.Add("search_lang", searchLang)
	}

	fullURL := baseURL + "?" + params.Encode()

	// Create request
	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Add headers
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("X-Subscription-Token", t.braveAPIKey)

	// Perform request
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// Check status code
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("[WebSearch] API error: status %d, body: %s", resp.StatusCode, string(body))
		return nil, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Parse response - handle gzip decompression
	var reader io.Reader = resp.Body

	// Handle gzip decompression if needed
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gzipReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to decompress gzip response: %w", err)
		}
		defer gzipReader.Close()
		reader = gzipReader
	}

	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var searchResp BraveSearchResponse
	if err := json.Unmarshal(body, &searchResp); err != nil {
		log.Printf("[WebSearch] Parse error: %v, body: %s", err, string(body)[:500])
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	log.Printf("[WebSearch] Success: got %d results for query '%s'", len(searchResp.Web.Results), query)
	return searchResp.Web.Results, nil
}

// formatSearchResults formats search results for display
func (t *WebSearchTool) formatSearchResults(results []BraveSearchResult, query string) string {
	if len(results) == 0 {
		return fmt.Sprintf("No results found for query: '%s'", query)
	}

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Found %d results for '%s':\n\n", len(results), query))

	for i, result := range results {
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

// Helper methods
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

// ToolResult is imported from the tools package
