package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

// SessionStatusTool provides session usage and status information
type SessionStatusTool struct {
	services *types.ToolServices
}

func NewSessionStatusTool(services *types.ToolServices) *SessionStatusTool {
	return &SessionStatusTool{services: services}
}

func (t *SessionStatusTool) Name() string {
	return "SessionStatus"
}

func (t *SessionStatusTool) Description() string {
	return "Get detailed status information about the current session or a specific session"
}

func (t *SessionStatusTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"sessionKey": map[string]interface{}{
				"type":        "string",
				"description": "Session key to get status for (optional, defaults to current)",
			},
		},
	}
}

func (t *SessionStatusTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	sessionKey := toolargs.GetString(args, "sessionKey", "")

	if t.services.Gateway == nil {
		return &types.ToolResult{
			Success: false,
			Error:   "gateway service not available",
		}, nil
	}

	status, err := t.services.Gateway.GetSessionStatus(ctx, sessionKey)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to get session status: %v", err),
		}, nil
	}

	content := t.formatSessionStatus(status)

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data:    status,
	}, nil
}

func (t *SessionStatusTool) formatSessionStatus(status map[string]interface{}) string {
	loc := time.UTC
	if t.services != nil && t.services.ConfigMgr != nil {
		loc = t.services.ConfigMgr.GetLocation()
	}

	var builder strings.Builder
	builder.WriteString("Session Status:\n\n")

	// Basic session info
	if sessionKey, ok := status["session_key"].(string); ok {
		builder.WriteString(fmt.Sprintf("Session Key: %s\n", sessionKey))
	}
	if userID, ok := status["user_id"].(string); ok {
		builder.WriteString(fmt.Sprintf("User ID: %s\n", userID))
	}
	if channelID, ok := status["channel_id"].(string); ok {
		builder.WriteString(fmt.Sprintf("Channel ID: %s\n", channelID))
	}
	if messageCount, ok := status["message_count"].(int); ok {
		builder.WriteString(fmt.Sprintf("Message Count: %d\n", messageCount))
	}

	// Timing info
	if createdAt, ok := status["created_at"].(time.Time); ok {
		builder.WriteString(fmt.Sprintf("Created: %s\n", createdAt.In(loc).Format("2006-01-02 15:04:05 MST")))
	}
	if updatedAt, ok := status["updated_at"].(time.Time); ok {
		builder.WriteString(fmt.Sprintf("Last Updated: %s\n", updatedAt.In(loc).Format("2006-01-02 15:04:05 MST")))
	}

	// Model and usage info
	if model, ok := status["model"].(string); ok {
		builder.WriteString(fmt.Sprintf("Model: %s\n", model))
	}
	if tokensUsed, ok := status["tokens_used"].(int); ok {
		builder.WriteString(fmt.Sprintf("Tokens Used: %d\n", tokensUsed))
	}
	if cost, ok := status["estimated_cost"].(float64); ok {
		builder.WriteString(fmt.Sprintf("Estimated Cost: $%.4f\n", cost))
	}

	// Context budget gauge (conduit-2v0t) — show context-window consumption up front
	// because it's the most actionable status signal for the agent.
	if budget, ok := status["context_budget"].(map[string]interface{}); ok && budget != nil {
		builder.WriteString("\nContext Budget:\n")
		if model, ok := budget["model"].(string); ok && model != "" {
			builder.WriteString(fmt.Sprintf("  Model:            %s\n", model))
		}
		if window, ok := budget["model_window"].(int); ok {
			builder.WriteString(fmt.Sprintf("  Window:           %d tokens", window))
			if isDefault, _ := budget["model_window_is_default"].(bool); isDefault {
				builder.WriteString(" (default — model not in lookup table)")
			}
			builder.WriteString("\n")
		}
		if prompt, ok := budget["prompt_tokens"].(int); ok {
			builder.WriteString(fmt.Sprintf("  Prompt tokens:    %d\n", prompt))
		}
		if completion, ok := budget["completion_tokens"].(int); ok {
			builder.WriteString(fmt.Sprintf("  Last completion:  %d\n", completion))
		}
		if pct, ok := budget["percent_used"].(float64); ok {
			builder.WriteString(fmt.Sprintf("  Percent used:     %.2f%%\n", pct))
		}
		if remaining, ok := budget["remaining_tokens"].(int); ok {
			builder.WriteString(fmt.Sprintf("  Remaining:        %d tokens\n", remaining))
		}
	}

	// Fuel gauge (conduit-zojv) — rate-limit headroom and rolling token consumption.
	if gauge, ok := status["fuel_gauge"].(map[string]interface{}); ok && gauge != nil {
		builder.WriteString("\nFuel Gauge:\n")
		if tokenUsage, ok := gauge["token_usage"].(map[string]interface{}); ok {
			if hour, ok := tokenUsage["hour"].(map[string]interface{}); ok {
				builder.WriteString(fmt.Sprintf("  Tokens (1h):  %v req, %v in, %v out\n",
					hour["requests"], hour["input_tokens"], hour["output_tokens"]))
			}
			if day, ok := tokenUsage["day"].(map[string]interface{}); ok {
				builder.WriteString(fmt.Sprintf("  Tokens (24h): %v req, %v in, %v out\n",
					day["requests"], day["input_tokens"], day["output_tokens"]))
			}
		}
		if rl, ok := gauge["rate_limit"].(map[string]interface{}); ok {
			enabled, _ := rl["enabled"].(bool)
			if enabled {
				if anon, ok := rl["anonymous"].(map[string]interface{}); ok {
					builder.WriteString(fmt.Sprintf("  Rate limit (anon):  limit=%v, active_buckets=%v\n",
						anon["limit"], anon["active_buckets"]))
				}
				if auth, ok := rl["authenticated"].(map[string]interface{}); ok {
					builder.WriteString(fmt.Sprintf("  Rate limit (auth):  limit=%v, active_buckets=%v\n",
						auth["limit"], auth["active_buckets"]))
				}
			} else {
				builder.WriteString("  Rate limit: disabled\n")
			}
		}
		builder.WriteString(formatProviderSlots(gauge)) // conduit-38cz
	}
	builder.WriteString(formatSubAgents(status)) // conduit-38cz

	// Context info
	if context, ok := status["context"].(map[string]interface{}); ok && len(context) > 0 {
		builder.WriteString("\nContext:\n")
		for key, value := range context {
			builder.WriteString(fmt.Sprintf("  %s: %v\n", key, value))
		}
	}

	return builder.String()
}

