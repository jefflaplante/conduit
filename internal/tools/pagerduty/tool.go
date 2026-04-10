//go:build with_pagerduty

// Package pagerduty implements the PagerDuty incident management tool.
package pagerduty

import (
	"context"
	"fmt"
	"time"

	"conduit/internal/config"
	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

// SecurityTier represents the risk level of a PagerDuty operation.
type SecurityTier string

const (
	TierRead      SecurityTier = "read"      // list, get - auto-approved
	TierModify    SecurityTier = "modify"    // ack, snooze, add_note - auto-approved
	TierDangerous SecurityTier = "dangerous" // resolve, trigger - requires confirmation
)

// actionTier maps each action to its security tier.
var actionTier = map[string]SecurityTier{
	"list_incidents": TierRead,
	"get_incident":   TierRead,
	"acknowledge":    TierModify,
	"snooze":         TierModify,
	"add_note":       TierModify,
	"resolve":        TierDangerous,
	"trigger":        TierDangerous,
}

// PagerDutyTool provides PagerDuty incident management via the tool interface.
type PagerDutyTool struct {
	services *types.ToolServices
	config   *config.PagerDutyConfig
	client   *Client
}

// NewPagerDutyTool creates a new PagerDuty tool with the given services and configuration.
func NewPagerDutyTool(services *types.ToolServices, cfg *config.PagerDutyConfig) (*PagerDutyTool, error) {
	if cfg == nil {
		return nil, fmt.Errorf("pagerduty config is required")
	}

	client := NewClient(*cfg)

	return &PagerDutyTool{
		services: services,
		config:   cfg,
		client:   client,
	}, nil
}

// Name returns the tool name.
func (t *PagerDutyTool) Name() string { return "PagerDuty" }

// Description returns a human-readable description of the tool's capabilities.
func (t *PagerDutyTool) Description() string {
	return `PagerDuty incident management tool. Supported actions:
- list_incidents: List incidents (filtered by status, urgency, service)
- get_incident: Get detailed information about a specific incident
- acknowledge: Acknowledge an incident (stops escalation)
- resolve: Resolve an incident (requires confirmation)
- snooze: Snooze an incident for a duration
- add_note: Add a note to an incident
- trigger: Trigger a new incident (requires confirmation)

Security Tiers:
- Read (auto-approved): list_incidents, get_incident
- Modify (auto-approved): acknowledge, snooze, add_note
- Dangerous (requires confirmation): resolve, trigger`
}

// Parameters returns the JSON schema for the tool's parameters.
func (t *PagerDutyTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"description": "The PagerDuty operation to perform",
				"enum":        []string{"list_incidents", "get_incident", "acknowledge", "resolve", "snooze", "add_note", "trigger"},
			},
			"incident_id": map[string]interface{}{
				"type":        "string",
				"description": "Incident ID for get/acknowledge/resolve/snooze/add_note actions",
			},
			"status": map[string]interface{}{
				"type":        "string",
				"description": "Filter incidents by status (for list_incidents)",
				"enum":        []string{"triggered", "acknowledged", "resolved"},
			},
			"urgency": map[string]interface{}{
				"type":        "string",
				"description": "Filter incidents by urgency (for list_incidents)",
				"enum":        []string{"high", "low"},
			},
			"service_id": map[string]interface{}{
				"type":        "string",
				"description": "Service ID to filter incidents or for triggering new incident",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum number of incidents to return (default 25, max 100)",
			},
			"note": map[string]interface{}{
				"type":        "string",
				"description": "Note content for add_note action",
			},
			"snooze_duration": map[string]interface{}{
				"type":        "integer",
				"description": "Duration in seconds to snooze the incident (for snooze action)",
			},
			"title": map[string]interface{}{
				"type":        "string",
				"description": "Title for new incident (for trigger action)",
			},
			"details": map[string]interface{}{
				"type":        "string",
				"description": "Details/body for new incident (for trigger action)",
			},
			"escalation_policy_id": map[string]interface{}{
				"type":        "string",
				"description": "Escalation policy ID for trigger action (uses default if not specified)",
			},
			"confirmed": map[string]interface{}{
				"type":        "boolean",
				"description": "Confirmation flag for dangerous actions (resolve, trigger). Set to true to proceed.",
			},
		},
		"required": []string{"action"},
	}
}

