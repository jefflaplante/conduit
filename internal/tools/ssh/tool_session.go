//go:build with_ssh

package ssh

import (
	"context"
	"fmt"
	"strings"
	"time"

	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

// sessionStart starts a new persistent session on a host
func (t *SSHTool) sessionStart(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	host := toolargs.GetString(args, "host", "")

	// Validate required parameters
	if host == "" {
		return types.NewErrorResult("missing_parameter", "host parameter is required for session_start action").
			WithParameter("host", nil).
			WithAvailableValues(t.getHostNames()).
			WithSuggestions([]string{"Use action=hosts to list available hosts"}), nil
	}

	// Look up host configuration
	hostConfig := t.config.GetHostByName(host)
	if hostConfig == nil {
		return types.NewErrorResult("invalid_host", fmt.Sprintf("host '%s' not found in configuration", host)).
			WithParameter("host", host).
			WithAvailableValues(t.getHostNames()).
			WithSuggestions([]string{"Use action=hosts to see all configured hosts"}), nil
	}

	// Check if host is enabled
	if !hostConfig.IsHostEnabled() {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("host '%s' is disabled", host),
		}, nil
	}

	// Start the session
	sessionID, err := t.sessionManager.StartSession(host)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to start session: %v", err),
			Data: map[string]interface{}{
				"host":          host,
				"session_count": t.sessionManager.SessionCount(),
				"max_sessions":  t.sessionManager.maxSessions,
			},
		}, nil
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Started persistent session on %s\nSession ID: %s\n\nUse action=session_send with session_id=\"%s\" to send commands.\nUse action=session_close with session_id=\"%s\" to close when done.",
			host, sessionID, sessionID, sessionID),
		Data: map[string]interface{}{
			"session_id":    sessionID,
			"host":          host,
			"session_count": t.sessionManager.SessionCount(),
		},
	}, nil
}

// sessionSend sends a command to an existing persistent session
func (t *SSHTool) sessionSend(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	sessionID := toolargs.GetString(args, "session_id", "")
	command := toolargs.GetString(args, "command", "")
	timeout := toolargs.GetInt(args, "timeout", 30)

	// Validate required parameters
	if sessionID == "" {
		sessions := t.sessionManager.ListSessions()
		sessionIDs := make([]string, 0, len(sessions))
		for _, s := range sessions {
			sessionIDs = append(sessionIDs, s.ID)
		}
		return types.NewErrorResult("missing_parameter", "session_id parameter is required for session_send action").
			WithParameter("session_id", nil).
			WithAvailableValues(sessionIDs).
			WithSuggestions([]string{"Use action=session_list to see active sessions", "Use action=session_start to create a new session"}), nil
	}

	if command == "" {
		return types.NewErrorResult("missing_parameter", "command parameter is required for session_send action").
			WithParameter("command", nil).
			WithExamples([]string{"ls -la", "cd /var/log", "export FOO=bar"}), nil
	}

	// Get session info for security classification
	sessionInfo, err := t.sessionManager.GetSession(sessionID)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("session not found: %s", sessionID),
			Data: map[string]interface{}{
				"session_id": sessionID,
			},
		}, nil
	}

	// Classify the command for security
	hostConfig := t.config.GetHostByName(sessionInfo.Host)
	hostTier := ""
	if hostConfig != nil {
		hostTier = hostConfig.SecurityTier
	}
	classification := t.securityEngine.ValidateCommandForHost(command, hostTier)

	// Block if command is blocked
	if classification.Blocked {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("command blocked: %s", classification.Reason),
			Data: map[string]interface{}{
				"tier":         string(classification.Tier),
				"reason":       classification.Reason,
				"base_cmd":     classification.BaseCommand,
				"session_id":   sessionID,
				"has_subshell": classification.HasSubshell,
			},
		}, nil
	}

	// conduit-w3l7: approval-tier commands are frozen (session + host +
	// command) and run only after a human "YES <code>" reply.
	if classification.RequiresApproval {
		host := sessionInfo.Host
		return t.gate(ctx, t.sessionOperation(sessionID, host, command, timeout, classification),
			func(context.Context) (*types.ToolResult, error) {
				current, err := t.sessionManager.GetSession(sessionID)
				if err != nil {
					return &types.ToolResult{Success: false, Error: fmt.Sprintf("session %s is gone; nothing was run", sessionID)}, nil
				}
				if current.Host != host {
					return &types.ToolResult{Success: false, Error: fmt.Sprintf("session %s now points at %s, not the approved host %s; nothing was run", sessionID, current.Host, host)}, nil
				}
				return t.runSessionCommand(sessionID, current, command, timeout, classification, approvedBy)
			})
	}

	return t.runSessionCommand(sessionID, sessionInfo, command, timeout, classification, "")
}

