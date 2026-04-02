// Package datadog implements the Datadog observability tool for metrics and logs.
package datadog

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"conduit/internal/config"
	"conduit/internal/tools/types"
)

// DatadogTool provides Datadog observability access via the tool interface.
type DatadogTool struct {
	services   *types.ToolServices
	config     *config.DatadogConfig
	client     *Client
	logsClient *LogsClient
}

// NewDatadogTool creates a new Datadog tool with the given services and configuration.
func NewDatadogTool(services *types.ToolServices, cfg *config.DatadogConfig) (*DatadogTool, error) {
	if cfg == nil {
		return nil, fmt.Errorf("datadog config is required")
	}

	client := NewClient(*cfg)
	logsClient := NewLogsClient(client)

	return &DatadogTool{
		services:   services,
		config:     cfg,
		client:     client,
		logsClient: logsClient,
	}, nil
}

// Name returns the tool name.
func (t *DatadogTool) Name() string { return "Datadog" }

// Description returns a human-readable description of the tool's capabilities.
func (t *DatadogTool) Description() string {
	return `Datadog observability tool for metrics and logs. All actions are read-only.

METRICS ACTIONS:
- query_metrics: Query time series data using Datadog metric query syntax
- list_metrics: List available metric names (optionally filtered by prefix)
- get_metric_metadata: Get metadata for a specific metric (type, unit, description)

LOG ACTIONS:
- search_logs: Search logs with filters (query, service, host, status, time range)
- get_log: Get a single log entry by ID
- list_indexes: List available log indexes

Examples:
- Query metrics: action=query_metrics, query="avg:system.cpu.user{*}", from=-3600
- Search logs: action=search_logs, query="error timeout", service="api", from="-1h"
- Get log by ID: action=get_log, log_id="abc123"
- List log indexes: action=list_indexes`
}

// Parameters returns the JSON schema for the tool's parameters.
func (t *DatadogTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"description": "The Datadog operation to perform",
				"enum":        []string{"query_metrics", "list_metrics", "get_metric_metadata", "search_logs", "get_log", "list_indexes"},
			},
			// Metrics parameters
			"query": map[string]interface{}{
				"type":        "string",
				"description": "For query_metrics: Datadog metric query (e.g., 'avg:system.cpu.user{*}'). For search_logs: Datadog log query string.",
			},
			"from": map[string]interface{}{
				"type":        "string",
				"description": "Start time. For metrics: Unix timestamp or negative offset in seconds (e.g., -3600). For logs: RFC3339 or relative like '-1h', '-15m'.",
			},
			"to": map[string]interface{}{
				"type":        "string",
				"description": "End time. For metrics: Unix timestamp or 0 for now. For logs: RFC3339 format (defaults to now).",
			},
			"metric": map[string]interface{}{
				"type":        "string",
				"description": "Metric name for get_metric_metadata action",
			},
			"filter": map[string]interface{}{
				"type":        "string",
				"description": "Filter prefix for list_metrics (e.g., 'system.cpu' to list all CPU metrics)",
			},
			// Logs parameters
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum number of logs to return (default 100, max 1000)",
			},
			"service": map[string]interface{}{
				"type":        "string",
				"description": "Filter logs by service name",
			},
			"host": map[string]interface{}{
				"type":        "string",
				"description": "Filter logs by host name",
			},
			"status": map[string]interface{}{
				"type":        "string",
				"description": "Filter logs by status",
				"enum":        []string{"info", "warn", "error", "debug"},
			},
			"cursor": map[string]interface{}{
				"type":        "string",
				"description": "Pagination cursor from previous search results",
			},
			"log_id": map[string]interface{}{
				"type":        "string",
				"description": "Log ID for get_log action",
			},
			"indexes": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Specific log indexes to search (optional)",
			},
		},
		"required": []string{"action"},
	}
}

