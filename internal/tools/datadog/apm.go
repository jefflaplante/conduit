package datadog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"conduit/internal/tools/types"
)

// MaxSpansDisplay is the maximum number of spans to include in detailed output.
// Traces with more spans are summarized.
const MaxSpansDisplay = 50

// Span represents a single span within a trace.
type Span struct {
	TraceID  string             `json:"trace_id"`
	SpanID   string             `json:"span_id"`
	ParentID string             `json:"parent_id,omitempty"`
	Service  string             `json:"service"`
	Name     string             `json:"name"` // operation name
	Resource string             `json:"resource"`
	Type     string             `json:"type,omitempty"`
	Start    time.Time          `json:"start"`
	Duration time.Duration      `json:"duration"`
	Error    int                `json:"error"`
	Meta     map[string]string  `json:"meta,omitempty"`
	Metrics  map[string]float64 `json:"metrics,omitempty"`
}

// Trace represents a distributed trace with all its spans.
type Trace struct {
	TraceID   string        `json:"trace_id"`
	RootSpan  *Span         `json:"root_span,omitempty"`
	Spans     []Span        `json:"spans"`
	Services  []string      `json:"services"`
	Duration  time.Duration `json:"duration"`
	StartTime time.Time     `json:"start_time"`
	HasError  bool          `json:"has_error"`
}

// TraceSearchResult contains the results of a trace search.
type TraceSearchResult struct {
	Traces     []TraceSummary `json:"traces"`
	Count      int            `json:"count"`
	NextCursor string         `json:"next_cursor,omitempty"`
	HasMore    bool           `json:"has_more"`
}

// TraceSummary provides a concise view of a trace for search results.
type TraceSummary struct {
	TraceID   string        `json:"trace_id"`
	Service   string        `json:"service"`
	Operation string        `json:"operation"`
	Resource  string        `json:"resource"`
	Duration  time.Duration `json:"duration"`
	Status    string        `json:"status"` // "ok" or "error"
	Timestamp time.Time     `json:"timestamp"`
	SpanCount int           `json:"span_count,omitempty"`
}

// APMClient provides APM trace query operations via the Datadog API.
type APMClient struct {
	client *Client
}

// NewAPMClient creates a new APMClient wrapping the given API client.
func NewAPMClient(client *Client) *APMClient {
	return &APMClient{client: client}
}

// SearchTracesParams contains the parameters for a trace search.
type SearchTracesParams struct {
	Service     string    // Required: service name to filter by
	Operation   string    // Optional: operation/span name filter
	Resource    string    // Optional: resource filter (e.g., endpoint path)
	From        time.Time // Start of time range
	To          time.Time // End of time range
	MinDuration string    // Optional: minimum duration (e.g., "1s", "500ms")
	Status      string    // Optional: "ok" or "error"
	Limit       int       // Maximum traces to return (default 20)
	Cursor      string    // Pagination cursor from previous response
}

