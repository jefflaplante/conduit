package datadog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// MaxMessageLength is the maximum number of characters to include in a log message.
// Messages longer than this are truncated with an ellipsis.
const MaxMessageLength = 2000

// LogEntry represents a single log entry from Datadog.
type LogEntry struct {
	ID        string            `json:"id"`
	Timestamp time.Time         `json:"timestamp"`
	Status    string            `json:"status"`
	Service   string            `json:"service,omitempty"`
	Host      string            `json:"host,omitempty"`
	Message   string            `json:"message"`
	Tags      []string          `json:"tags,omitempty"`
	Attrs     map[string]string `json:"attributes,omitempty"`
}

// LogSearchResult contains the results of a log search.
type LogSearchResult struct {
	Logs       []LogEntry `json:"logs"`
	Count      int        `json:"count"`
	NextCursor string     `json:"next_cursor,omitempty"`
	HasMore    bool       `json:"has_more"`
}

// LogIndex represents a Datadog log index.
type LogIndex struct {
	Name           string `json:"name"`
	NumRetDays     int    `json:"num_retention_days,omitempty"`
	DailyLimit     int64  `json:"daily_limit,omitempty"`
	IsRateLimited  bool   `json:"is_rate_limited,omitempty"`
	ExclusionRatio int    `json:"exclusion_ratio,omitempty"`
}

// LogsClient provides log search operations via the Datadog API.
type LogsClient struct {
	client *Client
}

// NewLogsClient creates a new LogsClient wrapping the given API client.
func NewLogsClient(client *Client) *LogsClient {
	return &LogsClient{client: client}
}

// SearchLogsParams contains the parameters for a log search.
type SearchLogsParams struct {
	Query   string    // Datadog query string (e.g., "service:api status:error")
	From    time.Time // Start of time range
	To      time.Time // End of time range
	Limit   int       // Maximum logs to return (default 100)
	Service string    // Optional: filter by service name
	Host    string    // Optional: filter by host
	Status  string    // Optional: filter by status (info, warn, error)
	Cursor  string    // Pagination cursor from previous response
	Indexes []string  // Optional: specific indexes to search
}

