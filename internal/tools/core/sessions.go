package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	"conduit/internal/sessions"
	"conduit/internal/tools/types"
)

// SessionsListTool lists active sessions
type SessionsListTool struct {
	services *types.ToolServices
}

func NewSessionsListTool(services *types.ToolServices) *SessionsListTool {
	return &SessionsListTool{services: services}
}

func (t *SessionsListTool) Name() string {
	return "SessionsList"
}

func (t *SessionsListTool) Description() string {
	return "List active sessions with their metadata and recent activity"
}

func (t *SessionsListTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"activeMinutes": map[string]interface{}{
				"type":        "integer",
				"description": "Show sessions active within this many minutes",
				"default":     60,
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum number of sessions to return",
				"default":     20,
			},
		},
	}
}

func (t *SessionsListTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	activeMinutes := t.getIntArg(args, "activeMinutes", 60)
	limit := t.getIntArg(args, "limit", 20)

	if t.services.SessionStore == nil {
		return &types.ToolResult{
			Success: false,
			Error:   "session store not available",
		}, nil
	}

	// Get active sessions
	activeSessions, err := t.services.SessionStore.ListActiveSessions(limit)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to list sessions: %v", err),
		}, nil
	}

	// Filter by activity time
	cutoffTime := time.Now().Add(-time.Duration(activeMinutes) * time.Minute)
	var filteredSessions []sessions.Session
	for _, session := range activeSessions {
		if session.UpdatedAt.After(cutoffTime) {
			filteredSessions = append(filteredSessions, session)
		}
	}

	// Format output
	content := t.formatSessionList(filteredSessions)

	return &types.ToolResult{
		Success: true,
		Content: content,
		Data: map[string]interface{}{
			"sessions":      filteredSessions,
			"total":         len(filteredSessions),
			"activeMinutes": activeMinutes,
			"limit":         limit,
		},
	}, nil
}

func (t *SessionsListTool) formatSessionList(sessionList []sessions.Session) string {
	if len(sessionList) == 0 {
		return "No active sessions found."
	}

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Found %d active sessions:\n\n", len(sessionList)))

	for i, session := range sessionList {
		timeSince := time.Since(session.UpdatedAt)
		builder.WriteString(fmt.Sprintf("%d. **%s**\n", i+1, session.Key))
		builder.WriteString(fmt.Sprintf("   User: %s | Channel: %s\n", session.UserID, session.ChannelID))
		builder.WriteString(fmt.Sprintf("   Messages: %d | Last active: %s ago\n",
			session.MessageCount, t.formatDuration(timeSince)))
		builder.WriteString(fmt.Sprintf("   Created: %s\n",
			session.CreatedAt.Format("2006-01-02 15:04:05")))
		if len(session.Context) > 0 {
			builder.WriteString(fmt.Sprintf("   Context: %v\n", session.Context))
		}
		builder.WriteString("\n")
	}

	return builder.String()
}

func (t *SessionsListTool) formatDuration(d time.Duration) string {
	if d < time.Minute {
		return "< 1 minute"
	}
	if d < time.Hour {
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%d hours", int(d.Hours()))
	}
	return fmt.Sprintf("%d days", int(d.Hours()/24))
}

func (t *SessionsListTool) getIntArg(args map[string]interface{}, key string, defaultVal int) int {
	if val, ok := args[key].(float64); ok {
		return int(val)
	}
	if val, ok := args[key].(int); ok {
		return val
	}
	return defaultVal
}

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

	sessionKey := t.getStringArg(args, "sessionKey", "")
	label := t.getStringArg(args, "label", "")

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

	err := t.services.Gateway.SendToSession(ctx, sessionKey, label, message)
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

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Message sent successfully to %s", target),
		Data: map[string]interface{}{
			"target":     target,
			"sessionKey": sessionKey,
			"label":      label,
			"message":    message,
		},
	}, nil
}

func (t *SessionsSendTool) getStringArg(args map[string]interface{}, key, defaultVal string) string {
	if val, ok := args[key].(string); ok {
		return val
	}
	return defaultVal
}

// SessionsSpawnTool spawns new sub-agent sessions
type SessionsSpawnTool struct {
	services *types.ToolServices
}

func NewSessionsSpawnTool(services *types.ToolServices) *SessionsSpawnTool {
	return &SessionsSpawnTool{services: services}
}

