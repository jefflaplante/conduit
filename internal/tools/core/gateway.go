package core

import (
	"context"
	"fmt"
	"time"

	"conduit/internal/tools/types"
)

// GatewayTool provides gateway management operations
type GatewayTool struct {
	services *types.ToolServices
}

func NewGatewayTool(services *types.ToolServices) *GatewayTool {
	return &GatewayTool{services: services}
}

func (t *GatewayTool) Name() string {
	return "Gateway"
}

func (t *GatewayTool) Description() string {
	return "Manage gateway operations including status, channels, configuration, metrics, and skill hot-reload"
}

func (t *GatewayTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type": "string",
				"enum": []string{
					"status", "restart", "channels", "enable_channel", "disable_channel",
					"config", "update_config", "metrics", "version", "debug_prompt",
					"reload_skills",
				},
				"description": "Gateway operation to perform",
			},
			"channelId": map[string]interface{}{
				"type":        "string",
				"description": "Channel ID for channel operations (required for enable_channel/disable_channel)",
			},
			"session": map[string]interface{}{
				"type":        "string",
				"description": "Session key for debug_prompt action (uses current session if omitted)",
			},
			"config": map[string]interface{}{
				"type": "object",
				"description": "Changes for update_config, merge-patch style: nested objects merge, other values replace, null deletes. " +
					"Top-level keys may be dot-paths; array elements are addressed by their \"name\" (providers, channels). " +
					"Example: {\"ai.providers.z-ai.timeout_seconds\": 600}. Secrets only as ${ENV_VAR} references. " +
					"Every update needs the owner's approval in chat.",
			},
		},
		"required": []string{"action"},
	}
}

func (t *GatewayTool) GetActionDocs() map[string]types.ActionDoc {
	return map[string]types.ActionDoc{
		"status": {
			Description: "Get gateway run state and version",
			Returns:     "status, version (plus any other fields the gateway reports)",
		},
		"restart": {
			Description: "Restart the gateway (requires confirmation from user)",
			Returns:     "restart confirmation with timestamp",
		},
		"channels": {
			Description: "List all configured channels with their status",
			Returns:     "per-channel status, status message, message count and other adapter details",
		},
		"enable_channel": {
			Description:    "Enable a disabled channel",
			RequiredParams: []string{"channelId"},
			Returns:        "confirmation with channelId and timestamp",
		},
		"disable_channel": {
			Description:    "Disable an active channel",
			RequiredParams: []string{"channelId"},
			Returns:        "confirmation with channelId and timestamp",
		},
		"config": {
			Description: "Get current gateway configuration",
			Returns:     "AI providers/models and workspace settings, secrets redacted",
		},
		"update_config": {
			Description: "Change config.json and hot-reload what can be applied live (provider settings of existing providers, " +
				"pricing overrides, call_log, subagent_default_model). Other keys are saved and need a restart. " +
				"Always requires the owner's approval (\"YES <code>\" in chat); fails closed in non-interactive turns.",
			RequiredParams: []string{"config"},
			Returns:        "pending approval; once approved, the keys applied live, the keys saved for restart, and their redacted values",
		},
		"metrics": {
			Description: "Get gateway performance metrics",
			Returns:     "whatever metrics the gateway reports, as key: value lines",
		},
		"version": {
			Description: "Get Conduit version string",
			Returns:     "version string",
		},
		"debug_prompt": {
			Description:    "Inspect system prompt sections, sizes, and budget allocation",
			OptionalParams: []string{"session"},
			Returns:        "section list with priority, char count, inclusion status",
		},
		"reload_skills": {
			Description: "Hot-reload skills from the filesystem without restarting the gateway. " +
				"Re-scans all skill search paths for SKILL.md files, registers new skill tools, " +
				"and removes tools for deleted skills. Use after writing or editing a SKILL.md file " +
				"(e.g. via the Write tool) so the new skill becomes callable immediately. " +
				"No parameters needed.",
			Returns: "count of skill tools registered after reload",
		},
	}
}

func (t *GatewayTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	action, ok := args["action"].(string)
	if !ok {
		return &types.ToolResult{
			Success: false,
			Error:   "action parameter is required and must be a string",
		}, nil
	}

	// Check if gateway service is available
	if t.services.Gateway == nil {
		return &types.ToolResult{
			Success: false,
			Error:   "gateway service not available",
		}, nil
	}

	switch action {
	case "status":
		return t.getStatus(ctx)
	case "restart":
		return t.restart(ctx)
	case "channels":
		return t.getChannels(ctx)
	case "enable_channel":
		return t.enableChannel(ctx, args)
	case "disable_channel":
		return t.disableChannel(ctx, args)
	case "config":
		return t.getConfig(ctx)
	case "update_config":
		return t.updateConfig(ctx, args)
	case "metrics":
		return t.getMetrics(ctx)
	case "version":
		return t.getVersion(ctx)
	case "debug_prompt":
		return t.debugPrompt(ctx, args)
	case "reload_skills":
		return t.reloadSkills(ctx)
	default:
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("unknown action: %s", action),
		}, nil
	}
}