// SearchLogs searches for logs matching the given parameters.
func (lc *LogsClient) SearchLogs(ctx context.Context, params SearchLogsParams) (*LogSearchResult, error) {
	// Build query string with filters
	queryParts := []string{}
	if params.Query != "" {
		queryParts = append(queryParts, params.Query)
	}
	if params.Service != "" {
		queryParts = append(queryParts, fmt.Sprintf("service:%s", params.Service))
	}
	if params.Host != "" {
		queryParts = append(queryParts, fmt.Sprintf("host:%s", params.Host))
	}
	if params.Status != "" {
		queryParts = append(queryParts, fmt.Sprintf("status:%s", params.Status))
	}

	query := "*"
	if len(queryParts) > 0 {
		query = strings.Join(queryParts, " ")
	}

	// Default limit
	limit := params.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000 // API max
	}

	// Default time range (last 15 minutes if not specified)
	from := params.From
	to := params.To
	if from.IsZero() {
		from = time.Now().Add(-15 * time.Minute)
	}
	if to.IsZero() {
		to = time.Now()
	}

	// Build request body
	reqBody := map[string]interface{}{
		"filter": map[string]interface{}{
			"query": query,
			"from":  from.Format(time.RFC3339),
			"to":    to.Format(time.RFC3339),
		},
		"sort": "-timestamp",
		"page": map[string]interface{}{
			"limit": limit,
		},
	}

	if len(params.Indexes) > 0 {
		reqBody["filter"].(map[string]interface{})["indexes"] = params.Indexes
	}

	if params.Cursor != "" {
		reqBody["page"].(map[string]interface{})["cursor"] = params.Cursor
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	resp, err := lc.client.Do(ctx, http.MethodPost, "api/v2/logs/events/search", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, truncateString(string(body), 500))
	}

	var apiResp struct {
		Data []struct {
			ID         string `json:"id"`
			Attributes struct {
				Timestamp  time.Time              `json:"timestamp"`
				Status     string                 `json:"status"`
				Service    string                 `json:"service"`
				Host       string                 `json:"host"`
				Message    string                 `json:"message"`
				Tags       []string               `json:"tags"`
				Attributes map[string]interface{} `json:"attributes"`
			} `json:"attributes"`
		} `json:"data"`
		Meta struct {
			Page struct {
				After string `json:"after"`
			} `json:"page"`
		} `json:"meta"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	result := &LogSearchResult{
		Logs:       make([]LogEntry, 0, len(apiResp.Data)),
		Count:      len(apiResp.Data),
		NextCursor: apiResp.Meta.Page.After,
		HasMore:    apiResp.Meta.Page.After != "",
	}

	for _, item := range apiResp.Data {
		attrs := item.Attributes

		// Convert nested attributes to flat string map
		flatAttrs := make(map[string]string)
		for k, v := range attrs.Attributes {
			switch val := v.(type) {
			case string:
				flatAttrs[k] = val
			case float64:
				flatAttrs[k] = fmt.Sprintf("%v", val)
			case bool:
				flatAttrs[k] = fmt.Sprintf("%v", val)
			default:
				// Skip complex nested objects
			}
		}

		entry := LogEntry{
			ID:        item.ID,
			Timestamp: attrs.Timestamp,
			Status:    attrs.Status,
			Service:   attrs.Service,
			Host:      attrs.Host,
			Message:   truncateString(attrs.Message, MaxMessageLength),
			Tags:      attrs.Tags,
			Attrs:     flatAttrs,
		}
		result.Logs = append(result.Logs, entry)
	}

	return result, nil
}

// GetLog retrieves a single log entry by ID.
func (lc *LogsClient) GetLog(ctx context.Context, logID string) (*LogEntry, error) {
	if logID == "" {
		return nil, fmt.Errorf("log_id is required")
	}

	// The v2 API doesn't have a direct get-by-ID endpoint.
	// We search with a filter that should return exactly that log.
	// Note: This is a workaround since DD doesn't expose GET /logs/:id in v2.
	// We use a very narrow time window (24h) and the @id filter.
	reqBody := map[string]interface{}{
		"filter": map[string]interface{}{
			"query": fmt.Sprintf("@id:%s", logID),
			"from":  time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
			"to":    time.Now().Format(time.RFC3339),
		},
		"page": map[string]interface{}{
			"limit": 1,
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	resp, err := lc.client.Do(ctx, http.MethodPost, "api/v2/logs/events/search", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, truncateString(string(body), 500))
	}

	var apiResp struct {
		Data []struct {
			ID         string `json:"id"`
			Attributes struct {
				Timestamp  time.Time              `json:"timestamp"`
				Status     string                 `json:"status"`
				Service    string                 `json:"service"`
				Host       string                 `json:"host"`
				Message    string                 `json:"message"`
				Tags       []string               `json:"tags"`
				Attributes map[string]interface{} `json:"attributes"`
			} `json:"attributes"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(apiResp.Data) == 0 {
		return nil, fmt.Errorf("log not found: %s", logID)
	}

	item := apiResp.Data[0]
	attrs := item.Attributes

	flatAttrs := make(map[string]string)
	for k, v := range attrs.Attributes {
		switch val := v.(type) {
		case string:
			flatAttrs[k] = val
		case float64:
			flatAttrs[k] = fmt.Sprintf("%v", val)
		case bool:
			flatAttrs[k] = fmt.Sprintf("%v", val)
		}
	}

	return &LogEntry{
		ID:        item.ID,
		Timestamp: attrs.Timestamp,
		Status:    attrs.Status,
		Service:   attrs.Service,
		Host:      attrs.Host,
		Message:   attrs.Message, // Full message for single log
		Tags:      attrs.Tags,
		Attrs:     flatAttrs,
	}, nil
}

// ListIndexes returns all configured log indexes.
func (lc *LogsClient) ListIndexes(ctx context.Context) ([]LogIndex, error) {
	resp, err := lc.client.Do(ctx, http.MethodGet, "api/v1/logs/config/indexes", nil)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, truncateString(string(body), 500))
	}

	var apiResp struct {
		Indexes []struct {
			Name   string `json:"name"`
			Filter struct {
				Query string `json:"query"`
			} `json:"filter"`
			NumRetentionDays int   `json:"num_retention_days"`
			DailyLimit       int64 `json:"daily_limit"`
			IsRateLimited    bool  `json:"is_rate_limited"`
		} `json:"indexes"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	indexes := make([]LogIndex, 0, len(apiResp.Indexes))
	for _, idx := range apiResp.Indexes {
		indexes = append(indexes, LogIndex{
			Name:          idx.Name,
			NumRetDays:    idx.NumRetentionDays,
			DailyLimit:    idx.DailyLimit,
			IsRateLimited: idx.IsRateLimited,
		})
	}

	return indexes, nil
}

// truncateString truncates a string to the specified length, adding an ellipsis if truncated.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
