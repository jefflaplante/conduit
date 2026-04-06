package web

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"conduit/internal/config"
	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewWebSearchTool_NilServices(t *testing.T) {
	tool := NewWebSearchTool(nil)
	require.NotNil(t, tool)
	assert.NotNil(t, tool.httpClient, "should create default HTTP client when services is nil")
	assert.Empty(t, tool.braveAPIKey)
}

func TestNewWebSearchTool_WithServicesNoClient(t *testing.T) {
	services := &types.ToolServices{
		WebClient: nil,
	}

	tool := NewWebSearchTool(services)
	require.NotNil(t, tool)
	assert.NotNil(t, tool.httpClient, "should create default client when services.WebClient is nil")
}

func TestNewWebSearchTool_WithClient(t *testing.T) {
	customClient := &http.Client{Timeout: 15 * time.Second}
	services := &types.ToolServices{
		WebClient: customClient,
	}

	tool := NewWebSearchTool(services)
	require.NotNil(t, tool)
	assert.Equal(t, customClient, tool.httpClient)
}

func TestNewWebSearchTool_WithConfig(t *testing.T) {
	customClient := &http.Client{Timeout: 15 * time.Second}
	services := &types.ToolServices{
		WebClient: customClient,
		ConfigMgr: &config.Config{
			Tools: config.ToolsConfig{
				Services: map[string]map[string]interface{}{
					"brave": {
						"api_key": "test-api-key",
					},
				},
			},
		},
	}

	tool := NewWebSearchTool(services)
	require.NotNil(t, tool)
	assert.Equal(t, "test-api-key", tool.braveAPIKey)
}

func TestNewWebSearchTool_ConfigNoAPIKey(t *testing.T) {
	services := &types.ToolServices{
		WebClient: &http.Client{},
		ConfigMgr: &config.Config{
			Tools: config.ToolsConfig{
				Services: map[string]map[string]interface{}{
					"brave": {
						"other_setting": "value",
					},
				},
			},
		},
	}

	tool := NewWebSearchTool(services)
	require.NotNil(t, tool)
	assert.Empty(t, tool.braveAPIKey)
}

func TestNewWebSearchTool_ConfigAPIKeyWrongType(t *testing.T) {
	services := &types.ToolServices{
		WebClient: &http.Client{},
		ConfigMgr: &config.Config{
			Tools: config.ToolsConfig{
				Services: map[string]map[string]interface{}{
					"brave": {
						"api_key": 12345, // wrong type
					},
				},
			},
		},
	}

	tool := NewWebSearchTool(services)
	require.NotNil(t, tool)
	assert.Empty(t, tool.braveAPIKey)
}

func TestNewWebSearchTool_ConfigNoBraveService(t *testing.T) {
	services := &types.ToolServices{
		WebClient: &http.Client{},
		ConfigMgr: &config.Config{
			Tools: config.ToolsConfig{
				Services: map[string]map[string]interface{}{
					"other_service": {
						"api_key": "key",
					},
				},
			},
		},
	}

	tool := NewWebSearchTool(services)
	require.NotNil(t, tool)
	assert.Empty(t, tool.braveAPIKey)
}

func TestWebSearchTool_Name(t *testing.T) {
	tool := NewWebSearchTool(nil)
	assert.Equal(t, "WebSearch", tool.Name())
}

func TestWebSearchTool_Description(t *testing.T) {
	tool := NewWebSearchTool(nil)
	desc := tool.Description()
	assert.Contains(t, strings.ToLower(desc), "search")
	assert.Contains(t, strings.ToLower(desc), "brave")
}

