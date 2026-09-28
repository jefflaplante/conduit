package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	"conduit/internal/sessions"
	toolargs "conduit/internal/tools/args"
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
	activeMinutes := toolargs.GetInt(args, "activeMinutes", 60)
	limit := toolargs.GetInt(args, "limit", 20)

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

// SelfTest implements types.SelfTester for SessionsListTool.
func (t *SessionsListTool) SelfTest(ctx context.Context, opts *types.SelfTestOptions) *types.SelfTestResult {
	start := time.Now()

	if opts == nil {
		opts = types.DefaultSelfTestOptions()
	}

	result := &types.SelfTestResult{
		Status:   types.SelfTestStatusOK,
		TestedAt: time.Now(),
	}

	deps := []types.DependencyStatus{}

	// Check SessionStore - required for this tool
	storeDep := types.DependencyStatus{
		Name:     "SessionStore",
		Required: true,
	}

	if t.services == nil || t.services.SessionStore == nil {
		storeDep.Available = false
		storeDep.Status = "not_configured"
		storeDep.Message = "Session store not available"
		result.Status = types.SelfTestStatusFailed
		result.Message = "SessionsList tool is not functional: session store unavailable"
		result.Suggestions = []string{
			"Ensure database is configured",
			"Check that SessionStore is initialized in ToolServices",
		}
	} else {
		storeDep.Available = true
		storeDep.Status = "connected"
		result.Capabilities = []string{"list_sessions", "filter_by_activity"}
		result.Status = types.SelfTestStatusOK
		result.Message = "SessionsList tool is fully functional"

		if opts.Verbose {
			// Get session count for details
			sessions, err := t.services.SessionStore.ListActiveSessions(100)
			if err == nil {
				result.Details = map[string]interface{}{
					"active_session_count": len(sessions),
				}
			}
		}
	}
	deps = append(deps, storeDep)

	result.Dependencies = deps
	result.TestDuration = time.Since(start)

	if opts.IncludeExamples && result.IsFunctional() {
		result.Examples = []types.ToolExample{
			{
				Name:        "List recent sessions",
				Description: "List sessions active in the last hour",
				Args: map[string]interface{}{
					"activeMinutes": 60,
					"limit":         10,
				},
				Expected: "Returns list of recently active sessions with metadata",
			},
		}
	}

	return result
}
