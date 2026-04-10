//go:build with_datadog

// Package datadog implements the Datadog monitoring tool.
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

	"conduit/internal/config"
	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

// SecurityTier represents the risk level of a Datadog operation.
type SecurityTier string

const (
	TierRead   SecurityTier = "read"
	TierModify SecurityTier = "modify"
)

// MonitorState represents the possible states of a Datadog monitor.
type MonitorState string

const (
	StateOK      MonitorState = "OK"
	StateAlert   MonitorState = "Alert"
	StateWarn    MonitorState = "Warn"
	StateNoData  MonitorState = "No Data"
	StateUnknown MonitorState = "Unknown"
)

// Monitor represents a Datadog monitor.
type Monitor struct {
	ID              int64           `json:"id"`
	Name            string          `json:"name"`
	Type            string          `json:"type"`
	Query           string          `json:"query"`
	Message         string          `json:"message,omitempty"`
	Tags            []string        `json:"tags,omitempty"`
	OverallState    string          `json:"overall_state,omitempty"`
	Priority        *int            `json:"priority,omitempty"`
	Created         *time.Time      `json:"created,omitempty"`
	Modified        *time.Time      `json:"modified,omitempty"`
	Options         *MonitorOptions `json:"options,omitempty"`
	State           *MonitorState_  `json:"state,omitempty"`
	Creator         *Creator        `json:"creator,omitempty"`
	RestrictedRoles []string        `json:"restricted_roles,omitempty"`
}

// MonitorOptions contains monitor configuration options.
type MonitorOptions struct {
	Thresholds          map[string]interface{} `json:"thresholds,omitempty"`
	NotifyNoData        bool                   `json:"notify_no_data,omitempty"`
	NoDataTimeframe     *int                   `json:"no_data_timeframe,omitempty"`
	NotifyAudit         bool                   `json:"notify_audit,omitempty"`
	Silenced            map[string]interface{} `json:"silenced,omitempty"`
	TimeoutH            *int                   `json:"timeout_h,omitempty"`
	EscalationMessage   string                 `json:"escalation_message,omitempty"`
	RenotifyInterval    *int                   `json:"renotify_interval,omitempty"`
	IncludeTags         bool                   `json:"include_tags,omitempty"`
	RequireFullWindow   bool                   `json:"require_full_window,omitempty"`
	NewGroupDelay       *int                   `json:"new_group_delay,omitempty"`
	EvaluationDelay     *int                   `json:"evaluation_delay,omitempty"`
	MinLocationFailed   *int                   `json:"min_location_failed,omitempty"`
	MinFailureDuration  *int                   `json:"min_failure_duration,omitempty"`
	OnMissingData       string                 `json:"on_missing_data,omitempty"`
	NotificationPresets []string               `json:"notification_preset_name,omitempty"`
}

// MonitorState_ contains the current state information of a monitor.
type MonitorState_ struct {
	Groups map[string]MonitorGroupState `json:"groups,omitempty"`
}

// MonitorGroupState represents the state of a monitor group.
type MonitorGroupState struct {
	Name            string `json:"name,omitempty"`
	Status          string `json:"status,omitempty"`
	LastTriggeredTS *int64 `json:"last_triggered_ts,omitempty"`
	LastResolvedTS  *int64 `json:"last_resolved_ts,omitempty"`
	LastNotifiedTS  *int64 `json:"last_notified_ts,omitempty"`
	LastNodataTS    *int64 `json:"last_nodata_ts,omitempty"`
}

// Creator contains information about who created a monitor.
type Creator struct {
	Name   string `json:"name,omitempty"`
	Handle string `json:"handle,omitempty"`
	Email  string `json:"email,omitempty"`
}

// MuteOptions contains options for muting a monitor.
type MuteOptions struct {
	Scope string `json:"scope,omitempty"`
	End   *int64 `json:"end,omitempty"`
}

// MonitorTool provides Datadog monitor management via the tool interface.
type MonitorTool struct {
	services *types.ToolServices
	config   *config.DatadogConfig
	client   *Client
}