func TestWebSearchTool_Parameters(t *testing.T) {
	tool := NewWebSearchTool(nil)
	params := tool.Parameters()

	require.NotNil(t, params)
	assert.Equal(t, "object", params["type"])

	props, ok := params["properties"].(map[string]interface{})
	require.True(t, ok)
	assert.Contains(t, props, "query")
	assert.Contains(t, props, "count")
	assert.Contains(t, props, "country")
	assert.Contains(t, props, "freshness")
	assert.Contains(t, props, "search_lang")

	required, ok := params["required"].([]string)
	require.True(t, ok)
	assert.Contains(t, required, "query")
}

func TestWebSearchTool_Execute_MissingQuery(t *testing.T) {
	tool := &WebSearchTool{
		httpClient:  &http.Client{},
		braveAPIKey: "test-key",
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "query parameter is required")
}

func TestWebSearchTool_Execute_InvalidQueryType(t *testing.T) {
	tool := &WebSearchTool{
		httpClient:  &http.Client{},
		braveAPIKey: "test-key",
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": 12345,
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "query parameter is required")
}

func TestWebSearchTool_Execute_NoAPIKey(t *testing.T) {
	tool := &WebSearchTool{
		httpClient:  &http.Client{},
		braveAPIKey: "",
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "test query",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "API key not configured")
}

func TestWebSearch_ShortAPIKey(t *testing.T) {
	// Previously the code did braveAPIKey[:8]+"..." which panics
	// if the key is shorter than 8 characters. The fix logs only
	// the key length, so this test verifies no panic occurs with
	// a short key.
	tool := &WebSearchTool{
		braveAPIKey: "abc", // Only 3 chars, would panic with [:8]
	}

	// Execute should reach the logging line and not panic.
	// It will return an error because we don't have a real HTTP client,
	// but the important thing is it doesn't panic.
	result, err := tool.Execute(nil, map[string]interface{}{
		"query": "test",
	})

	// We expect a non-nil result (the API key is set, so it won't hit
	// the "not configured" error). It might fail later due to nil httpClient
	// or nil context, but we just need to verify no panic from logging.
	if err != nil {
		// An actual error from nil context/client is fine.
		// The test is about not panicking.
		return
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestWebSearch_EmptyAPIKey(t *testing.T) {
	tool := &WebSearchTool{
		braveAPIKey: "",
	}

	result, err := tool.Execute(nil, map[string]interface{}{
		"query": "test",
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Success, "expected failure when API key is not configured")
}

func TestWebSearchTool_Execute_InvalidCount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify count was normalized
		count := r.URL.Query().Get("count")
		assert.Equal(t, "10", count) // Should be normalized to 10

		resp := BraveSearchResponse{}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	tool := &WebSearchTool{
		httpClient:  server.Client(),
		braveAPIKey: "test-key",
	}

	// Test count < 1
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "test",
		"count": 0,
	})

	// This will fail because the httptest server URL doesn't match Brave's URL
	// The test just ensures count normalization logic runs without panic
	require.NoError(t, err)
	assert.False(t, result.Success)
}

func TestWebSearchTool_Execute_CountTooHigh(t *testing.T) {
	tool := &WebSearchTool{
		httpClient:  &http.Client{},
		braveAPIKey: "test-key",
	}

	// Test count > 10 (will be normalized to 10)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "test",
		"count": 100,
	})

	// Request will fail due to network, but count normalization should have run
	require.NoError(t, err)
	assert.False(t, result.Success)
}

