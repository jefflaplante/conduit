package core

import (
	"context"
	"fmt"
	"strings"
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
			Description: "Get gateway health, uptime, connection count, and message stats",
			Returns:     "uptime, health, active_connections, total_messages, memory_usage",
		},
		"restart": {
			Description: "Restart the gateway (requires confirmation from user)",
			Returns:     "restart confirmation with timestamp",
		},
		"channels": {
			Description: "List all configured channels with their status",
			Returns:     "per-channel status, enabled state, last activity, message count",
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
			Returns:     "port, AI config, tools config, channels count",
		},
		"update_config": {
			Description:    "Update gateway configuration fields",
			RequiredParams: []string{"config"},
			Returns:        "confirmation with applied config and timestamp",
		},
		"metrics": {
			Description: "Get gateway performance metrics",
			Returns:     "requests/min, avg response time, error rate, total tokens, cost",
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
		Content: "Gateway restart initiated successfully",
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
	channelId := t.getStringArg(args, "channelId", "")
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
	channelId := t.getStringArg(args, "channelId", "")
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
func (t *GatewayTool) formatGatewayStatus(status map[string]interface{}) string {
	var builder strings.Builder
	builder.WriteString("Gateway Status:\n\n")

	if uptime, ok := status["uptime"].(time.Duration); ok {
		builder.WriteString(fmt.Sprintf("Uptime: %s\n", uptime))
	}
	if health, ok := status["health"].(string); ok {
		builder.WriteString(fmt.Sprintf("Health: %s\n", health))
	}
	if activeConnections, ok := status["active_connections"].(int); ok {
		builder.WriteString(fmt.Sprintf("Active Connections: %d\n", activeConnections))
	}
	if totalMessages, ok := status["total_messages"].(int64); ok {
		builder.WriteString(fmt.Sprintf("Total Messages: %d\n", totalMessages))
	}
	if memoryUsage, ok := status["memory_usage"].(string); ok {
		builder.WriteString(fmt.Sprintf("Memory Usage: %s\n", memoryUsage))
	}

	return builder.String()
}

func (t *GatewayTool) formatChannelStatus(channels map[string]interface{}) string {
	if len(channels) == 0 {
		return "No channels configured."
	}

	var builder strings.Builder
	builder.WriteString("Channel Status:\n\n")

	for channelId, info := range channels {
		builder.WriteString(fmt.Sprintf("**%s**\n", channelId))

		if channelInfo, ok := info.(map[string]interface{}); ok {
			if status, ok := channelInfo["status"].(string); ok {
				builder.WriteString(fmt.Sprintf("  Status: %s\n", status))
			}
			if enabled, ok := channelInfo["enabled"].(bool); ok {
				builder.WriteString(fmt.Sprintf("  Enabled: %t\n", enabled))
			}
			if lastActivity, ok := channelInfo["last_activity"].(time.Time); ok {
				builder.WriteString(fmt.Sprintf("  Last Activity: %s\n", lastActivity.Format("2006-01-02 15:04:05")))
			}
			if messageCount, ok := channelInfo["message_count"].(int64); ok {
				builder.WriteString(fmt.Sprintf("  Messages: %d\n", messageCount))
			}
		}
		builder.WriteString("\n")
	}

	return builder.String()
}

func (t *GatewayTool) formatConfiguration(config map[string]interface{}) string {
	var builder strings.Builder
	builder.WriteString("Gateway Configuration:\n\n")

	// Format key configuration sections
	if port, ok := config["port"].(int); ok {
		builder.WriteString(fmt.Sprintf("Port: %d\n", port))
	}

	if ai, ok := config["ai"].(map[string]interface{}); ok {
		builder.WriteString("AI Configuration:\n")
		if defaultProvider, ok := ai["default_provider"].(string); ok {
			builder.WriteString(fmt.Sprintf("  Default Provider: %s\n", defaultProvider))
		}
		if providers, ok := ai["providers"].([]interface{}); ok {
			builder.WriteString(fmt.Sprintf("  Providers: %d configured\n", len(providers)))
		}
	}

	if tools, ok := config["tools"].(map[string]interface{}); ok {
		builder.WriteString("Tools Configuration:\n")
		if enabledTools, ok := tools["enabled_tools"].([]interface{}); ok {
			builder.WriteString(fmt.Sprintf("  Enabled Tools: %d\n", len(enabledTools)))
		}
	}

	if channels, ok := config["channels"].([]interface{}); ok {
		builder.WriteString(fmt.Sprintf("Channels: %d configured\n", len(channels)))
	}

	return builder.String()
}

func (t *GatewayTool) formatMetrics(metrics map[string]interface{}) string {
	var builder strings.Builder
	builder.WriteString("Gateway Metrics:\n\n")

	if requestsPerMinute, ok := metrics["requests_per_minute"].(float64); ok {
		builder.WriteString(fmt.Sprintf("Requests/min: %.1f\n", requestsPerMinute))
	}
	if avgResponseTime, ok := metrics["avg_response_time"].(time.Duration); ok {
		builder.WriteString(fmt.Sprintf("Avg Response Time: %s\n", avgResponseTime))
	}
	if errorRate, ok := metrics["error_rate"].(float64); ok {
		builder.WriteString(fmt.Sprintf("Error Rate: %.2f%%\n", errorRate*100))
	}
	if totalTokens, ok := metrics["total_tokens"].(int64); ok {
		builder.WriteString(fmt.Sprintf("Total Tokens: %d\n", totalTokens))
	}
	if estimatedCost, ok := metrics["estimated_cost"].(float64); ok {
		builder.WriteString(fmt.Sprintf("Estimated Cost: $%.4f\n", estimatedCost))
	}

	return builder.String()
}

func (t *GatewayTool) debugPrompt(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	sessionKey := t.getStringArg(args, "session", "")
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
		content += " All skill tools are callable immediately."
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

func (t *GatewayTool) getStringArg(args map[string]interface{}, key, defaultVal string) string {
	if val, ok := args[key].(string); ok {
		return val
	}
	return defaultVal
}
