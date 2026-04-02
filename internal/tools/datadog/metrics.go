package datadog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

// metricsQueryResponse represents the Datadog V1 metrics query response.
type metricsQueryResponse struct {
	Status   string         `json:"status"`
	Error    string         `json:"error,omitempty"`
	FromDate int64          `json:"from_date"`
	ToDate   int64          `json:"to_date"`
	Query    string         `json:"query"`
	GroupBy  []string       `json:"group_by,omitempty"`
	Series   []metricSeries `json:"series"`
	Message  string         `json:"message,omitempty"`
}

// metricSeries represents a single time series in the query response.
type metricSeries struct {
	Metric      string      `json:"metric"`
	DisplayName string      `json:"display_name,omitempty"`
	Scope       string      `json:"scope"`
	Unit        []unitInfo  `json:"unit,omitempty"`
	Pointlist   [][]float64 `json:"pointlist"`
	Expression  string      `json:"expression,omitempty"`
	Aggr        string      `json:"aggr,omitempty"`
	TagSet      []string    `json:"tag_set,omitempty"`
}

// unitInfo represents unit metadata for a metric.
type unitInfo struct {
	Family      string  `json:"family,omitempty"`
	Name        string  `json:"name,omitempty"`
	ShortName   string  `json:"short_name,omitempty"`
	Plural      string  `json:"plural,omitempty"`
	ScaleFactor float64 `json:"scale_factor,omitempty"`
}

// listMetricsResponse represents the Datadog list metrics response.
type listMetricsResponse struct {
	Metrics []string `json:"metrics"`
	From    string   `json:"from,omitempty"`
}

// metricMetadataResponse represents the Datadog metric metadata response.
type metricMetadataResponse struct {
	Type        string `json:"type,omitempty"`
	Unit        string `json:"unit,omitempty"`
	PerUnit     string `json:"per_unit,omitempty"`
	Description string `json:"description,omitempty"`
	ShortName   string `json:"short_name,omitempty"`
	Integration string `json:"integration,omitempty"`
	Statsd      string `json:"statsd_interval,omitempty"`
}