func TestWebSearchTool_Execute_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request parameters
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Accept"))
		assert.Equal(t, "test-api-key", r.Header.Get("X-Subscription-Token"))

		q := r.URL.Query()
		assert.Equal(t, "golang testing", q.Get("q"))
		assert.Equal(t, "5", q.Get("count"))
		assert.Equal(t, "US", q.Get("country"))

		resp := BraveSearchResponse{
			Type: "search",
			Web: struct {
				Type    string              `json:"type"`
				Results []BraveSearchResult `json:"results"`
			}{
				Type: "web",
				Results: []BraveSearchResult{
					{
						Title:       "Go Testing Guide",
						URL:         "https://example.com/go-testing",
						Description: "A comprehensive guide to testing in Go.",
						Published:   "2024-01-15",
					},
					{
						Title:       "Testing Best Practices",
						URL:         "https://example.com/testing-best",
						Description: "Best practices for unit testing.",
					},
				},
			},
			Query: struct {
				Original string `json:"original"`
				Altered  string `json:"altered,omitempty"`
			}{
				Original: "golang testing",
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Create tool with custom HTTP client that redirects to test server
	tool := &WebSearchTool{
		httpClient:  server.Client(),
		braveAPIKey: "test-api-key",
	}

	// Override the base URL by using a transport that redirects requests
	origTransport := tool.httpClient.Transport
	tool.httpClient.Transport = &testTransport{
		server:   server,
		original: origTransport,
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "golang testing",
		"count": 5,
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "Go Testing Guide")
	assert.Contains(t, result.Content, "https://example.com/go-testing")
	assert.Contains(t, result.Content, "Testing Best Practices")
	assert.Equal(t, 2, result.Data["total"])
	assert.Equal(t, "golang testing", result.Data["query"])
	assert.Equal(t, "US", result.Data["country"])

	results, ok := result.Data["results"].([]BraveSearchResult)
	require.True(t, ok)
	assert.Len(t, results, 2)
}

// testTransport redirects requests to our test server
type testTransport struct {
	server   *httptest.Server
	original http.RoundTripper
}

func (t *testTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Redirect Brave API requests to test server
	if strings.Contains(req.URL.Host, "api.search.brave.com") {
		newURL := t.server.URL + req.URL.Path + "?" + req.URL.RawQuery
		newReq, _ := http.NewRequestWithContext(req.Context(), req.Method, newURL, req.Body)
		newReq.Header = req.Header
		return http.DefaultTransport.RoundTrip(newReq)
	}
	if t.original != nil {
		return t.original.RoundTrip(req)
	}
	return http.DefaultTransport.RoundTrip(req)
}

func TestWebSearchTool_Execute_GzipResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := BraveSearchResponse{
			Type: "search",
			Web: struct {
				Type    string              `json:"type"`
				Results []BraveSearchResult `json:"results"`
			}{
				Type: "web",
				Results: []BraveSearchResult{
					{
						Title:       "Gzip Test",
						URL:         "https://example.com/gzip",
						Description: "Testing gzip decompression.",
					},
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")

		gzWriter := gzip.NewWriter(w)
		json.NewEncoder(gzWriter).Encode(resp)
		gzWriter.Close()
	}))
	defer server.Close()

	tool := &WebSearchTool{
		httpClient:  server.Client(),
		braveAPIKey: "test-api-key",
	}

	tool.httpClient.Transport = &testTransport{server: server}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "gzip test",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "Gzip Test")
}

func TestWebSearchTool_Execute_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, "Invalid API key")
	}))
	defer server.Close()

	tool := &WebSearchTool{
		httpClient:  server.Client(),
		braveAPIKey: "invalid-key",
	}

	tool.httpClient.Transport = &testTransport{server: server}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "test",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "search failed")
	assert.Contains(t, result.Error, "401")
}

func TestWebSearchTool_Execute_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, "invalid json {{{")
	}))
	defer server.Close()

	tool := &WebSearchTool{
		httpClient:  server.Client(),
		braveAPIKey: "test-key",
	}

	tool.httpClient.Transport = &testTransport{server: server}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "test",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "search failed")
}

func TestWebSearchTool_Execute_EmptyResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := BraveSearchResponse{
			Type: "search",
			Web: struct {
				Type    string              `json:"type"`
				Results []BraveSearchResult `json:"results"`
			}{
				Type:    "web",
				Results: []BraveSearchResult{},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	tool := &WebSearchTool{
		httpClient:  server.Client(),
		braveAPIKey: "test-key",
	}

	tool.httpClient.Transport = &testTransport{server: server}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "no results query",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "No results found")
	assert.Equal(t, 0, result.Data["total"])
}