// GetActionDocs returns documentation for each action.
func (t *PagerDutyTool) GetActionDocs() map[string]types.ActionDoc {
	return map[string]types.ActionDoc{
		"list_incidents": {
			Description:    "List incidents, optionally filtered by status, urgency, or service",
			OptionalParams: []string{"status", "urgency", "service_id", "limit"},
			Returns:        "array of incidents with id, title, status, urgency, service, created_at",
		},
		"get_incident": {
			Description:    "Get detailed information about a specific incident",
			RequiredParams: []string{"incident_id"},
			Returns:        "incident details including assignments, notes, timeline",
		},
		"acknowledge": {
			Description:    "Acknowledge an incident (stops escalation timer)",
			RequiredParams: []string{"incident_id"},
			Returns:        "updated incident status",
		},
		"resolve": {
			Description:    "Resolve an incident (dangerous - requires confirmed=true)",
			RequiredParams: []string{"incident_id", "confirmed"},
			Returns:        "updated incident status",
		},
		"snooze": {
			Description:    "Snooze an incident for a specified duration",
			RequiredParams: []string{"incident_id", "snooze_duration"},
			Returns:        "snooze confirmation with wake time",
		},
		"add_note": {
			Description:    "Add a note to an incident timeline",
			RequiredParams: []string{"incident_id", "note"},
			Returns:        "note creation confirmation",
		},
		"trigger": {
			Description:    "Trigger a new incident (dangerous - requires confirmed=true)",
			RequiredParams: []string{"title", "confirmed"},
			OptionalParams: []string{"service_id", "escalation_policy_id", "details"},
			Returns:        "new incident details with id and status",
		},
	}
}

// Execute dispatches the requested action and returns a tool result.
func (t *PagerDutyTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	action := toolargs.GetString(args, "action", "")
	if action == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "action parameter is required",
		}, nil
	}

	// Check security tier for dangerous actions
	tier, ok := actionTier[action]
	if !ok {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("unknown action: %s", action),
		}, nil
	}

	if tier == TierDangerous {
		confirmed := toolargs.GetBool(args, "confirmed", false)
		if !confirmed {
			return &types.ToolResult{
				Success: false,
				Error:   fmt.Sprintf("action %q is dangerous and requires confirmed=true to proceed", action),
			}, nil
		}
	}

	switch action {
	case "list_incidents":
		return t.listIncidents(ctx, args)
	case "get_incident":
		return t.getIncident(ctx, args)
	case "acknowledge":
		return t.acknowledgeIncident(ctx, args)
	case "resolve":
		return t.resolveIncident(ctx, args)
	case "snooze":
		return t.snoozeIncident(ctx, args)
	case "add_note":
		return t.addNote(ctx, args)
	case "trigger":
		return t.triggerIncident(ctx, args)
	default:
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("unknown action: %s", action),
		}, nil
	}
}

// ClassifyAction returns the security tier for an action.
func ClassifyAction(action string) SecurityTier {
	if tier, ok := actionTier[action]; ok {
		return tier
	}
	return TierDangerous // Unknown actions are dangerous by default
}

