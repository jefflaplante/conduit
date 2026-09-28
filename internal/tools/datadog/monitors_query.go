//go:build with_datadog

package datadog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"conduit/internal/httpsafe"
	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

// ---------- Action implementations ----------

func (t *MonitorTool) executeListMonitors(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	// Build query parameters
	params := url.Values{}

	// Name filter (substring match via name parameter)
	if name := toolargs.GetString(args, "name", ""); name != "" {
		params.Set("name", name)
	}

	// Tags filter
	if tagsArg, ok := args["tags"]; ok {
		if tags, ok := tagsArg.([]interface{}); ok {
			tagStrs := make([]string, 0, len(tags))
			for _, tag := range tags {
				if s, ok := tag.(string); ok {
					tagStrs = append(tagStrs, s)
				}
			}
			if len(tagStrs) > 0 {
				params.Set("tags", strings.Join(tagStrs, ","))
			}
		}
	}

	path := "api/v1/monitor"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	resp, err := t.client.Do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to list monitors: %v", err)}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := httpsafe.ReadLimited(resp.Body, httpsafe.ErrorBodyLimit) // conduit-31jg.7
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("Datadog API error: %d - %s", resp.StatusCode, string(body)),
		}, nil
	}

	var monitors []Monitor
	if err := json.NewDecoder(httpsafe.LimitReader(resp.Body, httpsafe.APIBodyLimit)).Decode(&monitors); err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to decode response: %v", err)}, nil
	}

	// Filter by status if specified
	statusFilter := toolargs.GetString(args, "status", "")
	if statusFilter != "" {
		filtered := make([]Monitor, 0)
		for _, m := range monitors {
			if normalizeState(m.OverallState) == statusFilter {
				filtered = append(filtered, m)
			}
		}
		monitors = filtered
	}

	// Sort: Alert first, then Warn, then others
	sort.Slice(monitors, func(i, j int) bool {
		return statePriority(monitors[i].OverallState) < statePriority(monitors[j].OverallState)
	})

	// Build response with summaries
	var alerting, warning, ok_, noData int
	items := make([]map[string]interface{}, 0, len(monitors))
	for _, m := range monitors {
		state := normalizeState(m.OverallState)
		switch state {
		case "Alert":
			alerting++
		case "Warn":
			warning++
		case "OK":
			ok_++
		case "No Data":
			noData++
		}

		item := map[string]interface{}{
			"id":     m.ID,
			"name":   m.Name,
			"status": state,
			"type":   m.Type,
			"tags":   m.Tags,
		}

		// Highlight alerting monitors
		if state == "Alert" || state == "Warn" {
			item["highlighted"] = true
		}

		items = append(items, item)
	}

	// Build content summary
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d monitor(s)", len(monitors)))
	if alerting > 0 {
		sb.WriteString(fmt.Sprintf(" — **%d ALERTING**", alerting))
	}
	if warning > 0 {
		sb.WriteString(fmt.Sprintf(" — %d warning", warning))
	}
	if noData > 0 {
		sb.WriteString(fmt.Sprintf(" — %d no data", noData))
	}
	if ok_ > 0 {
		sb.WriteString(fmt.Sprintf(" — %d OK", ok_))
	}

	return &types.ToolResult{
		Success: true,
		Content: sb.String(),
		Data: map[string]interface{}{
			"monitors": items,
			"summary": map[string]interface{}{
				"total":    len(monitors),
				"alerting": alerting,
				"warning":  warning,
				"ok":       ok_,
				"no_data":  noData,
			},
		},
	}, nil
}

func (t *MonitorTool) executeGetMonitor(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	monitorID := toolargs.GetInt(args, "monitor_id", 0)
	if monitorID == 0 {
		return &types.ToolResult{Success: false, Error: "monitor_id parameter is required"}, nil
	}

	path := fmt.Sprintf("api/v1/monitor/%d", monitorID)
	resp, err := t.client.Do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to get monitor: %v", err)}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("monitor %d not found", monitorID)}, nil
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := httpsafe.ReadLimited(resp.Body, httpsafe.ErrorBodyLimit) // conduit-31jg.7
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("Datadog API error: %d - %s", resp.StatusCode, string(body)),
		}, nil
	}

	var monitor Monitor
	if err := json.NewDecoder(httpsafe.LimitReader(resp.Body, httpsafe.APIBodyLimit)).Decode(&monitor); err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to decode response: %v", err)}, nil
	}

	state := normalizeState(monitor.OverallState)
	content := fmt.Sprintf("Monitor %d: %s (Status: %s)", monitor.ID, monitor.Name, state)
	if state == "Alert" || state == "Warn" {
		content = fmt.Sprintf("**%s** — %s", state, content)
	}

	data := map[string]interface{}{
		"id":       monitor.ID,
		"name":     monitor.Name,
		"type":     monitor.Type,
		"query":    monitor.Query,
		"message":  monitor.Message,
		"status":   state,
		"tags":     monitor.Tags,
		"created":  monitor.Created,
		"modified": monitor.Modified,
	}

	if monitor.Options != nil {
		data["thresholds"] = monitor.Options.Thresholds
		data["notify_no_data"] = monitor.Options.NotifyNoData
		if monitor.Options.NoDataTimeframe != nil {
			data["no_data_timeframe"] = *monitor.Options.NoDataTimeframe
		}
		if len(monitor.Options.Silenced) > 0 {
			data["silenced"] = monitor.Options.Silenced
		}
	}

	if monitor.Creator != nil {
		data["creator"] = map[string]interface{}{
			"name":  monitor.Creator.Name,
			"email": monitor.Creator.Email,
		}
	}

	if monitor.Priority != nil {
		data["priority"] = *monitor.Priority
	}

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data:    data,
	}, nil
}