func TestWebSearchTool_Execute_WithFreshness(t *testing.T) {
	var receivedQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedQuery = r.URL.RawQuery
		resp := BraveSearchResponse{}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	tool := &WebSearchTool{
		httpClient:  server.Client(),
		braveAPIKey: "test-key",
	}

	tool.httpClient.Transport = &testTransport{server: server}

	_, _ = tool.Execute(context.Background(), map[string]interface{}{
		"query":     "fresh results",
		"freshness": "pd",
	})

	assert.Contains(t, receivedQuery, "freshness=pd")
}

func TestWebSearchTool_Execute_WithSearchLang(t *testing.T) {
	var receivedQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedQuery = r.URL.RawQuery
		resp := BraveSearchResponse{}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	tool := &WebSearchTool{
		httpClient:  server.Client(),
		braveAPIKey: "test-key",
	}

	tool.httpClient.Transport = &testTransport{server: server}

	_, _ = tool.Execute(context.Background(), map[string]interface{}{
		"query":       "german results",
		"search_lang": "de",
	})

	assert.Contains(t, receivedQuery, "search_lang=de")
}

func TestWebSearchTool_Execute_WithCountry(t *testing.T) {
	var receivedQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedQuery = r.URL.RawQuery
		resp := BraveSearchResponse{}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	tool := &WebSearchTool{
		httpClient:  server.Client(),
		braveAPIKey: "test-key",
	}

	tool.httpClient.Transport = &testTransport{server: server}

	_, _ = tool.Execute(context.Background(), map[string]interface{}{
		"query":   "german results",
		"country": "DE",
	})

	assert.Contains(t, receivedQuery, "country=DE")
}

func TestFormatSearchResults_Empty(t *testing.T) {
	tool := &WebSearchTool{}

	content := tool.formatSearchResults([]BraveSearchResult{}, "test query")

	assert.Contains(t, content, "No results found")
	assert.Contains(t, content, "test query")
}

func TestFormatSearchResults_SingleResult(t *testing.T) {
	tool := &WebSearchTool{}

	results := []BraveSearchResult{
		{
			Title:       "Test Title",
			URL:         "https://example.com",
			Description: "Test description",
			Published:   "2024-01-01",
		},
	}

	content := tool.formatSearchResults(results, "test")

	assert.Contains(t, content, "Found 1 results")
	assert.Contains(t, content, "1. **Test Title**")
	assert.Contains(t, content, "https://example.com")
	assert.Contains(t, content, "Test description")
	assert.Contains(t, content, "Published: 2024-01-01")
}

func TestFormatSearchResults_MultipleResults(t *testing.T) {
	tool := &WebSearchTool{}

	results := []BraveSearchResult{
		{
			Title:       "First Result",
			URL:         "https://first.com",
			Description: "First description",
		},
		{
			Title:       "Second Result",
			URL:         "https://second.com",
			Description: "Second description",
		},
		{
			Title:       "Third Result",
			URL:         "https://third.com",
			Description: "Third description",
		},
	}

	content := tool.formatSearchResults(results, "multiple")

	assert.Contains(t, content, "Found 3 results")
	assert.Contains(t, content, "1. **First Result**")
	assert.Contains(t, content, "2. **Second Result**")
	assert.Contains(t, content, "3. **Third Result**")
}

func TestFormatSearchResults_LongDescription(t *testing.T) {
	tool := &WebSearchTool{}

	longDesc := strings.Repeat("A", 300)
	results := []BraveSearchResult{
		{
			Title:       "Long Description Test",
			URL:         "https://example.com",
			Description: longDesc,
		},
	}

	content := tool.formatSearchResults(results, "test")

	// Description should be truncated to ~200 chars
	assert.Contains(t, content, "...")
	// Should not contain the full description
	assert.Less(t, len(content), 500)
}