func (t *SessionsSpawnTool) Name() string {
	return "SessionsSpawn"
}

func (t *SessionsSpawnTool) Description() string {
	return "Spawn a new sub-agent session to handle a specific task. By default, results are announced back to the user when complete. Set announce=false for quiet orchestration where you'll check results via SessionStatus."
}

func (t *SessionsSpawnTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task": map[string]interface{}{
				"type":        "string",
				"description": "Task description for the sub-agent",
			},
			"agentId": map[string]interface{}{
				"type":        "string",
				"description": "Specific agent ID to spawn (optional)",
			},
			"model": map[string]interface{}{
				"type":        "string",
				"description": "AI model to use for the sub-agent (optional)",
			},
			"label": map[string]interface{}{
				"type":        "string",
				"description": "Label for the spawned session (optional)",
			},
			"timeoutSeconds": map[string]interface{}{
				"type":        "integer",
				"description": "Session timeout in seconds",
				"default":     300,
			},
			"announce": map[string]interface{}{
				"type":        "boolean",
				"description": "Announce results back to user when complete (default: true). Set to false for quiet orchestration.",
				"default":     true,
			},
		},
		"required": []string{"task"},
	}
}

func (t *SessionsSpawnTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	task, ok := args["task"].(string)
	if !ok {
		return &types.ToolResult{
			Success: false,
			Error:   "task parameter is required and must be a string",
		}, nil
	}

	agentId := t.getStringArg(args, "agentId", "")
	model := t.getStringArg(args, "model", "")
	label := t.getStringArg(args, "label", "")
	timeoutSeconds := t.getIntArg(args, "timeoutSeconds", 300)
	announce := t.getBoolArg(args, "announce", true) // Default: announce results

	if t.services.Gateway == nil {
		return &types.ToolResult{
			Success: false,
			Error:   "gateway service not available",
		}, nil
	}

	// Spawn sub-agent with optional announcement
	sessionKey, err := t.services.Gateway.SpawnSubAgentWithCallback(
		ctx, task, agentId, model, label, timeoutSeconds,
		types.RequestChannelID(ctx), types.RequestUserID(ctx), announce,
	)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to spawn sub-agent: %v", err),
		}, nil
	}

	// Keep response minimal - session key is in Data if model needs it
	resultMsg := "Sub-agent spawned."
	if !announce {
		resultMsg += " Running quietly."
	}

	return &types.ToolResult{
		Success: true,
		Content: resultMsg,
		Data: map[string]interface{}{
			"sessionKey":     sessionKey,
			"task":           task,
			"agentId":        agentId,
			"model":          model,
			"label":          label,
			"timeoutSeconds": timeoutSeconds,
			"announce":       announce,
		},
	}, nil
}

func (t *SessionsSpawnTool) getStringArg(args map[string]interface{}, key, defaultVal string) string {
	if val, ok := args[key].(string); ok {
		return val
	}
	return defaultVal
}

func (t *SessionsSpawnTool) getIntArg(args map[string]interface{}, key string, defaultVal int) int {
	if val, ok := args[key].(float64); ok {
		return int(val)
	}
	if val, ok := args[key].(int); ok {
		return val
	}
	return defaultVal
}

func (t *SessionsSpawnTool) getBoolArg(args map[string]interface{}, key string, defaultVal bool) bool {
	if val, ok := args[key].(bool); ok {
		return val
	}
	return defaultVal
}

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
	sessionKey := t.getStringArg(args, "sessionKey", "")

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
		builder.WriteString(fmt.Sprintf("Created: %s\n", createdAt.Format("2006-01-02 15:04:05")))
	}
	if updatedAt, ok := status["updated_at"].(time.Time); ok {
		builder.WriteString(fmt.Sprintf("Last Updated: %s\n", updatedAt.Format("2006-01-02 15:04:05")))
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

	// Context info
	if context, ok := status["context"].(map[string]interface{}); ok && len(context) > 0 {
		builder.WriteString("\nContext:\n")
		for key, value := range context {
			builder.WriteString(fmt.Sprintf("  %s: %v\n", key, value))
		}
	}

	return builder.String()
}

func (t *SessionStatusTool) getStringArg(args map[string]interface{}, key, defaultVal string) string {
	if val, ok := args[key].(string); ok {
		return val
	}
	return defaultVal
}
