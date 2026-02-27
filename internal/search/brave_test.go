package search

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Mock Brave API responses
const (
	mockBraveSuccessResponse = `{
		"type": "search",
		"web": {
			"type": "web",
			"results": [
				{
					"title": "Conduit - AI-Powered Assistant",
					"url": "https://example.com/conduit",
					"description": "Conduit is an advanced AI assistant platform that provides powerful automation and integration capabilities.",
					"published": "2024-01-15",
					"thumbnail": "https://example.com/thumb.jpg"
				},
				{
					"title": "Getting Started with Conduit",
					"url": "https://docs.example.com/conduit",
					"description": "Learn how to set up and configure Conduit for your workflow automation needs."
				}
			]
		},
		"query": {
			"original": "Conduit AI assistant",
			"altered": ""
		}
	}`

	mockBraveEmptyResponse = `{
		"type": "search",
		"web": {
			"type": "web",
			"results": []
		},
		"query": {
			"original": "nonexistent query xyz123",
			"altered": ""
		}
	}`
)

func TestNewBraveDirectSearch(t *testing.T) {
	tests := []struct {
		name        string
		config      BraveSearchConfig
		expectError bool
		errorType   error
	}{
		{
			name: "valid config",
			config: BraveSearchConfig{
				APIKey:   "test-api-key",
				Endpoint: "https://api.example.com/search",
			},
			expectError: false,
		},
		{
			name: "missing API key",
			config: BraveSearchConfig{
				Endpoint: "https://api.example.com/search",
			},
			expectError: true,
			errorType:   ErrAPIKeyMissing,
		},
		{
			name: "defaults applied",
			config: BraveSearchConfig{
				APIKey: "test-api-key",
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cacheConfig := DefaultCacheConfig()
			brave, err := NewBraveDirectSearch(tt.config, cacheConfig)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorType != nil {
					assert.ErrorIs(t, err, tt.errorType)
				}
				assert.Nil(t, brave)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, brave)
				assert.Equal(t, "brave", brave.Name())
				assert.True(t, brave.IsAvailable())

				// Check that defaults were applied
				if tt.config.Endpoint == "" {
					assert.Equal(t, DefaultBraveConfig().Endpoint, brave.config.Endpoint)
				}
			}
		})
	}
}

func TestBraveDirectSearch_Search_Success(t *testing.T) {
	// Create a mock HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request headers
		assert.Equal(t, "application/json", r.Header.Get("Accept"))
		assert.Equal(t, "test-api-key", r.Header.Get("X-Subscription-Token"))

		// Verify query parameters
		query := r.URL.Query()
		assert.Equal(t, "Conduit AI assistant", query.Get("q"))
		assert.Equal(t, "5", query.Get("count"))
		assert.Equal(t, "US", query.Get("country"))

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, mockBraveSuccessResponse)
	}))
	defer server.Close()

	// Create Brave search instance with test server
	config := BraveSearchConfig{
		APIKey:         "test-api-key",
		Endpoint:       server.URL,
		Timeout:        5 * time.Second,
		MaxRetries:     1,
		DefaultResults: 5,
		MaxResults:     10,
	}
	cacheConfig := CacheConfig{TTLMinutes: 1, Enabled: false} // Disable cache for this test

	brave, err := NewBraveDirectSearch(config, cacheConfig)
	require.NoError(t, err)

	// Test search
	params := SearchParameters{
		Query:   "Conduit AI assistant",
		Count:   5,
		Country: "US",
	}

	ctx := context.Background()
	response, err := brave.Search(ctx, params)

	assert.NoError(t, err)
	assert.NotNil(t, response)
	assert.Equal(t, "Conduit AI assistant", response.Query)
	assert.Equal(t, "brave", response.Provider)
	assert.False(t, response.Cached)
	assert.Len(t, response.Results, 2)

	// Check first result
	result := response.Results[0]
	assert.Equal(t, "Conduit - AI-Powered Assistant", result.Title)
	assert.Equal(t, "https://example.com/conduit", result.URL)
	assert.Contains(t, result.Description, "Conduit is an advanced AI assistant")
	assert.Equal(t, "2024-01-15", result.Published)
	assert.Equal(t, "https://example.com/thumb.jpg", result.Thumbnail)

	// Check metadata
	assert.Contains(t, response.Metadata, "original_query")
	assert.Equal(t, "Conduit AI assistant", response.Metadata["original_query"])
}

func TestBraveDirectSearch_Search_EmptyResults(t *testing.T) {
	// Create a mock HTTP server that returns empty results
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, mockBraveEmptyResponse)
	}))
	defer server.Close()

	config := BraveSearchConfig{
		APIKey:   "test-api-key",
		Endpoint: server.URL,
	}
	cacheConfig := CacheConfig{TTLMinutes: 1, Enabled: false}

	brave, err := NewBraveDirectSearch(config, cacheConfig)
	require.NoError(t, err)

	params := SearchParameters{
		Query: "nonexistent query xyz123",
		Count: 5,
	}

	response, err := brave.Search(context.Background(), params)

	assert.NoError(t, err)
	assert.NotNil(t, response)
	assert.Len(t, response.Results, 0)
	assert.Equal(t, 0, response.Total)
}

