package search

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// BraveSearchConfig configures the Brave search strategy
type BraveSearchConfig struct {
	APIKey         string        `json:"api_key"`
	Endpoint       string        `json:"endpoint"`
	Timeout        time.Duration `json:"timeout"`
	MaxRetries     int           `json:"max_retries"`
	RetryDelay     time.Duration `json:"retry_delay"`
	DefaultResults int           `json:"default_results"`
	MaxResults     int           `json:"max_results"`
}

// DefaultBraveConfig returns the default Brave search configuration
func DefaultBraveConfig() BraveSearchConfig {
	return BraveSearchConfig{
		Endpoint:       "https://api.search.brave.com/res/v1/web/search",
		Timeout:        30 * time.Second,
		MaxRetries:     3,
		RetryDelay:     1 * time.Second,
		DefaultResults: 5,
		MaxResults:     10,
	}
}

// BraveDirectSearch implements direct Brave Search API integration
type BraveDirectSearch struct {
	config     BraveSearchConfig
	httpClient *http.Client
	cache      *SearchCache
}

// BraveAPIResponse represents the raw API response from Brave Search
type BraveAPIResponse struct {
	Type string `json:"type"`
	Web  struct {
		Type    string `json:"type"`
		Results []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
			Published   string `json:"published,omitempty"`
			Thumbnail   string `json:"thumbnail,omitempty"`
		} `json:"results"`
	} `json:"web"`
	Query struct {
		Original string `json:"original"`
		Altered  string `json:"altered,omitempty"`
	} `json:"query"`
}

// NewBraveDirectSearch creates a new Brave search strategy
func NewBraveDirectSearch(config BraveSearchConfig, cacheConfig CacheConfig) (*BraveDirectSearch, error) {
	if config.APIKey == "" {
		return nil, ErrAPIKeyMissing
	}

	// Apply defaults if not set
	if config.Endpoint == "" {
		config.Endpoint = DefaultBraveConfig().Endpoint
	}
	if config.Timeout == 0 {
		config.Timeout = DefaultBraveConfig().Timeout
	}
	if config.MaxRetries == 0 {
		config.MaxRetries = DefaultBraveConfig().MaxRetries
	}
	if config.DefaultResults == 0 {
		config.DefaultResults = DefaultBraveConfig().DefaultResults
	}
	if config.MaxResults == 0 {
		config.MaxResults = DefaultBraveConfig().MaxResults
	}

	httpClient := &http.Client{
		Timeout: config.Timeout,
	}

	cache := NewSearchCache(cacheConfig)

	return &BraveDirectSearch{
		config:     config,
		httpClient: httpClient,
		cache:      cache,
	}, nil
}

// Name returns the strategy identifier
func (b *BraveDirectSearch) Name() string {
	return "brave"
}

// IsAvailable checks if the Brave API is available
func (b *BraveDirectSearch) IsAvailable() bool {
	return b.config.APIKey != ""
}

// Search performs a search using the Brave Search API
func (b *BraveDirectSearch) Search(ctx context.Context, params SearchParameters) (*SearchResponse, error) {
	// Validate parameters
	if err := params.Validate(); err != nil {
		return nil, &SearchError{
			Code:    "invalid_parameters",
			Message: err.Error(),
			Type:    "validation_error",
			Err:     err,
		}
	}

	// Check cache first
	if cached, found := b.cache.Get(params); found {
		return cached, nil
	}

	// Normalize count
	count := params.Count
	if count <= 0 {
		count = b.config.DefaultResults
	}
	if count > b.config.MaxResults {
		count = b.config.MaxResults
	}

	// Perform the API request with retries
	response, err := b.searchWithRetries(ctx, params, count)
	if err != nil {
		// Wrap the error in SearchError if it isn't already
		var searchErr *SearchError
		if !errors.As(err, &searchErr) {
			searchErr = &SearchError{
				Code:    "search_failed",
				Message: err.Error(),
				Type:    "api_error",
				Err:     err,
			}
		}
		return nil, searchErr
	}

	// Cache the response
	b.cache.Set(params, response)

	return response, nil
}

// searchWithRetries performs the search with retry logic
func (b *BraveDirectSearch) searchWithRetries(ctx context.Context, params SearchParameters, count int) (*SearchResponse, error) {
	var lastErr error

	for attempt := 0; attempt <= b.config.MaxRetries; attempt++ {
		if attempt > 0 {
			// Wait before retrying
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(b.config.RetryDelay * time.Duration(attempt)):
			}
		}

		response, err := b.performSearch(ctx, params, count)
		if err == nil {
			return response, nil
		}

		lastErr = err

		// Don't retry if it's not a retryable error
		if !IsRetryableError(err) {
			break
		}
	}

	return nil, fmt.Errorf("search failed after %d attempts: %w", b.config.MaxRetries+1, lastErr)
}