// runSessionCommand sends an authorized command to a persistent session.
func (t *SSHTool) runSessionCommand(sessionID string, sessionInfo *SessionInfo, command string, timeout int, classification *ClassificationResult, approver string) (*types.ToolResult, error) {
	// Send the command
	execTimeout := time.Duration(timeout) * time.Second
	output, err := t.sessionManager.SendCommand(sessionID, command, execTimeout)
	if err != nil {
		// Log failed execution to audit
		if t.auditLogger != nil && t.config.Audit.LogCommands {
			_ = t.auditLogger.LogExecution(&AuditEntry{
				SessionID:    sessionID,
				Host:         sessionInfo.Host,
				Command:      command,
				SecurityTier: string(classification.Tier),
				Approved:     true, // Command was approved by security check
				ApprovedBy:   approver,
				ExitCode:     -1,
				Duration:     "0s",
				Error:        err.Error(),
			})
		}

		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to send command: %v", err),
			Data: map[string]interface{}{
				"session_id": sessionID,
				"command":    command,
			},
		}, nil
	}

	// Log successful execution to audit
	if t.auditLogger != nil && t.config.Audit.LogCommands {
		_ = t.auditLogger.LogExecution(&AuditEntry{
			SessionID:    sessionID,
			Host:         sessionInfo.Host,
			Command:      command,
			SecurityTier: string(classification.Tier),
			Approved:     true, // Command was approved by security check
			ApprovedBy:   approver,
			ExitCode:     output.ExitCode,
			Duration:     output.Duration.String(),
			Stdout:       output.Stdout,
			Stderr:       output.Stderr,
		})
	}

	// Build response
	var content strings.Builder
	content.WriteString(fmt.Sprintf("Session: %s (host: %s)\n", sessionID, sessionInfo.Host))
	content.WriteString(fmt.Sprintf("Command: %s\n", command))
	content.WriteString(fmt.Sprintf("Exit code: %d\n", output.ExitCode))
	content.WriteString(fmt.Sprintf("Duration: %v\n", output.Duration))

	if output.Stdout != "" {
		content.WriteString("\n--- stdout ---\n")
		content.WriteString(output.Stdout)
	}

	if output.Stderr != "" {
		content.WriteString("\n--- stderr ---\n")
		content.WriteString(output.Stderr)
	}

	return &types.ToolResult{
		Success: output.ExitCode == 0,
		Content: content.String(),
		Data: map[string]interface{}{
			"session_id":    sessionID,
			"host":          sessionInfo.Host,
			"command":       command,
			"exit_code":     output.ExitCode,
			"stdout":        output.Stdout,
			"stderr":        output.Stderr,
			"duration":      output.Duration.String(),
			"tier":          string(classification.Tier),
			"base_cmd":      classification.BaseCommand,
			"command_count": sessionInfo.CommandCount + 1,
		},
	}, nil
}

// sessionClose closes a persistent session
func (t *SSHTool) sessionClose(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	sessionID := toolargs.GetString(args, "session_id", "")

	// Validate required parameters
	if sessionID == "" {
		sessions := t.sessionManager.ListSessions()
		sessionIDs := make([]string, 0, len(sessions))
		for _, s := range sessions {
			sessionIDs = append(sessionIDs, s.ID)
		}
		return types.NewErrorResult("missing_parameter", "session_id parameter is required for session_close action").
			WithParameter("session_id", nil).
			WithAvailableValues(sessionIDs).
			WithSuggestions([]string{"Use action=session_list to see active sessions"}), nil
	}

	// Get session info before closing
	sessionInfo, err := t.sessionManager.GetSession(sessionID)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("session not found: %s", sessionID),
			Data: map[string]interface{}{
				"session_id": sessionID,
			},
		}, nil
	}

	// Close the session
	if err := t.sessionManager.CloseSession(sessionID); err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to close session: %v", err),
			Data: map[string]interface{}{
				"session_id": sessionID,
			},
		}, nil
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Closed session %s (host: %s)\nCommands executed: %d\nSession duration: %v",
			sessionID, sessionInfo.Host, sessionInfo.CommandCount, time.Since(sessionInfo.CreatedAt).Round(time.Second)),
		Data: map[string]interface{}{
			"session_id":    sessionID,
			"host":          sessionInfo.Host,
			"command_count": sessionInfo.CommandCount,
			"session_count": t.sessionManager.SessionCount(),
		},
	}, nil
}

// sessionList lists all active persistent sessions
func (t *SSHTool) sessionList(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	sessions := t.sessionManager.ListSessions()

	if len(sessions) == 0 {
		return &types.ToolResult{
			Success: true,
			Content: "No active persistent sessions.\n\nUse action=session_start with host parameter to create a new session.",
			Data: map[string]interface{}{
				"sessions":     []interface{}{},
				"count":        0,
				"max_sessions": t.sessionManager.maxSessions,
			},
		}, nil
	}

	var content strings.Builder
	content.WriteString(fmt.Sprintf("%d active session(s) (max %d):\n\n", len(sessions), t.sessionManager.maxSessions))

	sessionData := make([]map[string]interface{}, 0, len(sessions))
	for i, s := range sessions {
		idleTime := time.Since(s.LastUsedAt).Round(time.Second)
		sessionAge := time.Since(s.CreatedAt).Round(time.Second)

		content.WriteString(fmt.Sprintf("%d. Session %s\n", i+1, s.ID))
		content.WriteString(fmt.Sprintf("   Host: %s\n", s.Host))
		content.WriteString(fmt.Sprintf("   Commands: %d\n", s.CommandCount))
		content.WriteString(fmt.Sprintf("   Age: %v, Idle: %v\n", sessionAge, idleTime))

		sessionData = append(sessionData, map[string]interface{}{
			"id":            s.ID,
			"host":          s.Host,
			"created_at":    s.CreatedAt.Format(time.RFC3339),
			"last_used_at":  s.LastUsedAt.Format(time.RFC3339),
			"command_count": s.CommandCount,
			"idle_seconds":  int(idleTime.Seconds()),
			"age_seconds":   int(sessionAge.Seconds()),
		})
	}

	return &types.ToolResult{
		Success: true,
		Content: content.String(),
		Data: map[string]interface{}{
			"sessions":     sessionData,
			"count":        len(sessions),
			"max_sessions": t.sessionManager.maxSessions,
		},
	}, nil
}
