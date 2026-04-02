package datadog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"conduit/internal/config"
	toolargs "conduit/internal/tools/args"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ = toolargs.GetString // silence unused import during transition

func setupTestTool(t *testing.T, handler http.HandlerFunc) *DatadogTool {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	cfg := &config.DatadogConfig{
		Enabled:      true,
		APIKey:       "test-api-key",
		AppKey:       "test-app-key",
		Site:         "datadoghq.com",
		RateLimitRPS: 100,
	}

	tool, err := NewDatadogTool(nil, cfg)
	require.NoError(t, err)

	// Override baseURL to point at test server
	tool.client.baseURL = srv.URL + "/"

	return tool
}

func TestNewDatadogTool(t *testing.T) {
	cfg := &config.DatadogConfig{
		Enabled: true,
		APIKey:  "test-key",
		AppKey:  "test-app",
	}

	tool, err := NewDatadogTool(nil, cfg)
	require.NoError(t, err)
	assert.NotNil(t, tool)
	assert.NotNil(t, tool.client)
}

func TestNewDatadogTool_NilConfig(t *testing.T) {
	_, err := NewDatadogTool(nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config is required")
}

func TestDatadogTool_Name(t *testing.T) {
	tool := setupTestTool(t, func(w http.ResponseWriter, r *http.Request) {})
	assert.Equal(t, "Datadog", tool.Name())
}

func TestDatadogTool_Execute_MissingAction(t *testing.T) {
	tool := setupTestTool(t, func(w http.ResponseWriter, r *http.Request) {})

	result, err := tool.Execute(context.Background(), map[string]interface{}{})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "action parameter is required")
}

func TestDatadogTool_Execute_UnknownAction(t *testing.T) {
	tool := setupTestTool(t, func(w http.ResponseWriter, r *http.Request) {})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "invalid_action",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "Unknown action")
}

func TestDatadogTool_QueryMetrics_Success(t *testing.T) {
	tool := setupTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/query", r.URL.Path)
		assert.Equal(t, "avg:system.cpu.user{*}", r.URL.Query().Get("query"))

		resp := metricsQueryResponse{
			Status:   "ok",
			FromDate: 1700000000,
			ToDate:   1700003600,
			Query:    "avg:system.cpu.user{*}",
			Series: []metricSeries{
				{
					Metric: "system.cpu.user",
					Scope:  "host:test-host",
					Unit: []unitInfo{
						{Name: "percent"},
					},
					Pointlist: [][]float64{
						{1700000000000, 25.5},
						{1700001000000, 30.2},
						{1700002000000, 22.1},
					},
					Aggr:   "avg",
					TagSet: []string{"host:test-host"},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "query_metrics",
		"query":  "avg:system.cpu.user{*}",
		"from":   float64(-3600),
		"to":     float64(0),
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "1 series")
	assert.Contains(t, result.Content, "3 total data points")

	series, ok := result.Data["series"].([]interface{})
	require.True(t, ok)
	assert.Len(t, series, 1)

	firstSeries := series[0].(map[string]interface{})
	assert.Equal(t, "system.cpu.user", firstSeries["metric"])
	assert.Equal(t, "percent", firstSeries["unit"])

	points := firstSeries["points"].([]map[string]interface{})
	assert.Len(t, points, 3)
	assert.Equal(t, 25.5, points[0]["value"])
}

func TestDatadogTool_QueryMetrics_MissingQuery(t *testing.T) {
	tool := setupTestTool(t, func(w http.ResponseWriter, r *http.Request) {})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "query_metrics",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "query is required")
}

func TestDatadogTool_QueryMetrics_InvalidTimeRange(t *testing.T) {
	tool := setupTestTool(t, func(w http.ResponseWriter, r *http.Request) {})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "query_metrics",
		"query":  "avg:system.cpu.user{*}",
		"from":   float64(1000),
		"to":     float64(500), // to is before from
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "from must be before to")
}

func TestDatadogTool_QueryMetrics_TimeRangeTooLarge(t *testing.T) {
	tool := setupTestTool(t, func(w http.ResponseWriter, r *http.Request) {})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "query_metrics",
		"query":  "avg:system.cpu.user{*}",
		"from":   float64(1000000000),
		"to":     float64(1000000000 + 8*24*3600), // 8 days
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "exceeds 7 days")
}