// performSearch performs a single search API request
func (b *BraveDirectSearch) performSearch(ctx context.Context, params SearchParameters, count int) (*SearchResponse, error) {
	// Build the API URL
	apiURL, err := b.buildAPIURL(params, count)
	if err != nil {
		return nil, fmt.Errorf("failed to build API URL: %w", err)
	}

	// Create the request
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("X-Subscription-Token", b.config.APIKey)
	req.Header.Set("User-Agent", "Conduit-Go/1.0")

	// Perform the request
	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, b.mapNetworkError(err)
	}
	defer resp.Body.Close()

	// Check status code
	if err := b.checkHTTPStatus(resp); err != nil {
		return nil, err
	}

	// Parse response
	return b.parseResponse(resp, params)
}

// buildAPIURL constructs the Brave Search API URL with parameters
func (b *BraveDirectSearch) buildAPIURL(params SearchParameters, count int) (string, error) {
	queryParams := url.Values{}
	queryParams.Add("q", params.Query)
	queryParams.Add("count", strconv.Itoa(count))

	if params.Country != "" {
		queryParams.Add("country", params.Country)
	}
	if params.SearchLang != "" {
		queryParams.Add("search_lang", params.SearchLang)
	}
	if params.UILang != "" {
		queryParams.Add("ui_lang", params.UILang)
	}
	if params.Freshness != "" {
		queryParams.Add("freshness", params.Freshness)
	}

	return b.config.Endpoint + "?" + queryParams.Encode(), nil
}

// checkHTTPStatus checks the HTTP response status and maps errors
func (b *BraveDirectSearch) checkHTTPStatus(resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized:
		return ErrAPIUnauthorized
	case http.StatusPaymentRequired:
		return ErrAPIQuotaExceeded
	case http.StatusTooManyRequests:
		return ErrAPIRateLimit
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable:
		return ErrAPIServerError
	default:
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}
}

// parseResponse parses the Brave API response
func (b *BraveDirectSearch) parseResponse(resp *http.Response, params SearchParameters) (*SearchResponse, error) {
	var reader io.Reader = resp.Body

	contentEncoding := resp.Header.Get("Content-Encoding")
	log.Printf("[BraveSearch] Content-Encoding: %q", contentEncoding)

	// Handle gzip decompression if needed
	if contentEncoding == "gzip" {
		log.Printf("[BraveSearch] Detected gzip encoding, decompressing...")
		gzipReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			log.Printf("[BraveSearch] gzip decompression failed: %v", err)
			return nil, fmt.Errorf("failed to decompress gzip response: %w", err)
		}
		defer gzipReader.Close()
		reader = gzipReader
		log.Printf("[BraveSearch] gzip reader created successfully")
	} else {
		log.Printf("[BraveSearch] No gzip encoding detected, reading directly")
	}

	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var apiResp BraveAPIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to parse JSON response: %w", err)
	}

	// Convert to standard format
	results := make([]SearchResult, len(apiResp.Web.Results))
	for i, result := range apiResp.Web.Results {
		results[i] = SearchResult{
			Title:       result.Title,
			URL:         result.URL,
			Description: result.Description,
			Published:   result.Published,
			Thumbnail:   result.Thumbnail,
		}
	}

	return &SearchResponse{
		Results:   results,
		Query:     params.Query,
		Total:     len(results),
		Provider:  "brave",
		Cached:    false,
		Timestamp: time.Now(),
		Metadata: map[string]interface{}{
			"original_query": apiResp.Query.Original,
			"altered_query":  apiResp.Query.Altered,
		},
	}, nil
}

// mapNetworkError maps Go HTTP errors to our error types
func (b *BraveDirectSearch) mapNetworkError(err error) error {
	if err == nil {
		return nil
	}

	errStr := strings.ToLower(err.Error())
	if strings.Contains(errStr, "timeout") || strings.Contains(errStr, "deadline exceeded") {
		return ErrNetworkTimeout
	}

	return fmt.Errorf("%w: %v", ErrNetworkError, err)
}

// GetCapabilities returns the capabilities of the Brave search strategy
func (b *BraveDirectSearch) GetCapabilities() SearchCapabilities {
	return SearchCapabilities{
		SupportsCountry:   true,
		SupportsLanguage:  true,
		SupportsFreshness: true,
		MaxResults:        b.config.MaxResults,
		DefaultResults:    b.config.DefaultResults,
		HasCaching:        true,
		RequiresAPIKey:    true,
	}
}

// GetCache returns the cache instance for testing/debugging
func (b *BraveDirectSearch) GetCache() *SearchCache {
	return b.cache
}