// SelfTest implements types.SelfTester for PagerDutyTool.
func (t *PagerDutyTool) SelfTest(ctx context.Context, opts *types.SelfTestOptions) *types.SelfTestResult {
	start := time.Now()

	if opts == nil {
		opts = types.DefaultSelfTestOptions()
	}

	result := &types.SelfTestResult{
		Status:   types.SelfTestStatusOK,
		TestedAt: time.Now(),
	}

	deps := []types.DependencyStatus{}

	// Check PagerDuty API configuration
	apiDep := types.DependencyStatus{
		Name:     "PagerDutyAPI",
		Required: true,
	}

	if t.config == nil || t.config.APIToken == "" {
		apiDep.Available = false
		apiDep.Status = "not_configured"
		apiDep.Message = "PagerDuty API token not configured"
		result.Status = types.SelfTestStatusFailed
		result.Message = "PagerDuty API is not configured"
		result.Suggestions = []string{
			"Set PAGERDUTY_API_TOKEN environment variable",
			"Configure api_token in pagerduty config section",
		}
		deps = append(deps, apiDep)
		result.Dependencies = deps
		result.TestDuration = time.Since(start)
		return result
	}

	apiDep.Available = true
	apiDep.Status = "configured"

	// Test API connectivity using the abilities endpoint (lightweight, read-only)
	validationErr := t.validateAPIConnection(ctx)

	if validationErr != nil {
		apiDep.Status = "connection_failed"
		apiDep.Message = validationErr.Error()
		result.Status = types.SelfTestStatusDegraded
		result.Message = "PagerDuty API configured but not connected"
		result.Capabilities = []string{}
		result.UnavailableCapabilities = []string{
			"list_incidents", "get_incident",
			"acknowledge", "resolve", "snooze",
			"add_note", "trigger",
		}
		result.Suggestions = []string{
			"Check network connectivity to PagerDuty API",
			"Verify API token is valid",
			"Ensure token has not been revoked",
		}
	} else {
		apiDep.Status = "connected"
		apiDep.Message = "API token validated successfully"
		result.Status = types.SelfTestStatusOK
		result.Message = "PagerDuty API connected and ready"
		result.Capabilities = []string{
			"list_incidents", "get_incident",
			"acknowledge", "resolve", "snooze",
			"add_note", "trigger",
		}

		if opts.Verbose {
			result.Details = map[string]interface{}{
				"base_url":           t.config.EffectiveBaseURL(),
				"default_service_id": t.config.DefaultServiceID,
			}
		}
	}

	deps = append(deps, apiDep)
	result.Dependencies = deps
	result.TestDuration = time.Since(start)

	if opts.IncludeExamples && result.IsFunctional() {
		result.Examples = []types.ToolExample{
			{
				Name:        "List active incidents",
				Description: "List all triggered and acknowledged incidents",
				Args: map[string]interface{}{
					"action": "list_incidents",
					"status": "triggered",
				},
				Expected: "Returns list of active incidents with IDs and titles",
			},
			{
				Name:        "Acknowledge incident",
				Description: "Acknowledge an incident to stop escalation",
				Args: map[string]interface{}{
					"action":      "acknowledge",
					"incident_id": "P1234567",
				},
				Expected: "Incident status updated to acknowledged",
			},
			{
				Name:        "Add incident note",
				Description: "Add investigation notes to an incident",
				Args: map[string]interface{}{
					"action":      "add_note",
					"incident_id": "P1234567",
					"note":        "Investigating root cause...",
				},
				Expected: "Note added to incident timeline",
			},
		}
	}

	return result
}

// validateAPIConnection tests connectivity to the PagerDuty API.
func (t *PagerDutyTool) validateAPIConnection(ctx context.Context) error {
	// Use the /abilities endpoint - it's lightweight and always accessible with valid token
	resp, err := t.client.Do(ctx, "GET", "/abilities", nil)
	if err != nil {
		return fmt.Errorf("failed to connect: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		return nil
	}

	if resp.StatusCode == 401 {
		return fmt.Errorf("invalid API token (HTTP 401)")
	}

	if resp.StatusCode == 403 {
		return fmt.Errorf("forbidden - check API token permissions (HTTP 403)")
	}

	return fmt.Errorf("unexpected response (HTTP %d)", resp.StatusCode)
}