func TestDatadogTool_QueryMetrics_APIError(t *testing.T) {
	tool := setupTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		resp := metricsQueryResponse{
			Status: "error",
			Error:  "Invalid query syntax",
		}
		json.NewEncoder(w).Encode(resp)
	})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "query_metrics",
		"query":  "invalid:query",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "Invalid query syntax")
}

func TestDatadogTool_ListMetrics_Success(t *testing.T) {
	tool := setupTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/metrics", r.URL.Path)

		resp := listMetricsResponse{
			Metrics: []string{
				"system.cpu.idle",
				"system.cpu.system",
				"system.cpu.user",
				"system.mem.free",
				"system.mem.used",
			},
		}
		json.NewEncoder(w).Encode(resp)
	})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "list_metrics",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "Found 5 metrics")

	metrics, ok := result.Data["metrics"].([]interface{})
	require.True(t, ok)
	assert.Len(t, metrics, 5)
	assert.Equal(t, "system.cpu.idle", metrics[0]) // Should be sorted
}

func TestDatadogTool_ListMetrics_WithFilter(t *testing.T) {
	tool := setupTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "system.cpu", r.URL.Query().Get("filter"))

		resp := listMetricsResponse{
			Metrics: []string{
				"system.cpu.idle",
				"system.cpu.system",
				"system.cpu.user",
			},
		}
		json.NewEncoder(w).Encode(resp)
	})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "list_metrics",
		"filter": "system.cpu",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "matching filter 'system.cpu'")
}

func TestDatadogTool_ListMetrics_Truncation(t *testing.T) {
	// Create a handler that returns more than 100 metrics
	tool := setupTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		metrics := make([]string, 150)
		for i := 0; i < 150; i++ {
			metrics[i] = "metric." + string(rune('a'+i/26)) + string(rune('a'+i%26))
		}
		resp := listMetricsResponse{Metrics: metrics}
		json.NewEncoder(w).Encode(resp)
	})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "list_metrics",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "showing first 100")
	assert.Equal(t, 150, result.Data["total_count"])
	assert.Equal(t, 100, result.Data["displayed"])
	assert.True(t, result.Data["truncated"].(bool))
}

func TestDatadogTool_GetMetricMetadata_Success(t *testing.T) {
	tool := setupTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/metrics/system.cpu.user", r.URL.Path)

		resp := metricMetadataResponse{
			Type:        "gauge",
			Unit:        "percent",
			Description: "The percent of time the CPU spent running user space processes",
			ShortName:   "CPU User",
			Integration: "system",
		}
		json.NewEncoder(w).Encode(resp)
	})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "get_metric_metadata",
		"metric": "system.cpu.user",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "system.cpu.user")
	assert.Contains(t, result.Content, "type=gauge")
	assert.Contains(t, result.Content, "unit=percent")

	assert.Equal(t, "gauge", result.Data["type"])
	assert.Equal(t, "percent", result.Data["unit"])
	assert.Equal(t, "system", result.Data["integration"])
}

func TestDatadogTool_GetMetricMetadata_MissingMetric(t *testing.T) {
	tool := setupTestTool(t, func(w http.ResponseWriter, r *http.Request) {})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "get_metric_metadata",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "metric name is required")
}

func TestDatadogTool_GetMetricMetadata_NotFound(t *testing.T) {
	tool := setupTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "get_metric_metadata",
		"metric": "nonexistent.metric",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "not found")
}

func TestDatadogTool_GetMetricMetadata_WithPerUnit(t *testing.T) {
	tool := setupTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		resp := metricMetadataResponse{
			Type:    "rate",
			Unit:    "request",
			PerUnit: "second",
		}
		json.NewEncoder(w).Encode(resp)
	})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "get_metric_metadata",
		"metric": "requests.rate",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "unit=request/second")
	assert.Equal(t, "request", result.Data["unit"])
	assert.Equal(t, "second", result.Data["per_unit"])
}

func TestDatadogTool_APIError_Unauthorized(t *testing.T) {
	tool := setupTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string][]string{
			"errors": {"Invalid API key"},
		})
	})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "list_metrics",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "Invalid API key")
}

func TestDatadogTool_APIError_RateLimit(t *testing.T) {
	tool := setupTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string][]string{
			"errors": {"Rate limit exceeded"},
		})
	})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "list_metrics",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "Rate limit exceeded")
}