// SelfTest implements types.SelfTester for SessionStatusTool.
func (t *SessionStatusTool) SelfTest(ctx context.Context, opts *types.SelfTestOptions) *types.SelfTestResult {
	start := time.Now()

	if opts == nil {
		opts = types.DefaultSelfTestOptions()
	}

	result := &types.SelfTestResult{
		Status:   types.SelfTestStatusOK,
		TestedAt: time.Now(),
	}

	deps := []types.DependencyStatus{}

	// Check Gateway service - required for getting session status
	gatewayDep := types.DependencyStatus{
		Name:     "GatewayService",
		Required: true,
	}

	if t.services == nil || t.services.Gateway == nil {
		gatewayDep.Available = false
		gatewayDep.Status = "not_configured"
		gatewayDep.Message = "Gateway service not available"
		result.Status = types.SelfTestStatusFailed
		result.Message = "SessionStatus tool is not functional: gateway service unavailable"
		result.Suggestions = []string{
			"Ensure gateway is properly initialized",
			"Check that ToolServices has Gateway set",
		}
	} else {
		gatewayDep.Available = true
		gatewayDep.Status = "connected"
		result.Capabilities = []string{"current_session_status", "specific_session_status"}
		result.Status = types.SelfTestStatusOK
		result.Message = "SessionStatus tool is fully functional"
	}
	deps = append(deps, gatewayDep)

	result.Dependencies = deps
	result.TestDuration = time.Since(start)

	if opts.IncludeExamples && result.IsFunctional() {
		result.Examples = []types.ToolExample{
			{
				Name:        "Get current session status",
				Description: "Get status of the current session",
				Args:        map[string]interface{}{},
				Expected:    "Returns session key, user, channel, message count, and token usage",
			},
			{
				Name:        "Get specific session status",
				Description: "Get status of a specific session by key",
				Args: map[string]interface{}{
					"sessionKey": "session-abc123",
				},
				Expected: "Returns detailed status for the specified session",
			},
		}
	}

	return result
}
