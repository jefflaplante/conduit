package core

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	toolargs "conduit/internal/tools/args"
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
				"type":        "object",
				"description": "Configuration updates for update_config action",
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
			Description:    "Update gateway configuration fields",
			RequiredParams: []string{"config"},
			Returns:        "confirmation with applied config and timestamp",
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

func (t *GatewayTool) getStatus(ctx context.Context) (*types.ToolResult, error) {
	status, err := t.services.Gateway.GetGatewayStatus()
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to get gateway status: %v", err),
		}, nil
	}

	content := t.formatGatewayStatus(status)

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data:    status,
	}, nil
}

func (t *GatewayTool) restart(ctx context.Context) (*types.ToolResult, error) {
	err := t.services.Gateway.RestartGateway(ctx)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to restart gateway: %v", err),
		}, nil
	}

	return &types.ToolResult{
		Success: true,
		Content: "Gateway restart initiated. Drain timeout: 30s. Active sessions preserved in breadcrumb for resumption after restart.",
		Data: map[string]interface{}{
			"action":    "restart",
			"timestamp": time.Now(),
		},
	}, nil
}

func (t *GatewayTool) getChannels(ctx context.Context) (*types.ToolResult, error) {
	channels, err := t.services.Gateway.GetChannelStatus()
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to get channel status: %v", err),
		}, nil
	}

	content := t.formatChannelStatus(channels)

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data:    channels,
	}, nil
}

func (t *GatewayTool) enableChannel(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	channelId := toolargs.GetString(args, "channelId", "")
	if channelId == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "channelId parameter is required for enable_channel action",
		}, nil
	}

	err := t.services.Gateway.EnableChannel(ctx, channelId)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to enable channel %s: %v", channelId, err),
		}, nil
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Channel %s enabled successfully", channelId),
		Data: map[string]interface{}{
			"action":    "enable_channel",
			"channelId": channelId,
			"timestamp": time.Now(),
		},
	}, nil
}

func (t *GatewayTool) disableChannel(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	channelId := toolargs.GetString(args, "channelId", "")
	if channelId == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "channelId parameter is required for disable_channel action",
		}, nil
	}

	err := t.services.Gateway.DisableChannel(ctx, channelId)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to disable channel %s: %v", channelId, err),
		}, nil
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Channel %s disabled successfully", channelId),
		Data: map[string]interface{}{
			"action":    "disable_channel",
			"channelId": channelId,
			"timestamp": time.Now(),
		},
	}, nil
}

func (t *GatewayTool) getConfig(ctx context.Context) (*types.ToolResult, error) {
	config, err := t.services.Gateway.GetConfiguration()
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to get configuration: %v", err),
		}, nil
	}

	content := t.formatConfiguration(config)

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data:    config,
	}, nil
}

func (t *GatewayTool) updateConfig(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	config, ok := args["config"].(map[string]interface{})
	if !ok {
		return &types.ToolResult{
			Success: false,
			Error:   "config parameter is required for update_config action and must be an object",
		}, nil
	}

	err := t.services.Gateway.UpdateConfiguration(ctx, config)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to update configuration: %v", err),
		}, nil
	}

	return &types.ToolResult{
		Success: true,
		Content: "Configuration updated successfully",
		Data: map[string]interface{}{
			"action":    "update_config",
			"config":    config,
			"timestamp": time.Now(),
		},
	}, nil
}

func (t *GatewayTool) getMetrics(ctx context.Context) (*types.ToolResult, error) {
	metrics, err := t.services.Gateway.GetMetrics()
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to get metrics: %v", err),
		}, nil
	}

	content := t.formatMetrics(metrics)

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data:    metrics,
	}, nil
}

func (t *GatewayTool) getVersion(ctx context.Context) (*types.ToolResult, error) {
	version := t.services.Gateway.GetVersion()

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Conduit Gateway Version: %s", version),
		Data: map[string]interface{}{
			"version":   version,
			"timestamp": time.Now(),
		},
	}, nil
}

// Formatting methods
//
// conduit-31jg.71: these used to type-assert shapes the gateway never
// produces (uptime as time.Duration, channel info as a map, config
// "providers" count only, ...), so Content came out as little more than a
// heading and the model had to read the Data JSON. They now render the real
// shapes returned by internal/gateway/status_ops.go.

func (t *GatewayTool) formatGatewayStatus(status map[string]interface{}) string {
	var builder strings.Builder
	builder.WriteString("Gateway Status:\n")
	writeKeyValues(&builder, status, "  ", 0)
	return builder.String()
}

