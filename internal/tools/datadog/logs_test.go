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

func setupTestLogsClient(handler http.HandlerFunc) (*LogsClient, *httptest.Server) {
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
	return NewLogsClient(client), srv
}

func TestLogsClient_SearchLogs_Basic(t *testing.T) {
	now := time.Now()
	logsClient, srv := setupTestLogsClient(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/v2/logs/events/search", r.URL.Path)

		// Verify headers
		assert.Equal(t, "test-api-key", r.Header.Get("DD-API-KEY"))
		assert.Equal(t, "test-app-key", r.Header.Get("DD-APPLICATION-KEY"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		// Decode request body
		var reqBody map[string]interface{}
		err := json.NewDecoder(r.Body).Decode(&reqBody)
		require.NoError(t, err)

		filter := reqBody["filter"].(map[string]interface{})
		assert.Contains(t, filter["query"], "error timeout")

		resp := map[string]interface{}{
			"data": []map[string]interface{}{
				{
					"id": "log-123",
					"attributes": map[string]interface{}{
						"timestamp": now.Format(time.RFC3339),
						"status":    "error",
						"service":   "api-service",
						"host":      "host-1",
						"message":   "Connection timeout error",
						"tags":      []string{"env:prod"},
					},
				},
				{
					"id": "log-456",
					"attributes": map[string]interface{}{
						"timestamp": now.Add(-1 * time.Minute).Format(time.RFC3339),
						"status":    "error",
						"service":   "api-service",
						"host":      "host-2",
						"message":   "Database timeout",
						"tags":      []string{"env:prod"},
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

	result, err := logsClient.SearchLogs(context.Background(), SearchLogsParams{
		Query: "error timeout",
		From:  now.Add(-1 * time.Hour),
		To:    now,
		Limit: 100,
	})

	require.NoError(t, err)
	assert.Equal(t, 2, result.Count)
	assert.True(t, result.HasMore)
	assert.Equal(t, "cursor-next-page", result.NextCursor)
	assert.Len(t, result.Logs, 2)

	assert.Equal(t, "log-123", result.Logs[0].ID)
	assert.Equal(t, "error", result.Logs[0].Status)
	assert.Equal(t, "api-service", result.Logs[0].Service)
	assert.Equal(t, "Connection timeout error", result.Logs[0].Message)
}

func TestLogsClient_SearchLogs_WithFilters(t *testing.T) {
	logsClient, srv := setupTestLogsClient(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]interface{}
		json.NewDecoder(r.Body).Decode(&reqBody)

		filter := reqBody["filter"].(map[string]interface{})
		query := filter["query"].(string)

		// Verify that service, host, and status are included in the query
		assert.Contains(t, query, "service:my-service")
		assert.Contains(t, query, "host:my-host")
		assert.Contains(t, query, "status:error")

		resp := map[string]interface{}{
			"data": []map[string]interface{}{},
			"meta": map[string]interface{}{
				"page": map[string]interface{}{},
			},
		}
		json.NewEncoder(w).Encode(resp)
	})
	defer srv.Close()

	_, err := logsClient.SearchLogs(context.Background(), SearchLogsParams{
		Service: "my-service",
		Host:    "my-host",
		Status:  "error",
	})

	require.NoError(t, err)
}

func TestLogsClient_SearchLogs_WithPagination(t *testing.T) {
	logsClient, srv := setupTestLogsClient(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]interface{}
		json.NewDecoder(r.Body).Decode(&reqBody)

		page := reqBody["page"].(map[string]interface{})
		assert.Equal(t, "previous-cursor", page["cursor"])

		resp := map[string]interface{}{
			"data": []map[string]interface{}{},
			"meta": map[string]interface{}{
				"page": map[string]interface{}{},
			},
		}
		json.NewEncoder(w).Encode(resp)
	})
	defer srv.Close()

	_, err := logsClient.SearchLogs(context.Background(), SearchLogsParams{
		Cursor: "previous-cursor",
	})

	require.NoError(t, err)
}

func TestLogsClient_SearchLogs_MessageTruncation(t *testing.T) {
	longMessage := make([]byte, MaxMessageLength+500)
	for i := range longMessage {
		longMessage[i] = 'x'
	}

	logsClient, srv := setupTestLogsClient(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"data": []map[string]interface{}{
				{
					"id": "log-long",
					"attributes": map[string]interface{}{
						"timestamp": time.Now().Format(time.RFC3339),
						"status":    "info",
						"message":   string(longMessage),
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

	result, err := logsClient.SearchLogs(context.Background(), SearchLogsParams{})

	require.NoError(t, err)
	require.Len(t, result.Logs, 1)
	assert.Equal(t, MaxMessageLength, len(result.Logs[0].Message))
	assert.True(t, result.Logs[0].Message[len(result.Logs[0].Message)-3:] == "...")
}

func TestLogsClient_SearchLogs_APIError(t *testing.T) {
	logsClient, srv := setupTestLogsClient(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"errors": ["Invalid query syntax"]}`))
	})
	defer srv.Close()

	_, err := logsClient.SearchLogs(context.Background(), SearchLogsParams{
		Query: "invalid[[",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "API error")
}

func TestLogsClient_GetLog_Success(t *testing.T) {
	now := time.Now()
	logsClient, srv := setupTestLogsClient(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"data": []map[string]interface{}{
				{
					"id": "log-abc123",
					"attributes": map[string]interface{}{
						"timestamp": now.Format(time.RFC3339),
						"status":    "warn",
						"service":   "worker",
						"host":      "worker-1",
						"message":   "Memory usage high",
						"tags":      []string{"env:staging"},
						"attributes": map[string]interface{}{
							"memory_mb": float64(1024),
						},
					},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	})
	defer srv.Close()

	log, err := logsClient.GetLog(context.Background(), "log-abc123")

	require.NoError(t, err)
	assert.Equal(t, "log-abc123", log.ID)
	assert.Equal(t, "warn", log.Status)
	assert.Equal(t, "worker", log.Service)
	assert.Equal(t, "Memory usage high", log.Message)
	assert.Equal(t, "1024", log.Attrs["memory_mb"])
}

func TestLogsClient_GetLog_NotFound(t *testing.T) {
	logsClient, srv := setupTestLogsClient(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"data": []map[string]interface{}{},
		}
		json.NewEncoder(w).Encode(resp)
	})
	defer srv.Close()

	_, err := logsClient.GetLog(context.Background(), "nonexistent-log")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "log not found")
}

func TestLogsClient_GetLog_EmptyID(t *testing.T) {
	logsClient, srv := setupTestLogsClient(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not make API call with empty ID")
	})
	defer srv.Close()

	_, err := logsClient.GetLog(context.Background(), "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "log_id is required")
}

func TestLogsClient_ListIndexes_Success(t *testing.T) {
	logsClient, srv := setupTestLogsClient(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/api/v1/logs/config/indexes", r.URL.Path)

		resp := map[string]interface{}{
			"indexes": []map[string]interface{}{
				{
					"name":               "main",
					"num_retention_days": 15,
					"daily_limit":        int64(100000000),
					"is_rate_limited":    false,
				},
				{
					"name":               "errors",
					"num_retention_days": 30,
					"daily_limit":        int64(10000000),
					"is_rate_limited":    true,
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	})
	defer srv.Close()

	indexes, err := logsClient.ListIndexes(context.Background())

	require.NoError(t, err)
	assert.Len(t, indexes, 2)

	assert.Equal(t, "main", indexes[0].Name)
	assert.Equal(t, 15, indexes[0].NumRetDays)
	assert.False(t, indexes[0].IsRateLimited)

	assert.Equal(t, "errors", indexes[1].Name)
	assert.Equal(t, 30, indexes[1].NumRetDays)
	assert.True(t, indexes[1].IsRateLimited)
}

func TestLogsClient_ListIndexes_APIError(t *testing.T) {
	logsClient, srv := setupTestLogsClient(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"errors": ["Insufficient permissions"]}`))
	})
	defer srv.Close()

	_, err := logsClient.ListIndexes(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "API error")
}

func TestTruncateString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		expected string
	}{
		{
			name:     "short string unchanged",
			input:    "hello",
			maxLen:   10,
			expected: "hello",
		},
		{
			name:     "exact length unchanged",
			input:    "hello",
			maxLen:   5,
			expected: "hello",
		},
		{
			name:     "long string truncated with ellipsis",
			input:    "hello world",
			maxLen:   8,
			expected: "hello...",
		},
		{
			name:     "very short max length",
			input:    "hello",
			maxLen:   3,
			expected: "hel",
		},
		{
			name:     "empty string",
			input:    "",
			maxLen:   10,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateString(tt.input, tt.maxLen)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSearchLogsParams_Defaults(t *testing.T) {
	logsClient, srv := setupTestLogsClient(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]interface{}
		json.NewDecoder(r.Body).Decode(&reqBody)

		// With empty params, query should be "*"
		filter := reqBody["filter"].(map[string]interface{})
		assert.Equal(t, "*", filter["query"])

		// Limit should be default 100
		page := reqBody["page"].(map[string]interface{})
		assert.Equal(t, float64(100), page["limit"])

		resp := map[string]interface{}{
			"data": []map[string]interface{}{},
			"meta": map[string]interface{}{
				"page": map[string]interface{}{},
			},
		}
		json.NewEncoder(w).Encode(resp)
	})
	defer srv.Close()

	_, err := logsClient.SearchLogs(context.Background(), SearchLogsParams{})
	require.NoError(t, err)
}

func TestSearchLogsParams_LimitCapped(t *testing.T) {
	logsClient, srv := setupTestLogsClient(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]interface{}
		json.NewDecoder(r.Body).Decode(&reqBody)

		// Limit should be capped at 1000
		page := reqBody["page"].(map[string]interface{})
		assert.Equal(t, float64(1000), page["limit"])

		resp := map[string]interface{}{
			"data": []map[string]interface{}{},
			"meta": map[string]interface{}{
				"page": map[string]interface{}{},
			},
		}
		json.NewEncoder(w).Encode(resp)
	})
	defer srv.Close()

	_, err := logsClient.SearchLogs(context.Background(), SearchLogsParams{
		Limit: 5000, // Should be capped to 1000
	})
	require.NoError(t, err)
}