// executeQueryMetrics queries time series data from Datadog.
func (t *DatadogTool) executeQueryMetrics(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	query := toolargs.GetString(args, "query", "")
	if query == "" {
		return types.NewErrorResult("missing_parameter", "query is required for query_metrics action").
			WithParameter("query", nil).
			WithExamples([]string{
				"avg:system.cpu.user{*}",
				"sum:requests.count{service:web} by {host}",
				"max:system.mem.used{*}",
			}), nil
	}

	// Parse time range
	now := time.Now().Unix()
	from := toolargs.GetInt64(args, "from", -3600) // Default: 1 hour ago
	to := toolargs.GetInt64(args, "to", 0)         // Default: now

	// Convert relative times to absolute
	if from <= 0 {
		from = now + from
	}
	if to <= 0 {
		to = now + to
	}

	// Validate time range
	if from >= to {
		return types.NewErrorResult("invalid_parameter", "from must be before to").
			WithParameter("from", from).
			WithSuggestions([]string{
				"Use negative values for relative time (e.g., from=-3600 for 1 hour ago)",
				"Ensure from timestamp is earlier than to timestamp",
			}), nil
	}

	// Check for excessively large time ranges (> 7 days)
	maxRange := int64(7 * 24 * 3600)
	if to-from > maxRange {
		return types.NewErrorResult("time_range_too_large",
			fmt.Sprintf("Time range exceeds 7 days (%d seconds requested)", to-from)).
			WithSuggestions([]string{
				"Reduce the time range to 7 days or less",
				"Query multiple smaller time ranges if needed",
			}), nil
	}

	// Build query parameters
	params := url.Values{}
	params.Set("query", query)
	params.Set("from", fmt.Sprintf("%d", from))
	params.Set("to", fmt.Sprintf("%d", to))

	path := "api/v1/query?" + params.Encode()

	resp, err := t.client.Do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to query metrics: %v", err),
		}, nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to read response: %v", err),
		}, nil
	}

	if resp.StatusCode != http.StatusOK {
		return handleAPIError(resp.StatusCode, body, "query metrics")
	}

	var queryResp metricsQueryResponse
	if err := json.Unmarshal(body, &queryResp); err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to parse response: %v", err),
		}, nil
	}

	if queryResp.Status == "error" || queryResp.Error != "" {
		errMsg := queryResp.Error
		if errMsg == "" {
			errMsg = queryResp.Message
		}
		return types.NewErrorResult("query_error", fmt.Sprintf("Datadog query error: %s", errMsg)).
			WithParameter("query", query).
			WithSuggestions([]string{
				"Check metric name exists using list_metrics action",
				"Verify query syntax: aggregation:metric.name{tags}",
				"Valid aggregations: avg, sum, min, max, count",
			}), nil
	}

	// Format results for AI consumption
	seriesData := make([]interface{}, 0, len(queryResp.Series))
	totalPoints := 0

	for _, series := range queryResp.Series {
		points := make([]map[string]interface{}, 0, len(series.Pointlist))
		for _, point := range series.Pointlist {
			if len(point) >= 2 {
				// point[0] is timestamp in milliseconds, point[1] is value
				ts := int64(point[0] / 1000) // Convert to seconds
				points = append(points, map[string]interface{}{
					"timestamp": ts,
					"time":      time.Unix(ts, 0).Format(time.RFC3339),
					"value":     point[1],
				})
			}
		}
		totalPoints += len(points)

		seriesItem := map[string]interface{}{
			"metric": series.Metric,
			"scope":  series.Scope,
			"points": points,
		}

		if series.DisplayName != "" {
			seriesItem["display_name"] = series.DisplayName
		}
		if len(series.Unit) > 0 && series.Unit[0].Name != "" {
			seriesItem["unit"] = series.Unit[0].Name
		}
		if series.Aggr != "" {
			seriesItem["aggregation"] = series.Aggr
		}
		if len(series.TagSet) > 0 {
			seriesItem["tags"] = series.TagSet
		}

		seriesData = append(seriesData, seriesItem)
	}

	fromTime := time.Unix(queryResp.FromDate, 0).Format(time.RFC3339)
	toTime := time.Unix(queryResp.ToDate, 0).Format(time.RFC3339)

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Query returned %d series with %d total data points from %s to %s",
			len(queryResp.Series), totalPoints, fromTime, toTime),
		Data: map[string]interface{}{
			"query":        queryResp.Query,
			"from":         fromTime,
			"to":           toTime,
			"series_count": len(queryResp.Series),
			"point_count":  totalPoints,
			"series":       seriesData,
		},
	}, nil
}

// executeListMetrics lists available metric names.
func (t *DatadogTool) executeListMetrics(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	filter := toolargs.GetString(args, "filter", "")

	// Build query parameters - list metrics from the last 24 hours
	params := url.Values{}
	params.Set("from", fmt.Sprintf("%d", time.Now().Add(-24*time.Hour).Unix()))
	if filter != "" {
		params.Set("filter", filter)
	}

	path := "api/v1/metrics?" + params.Encode()

	resp, err := t.client.Do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to list metrics: %v", err),
		}, nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to read response: %v", err),
		}, nil
	}

	if resp.StatusCode != http.StatusOK {
		return handleAPIError(resp.StatusCode, body, "list metrics")
	}

	var listResp listMetricsResponse
	if err := json.Unmarshal(body, &listResp); err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to parse response: %v", err),
		}, nil
	}

	metrics := listResp.Metrics
	sort.Strings(metrics)

	// Limit output for readability
	const maxDisplay = 100
	truncated := false
	displayMetrics := metrics
	if len(metrics) > maxDisplay {
		displayMetrics = metrics[:maxDisplay]
		truncated = true
	}

	content := fmt.Sprintf("Found %d metrics", len(metrics))
	if filter != "" {
		content += fmt.Sprintf(" matching filter '%s'", filter)
	}
	if truncated {
		content += fmt.Sprintf(" (showing first %d)", maxDisplay)
	}

	// Convert to interface slice for Data
	metricsInterface := make([]interface{}, len(displayMetrics))
	for i, m := range displayMetrics {
		metricsInterface[i] = m
	}

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data: map[string]interface{}{
			"total_count": len(metrics),
			"displayed":   len(displayMetrics),
			"truncated":   truncated,
			"metrics":     metricsInterface,
		},
	}, nil
}

