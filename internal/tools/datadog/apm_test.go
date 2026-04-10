//go:build with_datadog

package datadog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"conduit/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestAPMClient(handler http.HandlerFunc) (*APMClient, *httptest.Server) {
	srv := httptest.NewServer(handler)
	cfg := config.DatadogConfig{
		Enabled:      true,
		APIKey:       "test-api-key",
		AppKey:       "test-app-key",
		Site:         "datadoghq.com",
		RateLimitRPS: 100,
	}
	client := NewClient(cfg)
	client.baseURL = srv.URL + "/"
	return NewAPMClient(client), srv
}

func TestAPMClient_SearchTraces_Basic(t *testing.T) {
	now := time.Now()
	apmClient, srv := setupTestAPMClient(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/v2/spans/events/search", r.URL.Path)

		// Verify headers
		assert.Equal(t, "test-api-key", r.Header.Get("DD-API-KEY"))
		assert.Equal(t, "test-app-key", r.Header.Get("DD-APPLICATION-KEY"))

		// Decode and verify request body
		var reqBody map[string]interface{}
		err := json.NewDecoder(r.Body).Decode(&reqBody)
		require.NoError(t, err)

		filter := reqBody["filter"].(map[string]interface{})
		query := filter["query"].(string)
		assert.Contains(t, query, "service:api-gateway")

		resp := map[string]interface{}{
			"data": []map[string]interface{}{
				{
					"id":   "span-123",
					"type": "span",
					"attributes": map[string]interface{}{
						"timestamp":     now.Format(time.RFC3339Nano),
						"service":       "api-gateway",
						"resource_name": "GET /api/users",
						"span_name":     "http.request",
						"duration":      int64(250000000), // 250ms in nanoseconds
						"status":        "ok",
						"trace_id":      "abc123def456",
						"span_id":       "span-123",
						"span_count":    5,
					},
				},
				{
					"id":   "span-456",
					"type": "span",
					"attributes": map[string]interface{}{
						"timestamp":     now.Add(-1 * time.Minute).Format(time.RFC3339Nano),
						"service":       "api-gateway",
						"resource_name": "POST /api/checkout",
						"span_name":     "http.request",
						"duration":      int64(1500000000), // 1.5s
						"status":        "error",
						"trace_id":      "xyz789",
						"span_id":       "span-456",
						"span_count":    12,
					},
				},
			},
			"meta": map[string]interface{}{
				"page": map[string]interface{}{
					"after": "cursor-next-page",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	defer srv.Close()

	result, err := apmClient.SearchTraces(context.Background(), SearchTracesParams{
		Service: "api-gateway",
		From:    now.Add(-1 * time.Hour),
		To:      now,
		Limit:   20,
	})

	require.NoError(t, err)
	assert.Equal(t, 2, result.Count)
	assert.True(t, result.HasMore)
	assert.Equal(t, "cursor-next-page", result.NextCursor)
	assert.Len(t, result.Traces, 2)

	// Check first trace
	assert.Equal(t, "abc123def456", result.Traces[0].TraceID)
	assert.Equal(t, "api-gateway", result.Traces[0].Service)
	assert.Equal(t, "http.request", result.Traces[0].Operation)
	assert.Equal(t, "GET /api/users", result.Traces[0].Resource)
	assert.Equal(t, "ok", result.Traces[0].Status)
	assert.Equal(t, 5, result.Traces[0].SpanCount)

	// Check second trace (error)
	assert.Equal(t, "xyz789", result.Traces[1].TraceID)
	assert.Equal(t, "error", result.Traces[1].Status)
}

func TestAPMClient_SearchTraces_WithFilters(t *testing.T) {
	apmClient, srv := setupTestAPMClient(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]interface{}
		json.NewDecoder(r.Body).Decode(&reqBody)

		filter := reqBody["filter"].(map[string]interface{})
		query := filter["query"].(string)

		// Verify all filters are included
		assert.Contains(t, query, "service:my-service")
		assert.Contains(t, query, "operation_name:http.request")
		assert.Contains(t, query, "resource_name:/api/checkout")
		assert.Contains(t, query, "status:error")
		assert.Contains(t, query, "duration:>") // Duration filter

		resp := map[string]interface{}{
			"data": []map[string]interface{}{},
			"meta": map[string]interface{}{
				"page": map[string]interface{}{},
			},
		}
		json.NewEncoder(w).Encode(resp)
	})
	defer srv.Close()

	_, err := apmClient.SearchTraces(context.Background(), SearchTracesParams{
		Service:     "my-service",
		Operation:   "http.request",
		Resource:    "/api/checkout",
		Status:      "error",
		MinDuration: "500ms",
	})

	require.NoError(t, err)
}

func TestAPMClient_SearchTraces_RequiresService(t *testing.T) {
	apmClient, srv := setupTestAPMClient(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not make API call without service")
	})
	defer srv.Close()

	_, err := apmClient.SearchTraces(context.Background(), SearchTracesParams{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "service parameter is required")
}

func TestAPMClient_SearchTraces_DeduplicatesTraces(t *testing.T) {
	now := time.Now()
	apmClient, srv := setupTestAPMClient(func(w http.ResponseWriter, r *http.Request) {
		// Return multiple spans from the same trace
		resp := map[string]interface{}{
			"data": []map[string]interface{}{
				{
					"id": "span-1",
					"attributes": map[string]interface{}{
						"timestamp":     now.Format(time.RFC3339Nano),
						"service":       "api",
						"resource_name": "GET /users",
						"span_name":     "http.request",
						"duration":      int64(100000000),
						"status":        "ok",
						"trace_id":      "same-trace-id",
						"span_id":       "span-1",
					},
				},
				{
					"id": "span-2",
					"attributes": map[string]interface{}{
						"timestamp":     now.Format(time.RFC3339Nano),
						"service":       "db",
						"resource_name": "SELECT",
						"span_name":     "db.query",
						"duration":      int64(50000000),
						"status":        "ok",
						"trace_id":      "same-trace-id", // Same trace ID
						"span_id":       "span-2",
					},
				},
			},
			"meta": map[string]interface{}{
				"page": map[string]interface{}{},
			},
		}
		json.NewEncoder(w).Encode(resp)
	})
	defer srv.Close()

	result, err := apmClient.SearchTraces(context.Background(), SearchTracesParams{
		Service: "api",
	})

	require.NoError(t, err)
	// Should only have one trace due to deduplication
	assert.Equal(t, 1, result.Count)
	assert.Equal(t, "same-trace-id", result.Traces[0].TraceID)
}

func TestAPMClient_SearchTraces_APIError(t *testing.T) {
	apmClient, srv := setupTestAPMClient(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"errors": ["Invalid query syntax"]}`))
	})
	defer srv.Close()

	_, err := apmClient.SearchTraces(context.Background(), SearchTracesParams{
		Service: "api",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "API error")
}

func TestAPMClient_GetTrace_Success(t *testing.T) {
	now := time.Now()
	apmClient, srv := setupTestAPMClient(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]interface{}
		json.NewDecoder(r.Body).Decode(&reqBody)

		filter := reqBody["filter"].(map[string]interface{})
		assert.Contains(t, filter["query"], "trace_id:test-trace-123")

		resp := map[string]interface{}{
			"data": []map[string]interface{}{
				{
					"attributes": map[string]interface{}{
						"timestamp":     now.Format(time.RFC3339Nano),
						"service":       "api-gateway",
						"resource_name": "GET /api/users",
						"span_name":     "http.request",
						"duration":      int64(500000000), // 500ms
						"status":        "ok",
						"trace_id":      "test-trace-123",
						"span_id":       "root-span",
						"parent_id":     "",
						"type":          "web",
					},
				},
				{
					"attributes": map[string]interface{}{
						"timestamp":     now.Add(10 * time.Millisecond).Format(time.RFC3339Nano),
						"service":       "users-service",
						"resource_name": "UsersService.GetUser",
						"span_name":     "grpc.request",
						"duration":      int64(200000000), // 200ms
						"status":        "ok",
						"trace_id":      "test-trace-123",
						"span_id":       "child-span-1",
						"parent_id":     "root-span",
						"type":          "rpc",
					},
				},
				{
					"attributes": map[string]interface{}{
						"timestamp":     now.Add(15 * time.Millisecond).Format(time.RFC3339Nano),
						"service":       "postgres",
						"resource_name": "SELECT * FROM users",
						"span_name":     "db.query",
						"duration":      int64(50000000), // 50ms
						"status":        "error",
						"trace_id":      "test-trace-123",
						"span_id":       "child-span-2",
						"parent_id":     "child-span-1",
						"type":          "sql",
						"tags":          []string{"error.type:timeout"},
					},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	})
	defer srv.Close()

	trace, err := apmClient.GetTrace(context.Background(), "test-trace-123")

	require.NoError(t, err)
	assert.Equal(t, "test-trace-123", trace.TraceID)
	assert.Equal(t, 3, len(trace.Spans))
	assert.True(t, trace.HasError) // Has error span
	assert.Equal(t, 3, len(trace.Services))
	assert.Contains(t, trace.Services, "api-gateway")
	assert.Contains(t, trace.Services, "users-service")
	assert.Contains(t, trace.Services, "postgres")

	// Check root span
	require.NotNil(t, trace.RootSpan)
	assert.Equal(t, "api-gateway", trace.RootSpan.Service)
	assert.Equal(t, "http.request", trace.RootSpan.Name)
}

func TestAPMClient_GetTrace_NotFound(t *testing.T) {
	apmClient, srv := setupTestAPMClient(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"data": []map[string]interface{}{},
		}
		json.NewEncoder(w).Encode(resp)
	})
	defer srv.Close()

	_, err := apmClient.GetTrace(context.Background(), "nonexistent-trace")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "trace not found")
}

func TestAPMClient_GetTrace_EmptyID(t *testing.T) {
	apmClient, srv := setupTestAPMClient(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not make API call with empty ID")
	})
	defer srv.Close()

	_, err := apmClient.GetTrace(context.Background(), "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "trace_id is required")
}

func TestParseDurationToNanos(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int64
		wantErr  bool
	}{
		{
			name:     "go duration seconds",
			input:    "1s",
			expected: 1e9,
			wantErr:  false,
		},
		{
			name:     "go duration milliseconds",
			input:    "500ms",
			expected: 500e6,
			wantErr:  false,
		},
		{
			name:     "go duration microseconds",
			input:    "100us",
			expected: 100e3,
			wantErr:  false,
		},
		{
			name:     "numeric only (milliseconds)",
			input:    "250",
			expected: 250e6,
			wantErr:  false,
		},
		{
			name:     "with spaces",
			input:    " 1s ",
			expected: 1e9,
			wantErr:  false,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
		{
			name:    "invalid format",
			input:   "abc",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseDurationToNanos(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		name     string
		input    time.Duration
		expected string
	}{
		{
			name:     "microseconds",
			input:    500 * time.Microsecond,
			expected: "500.00us",
		},
		{
			name:     "milliseconds",
			input:    250 * time.Millisecond,
			expected: "250.00ms",
		},
		{
			name:     "seconds",
			input:    2500 * time.Millisecond,
			expected: "2.50s",
		},
		{
			name:     "sub-millisecond",
			input:    100 * time.Nanosecond,
			expected: "0.10us",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatDuration(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Integration test with tool dispatch
func TestDatadogTool_SearchTraces(t *testing.T) {
	now := time.Now()
	tool, srv := setupTestDatadogTool(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/spans/events/search" {
			resp := map[string]interface{}{
				"data": []map[string]interface{}{
					{
						"id": "span-1",
						"attributes": map[string]interface{}{
							"timestamp":     now.Format(time.RFC3339Nano),
							"service":       "checkout-service",
							"resource_name": "POST /api/checkout",
							"span_name":     "http.request",
							"duration":      int64(2000000000), // 2s (slow)
							"status":        "ok",
							"trace_id":      "slow-trace-123",
							"span_id":       "span-1",
							"span_count":    8,
						},
					},
				},
				"meta": map[string]interface{}{
					"page": map[string]interface{}{},
				},
			}
			json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	defer srv.Close()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":       "search_traces",
		"service":      "checkout-service",
		"min_duration": "1s",
		"from":         "-1h",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "1 trace(s)")
	assert.Contains(t, result.Content, "checkout-service")

	data := result.Data
	assert.Equal(t, 1, data["count"])

	traces := data["traces"].([]interface{})
	assert.Len(t, traces, 1)

	trace := traces[0].(map[string]interface{})
	assert.Equal(t, "slow-trace-123", trace["trace_id"])
	assert.Equal(t, "POST /api/checkout", trace["resource"])
}

func TestDatadogTool_GetTrace(t *testing.T) {
	now := time.Now()
	tool, srv := setupTestDatadogTool(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/spans/events/search" {
			resp := map[string]interface{}{
				"data": []map[string]interface{}{
					{
						"attributes": map[string]interface{}{
							"timestamp":     now.Format(time.RFC3339Nano),
							"service":       "api",
							"resource_name": "GET /users",
							"span_name":     "http.request",
							"duration":      int64(100000000),
							"status":        "ok",
							"trace_id":      "trace-xyz",
							"span_id":       "span-root",
							"parent_id":     "",
							"type":          "web",
						},
					},
					{
						"attributes": map[string]interface{}{
							"timestamp":     now.Add(5 * time.Millisecond).Format(time.RFC3339Nano),
							"service":       "db",
							"resource_name": "SELECT",
							"span_name":     "db.query",
							"duration":      int64(20000000),
							"status":        "ok",
							"trace_id":      "trace-xyz",
							"span_id":       "span-child",
							"parent_id":     "span-root",
							"type":          "sql",
						},
					},
				},
			}
			json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	defer srv.Close()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":   "get_trace",
		"trace_id": "trace-xyz",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "trace-xyz")
	assert.Contains(t, result.Content, "2 spans")
	assert.Contains(t, result.Content, "2 service(s)")

	data := result.Data
	assert.Equal(t, 2, data["span_count"])
	assert.Equal(t, false, data["has_error"])

	services := data["services"].([]string)
	assert.Contains(t, services, "api")
	assert.Contains(t, services, "db")

	// Check root span
	rootSpan := data["root_span"].(map[string]interface{})
	assert.Equal(t, "api", rootSpan["service"])
}

func TestDatadogTool_SearchTraces_MissingService(t *testing.T) {
	tool, srv := setupTestDatadogTool(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not call API when service is missing")
	})
	defer srv.Close()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "search_traces",
		// Missing service
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "service is required")
}

func TestDatadogTool_GetTrace_MissingTraceID(t *testing.T) {
	tool, srv := setupTestDatadogTool(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not call API when trace_id is missing")
	})
	defer srv.Close()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "get_trace",
		// Missing trace_id
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "trace_id is required")
}

// Helper to create test tool
func setupTestDatadogTool(handler http.HandlerFunc) (*DatadogTool, *httptest.Server) {
	srv := httptest.NewServer(handler)
	cfg := &config.DatadogConfig{
		Enabled:      true,
		APIKey:       "test-api-key",
		AppKey:       "test-app-key",
		Site:         "datadoghq.com",
		RateLimitRPS: 100,
	}

	client := NewClient(*cfg)
	client.baseURL = srv.URL + "/"

	tool := &DatadogTool{
		config:     cfg,
		client:     client,
		logsClient: NewLogsClient(client),
		apmClient:  NewAPMClient(client),
	}

	return tool, srv
}