// GetActionDocs returns documentation for each action.
func (t *DatadogTool) GetActionDocs() map[string]types.ActionDoc {
	return map[string]types.ActionDoc{
		// Metrics actions
		"query_metrics": {
			Description:    "Query time series data using Datadog metric query syntax",
			RequiredParams: []string{"query"},
			OptionalParams: []string{"from", "to"},
			Returns:        "Time series data with timestamps, values, metric name, scope, and unit",
		},
		"list_metrics": {
			Description:    "List available metric names from the last 24 hours",
			OptionalParams: []string{"filter"},
			Returns:        "Array of metric names matching the filter",
		},
		"get_metric_metadata": {
			Description:    "Get metadata for a specific metric including type, unit, and description",
			RequiredParams: []string{"metric"},
			Returns:        "Metric metadata: type, unit, description, per_unit, integration, short_name",
		},
		// Logs actions
		"search_logs": {
			Description:    "Search logs with filters and pagination",
			RequiredParams: []string{},
			OptionalParams: []string{"query", "from", "to", "limit", "service", "host", "status", "cursor", "indexes"},
			Returns:        "List of log entries with timestamps, status, service, host, and message",
		},
		"get_log": {
			Description:    "Retrieve a single log entry by ID",
			RequiredParams: []string{"log_id"},
			OptionalParams: []string{},
			Returns:        "Full log entry with all attributes",
		},
		"list_indexes": {
			Description:    "List all configured log indexes",
			RequiredParams: []string{},
			OptionalParams: []string{},
			Returns:        "List of log indexes with retention and rate limit info",
		},
	}
}

// Execute dispatches the requested action and returns a tool result.
func (t *DatadogTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	action := getStringArg(args, "action", "")
	if action == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "action parameter is required",
		}, nil
	}

	switch action {
	// Metrics actions
	case "query_metrics":
		return t.executeQueryMetrics(ctx, args)
	case "list_metrics":
		return t.executeListMetrics(ctx, args)
	case "get_metric_metadata":
		return t.executeGetMetricMetadata(ctx, args)
	// Logs actions
	case "search_logs":
		return t.executeSearchLogs(ctx, args)
	case "get_log":
		return t.executeGetLog(ctx, args)
	case "list_indexes":
		return t.executeListIndexes(ctx)
	default:
		return types.NewErrorResult("invalid_action",
			fmt.Sprintf("Unknown action: %s", action)).
			WithParameter("action", action).
			WithAvailableValues([]string{"query_metrics", "list_metrics", "get_metric_metadata", "search_logs", "get_log", "list_indexes"}), nil
	}
}

// --- Helper functions ---

func getStringArg(args map[string]interface{}, key, defaultVal string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return defaultVal
}

func getInt64Arg(args map[string]interface{}, key string, defaultVal int64) int64 {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case float64:
			return int64(n)
		case int:
			return int64(n)
		case int64:
			return n
		}
	}
	return defaultVal
}

func getIntArg(args map[string]interface{}, key string, defaultVal int) int {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		case int64:
			return int(n)
		case json.Number:
			if i, err := n.Int64(); err == nil {
				return int(i)
			}
		}
	}
	return defaultVal
}

// ---------- Logs action handlers ----------