func (t *MonitorTool) executeGetMonitorStatus(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	monitorID := toolargs.GetInt(args, "monitor_id", 0)
	if monitorID == 0 {
		return &types.ToolResult{Success: false, Error: "monitor_id parameter is required"}, nil
	}

	// Get monitor with group states
	path := fmt.Sprintf("api/v1/monitor/%d?group_states=all", monitorID)
	resp, err := t.client.Do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to get monitor status: %v", err)}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("monitor %d not found", monitorID)}, nil
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := httpsafe.ReadLimited(resp.Body, httpsafe.ErrorBodyLimit) // conduit-31jg.7
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("Datadog API error: %d - %s", resp.StatusCode, string(body)),
		}, nil
	}

	var monitor Monitor
	if err := json.NewDecoder(httpsafe.LimitReader(resp.Body, httpsafe.APIBodyLimit)).Decode(&monitor); err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to decode response: %v", err)}, nil
	}

	state := normalizeState(monitor.OverallState)
	content := fmt.Sprintf("Monitor %d Status: %s", monitor.ID, state)
	if state == "Alert" || state == "Warn" {
		content = fmt.Sprintf("**%s** — Monitor %d: %s", state, monitor.ID, monitor.Name)
	}

	data := map[string]interface{}{
		"id":            monitor.ID,
		"name":          monitor.Name,
		"overall_state": state,
		"type":          monitor.Type,
	}

	// Process group states
	if monitor.State != nil && len(monitor.State.Groups) > 0 {
		groups := make([]map[string]interface{}, 0, len(monitor.State.Groups))
		var lastTriggered *time.Time

		for name, gs := range monitor.State.Groups {
			group := map[string]interface{}{
				"name":   name,
				"status": normalizeState(gs.Status),
			}

			if gs.LastTriggeredTS != nil && *gs.LastTriggeredTS > 0 {
				t := time.Unix(*gs.LastTriggeredTS/1000, 0) // Convert from millis
				group["last_triggered"] = t.Format(time.RFC3339)
				if lastTriggered == nil || t.After(*lastTriggered) {
					lastTriggered = &t
				}
			}

			if gs.LastResolvedTS != nil && *gs.LastResolvedTS > 0 {
				group["last_resolved"] = time.Unix(*gs.LastResolvedTS/1000, 0).Format(time.RFC3339)
			}

			groups = append(groups, group)
		}

		// Sort groups by status (Alert first)
		sort.Slice(groups, func(i, j int) bool {
			return statePriority(groups[i]["status"].(string)) < statePriority(groups[j]["status"].(string))
		})

		data["groups"] = groups
		data["group_count"] = len(groups)

		if lastTriggered != nil {
			data["last_triggered"] = lastTriggered.Format(time.RFC3339)
			content += fmt.Sprintf(" (last triggered: %s)", lastTriggered.Format(time.RFC3339))
		}
	}

	// Include silenced info if present
	if monitor.Options != nil && len(monitor.Options.Silenced) > 0 {
		data["silenced"] = monitor.Options.Silenced
		content += " [MUTED]"
	}

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data:    data,
	}, nil
}

// ---------- Helper functions ----------

// normalizeState converts Datadog API state strings to normalized display values.
func normalizeState(state string) string {
	switch strings.ToLower(state) {
	case "ok":
		return "OK"
	case "alert":
		return "Alert"
	case "warn":
		return "Warn"
	case "no data":
		return "No Data"
	default:
		if state == "" {
			return "Unknown"
		}
		return state
	}
}

// statePriority returns sort priority (lower = more urgent).
func statePriority(state string) int {
	switch normalizeState(state) {
	case "Alert":
		return 0
	case "Warn":
		return 1
	case "No Data":
		return 2
	case "OK":
		return 3
	default:
		return 4
	}
}