// formatChannelStatus renders map[channelID]channels.ChannelStatus (a
// struct: status, message, details, timestamp) or plain maps of the same
// fields; values are normalized through JSON so either shape works.
func (t *GatewayTool) formatChannelStatus(channels map[string]interface{}) string {
	if len(channels) == 0 {
		return "No channels configured."
	}

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Channel Status (%d):\n", len(channels)))

	for _, channelID := range sortedKeys(channels) {
		info := toGenericMap(channels[channelID])
		line := fmt.Sprintf("- %s", channelID)
		if st := scalarString(info["status"]); st != "" {
			line += ": " + st
		}
		if msg := scalarString(info["message"]); msg != "" {
			line += " (" + msg + ")"
		}
		builder.WriteString(line + "\n")
		if details, ok := info["details"].(map[string]interface{}); ok && len(details) > 0 {
			writeKeyValues(&builder, details, "    ", 1)
		}
	}

	return builder.String()
}

// formatConfiguration renders the redacted config (conduit-31jg.56:
// {"ai": ..., "workspace": ...} after config.Redacted). It must only ever be
// given redacted data: after a summary it appends the full redacted JSON so
// the model does not need Data for details.
func (t *GatewayTool) formatConfiguration(config map[string]interface{}) string {
	var builder strings.Builder
	builder.WriteString("Gateway Configuration (secrets redacted):\n")

	if ai, ok := config["ai"].(map[string]interface{}); ok {
		builder.WriteString("AI:\n")
		if v := scalarString(ai["default_provider"]); v != "" {
			builder.WriteString(fmt.Sprintf("  Default provider: %s\n", v))
		}
		if providers, ok := ai["providers"].([]interface{}); ok {
			builder.WriteString(fmt.Sprintf("  Providers (%d):\n", len(providers)))
			for _, p := range providers {
				pm, ok := p.(map[string]interface{})
				if !ok {
					continue
				}
				line := fmt.Sprintf("    - %s", scalarString(pm["name"]))
				if typ := scalarString(pm["type"]); typ != "" {
					line += " (" + typ + ")"
				}
				if model := scalarString(pm["model"]); model != "" {
					line += " model=" + model
				}
				if fb := scalarString(pm["fallback_model"]); fb != "" {
					line += " fallback=" + fb
				}
				if cw := scalarString(pm["context_window"]); cw != "" && cw != "0" {
					line += " context_window=" + cw
				}
				if auth, ok := pm["auth"].(map[string]interface{}); ok {
					if at := scalarString(auth["type"]); at != "" {
						line += " auth=" + at
					}
				}
				builder.WriteString(line + "\n")
			}
		}
		if v := scalarString(ai["subagent_default_model"]); v != "" {
			builder.WriteString(fmt.Sprintf("  Sub-agent default model: %s\n", v))
		}
		if v := scalarString(ai["max_tokens"]); v != "" && v != "0" {
			builder.WriteString(fmt.Sprintf("  Max output tokens: %s\n", v))
		}
		if aliases, ok := ai["model_aliases"].(map[string]interface{}); ok && len(aliases) > 0 {
			parts := make([]string, 0, len(aliases))
			for _, k := range sortedKeys(aliases) {
				parts = append(parts, k+"="+scalarString(aliases[k]))
			}
			builder.WriteString(fmt.Sprintf("  Model aliases: %s\n", strings.Join(parts, ", ")))
		}
	}

	if ws, ok := config["workspace"].(map[string]interface{}); ok {
		builder.WriteString("Workspace:\n")
		if v := scalarString(ws["context_dir"]); v != "" {
			builder.WriteString(fmt.Sprintf("  Context dir: %s\n", v))
		}
	}

	if raw, err := json.Marshal(config); err == nil {
		builder.WriteString("\nFull configuration (redacted JSON): ")
		builder.Write(raw)
		builder.WriteString("\n")
	}

	return builder.String()
}

func (t *GatewayTool) formatMetrics(metrics map[string]interface{}) string {
	var builder strings.Builder
	builder.WriteString("Gateway Metrics:\n")
	if len(metrics) == 0 {
		builder.WriteString("  (none reported)\n")
		return builder.String()
	}
	writeKeyValues(&builder, metrics, "  ", 0)
	return builder.String()
}

// writeKeyValues writes m as sorted "key: value" lines. Nested maps are
// indented (up to maxNestedDepth); lists of scalars are joined, longer or
// structured lists are summarized by length. conduit-31jg.71
func writeKeyValues(b *strings.Builder, m map[string]interface{}, indent string, depth int) {
	const maxNestedDepth = 2
	for _, k := range sortedKeys(m) {
		v := m[k]
		switch val := normalizeValue(v).(type) {
		case map[string]interface{}:
			if depth >= maxNestedDepth || len(val) == 0 {
				b.WriteString(fmt.Sprintf("%s%s: {%d fields}\n", indent, k, len(val)))
				continue
			}
			b.WriteString(fmt.Sprintf("%s%s:\n", indent, k))
			writeKeyValues(b, val, indent+"  ", depth+1)
		case []interface{}:
			b.WriteString(fmt.Sprintf("%s%s: %s\n", indent, k, summarizeList(val)))
		default:
			b.WriteString(fmt.Sprintf("%s%s: %s\n", indent, k, scalarString(val)))
		}
	}
}