// NewMonitorTool creates a new Datadog monitor tool with the given services and configuration.
func NewMonitorTool(services *types.ToolServices, cfg *config.DatadogConfig) (*MonitorTool, error) {
	if cfg == nil {
		return nil, fmt.Errorf("datadog config is required")
	}

	if !cfg.Enabled {
		return nil, fmt.Errorf("datadog is not enabled")
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	client := NewClient(*cfg)

	return &MonitorTool{
		services: services,
		config:   cfg,
		client:   client,
	}, nil
}

// Name returns the tool name.
func (t *MonitorTool) Name() string { return "Datadog" }

// Description returns a human-readable description of the tool's capabilities.
func (t *MonitorTool) Description() string {
	return `Datadog monitoring tool. Supported actions:
- list_monitors: List all monitors with optional filters (name, tags, status)
- get_monitor: Get monitor details including query and thresholds
- get_monitor_status: Get current status with state history
- mute_monitor: Mute a monitor (requires confirmation) — parameters: monitor_id, scope (optional), end (optional timestamp)
- unmute_monitor: Unmute a monitor (requires confirmation)

Monitors in Alert or Warn state are highlighted in output. Useful for heartbeat checks ("any DD monitors firing?").`
}

// Parameters returns the JSON schema for the tool's parameters.
func (t *MonitorTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"description": "The Datadog operation to perform",
				"enum":        []string{"list_monitors", "get_monitor", "get_monitor_status", "mute_monitor", "unmute_monitor"},
			},
			"monitor_id": map[string]interface{}{
				"type":        "integer",
				"description": "Monitor ID for get/mute/unmute operations",
			},
			"name": map[string]interface{}{
				"type":        "string",
				"description": "Filter monitors by name (substring match)",
			},
			"tags": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Filter monitors by tags (e.g., [\"env:prod\", \"team:platform\"])",
			},
			"status": map[string]interface{}{
				"type":        "string",
				"description": "Filter monitors by status (OK, Alert, Warn, No Data)",
				"enum":        []string{"OK", "Alert", "Warn", "No Data"},
			},
			"scope": map[string]interface{}{
				"type":        "string",
				"description": "Scope for mute operation (e.g., \"host:myhost\")",
			},
			"end": map[string]interface{}{
				"type":        "integer",
				"description": "Unix timestamp when mute should end (omit for indefinite)",
			},
			"confirmed": map[string]interface{}{
				"type":        "boolean",
				"description": "Confirmation for modify operations (mute/unmute). Set to true to proceed.",
			},
		},
		"required": []string{"action"},
	}
}

// GetActionDocs returns documentation for each action.
func (t *MonitorTool) GetActionDocs() map[string]types.ActionDoc {
	return map[string]types.ActionDoc{
		"list_monitors": {
			Description:    "List all Datadog monitors with optional filters. Monitors in Alert/Warn state are highlighted.",
			RequiredParams: []string{},
			OptionalParams: []string{"name", "tags", "status"},
			Returns:        "List of monitors with id, name, status, and tags",
		},
		"get_monitor": {
			Description:    "Get detailed information about a specific monitor including query and thresholds.",
			RequiredParams: []string{"monitor_id"},
			OptionalParams: []string{},
			Returns:        "Full monitor details including configuration",
		},
		"get_monitor_status": {
			Description:    "Get current status of a monitor with state history and last triggered times.",
			RequiredParams: []string{"monitor_id"},
			OptionalParams: []string{},
			Returns:        "Monitor status with group states and timestamps",
		},
		"mute_monitor": {
			Description:    "Mute a monitor. Requires confirmation. Optionally specify scope and end time.",
			RequiredParams: []string{"monitor_id", "confirmed"},
			OptionalParams: []string{"scope", "end"},
			Returns:        "Confirmation of mute operation",
		},
		"unmute_monitor": {
			Description:    "Unmute a monitor. Requires confirmation.",
			RequiredParams: []string{"monitor_id", "confirmed"},
			OptionalParams: []string{"scope"},
			Returns:        "Confirmation of unmute operation",
		},
	}
}

// Execute dispatches the requested action and returns a tool result.
func (t *MonitorTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	action := toolargs.GetString(args, "action", "")
	if action == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "action parameter is required",
		}, nil
	}

	switch action {
	case "list_monitors":
		return t.executeListMonitors(ctx, args)
	case "get_monitor":
		return t.executeGetMonitor(ctx, args)
	case "get_monitor_status":
		return t.executeGetMonitorStatus(ctx, args)
	case "mute_monitor":
		return t.executeMuteMonitor(ctx, args)
	case "unmute_monitor":
		return t.executeUnmuteMonitor(ctx, args)
	default:
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("unknown action: %s", action),
		}, nil
	}
}

// classifyAction returns the security tier for a given action.
func classifyAction(action string) SecurityTier {
	switch action {
	case "list_monitors", "get_monitor", "get_monitor_status":
		return TierRead
	case "mute_monitor", "unmute_monitor":
		return TierModify
	default:
		return TierModify // Default to modify for safety
	}
}