// executeGetMetricMetadata retrieves metadata for a specific metric.
func (t *DatadogTool) executeGetMetricMetadata(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	metric := toolargs.GetString(args, "metric", "")
	if metric == "" {
		return types.NewErrorResult("missing_parameter", "metric name is required for get_metric_metadata action").
			WithParameter("metric", nil).
			WithSuggestions([]string{
				"Use list_metrics action to discover available metrics",
				"Example metrics: system.cpu.user, system.mem.used, docker.cpu.usage",
			}), nil
	}

	// URL encode the metric name (it may contain dots)
	path := fmt.Sprintf("api/v1/metrics/%s", url.PathEscape(metric))

	resp, err := t.client.Do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to get metric metadata: %v", err),
		}, nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to read response: %v", err),
		}, nil
	}

	if resp.StatusCode == http.StatusNotFound {
		return types.NewErrorResult("metric_not_found",
			fmt.Sprintf("Metric '%s' not found", metric)).
			WithParameter("metric", metric).
			WithSuggestions([]string{
				"Use list_metrics action to discover available metrics",
				"Check for typos in the metric name",
				"Metric names are case-sensitive",
			}), nil
	}

	if resp.StatusCode != http.StatusOK {
		return handleAPIError(resp.StatusCode, body, "get metric metadata")
	}

	var metadata metricMetadataResponse
	if err := json.Unmarshal(body, &metadata); err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to parse response: %v", err),
		}, nil
	}

	// Build content summary
	var parts []string
	if metadata.Type != "" {
		parts = append(parts, fmt.Sprintf("type=%s", metadata.Type))
	}
	if metadata.Unit != "" {
		unitStr := metadata.Unit
		if metadata.PerUnit != "" {
			unitStr += "/" + metadata.PerUnit
		}
		parts = append(parts, fmt.Sprintf("unit=%s", unitStr))
	}
	if metadata.Integration != "" {
		parts = append(parts, fmt.Sprintf("integration=%s", metadata.Integration))
	}

	content := fmt.Sprintf("Metric '%s'", metric)
	if len(parts) > 0 {
		content += ": " + strings.Join(parts, ", ")
	}

	data := map[string]interface{}{
		"metric": metric,
	}
	if metadata.Type != "" {
		data["type"] = metadata.Type
	}
	if metadata.Unit != "" {
		data["unit"] = metadata.Unit
	}
	if metadata.PerUnit != "" {
		data["per_unit"] = metadata.PerUnit
	}
	if metadata.Description != "" {
		data["description"] = metadata.Description
	}
	if metadata.ShortName != "" {
		data["short_name"] = metadata.ShortName
	}
	if metadata.Integration != "" {
		data["integration"] = metadata.Integration
	}

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data:    data,
	}, nil
}

// handleAPIError creates an appropriate error result for API errors.
func handleAPIError(statusCode int, body []byte, operation string) (*types.ToolResult, error) {
	var errResp struct {
		Errors []string `json:"errors"`
	}
	json.Unmarshal(body, &errResp)

	errMsg := fmt.Sprintf("API error (HTTP %d)", statusCode)
	if len(errResp.Errors) > 0 {
		errMsg = strings.Join(errResp.Errors, "; ")
	}

	result := types.NewErrorResult("api_error",
		fmt.Sprintf("Failed to %s: %s", operation, errMsg))

	switch statusCode {
	case http.StatusUnauthorized:
		result.WithSuggestions([]string{
			"Check that DD_API_KEY and DD_APPLICATION_KEY are set correctly",
			"Verify API key has not been revoked",
		})
	case http.StatusForbidden:
		result.WithSuggestions([]string{
			"Check that the application key has required permissions",
			"Verify the API scope includes metrics read access",
		})
	case http.StatusTooManyRequests:
		result.WithSuggestions([]string{
			"Rate limit exceeded - wait and retry",
			"Consider reducing query frequency",
		})
	}

	return result, nil
}