// normalizeValue converts structs and typed maps/slices to the generic
// map/slice shapes via JSON; scalars and time values pass through.
func normalizeValue(v interface{}) interface{} {
	switch v.(type) {
	case nil, string, bool, int, int32, int64, uint, uint32, uint64, float32, float64,
		time.Time, time.Duration, map[string]interface{}, []interface{}:
		return v
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	var out interface{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return string(raw)
	}
	return out
}

// toGenericMap returns v as map[string]interface{} (via JSON for structs),
// or nil.
func toGenericMap(v interface{}) map[string]interface{} {
	m, _ := normalizeValue(v).(map[string]interface{})
	return m
}

func summarizeList(list []interface{}) string {
	const maxItems = 10
	parts := make([]string, 0, len(list))
	for i, item := range list {
		switch item.(type) {
		case map[string]interface{}, []interface{}:
			return fmt.Sprintf("[%d items]", len(list))
		}
		if i == maxItems {
			parts = append(parts, fmt.Sprintf("... (+%d more)", len(list)-maxItems))
			break
		}
		parts = append(parts, scalarString(item))
	}
	return strings.Join(parts, ", ")
}

// scalarString formats a scalar for display; JSON numbers that are whole
// render without a decimal point.
func scalarString(v interface{}) string {
	switch val := v.(type) {
	case nil:
		return ""
	case string:
		return val
	case float64:
		if val == float64(int64(val)) {
			return fmt.Sprintf("%d", int64(val))
		}
		return fmt.Sprintf("%g", val)
	case time.Time:
		if val.IsZero() {
			return ""
		}
		return val.Format(time.RFC3339)
	default:
		return fmt.Sprintf("%v", val)
	}
}

func sortedKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (t *GatewayTool) debugPrompt(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	sessionKey := toolargs.GetString(args, "session", "")
	if sessionKey == "" {
		sessionKey = types.RequestSessionKey(ctx)
	}

	result, err := t.services.Gateway.GetSystemPromptDebug(ctx, sessionKey)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to get prompt debug: %v", err),
		}, nil
	}

	content := t.formatPromptDebug(result)

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data:    result,
	}, nil
}

func (t *GatewayTool) formatPromptDebug(data map[string]interface{}) string {
	var builder strings.Builder
	builder.WriteString("System Prompt Debug:\n\n")

	if totalChars, ok := data["total_chars"].(int); ok {
		builder.WriteString(fmt.Sprintf("Total chars:        %d\n", totalChars))
	}
	if estTokens, ok := data["estimated_tokens"].(int); ok {
		builder.WriteString(fmt.Sprintf("Estimated tokens:   %d\n", estTokens))
	}
	if ctxWindow, ok := data["context_window"].(int); ok {
		builder.WriteString(fmt.Sprintf("Context window:     %d\n", ctxWindow))
	}
	if budgetChars, ok := data["budget_chars"].(int); ok {
		builder.WriteString(fmt.Sprintf("Budget (chars):     %d\n", budgetChars))
	}
	if constrained, ok := data["budget_constrained"].(bool); ok {
		builder.WriteString(fmt.Sprintf("Budget constrained: %v\n", constrained))
	}

	builder.WriteString("\nSections:\n")
	builder.WriteString(fmt.Sprintf("  %-25s %4s %7s %s\n", "Name", "Pri", "Chars", "Status"))
	builder.WriteString(fmt.Sprintf("  %-25s %4s %7s %s\n", "----", "---", "-----", "------"))

	if sections, ok := data["sections"].([]map[string]interface{}); ok {
		for _, s := range sections {
			name, _ := s["name"].(string)
			priority, _ := s["priority"].(int)
			chars, _ := s["chars"].(int)
			included, _ := s["included"].(bool)
			status := "included"
			if !included {
				status = "DROPPED"
			}
			builder.WriteString(fmt.Sprintf("  %-25s P%-3d %7d %s\n", name, priority, chars, status))
		}
	}

	if dropped, ok := data["dropped_sections"].([]string); ok && len(dropped) > 0 {
		builder.WriteString(fmt.Sprintf("\nDropped sections: %s\n", strings.Join(dropped, ", ")))
	}
	// The full prompt_text stays in Data only (it can be tens of KB and is
	// not needed to judge the budget). conduit-31jg.71

	return builder.String()
}

func (t *GatewayTool) reloadSkills(ctx context.Context) (*types.ToolResult, error) {
	count, err := t.services.Gateway.ReloadSkillTools(ctx)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to reload skills: %v", err),
		}, nil
	}

	content := fmt.Sprintf("Skills reloaded successfully. %d skill tools now registered.", count)
	if count == 0 {
		content += " No SKILL.md files found in skill search paths."
	} else {
		content += " System prompt cache cleared — new skills available on next turn."
	}

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data: map[string]interface{}{
			"action":      "reload_skills",
			"skill_tools": count,
			"timestamp":   time.Now(),
		},
	}, nil
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