// SelfTest implements types.SelfTester for GatewayTool.
func (t *GatewayTool) SelfTest(ctx context.Context, opts *types.SelfTestOptions) *types.SelfTestResult {
	start := time.Now()

	if opts == nil {
		opts = types.DefaultSelfTestOptions()
	}

	result := &types.SelfTestResult{
		Status:       types.SelfTestStatusOK,
		Capabilities: []string{},
		TestedAt:     time.Now(),
	}

	deps := []types.DependencyStatus{}

	// Check Gateway service - required for all operations
	gatewayDep := types.DependencyStatus{
		Name:     "GatewayService",
		Required: true,
	}

	if t.services == nil || t.services.Gateway == nil {
		gatewayDep.Available = false
		gatewayDep.Status = "not_configured"
		gatewayDep.Message = "Gateway service not available in ToolServices"
		result.Status = types.SelfTestStatusFailed
		result.Message = "Gateway tool is not functional: gateway service unavailable"
		result.Suggestions = []string{
			"Ensure gateway is properly initialized",
			"Check that ToolServices has Gateway set",
		}
	} else {
		gatewayDep.Available = true
		gatewayDep.Status = "connected"
		result.Capabilities = []string{
			"status", "restart", "channels", "enable_channel", "disable_channel",
			"config", "update_config", "metrics", "version", "debug_prompt", "reload_skills",
		}
		result.Status = types.SelfTestStatusOK
		result.Message = "Gateway tool is fully functional"

		// Get verbose details if requested
		if opts.Verbose {
			status, err := t.services.Gateway.GetGatewayStatus()
			if err == nil {
				result.Details = map[string]interface{}{
					"gateway_status": status,
					"version":        t.services.Gateway.GetVersion(),
				}
			}
		}
	}
	deps = append(deps, gatewayDep)

	result.Dependencies = deps
	result.TestDuration = time.Since(start)

	if opts.IncludeExamples && result.IsFunctional() {
		result.Examples = []types.ToolExample{
			{
				Name:        "Get gateway status",
				Description: "Check gateway health, uptime, and connection stats",
				Args: map[string]interface{}{
					"action": "status",
				},
				Expected: "Returns uptime, health status, active connections, and message counts",
			},
			{
				Name:        "List channels",
				Description: "Get status of all configured channels",
				Args: map[string]interface{}{
					"action": "channels",
				},
				Expected: "Returns per-channel status including enabled state and message counts",
			},
			{
				Name:        "Get metrics",
				Description: "View gateway performance metrics",
				Args: map[string]interface{}{
					"action": "metrics",
				},
				Expected: "Returns requests/min, response times, error rate, and token usage",
			},
			{
				Name:        "Reload skills",
				Description: "Hot-reload skill tools from SKILL.md files",
				Args: map[string]interface{}{
					"action": "reload_skills",
				},
				Expected: "Reloads all skills and reports count of registered skill tools",
			},
		}
	}

	return result
}

// GetUsageExamples implements types.UsageExampleProvider for GatewayTool.
func (t *GatewayTool) GetUsageExamples() []types.ToolExample {
	return []types.ToolExample{
		{
			Name:        "Get gateway status",
			Description: "Check gateway health, uptime, and connection stats",
			Args: map[string]interface{}{
				"action": "status",
			},
			Expected: "Returns uptime, health status, active connections, and message counts",
		},
		{
			Name:        "List channels",
			Description: "Get status of all configured channels",
			Args: map[string]interface{}{
				"action": "channels",
			},
			Expected: "Returns per-channel status including enabled state and message counts",
		},
		{
			Name:        "Get metrics",
			Description: "View gateway performance metrics",
			Args: map[string]interface{}{
				"action": "metrics",
			},
			Expected: "Returns requests/min, response times, error rate, and token usage",
		},
		{
			Name:        "Debug system prompt",
			Description: "Inspect system prompt sections and budget allocation",
			Args: map[string]interface{}{
				"action": "debug_prompt",
			},
			Expected: "Returns section list with priority, char count, and inclusion status",
		},
		{
			Name:        "Reload skills",
			Description: "Hot-reload skill tools from SKILL.md files",
			Args: map[string]interface{}{
				"action": "reload_skills",
			},
			Expected: "Reloads all skills and reports count of registered skill tools",
		},
	}
}

// IncludeDataInModelOutput: conduit-31jg.39 opted this tool in because its
// formatters produced near-empty Content. conduit-31jg.71 fixed the
// formatters so Content carries every field (the config action appends the
// full redacted JSON), which made Data a duplicate — and for debug_prompt it
// re-sent the whole system prompt text. Opted out to save tokens.
func (t *GatewayTool) IncludeDataInModelOutput() bool { return false }