// requiresConfirmation checks if an action needs user confirmation.
func requiresConfirmation(action string) bool {
	return classifyAction(action) == TierModify
}

// checkConfirmation verifies that confirmation was provided for modify operations.
func checkConfirmation(action string, args map[string]interface{}) *types.ToolResult {
	if !requiresConfirmation(action) {
		return nil
	}

	confirmed := toolargs.GetBool(args, "confirmed", false)
	if !confirmed {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("action %q requires confirmation. Set confirmed=true to proceed.", action),
			Data: map[string]interface{}{
				"requires_confirmation": true,
				"action":                action,
				"security_tier":         string(TierModify),
			},
		}
	}
	return nil
}

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
		body, _ := io.ReadAll(resp.Body)
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("Datadog API error: %d - %s", resp.StatusCode, string(body)),
		}, nil
	}

	var monitors []Monitor
	if err := json.NewDecoder(resp.Body).Decode(&monitors); err != nil {
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
		body, _ := io.ReadAll(resp.Body)
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("Datadog API error: %d - %s", resp.StatusCode, string(body)),
		}, nil
	}

	var monitor Monitor
	if err := json.NewDecoder(resp.Body).Decode(&monitor); err != nil {
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
		body, _ := io.ReadAll(resp.Body)
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("Datadog API error: %d - %s", resp.StatusCode, string(body)),
		}, nil
	}

	var monitor Monitor
	if err := json.NewDecoder(resp.Body).Decode(&monitor); err != nil {
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

func (t *MonitorTool) executeMuteMonitor(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	// Check confirmation
	if result := checkConfirmation("mute_monitor", args); result != nil {
		return result, nil
	}

	monitorID := toolargs.GetInt(args, "monitor_id", 0)
	if monitorID == 0 {
		return &types.ToolResult{Success: false, Error: "monitor_id parameter is required"}, nil
	}

	// Build mute options
	muteOpts := MuteOptions{}
	if scope := toolargs.GetString(args, "scope", ""); scope != "" {
		muteOpts.Scope = scope
	}
	if end := toolargs.GetInt64(args, "end", 0); end > 0 {
		muteOpts.End = &end
	}

	// Encode body
	body, err := json.Marshal(muteOpts)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to encode mute options: %v", err)}, nil
	}

	path := fmt.Sprintf("api/v1/monitor/%d/mute", monitorID)
	resp, err := t.client.Do(ctx, http.MethodPost, path, strings.NewReader(string(body)))
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to mute monitor: %v", err)}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("monitor %d not found", monitorID)}, nil
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("Datadog API error: %d - %s", resp.StatusCode, string(respBody)),
		}, nil
	}

	content := fmt.Sprintf("Monitor %d has been muted", monitorID)
	data := map[string]interface{}{
		"monitor_id": monitorID,
		"muted":      true,
	}

	if muteOpts.Scope != "" {
		content += fmt.Sprintf(" (scope: %s)", muteOpts.Scope)
		data["scope"] = muteOpts.Scope
	}

	if muteOpts.End != nil {
		endTime := time.Unix(*muteOpts.End, 0)
		content += fmt.Sprintf(" until %s", endTime.Format(time.RFC3339))
		data["mute_end"] = endTime.Format(time.RFC3339)
	} else {
		content += " indefinitely"
		data["indefinite"] = true
	}

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data:    data,
	}, nil
}

func (t *MonitorTool) executeUnmuteMonitor(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	// Check confirmation
	if result := checkConfirmation("unmute_monitor", args); result != nil {
		return result, nil
	}

	monitorID := toolargs.GetInt(args, "monitor_id", 0)
	if monitorID == 0 {
		return &types.ToolResult{Success: false, Error: "monitor_id parameter is required"}, nil
	}

	// Build query params for scope if provided
	path := fmt.Sprintf("api/v1/monitor/%d/unmute", monitorID)
	if scope := toolargs.GetString(args, "scope", ""); scope != "" {
		path += "?scope=" + url.QueryEscape(scope)
	}

	resp, err := t.client.Do(ctx, http.MethodPost, path, nil)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to unmute monitor: %v", err)}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("monitor %d not found", monitorID)}, nil
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("Datadog API error: %d - %s", resp.StatusCode, string(respBody)),
		}, nil
	}

	content := fmt.Sprintf("Monitor %d has been unmuted", monitorID)
	data := map[string]interface{}{
		"monitor_id": monitorID,
		"muted":      false,
	}

	if scope := toolargs.GetString(args, "scope", ""); scope != "" {
		content += fmt.Sprintf(" (scope: %s)", scope)
		data["scope"] = scope
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