// executeSearchLogs handles the search_logs action.
func (t *DatadogTool) executeSearchLogs(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	params := SearchLogsParams{
		Query:   getStringArg(args, "query", ""),
		Limit:   getIntArg(args, "limit", 100),
		Service: getStringArg(args, "service", ""),
		Host:    getStringArg(args, "host", ""),
		Status:  getStringArg(args, "status", ""),
		Cursor:  getStringArg(args, "cursor", ""),
	}

	// Parse time range
	fromStr := getStringArg(args, "from", "")
	toStr := getStringArg(args, "to", "")

	if fromStr != "" {
		from, err := parseTime(fromStr)
		if err != nil {
			return &types.ToolResult{
				Success: false,
				Error:   fmt.Sprintf("invalid 'from' time: %v", err),
			}, nil
		}
		params.From = from
	}

	if toStr != "" {
		to, err := parseTime(toStr)
		if err != nil {
			return &types.ToolResult{
				Success: false,
				Error:   fmt.Sprintf("invalid 'to' time: %v", err),
			}, nil
		}
		params.To = to
	}

	// Parse indexes array
	if indexesRaw, ok := args["indexes"]; ok {
		switch v := indexesRaw.(type) {
		case []interface{}:
			for _, idx := range v {
				if s, ok := idx.(string); ok {
					params.Indexes = append(params.Indexes, s)
				}
			}
		case []string:
			params.Indexes = v
		}
	}

	result, err := t.logsClient.SearchLogs(ctx, params)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("search failed: %v", err),
		}, nil
	}

	// Convert to interface for Data field
	logsData := make([]interface{}, len(result.Logs))
	for i, log := range result.Logs {
		logsData[i] = map[string]interface{}{
			"id":         log.ID,
			"timestamp":  log.Timestamp.Format(time.RFC3339),
			"status":     log.Status,
			"service":    log.Service,
			"host":       log.Host,
			"message":    log.Message,
			"tags":       log.Tags,
			"attributes": log.Attrs,
		}
	}

	data := map[string]interface{}{
		"logs":     logsData,
		"count":    result.Count,
		"has_more": result.HasMore,
	}
	if result.NextCursor != "" {
		data["next_cursor"] = result.NextCursor
	}

	content := fmt.Sprintf("Found %d log(s)", result.Count)
	if result.HasMore {
		content += " (more available, use cursor to paginate)"
	}

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data:    data,
	}, nil
}

// executeGetLog handles the get_log action.
func (t *DatadogTool) executeGetLog(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	logID := getStringArg(args, "log_id", "")
	if logID == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "log_id parameter is required for get_log action",
		}, nil
	}

	log, err := t.logsClient.GetLog(ctx, logID)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to get log: %v", err),
		}, nil
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Retrieved log %s from %s", log.ID, log.Timestamp.Format(time.RFC3339)),
		Data: map[string]interface{}{
			"id":         log.ID,
			"timestamp":  log.Timestamp.Format(time.RFC3339),
			"status":     log.Status,
			"service":    log.Service,
			"host":       log.Host,
			"message":    log.Message,
			"tags":       log.Tags,
			"attributes": log.Attrs,
		},
	}, nil
}

// executeListIndexes handles the list_indexes action.
func (t *DatadogTool) executeListIndexes(ctx context.Context) (*types.ToolResult, error) {
	indexes, err := t.logsClient.ListIndexes(ctx)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to list indexes: %v", err),
		}, nil
	}

	indexData := make([]interface{}, len(indexes))
	for i, idx := range indexes {
		indexData[i] = map[string]interface{}{
			"name":               idx.Name,
			"num_retention_days": idx.NumRetDays,
			"daily_limit":        idx.DailyLimit,
			"is_rate_limited":    idx.IsRateLimited,
		}
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Found %d log index(es)", len(indexes)),
		Data: map[string]interface{}{
			"indexes": indexData,
			"count":   len(indexes),
		},
	}, nil
}

// parseTime parses a time string, supporting RFC3339 and relative formats like "-1h", "-15m".
func parseTime(s string) (time.Time, error) {
	// Try RFC3339 first
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}

	// Try relative format
	if len(s) > 1 && s[0] == '-' {
		s = s[1:]
		var value int
		var unit byte

		n, err := fmt.Sscanf(s, "%d%c", &value, &unit)
		if err != nil || n != 2 {
			return time.Time{}, fmt.Errorf("invalid relative time format: use -1h, -15m, -30s, etc.")
		}

		var duration time.Duration
		switch unit {
		case 's':
			duration = time.Duration(value) * time.Second
		case 'm':
			duration = time.Duration(value) * time.Minute
		case 'h':
			duration = time.Duration(value) * time.Hour
		case 'd':
			duration = time.Duration(value) * 24 * time.Hour
		default:
			return time.Time{}, fmt.Errorf("invalid time unit: %c (use s, m, h, or d)", unit)
		}

		return time.Now().Add(-duration), nil
	}

	return time.Time{}, fmt.Errorf("invalid time format: use RFC3339 or relative (-1h, -15m)")
}
