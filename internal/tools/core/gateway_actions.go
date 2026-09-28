package core

import (
	"context"
	"fmt"
	"time"

	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

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