func TestFormatSearchResults_NoDescription(t *testing.T) {
	tool := &WebSearchTool{}

	results := []BraveSearchResult{
		{
			Title:       "No Desc",
			URL:         "https://example.com",
			Description: "",
		},
	}

	content := tool.formatSearchResults(results, "test")

	assert.Contains(t, content, "No Desc")
	assert.Contains(t, content, "https://example.com")
}

func TestFormatSearchResults_NoPublished(t *testing.T) {
	tool := &WebSearchTool{}

	results := []BraveSearchResult{
		{
			Title:       "No Date",
			URL:         "https://example.com",
			Description: "Some description",
			Published:   "",
		},
	}

	content := tool.formatSearchResults(results, "test")

	assert.NotContains(t, content, "Published:")
}

func TestWebSearchTool_Execute_ContextCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tool := &WebSearchTool{
		httpClient:  server.Client(),
		braveAPIKey: "test-key",
	}

	tool.httpClient.Transport = &testTransport{server: server}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	result, err := tool.Execute(ctx, map[string]interface{}{
		"query": "test",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "search failed")
}

func TestWebSearchTool_Execute_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Create client with very short timeout
	client := &http.Client{
		Timeout: 10 * time.Millisecond,
	}

	tool := &WebSearchTool{
		httpClient:  client,
		braveAPIKey: "test-key",
	}

	tool.httpClient.Transport = &testTransport{server: server}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "test",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "search failed")
}

func TestWebSearchTool_Execute_ConnectionRefused(t *testing.T) {
	tool := &WebSearchTool{
		httpClient:  &http.Client{Timeout: 2 * time.Second},
		braveAPIKey: "test-key",
	}

	// Try to connect to a port that's not listening
	tool.httpClient.Transport = &testTransport{
		server: &httptest.Server{
			URL: "http://127.0.0.1:59998",
		},
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"query": "test",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "search failed")
}

func TestBraveSearchResult_ThumbnailTypes(t *testing.T) {
	// Test that BraveSearchResult can handle different thumbnail types
	jsonStr := `{
		"title": "Test",
		"url": "https://example.com",
		"description": "Test desc",
		"thumbnail": "https://example.com/thumb.jpg"
	}`

	var result BraveSearchResult
	err := json.Unmarshal([]byte(jsonStr), &result)
	require.NoError(t, err)
	assert.Equal(t, "Test", result.Title)

	// Test with object thumbnail
	jsonStr2 := `{
		"title": "Test2",
		"url": "https://example.com",
		"description": "Test desc",
		"thumbnail": {"src": "https://example.com/thumb.jpg"}
	}`

	var result2 BraveSearchResult
	err = json.Unmarshal([]byte(jsonStr2), &result2)
	require.NoError(t, err)
	assert.Equal(t, "Test2", result2.Title)
}

func TestWebSearchTool_Execute_CountFromFloat64(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := r.URL.Query().Get("count")
		assert.Equal(t, "5", count)

		resp := BraveSearchResponse{}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	tool := &WebSearchTool{
		httpClient:  server.Client(),
		braveAPIKey: "test-key",
	}

	tool.httpClient.Transport = &testTransport{server: server}

	// JSON numbers are parsed as float64
	_, _ = tool.Execute(context.Background(), map[string]interface{}{
		"query": "test",
		"count": float64(5),
	})
}