func TestDatadogTool_GetActionDocs(t *testing.T) {
	tool := setupTestTool(t, func(w http.ResponseWriter, r *http.Request) {})

	docs := tool.GetActionDocs()

	assert.Contains(t, docs, "query_metrics")
	assert.Contains(t, docs, "list_metrics")
	assert.Contains(t, docs, "get_metric_metadata")

	queryDoc := docs["query_metrics"]
	assert.Contains(t, queryDoc.RequiredParams, "query")
	assert.Contains(t, queryDoc.OptionalParams, "from")
	assert.Contains(t, queryDoc.OptionalParams, "to")
}

func TestGetStringArg(t *testing.T) {
	args := map[string]interface{}{
		"str":   "value",
		"int":   42,
		"empty": "",
	}

	assert.Equal(t, "value", toolargs.GetString(args, "str", "default"))
	assert.Equal(t, "default", toolargs.GetString(args, "int", "default"))
	assert.Equal(t, "", toolargs.GetString(args, "empty", "default"))
	assert.Equal(t, "default", toolargs.GetString(args, "missing", "default"))
}

func TestGetInt64Arg(t *testing.T) {
	args := map[string]interface{}{
		"float64": float64(123),
		"int":     42,
		"int64":   int64(999),
		"str":     "not a number",
	}

	assert.Equal(t, int64(123), toolargs.GetInt64(args, "float64", 0))
	assert.Equal(t, int64(42), toolargs.GetInt64(args, "int", 0))
	assert.Equal(t, int64(999), toolargs.GetInt64(args, "int64", 0))
	assert.Equal(t, int64(0), toolargs.GetInt64(args, "str", 0))
	assert.Equal(t, int64(-100), toolargs.GetInt64(args, "missing", -100))
}

// ---------- Log action tests ----------

func TestDatadogTool_SearchLogs_Success(t *testing.T) {
	tool := setupTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/logs/events/search" {
			resp := map[string]interface{}{
				"data": []map[string]interface{}{
					{
						"id": "log-123",
						"attributes": map[string]interface{}{
							"timestamp": "2024-01-15T10:30:00Z",
							"status":    "error",
							"service":   "api",
							"host":      "host-1",
							"message":   "Connection failed",
							"tags":      []string{"env:prod"},
						},
					},
				},
				"meta": map[string]interface{}{
					"page": map[string]interface{}{
						"after": "cursor-abc",
					},
				},
			}
			json.NewEncoder(w).Encode(resp)
		}
	})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action":  "search_logs",
		"query":   "error",
		"service": "api",
		"from":    "-1h",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "Found 1 log(s)")
	assert.Contains(t, result.Content, "more available")

	logs, ok := result.Data["logs"].([]interface{})
	require.True(t, ok)
	assert.Len(t, logs, 1)

	firstLog := logs[0].(map[string]interface{})
	assert.Equal(t, "log-123", firstLog["id"])
	assert.Equal(t, "error", firstLog["status"])
	assert.Equal(t, "api", firstLog["service"])
}

func TestDatadogTool_SearchLogs_WithPagination(t *testing.T) {
	tool := setupTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/logs/events/search" {
			var reqBody map[string]interface{}
			json.NewDecoder(r.Body).Decode(&reqBody)

			page := reqBody["page"].(map[string]interface{})
			assert.Equal(t, "prev-cursor", page["cursor"])

			resp := map[string]interface{}{
				"data": []map[string]interface{}{},
				"meta": map[string]interface{}{
					"page": map[string]interface{}{},
				},
			}
			json.NewEncoder(w).Encode(resp)
		}
	})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "search_logs",
		"cursor": "prev-cursor",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
}

func TestDatadogTool_SearchLogs_InvalidTimeFormat(t *testing.T) {
	tool := setupTestTool(t, func(w http.ResponseWriter, r *http.Request) {})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "search_logs",
		"from":   "not-a-time",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "invalid 'from' time")
}

func TestDatadogTool_SearchLogs_APIError(t *testing.T) {
	tool := setupTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/logs/events/search" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"errors": ["Invalid query"]}`))
		}
	})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "search_logs",
		"query":  "invalid[[",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "search failed")
}

func TestDatadogTool_GetLog_Success(t *testing.T) {
	tool := setupTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/logs/events/search" {
			resp := map[string]interface{}{
				"data": []map[string]interface{}{
					{
						"id": "log-xyz",
						"attributes": map[string]interface{}{
							"timestamp": "2024-01-15T10:30:00Z",
							"status":    "warn",
							"service":   "worker",
							"host":      "worker-1",
							"message":   "High memory usage",
							"tags":      []string{"env:staging"},
						},
					},
				},
			}
			json.NewEncoder(w).Encode(resp)
		}
	})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "get_log",
		"log_id": "log-xyz",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "Retrieved log log-xyz")
	assert.Equal(t, "log-xyz", result.Data["id"])
	assert.Equal(t, "warn", result.Data["status"])
}