// SearchTraces searches for traces matching the given parameters.
func (ac *APMClient) SearchTraces(ctx context.Context, params SearchTracesParams) (*TraceSearchResult, error) {
	// Build Datadog APM query string
	queryParts := []string{}

	// Service is required
	if params.Service == "" {
		return nil, fmt.Errorf("service parameter is required")
	}
	queryParts = append(queryParts, fmt.Sprintf("service:%s", params.Service))

	// Optional filters
	if params.Operation != "" {
		queryParts = append(queryParts, fmt.Sprintf("operation_name:%s", params.Operation))
	}
	if params.Resource != "" {
		queryParts = append(queryParts, fmt.Sprintf("resource_name:%s", params.Resource))
	}
	if params.Status != "" {
		if params.Status == "error" {
			queryParts = append(queryParts, "status:error")
		} else if params.Status == "ok" {
			queryParts = append(queryParts, "-status:error")
		}
	}
	if params.MinDuration != "" {
		dur, err := parseDurationToNanos(params.MinDuration)
		if err != nil {
			return nil, fmt.Errorf("invalid min_duration: %w", err)
		}
		queryParts = append(queryParts, fmt.Sprintf("duration:>%d", dur))
	}

	query := strings.Join(queryParts, " ")

	// Default limit
	limit := params.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100 // API max for spans endpoint
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

	// Build request body for spans/events/search API
	reqBody := map[string]interface{}{
		"filter": map[string]interface{}{
			"query": query,
			"from":  from.Format(time.RFC3339Nano),
			"to":    to.Format(time.RFC3339Nano),
		},
		"sort": "-timestamp",
		"page": map[string]interface{}{
			"limit": limit,
		},
	}

	if params.Cursor != "" {
		reqBody["page"].(map[string]interface{})["cursor"] = params.Cursor
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	resp, err := ac.client.Do(ctx, http.MethodPost, "api/v2/spans/events/search", bytes.NewReader(bodyBytes))
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
			Type       string `json:"type"`
			Attributes struct {
				Timestamp        time.Time              `json:"timestamp"`
				Service          string                 `json:"service"`
				ResourceName     string                 `json:"resource_name"`
				SpanName         string                 `json:"span_name"`
				Duration         int64                  `json:"duration"` // nanoseconds
				Status           string                 `json:"status"`
				TraceID          string                 `json:"trace_id"`
				SpanID           string                 `json:"span_id"`
				ParentID         string                 `json:"parent_id"`
				Host             string                 `json:"host"`
				IngestionReason  string                 `json:"ingestion_reason"`
				SpanCount        int                    `json:"span_count"`
				CustomAttributes map[string]interface{} `json:"custom"`
				Tags             []string               `json:"tags"`
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

	result := &TraceSearchResult{
		Traces:     make([]TraceSummary, 0, len(apiResp.Data)),
		Count:      len(apiResp.Data),
		NextCursor: apiResp.Meta.Page.After,
		HasMore:    apiResp.Meta.Page.After != "",
	}

	// De-duplicate by trace_id (spans endpoint returns spans, not traces)
	seenTraces := make(map[string]bool)
	for _, item := range apiResp.Data {
		attrs := item.Attributes

		// Skip if we've already seen this trace
		if seenTraces[attrs.TraceID] {
			continue
		}
		seenTraces[attrs.TraceID] = true

		status := "ok"
		if attrs.Status == "error" {
			status = "error"
		}

		summary := TraceSummary{
			TraceID:   attrs.TraceID,
			Service:   attrs.Service,
			Operation: attrs.SpanName,
			Resource:  attrs.ResourceName,
			Duration:  time.Duration(attrs.Duration),
			Status:    status,
			Timestamp: attrs.Timestamp,
			SpanCount: attrs.SpanCount,
		}
		result.Traces = append(result.Traces, summary)
	}
	result.Count = len(result.Traces)

	return result, nil
}

// GetTrace retrieves a full trace with all its spans.
func (ac *APMClient) GetTrace(ctx context.Context, traceID string) (*Trace, error) {
	if traceID == "" {
		return nil, fmt.Errorf("trace_id is required")
	}

	// Use the spans/events/search API with trace_id filter
	// The v1 GET /trace/{trace_id} endpoint is deprecated
	reqBody := map[string]interface{}{
		"filter": map[string]interface{}{
			"query": fmt.Sprintf("trace_id:%s", traceID),
			"from":  time.Now().Add(-24 * time.Hour).Format(time.RFC3339Nano),
			"to":    time.Now().Format(time.RFC3339Nano),
		},
		"page": map[string]interface{}{
			"limit": 1000, // Get all spans in the trace
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	resp, err := ac.client.Do(ctx, http.MethodPost, "api/v2/spans/events/search", bytes.NewReader(bodyBytes))
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
			Attributes struct {
				Timestamp    time.Time              `json:"timestamp"`
				Service      string                 `json:"service"`
				ResourceName string                 `json:"resource_name"`
				SpanName     string                 `json:"span_name"`
				Duration     int64                  `json:"duration"`
				Status       string                 `json:"status"`
				TraceID      string                 `json:"trace_id"`
				SpanID       string                 `json:"span_id"`
				ParentID     string                 `json:"parent_id"`
				Host         string                 `json:"host"`
				Type         string                 `json:"type"`
				Custom       map[string]interface{} `json:"custom"`
				Tags         []string               `json:"tags"`
			} `json:"attributes"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(apiResp.Data) == 0 {
		return nil, fmt.Errorf("trace not found: %s", traceID)
	}

	// Build trace from spans
	trace := &Trace{
		TraceID:  traceID,
		Spans:    make([]Span, 0, len(apiResp.Data)),
		Services: make([]string, 0),
	}

	serviceSet := make(map[string]bool)
	var earliestStart time.Time
	var latestEnd time.Time

	for _, item := range apiResp.Data {
		attrs := item.Attributes

		// Convert custom attributes to string map
		meta := make(map[string]string)
		for k, v := range attrs.Custom {
			switch val := v.(type) {
			case string:
				meta[k] = val
			case float64:
				meta[k] = fmt.Sprintf("%v", val)
			case bool:
				meta[k] = fmt.Sprintf("%v", val)
			}
		}

		// Parse tags into meta
		for _, tag := range attrs.Tags {
			if parts := strings.SplitN(tag, ":", 2); len(parts) == 2 {
				meta[parts[0]] = parts[1]
			}
		}

		errorVal := 0
		if attrs.Status == "error" {
			errorVal = 1
			trace.HasError = true
		}

		span := Span{
			TraceID:  attrs.TraceID,
			SpanID:   attrs.SpanID,
			ParentID: attrs.ParentID,
			Service:  attrs.Service,
			Name:     attrs.SpanName,
			Resource: attrs.ResourceName,
			Type:     attrs.Type,
			Start:    attrs.Timestamp,
			Duration: time.Duration(attrs.Duration),
			Error:    errorVal,
			Meta:     meta,
		}
		trace.Spans = append(trace.Spans, span)

		// Track services
		if !serviceSet[attrs.Service] {
			serviceSet[attrs.Service] = true
			trace.Services = append(trace.Services, attrs.Service)
		}

		// Track time range
		if earliestStart.IsZero() || attrs.Timestamp.Before(earliestStart) {
			earliestStart = attrs.Timestamp
		}
		spanEnd := attrs.Timestamp.Add(time.Duration(attrs.Duration))
		if latestEnd.IsZero() || spanEnd.After(latestEnd) {
			latestEnd = spanEnd
		}

		// Identify root span (no parent)
		if attrs.ParentID == "" || attrs.ParentID == "0" {
			trace.RootSpan = &span
		}
	}

	trace.StartTime = earliestStart
	trace.Duration = latestEnd.Sub(earliestStart)

	// Sort spans by start time
	sort.Slice(trace.Spans, func(i, j int) bool {
		return trace.Spans[i].Start.Before(trace.Spans[j].Start)
	})

	// Sort services alphabetically
	sort.Strings(trace.Services)

	return trace, nil
}

// parseDurationToNanos parses a duration string (e.g., "1s", "500ms") to nanoseconds.
func parseDurationToNanos(s string) (int64, error) {
	// Handle Datadog-style suffixes
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}

	// Try standard Go duration parsing first
	if d, err := time.ParseDuration(s); err == nil {
		return d.Nanoseconds(), nil
	}

	// Handle numeric-only input (assume milliseconds)
	if matched, _ := regexp.MatchString(`^\d+$`, s); matched {
		ms, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return 0, err
		}
		return ms * 1e6, nil
	}

	return 0, fmt.Errorf("invalid duration format: %s (use Go duration like 1s, 500ms, or numeric milliseconds)", s)
}

// formatDuration formats a duration for human-readable output.
func formatDuration(d time.Duration) string {
	if d < time.Millisecond {
		return fmt.Sprintf("%.2fus", float64(d.Nanoseconds())/1000)
	}
	if d < time.Second {
		return fmt.Sprintf("%.2fms", float64(d.Nanoseconds())/1e6)
	}
	return fmt.Sprintf("%.2fs", d.Seconds())
}

// executeSearchTraces handles the search_traces action.
func (t *DatadogTool) executeSearchTraces(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	service := getStringArg(args, "service", "")
	if service == "" {
		return types.NewErrorResult("missing_parameter", "service is required for search_traces action").
			WithParameter("service", nil).
			WithSuggestions([]string{
				"Specify the service name to search traces for",
				"Example: service=api-gateway",
			}), nil
	}

	params := SearchTracesParams{
		Service:     service,
		Operation:   getStringArg(args, "operation", ""),
		Resource:    getStringArg(args, "resource", ""),
		MinDuration: getStringArg(args, "min_duration", ""),
		Status:      getStringArg(args, "status", ""),
		Limit:       getIntArg(args, "limit", 20),
		Cursor:      getStringArg(args, "cursor", ""),
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

	result, err := t.apmClient.SearchTraces(ctx, params)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("search failed: %v", err),
		}, nil
	}

	// Convert to interface for Data field
	tracesData := make([]interface{}, len(result.Traces))
	var errorCount, slowCount int
	for i, tr := range result.Traces {
		tracesData[i] = map[string]interface{}{
			"trace_id":    tr.TraceID,
			"service":     tr.Service,
			"operation":   tr.Operation,
			"resource":    tr.Resource,
			"duration":    formatDuration(tr.Duration),
			"duration_ns": tr.Duration.Nanoseconds(),
			"status":      tr.Status,
			"timestamp":   tr.Timestamp.Format(time.RFC3339),
			"span_count":  tr.SpanCount,
		}
		if tr.Status == "error" {
			errorCount++
		}
		if tr.Duration > time.Second {
			slowCount++
		}
	}

	data := map[string]interface{}{
		"traces":   tracesData,
		"count":    result.Count,
		"has_more": result.HasMore,
	}
	if result.NextCursor != "" {
		data["next_cursor"] = result.NextCursor
	}

	// Build summary content
	content := fmt.Sprintf("Found %d trace(s) for service '%s'", result.Count, service)
	if errorCount > 0 {
		content += fmt.Sprintf(" (%d with errors)", errorCount)
	}
	if slowCount > 0 {
		content += fmt.Sprintf(" (%d >1s)", slowCount)
	}
	if result.HasMore {
		content += " (more available, use cursor to paginate)"
	}

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data:    data,
	}, nil
}

// executeGetTrace handles the get_trace action.
func (t *DatadogTool) executeGetTrace(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	traceID := getStringArg(args, "trace_id", "")
	if traceID == "" {
		return types.NewErrorResult("missing_parameter", "trace_id is required for get_trace action").
			WithParameter("trace_id", nil).
			WithSuggestions([]string{
				"Use search_traces to find trace IDs",
				"trace_id is a hexadecimal string like '1234567890abcdef'",
			}), nil
	}

	trace, err := t.apmClient.GetTrace(ctx, traceID)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to get trace: %v", err),
		}, nil
	}

	// Build span data with timeline
	spansData := make([]interface{}, 0, len(trace.Spans))
	summarized := false

	// If too many spans, summarize instead of showing all
	if len(trace.Spans) > MaxSpansDisplay {
		summarized = true
		// Show first and last spans plus any errors
		for i, span := range trace.Spans {
			if i < 10 || i >= len(trace.Spans)-5 || span.Error != 0 {
				spansData = append(spansData, formatSpanData(span, trace.StartTime))
			}
		}
	} else {
		for _, span := range trace.Spans {
			spansData = append(spansData, formatSpanData(span, trace.StartTime))
		}
	}

	// Build root span info
	var rootInfo map[string]interface{}
	if trace.RootSpan != nil {
		rootInfo = map[string]interface{}{
			"service":   trace.RootSpan.Service,
			"operation": trace.RootSpan.Name,
			"resource":  trace.RootSpan.Resource,
			"duration":  formatDuration(trace.RootSpan.Duration),
		}
	}

	data := map[string]interface{}{
		"trace_id":   trace.TraceID,
		"start_time": trace.StartTime.Format(time.RFC3339),
		"duration":   formatDuration(trace.Duration),
		"span_count": len(trace.Spans),
		"services":   trace.Services,
		"has_error":  trace.HasError,
		"spans":      spansData,
	}

	if rootInfo != nil {
		data["root_span"] = rootInfo
	}

	if summarized {
		data["summarized"] = true
		data["spans_shown"] = len(spansData)
	}

	// Build content summary
	status := "OK"
	if trace.HasError {
		status = "ERROR"
	}

	content := fmt.Sprintf("Trace %s: %d spans across %d service(s), duration %s, status %s",
		trace.TraceID, len(trace.Spans), len(trace.Services), formatDuration(trace.Duration), status)

	if summarized {
		content += fmt.Sprintf(" (showing %d of %d spans)", len(spansData), len(trace.Spans))
	}

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data:    data,
	}, nil
}

// formatSpanData formats a span for output, including timeline offset.
func formatSpanData(span Span, traceStart time.Time) map[string]interface{} {
	offset := span.Start.Sub(traceStart)

	data := map[string]interface{}{
		"span_id":   span.SpanID,
		"service":   span.Service,
		"operation": span.Name,
		"resource":  span.Resource,
		"duration":  formatDuration(span.Duration),
		"offset":    formatDuration(offset), // Time since trace start
		"timestamp": span.Start.Format(time.RFC3339),
	}

	if span.ParentID != "" && span.ParentID != "0" {
		data["parent_id"] = span.ParentID
	}

	if span.Error != 0 {
		data["error"] = true
	}

	if span.Type != "" {
		data["type"] = span.Type
	}

	// Include select metadata if present
	if span.Meta != nil {
		interesting := make(map[string]string)
		for _, key := range []string{"http.method", "http.status_code", "http.url", "error.msg", "error.type", "db.statement"} {
			if v, ok := span.Meta[key]; ok {
				interesting[key] = v
			}
		}
		if len(interesting) > 0 {
			data["meta"] = interesting
		}
	}

	return data
}
