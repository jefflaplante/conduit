package core

import (
	"context"
	"fmt"
	"time"

	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

// SessionsSendTool sends messages to other sessions
type SessionsSendTool struct {
	services *types.ToolServices
}

func NewSessionsSendTool(services *types.ToolServices) *SessionsSendTool {
	return &SessionsSendTool{services: services}
}

func (t *SessionsSendTool) Name() string {
	return "SessionsSend"
}

func (t *SessionsSendTool) Description() string {
	return "Send a message to another session by session key or label"
}

func (t *SessionsSendTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"message": map[string]interface{}{
				"type":        "string",
				"description": "Message content to send",
			},
			"sessionKey": map[string]interface{}{
				"type":        "string",
				"description": "Target session key",
			},
			"label": map[string]interface{}{
				"type":        "string",
				"description": "Target session label (alternative to sessionKey)",
			},
			"wake": map[string]interface{}{
				"type":        "boolean",
				"description": "If true, immediately re-activate the target session so it processes the message now (like receiving a new user message). Defaults to false (message is queued for next activation).",
			},
		},
		"required": []string{"message"},
	}
}

func (t *SessionsSendTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	message, ok := args["message"].(string)
	if !ok {
		return &types.ToolResult{
			Success: false,
			Error:   "message parameter is required and must be a string",
		}, nil
	}

	sessionKey := toolargs.GetString(args, "sessionKey", "")
	label := toolargs.GetString(args, "label", "")
	wake := toolargs.GetBool(args, "wake", false)

	if sessionKey == "" && label == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "either sessionKey or label must be provided",
		}, nil
	}

	if t.services.Gateway == nil {
		return &types.ToolResult{
			Success: false,
			Error:   "gateway service not available",
		}, nil
	}

	var err error
	if wake {
		err = t.services.Gateway.SendToSessionWake(ctx, sessionKey, label, message)
	} else {
		err = t.services.Gateway.SendToSession(ctx, sessionKey, label, message)
	}
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to send message: %v", err),
		}, nil
	}

	target := sessionKey
	if target == "" {
		target = label
	}

	wakeNote := ""
	if wake {
		wakeNote = " (session woken for immediate processing)"
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Message sent successfully to %s%s", target, wakeNote),
		Data: map[string]interface{}{
			"target":     target,
			"sessionKey": sessionKey,
			"label":      label,
			"message":    message,
			"wake":       wake,
		},
	}, nil
}

// SelfTest implements types.SelfTester for SessionsSendTool.
func (t *SessionsSendTool) SelfTest(ctx context.Context, opts *types.SelfTestOptions) *types.SelfTestResult {
	start := time.Now()

	if opts == nil {
		opts = types.DefaultSelfTestOptions()
	}

	result := &types.SelfTestResult{
		Status:   types.SelfTestStatusOK,
		TestedAt: time.Now(),
	}

	deps := []types.DependencyStatus{}

	// Check Gateway service - required for sending messages
	gatewayDep := types.DependencyStatus{
		Name:     "GatewayService",
		Required: true,
	}

	if t.services == nil || t.services.Gateway == nil {
		gatewayDep.Available = false
		gatewayDep.Status = "not_configured"
		gatewayDep.Message = "Gateway service not available"
		result.Status = types.SelfTestStatusFailed
		result.Message = "SessionsSend tool is not functional: gateway service unavailable"
		result.Suggestions = []string{
			"Ensure gateway is properly initialized",
			"Check that ToolServices has Gateway set",
		}
	} else {
		gatewayDep.Available = true
		gatewayDep.Status = "connected"
		result.Capabilities = []string{"send_by_key", "send_by_label"}
		result.Status = types.SelfTestStatusOK
		result.Message = "SessionsSend tool is fully functional"
	}
	deps = append(deps, gatewayDep)

	result.Dependencies = deps
	result.TestDuration = time.Since(start)

	if opts.IncludeExamples && result.IsFunctional() {
		result.Examples = []types.ToolExample{
			{
				Name:        "Send message by session key",
				Description: "Send a message to a specific session",
				Args: map[string]interface{}{
					"message":    "Hello from another session",
					"sessionKey": "session-abc123",
				},
				Expected: "Message delivered to the target session",
			},
			{
				Name:        "Send message by label",
				Description: "Send a message to a labeled session",
				Args: map[string]interface{}{
					"message": "Task complete",
					"label":   "worker-1",
				},
				Expected: "Message delivered to session with matching label",
			},
		}
	}

	return result
}