func TestDatadogTool_GetLog_MissingID(t *testing.T) {
	tool := setupTestTool(t, func(w http.ResponseWriter, r *http.Request) {})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "get_log",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "log_id parameter is required")
}

func TestDatadogTool_GetLog_NotFound(t *testing.T) {
	tool := setupTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/logs/events/search" {
			resp := map[string]interface{}{
				"data": []map[string]interface{}{},
			}
			json.NewEncoder(w).Encode(resp)
		}
	})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "get_log",
		"log_id": "nonexistent",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "log not found")
}

func TestDatadogTool_ListIndexes_Success(t *testing.T) {
	tool := setupTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/logs/config/indexes" {
			assert.Equal(t, http.MethodGet, r.Method)

			resp := map[string]interface{}{
				"indexes": []map[string]interface{}{
					{
						"name":               "main",
						"num_retention_days": 15,
						"daily_limit":        int64(100000000),
						"is_rate_limited":    false,
					},
					{
						"name":               "security",
						"num_retention_days": 30,
						"daily_limit":        int64(50000000),
						"is_rate_limited":    true,
					},
				},
			}
			json.NewEncoder(w).Encode(resp)
		}
	})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "list_indexes",
	})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Content, "Found 2 log index(es)")

	indexes, ok := result.Data["indexes"].([]interface{})
	require.True(t, ok)
	assert.Len(t, indexes, 2)

	firstIndex := indexes[0].(map[string]interface{})
	assert.Equal(t, "main", firstIndex["name"])
	assert.Equal(t, 15, firstIndex["num_retention_days"])
}

func TestDatadogTool_ListIndexes_APIError(t *testing.T) {
	tool := setupTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/logs/config/indexes" {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"errors": ["Insufficient permissions"]}`))
		}
	})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "list_indexes",
	})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "failed to list indexes")
}

func TestDatadogTool_GetActionDocs_IncludesLogActions(t *testing.T) {
	tool := setupTestTool(t, func(w http.ResponseWriter, r *http.Request) {})

	docs := tool.GetActionDocs()

	// Check log actions are documented
	assert.Contains(t, docs, "search_logs")
	assert.Contains(t, docs, "get_log")
	assert.Contains(t, docs, "list_indexes")

	searchDoc := docs["search_logs"]
	assert.Contains(t, searchDoc.OptionalParams, "query")
	assert.Contains(t, searchDoc.OptionalParams, "service")
	assert.Contains(t, searchDoc.OptionalParams, "cursor")

	getLogDoc := docs["get_log"]
	assert.Contains(t, getLogDoc.RequiredParams, "log_id")
}

func TestParseTime(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantErr   bool
		checkFunc func(t *testing.T, result interface{})
	}{
		{
			name:    "RFC3339 format",
			input:   "2024-01-15T10:30:00Z",
			wantErr: false,
		},
		{
			name:    "relative hours",
			input:   "-1h",
			wantErr: false,
		},
		{
			name:    "relative minutes",
			input:   "-30m",
			wantErr: false,
		},
		{
			name:    "relative seconds",
			input:   "-60s",
			wantErr: false,
		},
		{
			name:    "relative days",
			input:   "-7d",
			wantErr: false,
		},
		{
			name:    "invalid format",
			input:   "not-a-time",
			wantErr: true,
		},
		{
			name:    "invalid relative unit",
			input:   "-5x",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseTime(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.False(t, result.IsZero())
			}
		})
	}
}

func TestGetIntArg(t *testing.T) {
	args := map[string]interface{}{
		"float64": float64(123),
		"int":     42,
		"int64":   int64(999),
		"str":     "not a number",
	}

	assert.Equal(t, 123, toolargs.GetInt(args, "float64", 0))
	assert.Equal(t, 42, toolargs.GetInt(args, "int", 0))
	assert.Equal(t, 999, toolargs.GetInt(args, "int64", 0))
	assert.Equal(t, 0, toolargs.GetInt(args, "str", 0))
	assert.Equal(t, -100, toolargs.GetInt(args, "missing", -100))
}
