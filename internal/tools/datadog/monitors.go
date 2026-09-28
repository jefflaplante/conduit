//go:build with_datadog

// Package datadog implements the Datadog monitoring tool.
package datadog

import (
	"context"
	"fmt"
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

// IncludeDataInModelOutput opts this tool into having ToolResult.Data
// rendered for the model: ids and lists needed for follow-up calls live
// only in Data (conduit-31jg.39).
func (t *MonitorTool) IncludeDataInModelOutput() bool { return true }