func TestWebSearchTool_SelfTest_OK(t *testing.T) {
	tool := &WebSearchTool{
		httpClient:  &http.Client{Timeout: 30 * time.Second},
		braveAPIKey: "test-api-key",
	}

	result := tool.SelfTest(context.Background(), nil)

	require.NotNil(t, result)
	assert.Equal(t, types.SelfTestStatusOK, result.Status)
	assert.Contains(t, result.Message, "functional")
	assert.NotEmpty(t, result.Capabilities)
	assert.Contains(t, result.Capabilities, "web_search")
	assert.NotZero(t, result.TestDuration)
	assert.False(t, result.TestedAt.IsZero())

	// Check dependencies
	assert.Len(t, result.Dependencies, 2)

	// Find HTTPClient dependency
	var httpDep *types.DependencyStatus
	var braveDep *types.DependencyStatus
	for i := range result.Dependencies {
		if result.Dependencies[i].Name == "HTTPClient" {
			httpDep = &result.Dependencies[i]
		}
		if result.Dependencies[i].Name == "BraveAPIKey" {
			braveDep = &result.Dependencies[i]
		}
	}

	require.NotNil(t, httpDep)
	assert.True(t, httpDep.Available)
	assert.True(t, httpDep.Required)

	require.NotNil(t, braveDep)
	assert.True(t, braveDep.Available)
	assert.True(t, braveDep.Required)
}

func TestWebSearchTool_SelfTest_NoHTTPClient(t *testing.T) {
	tool := &WebSearchTool{
		httpClient:  nil,
		braveAPIKey: "test-api-key",
	}

	result := tool.SelfTest(context.Background(), nil)

	require.NotNil(t, result)
	assert.Equal(t, types.SelfTestStatusFailed, result.Status)
	assert.Contains(t, result.Message, "HTTP client")
	assert.NotEmpty(t, result.Suggestions)
}

func TestWebSearchTool_SelfTest_NoAPIKey(t *testing.T) {
	tool := &WebSearchTool{
		httpClient:  &http.Client{},
		braveAPIKey: "",
	}

	result := tool.SelfTest(context.Background(), nil)

	require.NotNil(t, result)
	assert.Equal(t, types.SelfTestStatusFailed, result.Status)
	assert.Contains(t, result.Message, "API key")
	assert.NotEmpty(t, result.Suggestions)

	// Verify API key dependency shows as missing
	var braveDep *types.DependencyStatus
	for i := range result.Dependencies {
		if result.Dependencies[i].Name == "BraveAPIKey" {
			braveDep = &result.Dependencies[i]
			break
		}
	}

	require.NotNil(t, braveDep)
	assert.False(t, braveDep.Available)
	assert.Equal(t, "missing", braveDep.Status)
}

func TestWebSearchTool_SelfTest_WithExamples(t *testing.T) {
	tool := &WebSearchTool{
		httpClient:  &http.Client{},
		braveAPIKey: "test-key",
	}

	opts := &types.SelfTestOptions{
		IncludeExamples: true,
	}

	result := tool.SelfTest(context.Background(), opts)

	require.NotNil(t, result)
	assert.Equal(t, types.SelfTestStatusOK, result.Status)
	assert.NotEmpty(t, result.Examples)
	assert.GreaterOrEqual(t, len(result.Examples), 1)

	// Check first example has required fields
	example := result.Examples[0]
	assert.NotEmpty(t, example.Name)
	assert.NotEmpty(t, example.Description)
	assert.NotNil(t, example.Args)
}

func TestWebSearchTool_SelfTest_NoExamples(t *testing.T) {
	tool := &WebSearchTool{
		httpClient:  &http.Client{},
		braveAPIKey: "test-key",
	}

	opts := &types.SelfTestOptions{
		IncludeExamples: false,
	}

	result := tool.SelfTest(context.Background(), opts)

	require.NotNil(t, result)
	assert.Equal(t, types.SelfTestStatusOK, result.Status)
	assert.Empty(t, result.Examples)
}

func TestWebSearchTool_SelfTest_NilOptions(t *testing.T) {
	tool := &WebSearchTool{
		httpClient:  &http.Client{},
		braveAPIKey: "test-key",
	}

	result := tool.SelfTest(context.Background(), nil)

	require.NotNil(t, result)
	assert.Equal(t, types.SelfTestStatusOK, result.Status)
	// Default options include examples
	assert.NotEmpty(t, result.Examples)
}
