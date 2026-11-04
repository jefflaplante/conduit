package core

import (
	"context"
	"fmt"
	"time"

	"conduit/internal/approval"
	"conduit/internal/tools/approvalgate"
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

// restart asks the owner before restarting the gateway (H14): a restart
// interrupts every live turn, so it runs only after a "YES <code>" reply on
// the originating channel. Non-interactive turns (cron, heartbeat,
// sub-agents, wakes, MCP) and a gateway without an approval channel fail
// closed.
func (t *GatewayTool) restart(ctx context.Context) (*types.ToolResult, error) {
	gw := t.services.Gateway
	op := approvalgate.Operation{
		Kind:  "gateway.restart",
		Title: "Restart the Conduit gateway",
		Fields: []approval.Field{
			{Name: "Effect", Value: "drains for up to 30s, then restarts; live turns resume after the restart"},
		},
		Params:  map[string]string{"action": "restart"},
		Audit:   map[string]string{"requested_via": "Gateway.restart"},
		Summary: "the gateway restart",
		Data:    map[string]interface{}{"action": "restart", "requires_approval": true},
	}
	return approvalgate.Request(ctx, t.approver(), op, func(execCtx context.Context) (*types.ToolResult, error) {
		if err := gw.RestartGateway(execCtx); err != nil {
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
	}), nil
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