func TestBraveDirectSearch_Search_APIErrors(t *testing.T) {
	tests := []struct {
		name          string
		statusCode    int
		responseBody  string
		expectedError error
	}{
		{
			name:          "unauthorized",
			statusCode:    http.StatusUnauthorized,
			responseBody:  `{"error": "Invalid API key"}`,
			expectedError: ErrAPIUnauthorized,
		},
		{
			name:          "quota exceeded",
			statusCode:    http.StatusPaymentRequired,
			responseBody:  `{"error": "Quota exceeded"}`,
			expectedError: ErrAPIQuotaExceeded,
		},
		{
			name:          "rate limit",
			statusCode:    http.StatusTooManyRequests,
			responseBody:  `{"error": "Rate limit exceeded"}`,
			expectedError: ErrAPIRateLimit,
		},
		{
			name:          "server error",
			statusCode:    http.StatusInternalServerError,
			responseBody:  `{"error": "Internal server error"}`,
			expectedError: ErrAPIServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				fmt.Fprint(w, tt.responseBody)
			}))
			defer server.Close()

			config := BraveSearchConfig{
				APIKey:     "test-api-key",
				Endpoint:   server.URL,
				MaxRetries: 0, // Don't retry for these tests
			}
			cacheConfig := CacheConfig{TTLMinutes: 1, Enabled: false}

			brave, err := NewBraveDirectSearch(config, cacheConfig)
			require.NoError(t, err)

			params := SearchParameters{Query: "test query"}

			_, err = brave.Search(context.Background(), params)

			assert.Error(t, err)
			assert.ErrorIs(t, err, tt.expectedError)
		})
	}
}

func TestBraveDirectSearch_Search_Cache(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, mockBraveSuccessResponse)
	}))
	defer server.Close()

	config := BraveSearchConfig{
		APIKey:   "test-api-key",
		Endpoint: server.URL,
	}
	cacheConfig := CacheConfig{TTLMinutes: 5, Enabled: true}

	brave, err := NewBraveDirectSearch(config, cacheConfig)
	require.NoError(t, err)

	params := SearchParameters{Query: "Conduit test"}

	// First request - should hit API
	response1, err := brave.Search(context.Background(), params)
	assert.NoError(t, err)
	assert.False(t, response1.Cached)
	assert.Equal(t, 1, requestCount)

	// Second request - should hit cache
	response2, err := brave.Search(context.Background(), params)
	assert.NoError(t, err)
	assert.True(t, response2.Cached)
	assert.Equal(t, 1, requestCount) // Request count shouldn't increase
}

func TestBraveDirectSearch_Search_Retries(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if requestCount < 3 {
			// Fail first two requests with server error
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// Succeed on third request
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, mockBraveSuccessResponse)
	}))
	defer server.Close()

	config := BraveSearchConfig{
		APIKey:     "test-api-key",
		Endpoint:   server.URL,
		MaxRetries: 3,
		RetryDelay: 10 * time.Millisecond, // Fast retries for testing
	}
	cacheConfig := CacheConfig{TTLMinutes: 1, Enabled: false}

	brave, err := NewBraveDirectSearch(config, cacheConfig)
	require.NoError(t, err)

	params := SearchParameters{Query: "retry test"}

	response, err := brave.Search(context.Background(), params)

	assert.NoError(t, err)
	assert.NotNil(t, response)
	assert.Equal(t, 3, requestCount) // Should have made 3 requests
}

func TestBraveDirectSearch_ParameterValidation(t *testing.T) {
	config := BraveSearchConfig{APIKey: "test-key"}
	cacheConfig := CacheConfig{TTLMinutes: 1, Enabled: false}

	brave, err := NewBraveDirectSearch(config, cacheConfig)
	require.NoError(t, err)

	tests := []struct {
		name        string
		params      SearchParams
		expectError bool
		errorType   error
	}{
		{
			name:        "empty query",
			params:      SearchParameters{},
			expectError: true,
			errorType:   ErrInvalidQuery,
		},
		{
			name: "invalid freshness",
			params: SearchParameters{
				Query:     "test",
				Freshness: "invalid",
			},
			expectError: true,
			errorType:   ErrInvalidFreshness,
		},
		{
			name: "valid parameters",
			params: SearchParameters{
				Query:      "test query",
				Count:      5,
				Country:    "US",
				Freshness:  "pw",
				SearchLang: "en",
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := brave.Search(context.Background(), tt.params)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorType != nil {
					assert.ErrorIs(t, err, tt.errorType)
				}
			} else {
				// We expect a network error since we're not setting up a server,
				// but parameter validation should pass
				assert.True(t, strings.Contains(err.Error(), "search failed") ||
					strings.Contains(err.Error(), "network"))
			}
		})
	}
}

func TestBraveDirectSearch_URLConstruction(t *testing.T) {
	config := BraveSearchConfig{APIKey: "test-key"}
	cacheConfig := CacheConfig{TTLMinutes: 1, Enabled: false}

	brave, err := NewBraveDirectSearch(config, cacheConfig)
	require.NoError(t, err)

	params := SearchParameters{
		Query:      "test query with spaces",
		Count:      5,
		Country:    "DE",
		SearchLang: "de",
		UILang:     "de",
		Freshness:  "pw",
	}

	url, err := brave.buildAPIURL(params, 5)
	assert.NoError(t, err)

	// Verify URL components
	assert.Contains(t, url, "q=test+query+with+spaces")
	assert.Contains(t, url, "count=5")
	assert.Contains(t, url, "country=DE")
	assert.Contains(t, url, "search_lang=de")
	assert.Contains(t, url, "ui_lang=de")
	assert.Contains(t, url, "freshness=pw")
}
