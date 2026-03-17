// Package ssh implements the SSH remote execution tool with security controls.
package ssh

import (
	"context"
	"fmt"
	"strings"
	"time"

	"conduit/internal/config"
	"conduit/internal/tools/types"
)

// ExecutionResult represents the result of an SSH command execution
type ExecutionResult struct {
	Host       string        `json:"host"`
	Command    string        `json:"command"`
	ExitCode   int           `json:"exit_code"`
	Stdout     string        `json:"stdout"`
	Stderr     string        `json:"stderr"`
	Duration   time.Duration `json:"duration"`
	Error      string        `json:"error,omitempty"`
	TimedOut   bool          `json:"timed_out,omitempty"`
}

// PoolStatus represents the status of the SSH connection pool
type PoolStatus struct {
	TotalConnections  int            `json:"total_connections"`
	ActiveConnections int            `json:"active_connections"`
	IdleConnections   int            `json:"idle_connections"`
	HostStats         map[string]int `json:"host_stats"`
}

// Client defines the interface for SSH client operations.
// This will be implemented by the actual SSH client/pool.
type Client interface {
	// Execute runs a command on the specified host
	Execute(ctx context.Context, host, command string, timeout time.Duration) (*ExecutionResult, error)

	// GetPoolStatus returns the current connection pool status
	GetPoolStatus() *PoolStatus

	// Close closes all connections in the pool
	Close() error
}

// SSHTool provides remote command execution via SSH with security controls
type SSHTool struct {
	services       *types.ToolServices
	securityEngine *SecurityEngine
	client         Client
	config         *config.RemoteSSHConfig
	sessionManager *SessionManager
	tunnelManager  *TunnelManager
}

// NewSSHTool creates a new SSH tool with the given services and configuration
func NewSSHTool(services *types.ToolServices, cfg *config.RemoteSSHConfig) (*SSHTool, error) {
	if cfg == nil {
		defaultCfg := config.DefaultRemoteSSHConfig()
		cfg = &defaultCfg
	}

	// Create security engine
	securityEngine, err := NewSecurityEngine(cfg.Security)
	if err != nil {
		return nil, fmt.Errorf("failed to create security engine: %w", err)
	}

	// Create session manager
	sessionManager := NewSessionManager(cfg.Sessions, cfg.Hosts, cfg.Defaults, cfg.Pool)

	return &SSHTool{
		services:       services,
		securityEngine: securityEngine,
		config:         cfg,
		sessionManager: sessionManager,
		tunnelManager:  NewTunnelManager(),
		// client will be set via SetClient when the real implementation is available
	}, nil
}

// SetClient sets the SSH client implementation
func (t *SSHTool) SetClient(client Client) {
	t.client = client
}

// Close cleans up the SSH tool resources
func (t *SSHTool) Close() {
	if t.sessionManager != nil {
		t.sessionManager.Close()
	}
	if t.tunnelManager != nil {
		t.tunnelManager.CloseAll()
	}
}

// GetSessionManager returns the session manager (for testing)
func (t *SSHTool) GetSessionManager() *SessionManager {
	return t.sessionManager
}

// GetTunnelManager returns the tunnel manager (for testing)
func (t *SSHTool) GetTunnelManager() *TunnelManager {
	return t.tunnelManager
}

// Name returns the tool name
func (t *SSHTool) Name() string {
	return "Ssh"
}

// Description returns the tool description with usage examples
func (t *SSHTool) Description() string {
	return `Execute commands on remote hosts via SSH with security controls.

Actions:
- exec: Execute a command on a remote host (one-shot)
- hosts: List configured SSH hosts
- status: Show connection pool, session, and tunnel status
- session_start: Start a persistent session on a host
- session_send: Send a command to an existing session
- session_close: Close a persistent session
- session_list: List active persistent sessions
- tunnel_create: Create a local port forwarding tunnel
- tunnel_close: Close an active tunnel
- tunnel_list: List all active tunnels

Security:
Commands are classified into security tiers (read, modify, dangerous, blocked).
Blocked commands are rejected. Dangerous commands may require approval.
Tunnels only bind to 127.0.0.1 (localhost) for security.

Persistent Sessions:
Sessions maintain shell state between commands (environment variables, working directory).
Max 5 concurrent sessions. Sessions auto-close after 10 minutes of idle time.

Examples:
- One-shot exec: action=exec, host="web-prod-1", command="ls -la /var/log"
- Start session: action=session_start, host="web-prod-1"
- Send to session: action=session_send, session_id="abc123", command="cd /var/log"
- Close session: action=session_close, session_id="abc123"
- List sessions: action=session_list
- Create tunnel: action=tunnel_create, host="db-server", local_port=3307, remote_host="localhost", remote_port=3306
- Close tunnel: action=tunnel_close, tunnel_id="abc-123"
- List tunnels: action=tunnel_list`
}

// Parameters returns the JSON schema for tool parameters
func (t *SSHTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"exec", "hosts", "status", "session_start", "session_send", "session_close", "session_list", "tunnel_create", "tunnel_close", "tunnel_list"},
				"description": "SSH operation to perform",
			},
			"host": map[string]interface{}{
				"type":        "string",
				"description": "Target host name (required for exec, session_start, and tunnel_create actions)",
			},
			"command": map[string]interface{}{
				"type":        "string",
				"description": "Command to execute (required for exec and session_send actions)",
			},
			"session_id": map[string]interface{}{
				"type":        "string",
				"description": "Session ID (required for session_send and session_close actions)",
			},
			"timeout": map[string]interface{}{
				"type":        "integer",
				"description": "Command execution timeout in seconds (default: 30)",
				"default":     30,
			},
			"local_port": map[string]interface{}{
				"type":        "integer",
				"description": "Local port to bind for tunnel (0 for auto-assign, must be >= 1024)",
			},
			"remote_host": map[string]interface{}{
				"type":        "string",
				"description": "Remote host to forward to (required for tunnel_create, typically 'localhost')",
			},
			"remote_port": map[string]interface{}{
				"type":        "integer",
				"description": "Remote port to forward to (required for tunnel_create)",
			},
			"tunnel_id": map[string]interface{}{
				"type":        "string",
				"description": "Tunnel ID to close (required for tunnel_close action)",
			},
		},
		"required": []string{"action"},
	}
}

// Execute runs the SSH tool with the given arguments
func (t *SSHTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	// Check if SSH is enabled
	if !t.config.Enabled {
		return &types.ToolResult{
			Success: false,
			Error:   "SSH remote execution is disabled in configuration",
		}, nil
	}

	action := t.getStringArg(args, "action", "")

	switch action {
	case "exec":
		return t.executeCommand(ctx, args)
	case "hosts":
		return t.listHosts(ctx, args)
	case "status":
		return t.getStatus(ctx, args)
	case "session_start":
		return t.sessionStart(ctx, args)
	case "session_send":
		return t.sessionSend(ctx, args)
	case "session_close":
		return t.sessionClose(ctx, args)
	case "session_list":
		return t.sessionList(ctx, args)
	case "tunnel_create":
		return t.createTunnel(ctx, args)
	case "tunnel_close":
		return t.closeTunnel(ctx, args)
	case "tunnel_list":
		return t.listTunnels(ctx, args)
	default:
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("unknown action: %s (valid actions: exec, hosts, status, session_start, session_send, session_close, session_list, tunnel_create, tunnel_close, tunnel_list)", action),
		}, nil
	}
}

// executeCommand executes a command on a remote host
func (t *SSHTool) executeCommand(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	host := t.getStringArg(args, "host", "")
	command := t.getStringArg(args, "command", "")
	timeout := t.getIntArg(args, "timeout", 30)

	// Validate required parameters
	if host == "" {
		return types.NewErrorResult("missing_parameter", "host parameter is required for exec action").
			WithParameter("host", nil).
			WithSuggestions([]string{"Use action=hosts to list available hosts"}), nil
	}

	if command == "" {
		return types.NewErrorResult("missing_parameter", "command parameter is required for exec action").
			WithParameter("command", nil).
			WithExamples([]string{"ls -la", "df -h", "ps aux"}), nil
	}

	// Look up host configuration
	hostConfig := t.config.GetHostByName(host)
	if hostConfig == nil {
		availableHosts := t.getHostNames()
		return types.NewErrorResult("invalid_host", fmt.Sprintf("host '%s' not found in configuration", host)).
			WithParameter("host", host).
			WithAvailableValues(availableHosts).
			WithSuggestions([]string{"Use action=hosts to see all configured hosts"}), nil
	}

	// Check if host is enabled
	if !hostConfig.IsHostEnabled() {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("host '%s' is disabled", host),
		}, nil
	}

	// Classify the command for security
	classification := t.securityEngine.ValidateCommandForHost(command, hostConfig.SecurityTier)

	// Block if command is blocked
	if classification.Blocked {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("command blocked: %s", classification.Reason),
			Data: map[string]interface{}{
				"tier":        string(classification.Tier),
				"reason":      classification.Reason,
				"base_cmd":    classification.BaseCommand,
				"warnings":    classification.Warnings,
				"has_subshell": classification.HasSubshell,
			},
		}, nil
	}

	// Check if approval is required (for dangerous tier)
	if classification.RequiresApproval {
		// For now, we'll include approval requirement in the response
		// The approval workflow will be implemented in a later phase
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("command requires approval (tier: %s)", classification.Tier),
			Data: map[string]interface{}{
				"tier":              string(classification.Tier),
				"reason":            classification.Reason,
				"base_cmd":          classification.BaseCommand,
				"requires_approval": true,
				"warnings":          classification.Warnings,
			},
		}, nil
	}

	// Check if client is available
	if t.client == nil {
		// Return classification info when client is not available (for testing/development)
		return &types.ToolResult{
			Success: true,
			Content: fmt.Sprintf("Command classified (client not connected):\nHost: %s\nCommand: %s\nTier: %s\nBase command: %s",
				host, command, classification.Tier, classification.BaseCommand),
			Data: map[string]interface{}{
				"host":         host,
				"command":      command,
				"tier":         string(classification.Tier),
				"base_cmd":     classification.BaseCommand,
				"reason":       classification.Reason,
				"warnings":     classification.Warnings,
				"client_ready": false,
			},
		}, nil
	}

	// Execute the command
	execTimeout := time.Duration(timeout) * time.Second
	result, err := t.client.Execute(ctx, host, command, execTimeout)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("execution failed: %v", err),
			Data: map[string]interface{}{
				"host":    host,
				"command": command,
				"tier":    string(classification.Tier),
			},
		}, nil
	}

	// Build response
	var content strings.Builder
	content.WriteString(fmt.Sprintf("Host: %s\n", result.Host))
	content.WriteString(fmt.Sprintf("Command: %s\n", result.Command))
	content.WriteString(fmt.Sprintf("Exit code: %d\n", result.ExitCode))
	content.WriteString(fmt.Sprintf("Duration: %v\n", result.Duration))

	if result.Stdout != "" {
		content.WriteString("\n--- stdout ---\n")
		content.WriteString(result.Stdout)
	}

	if result.Stderr != "" {
		content.WriteString("\n--- stderr ---\n")
		content.WriteString(result.Stderr)
	}

	return &types.ToolResult{
		Success: result.ExitCode == 0,
		Content: content.String(),
		Data: map[string]interface{}{
			"host":       result.Host,
			"command":    result.Command,
			"exit_code":  result.ExitCode,
			"stdout":     result.Stdout,
			"stderr":     result.Stderr,
			"duration":   result.Duration.String(),
			"timed_out":  result.TimedOut,
			"tier":       string(classification.Tier),
			"base_cmd":   classification.BaseCommand,
		},
	}, nil
}

// listHosts returns the list of configured SSH hosts
func (t *SSHTool) listHosts(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	hosts := t.config.GetEnabledHosts()

	if len(hosts) == 0 {
		return &types.ToolResult{
			Success: true,
			Content: "No SSH hosts configured.",
			Data:    map[string]interface{}{"count": 0},
		}, nil
	}

	var content strings.Builder
	content.WriteString(fmt.Sprintf("%d configured SSH host(s):\n\n", len(hosts)))

	hostData := make([]map[string]interface{}, 0, len(hosts))

	for i, host := range hosts {
		status := ""
		if !host.IsHostEnabled() {
			status = " (disabled)"
		}

		tierInfo := ""
		if host.SecurityTier != "" {
			tierInfo = fmt.Sprintf(" [tier: %s]", host.SecurityTier)
		}

		content.WriteString(fmt.Sprintf("%d. %s%s%s\n", i+1, host.Name, tierInfo, status))
		content.WriteString(fmt.Sprintf("   %s@%s:%d\n",
			host.GetUser(t.config.Defaults),
			host.Hostname,
			host.GetPort(t.config.Defaults)))

		if len(host.Groups) > 0 {
			content.WriteString(fmt.Sprintf("   Groups: %s\n", strings.Join(host.Groups, ", ")))
		}

		hostData = append(hostData, map[string]interface{}{
			"name":          host.Name,
			"hostname":      host.Hostname,
			"port":          host.GetPort(t.config.Defaults),
			"user":          host.GetUser(t.config.Defaults),
			"groups":        host.Groups,
			"security_tier": host.SecurityTier,
			"enabled":       host.IsHostEnabled(),
			"tags":          host.Tags,
		})
	}

	// Also list host groups if any
	if len(t.config.HostGroups) > 0 {
		content.WriteString(fmt.Sprintf("\n%d host group(s):\n", len(t.config.HostGroups)))
		for _, group := range t.config.HostGroups {
			tierInfo := ""
			if group.SecurityTier != "" {
				tierInfo = fmt.Sprintf(" [tier: %s]", group.SecurityTier)
			}
			content.WriteString(fmt.Sprintf("  - %s%s", group.Name, tierInfo))
			if group.Description != "" {
				content.WriteString(fmt.Sprintf(": %s", group.Description))
			}
			content.WriteString("\n")
		}
	}

	return &types.ToolResult{
		Success: true,
		Content: content.String(),
		Data: map[string]interface{}{
			"hosts":       hostData,
			"count":       len(hosts),
			"host_groups": t.config.HostGroups,
		},
	}, nil
}

// getStatus returns the connection pool status
func (t *SSHTool) getStatus(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	var content strings.Builder
	content.WriteString("SSH Connection Pool Status:\n\n")

	// Security configuration summary
	content.WriteString("Security Configuration:\n")
	content.WriteString(fmt.Sprintf("  Default tier: %s\n", t.config.Security.DefaultTier))
	content.WriteString(fmt.Sprintf("  Require approval: %v\n", t.config.Security.RequireApproval))
	content.WriteString(fmt.Sprintf("  Allow subshells: %v\n", t.config.Security.AllowSubshells))
	content.WriteString(fmt.Sprintf("  Allow pipes: %v\n", t.config.Security.AllowPipes))

	data := map[string]interface{}{
		"enabled": t.config.Enabled,
		"security": map[string]interface{}{
			"default_tier":     t.config.Security.DefaultTier,
			"require_approval": t.config.Security.RequireApproval,
			"allow_subshells":  t.config.Security.AllowSubshells,
			"allow_pipes":      t.config.Security.AllowPipes,
		},
	}

	if t.client != nil {
		poolStatus := t.client.GetPoolStatus()
		content.WriteString("\nConnection Pool:\n")
		content.WriteString(fmt.Sprintf("  Total connections: %d\n", poolStatus.TotalConnections))
		content.WriteString(fmt.Sprintf("  Active: %d\n", poolStatus.ActiveConnections))
		content.WriteString(fmt.Sprintf("  Idle: %d\n", poolStatus.IdleConnections))

		data["pool"] = map[string]interface{}{
			"total_connections":  poolStatus.TotalConnections,
			"active_connections": poolStatus.ActiveConnections,
			"idle_connections":   poolStatus.IdleConnections,
			"host_stats":         poolStatus.HostStats,
		}
		data["client_ready"] = true
	} else {
		content.WriteString("\nConnection Pool: Not initialized\n")
		data["client_ready"] = false
	}

	// Host summary
	enabledHosts := t.config.GetEnabledHosts()
	content.WriteString(fmt.Sprintf("\nConfigured Hosts: %d enabled\n", len(enabledHosts)))
	data["host_count"] = len(enabledHosts)

	// Session summary
	if t.sessionManager != nil {
		sessionCount := t.sessionManager.SessionCount()
		content.WriteString(fmt.Sprintf("\nPersistent Sessions: %d/%d active\n", sessionCount, t.sessionManager.maxSessions))
		data["sessions"] = map[string]interface{}{
			"active":       sessionCount,
			"max_sessions": t.sessionManager.maxSessions,
		}
	}

	// Tunnel summary
	if t.tunnelManager != nil {
		tunnelCount := len(t.tunnelManager.ListTunnels())
		content.WriteString(fmt.Sprintf("\nActive Tunnels: %d\n", tunnelCount))
		data["tunnels"] = map[string]interface{}{
			"active": tunnelCount,
		}
	}

	return &types.ToolResult{
		Success: true,
		Content: content.String(),
		Data:    data,
	}, nil
}

// sessionStart starts a new persistent session on a host
func (t *SSHTool) sessionStart(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	host := t.getStringArg(args, "host", "")

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
	sessionID := t.getStringArg(args, "session_id", "")
	command := t.getStringArg(args, "command", "")
	timeout := t.getIntArg(args, "timeout", 30)

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

	// Check if approval is required
	if classification.RequiresApproval {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("command requires approval (tier: %s)", classification.Tier),
			Data: map[string]interface{}{
				"tier":              string(classification.Tier),
				"reason":            classification.Reason,
				"base_cmd":          classification.BaseCommand,
				"requires_approval": true,
				"session_id":        sessionID,
			},
		}, nil
	}

	// Send the command
	execTimeout := time.Duration(timeout) * time.Second
	output, err := t.sessionManager.SendCommand(sessionID, command, execTimeout)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to send command: %v", err),
			Data: map[string]interface{}{
				"session_id": sessionID,
				"command":    command,
			},
		}, nil
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
	sessionID := t.getStringArg(args, "session_id", "")

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
			"session_id":     sessionID,
			"host":           sessionInfo.Host,
			"command_count":  sessionInfo.CommandCount,
			"session_count":  t.sessionManager.SessionCount(),
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

// createTunnel creates a new local port forwarding tunnel
func (t *SSHTool) createTunnel(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	host := t.getStringArg(args, "host", "")
	localPort := t.getIntArg(args, "local_port", 0)
	remoteHost := t.getStringArg(args, "remote_host", "")
	remotePort := t.getIntArg(args, "remote_port", 0)

	// Validate required parameters
	if host == "" {
		return types.NewErrorResult("missing_parameter", "host parameter is required for tunnel_create action").
			WithParameter("host", nil).
			WithSuggestions([]string{"Use action=hosts to list available hosts"}), nil
	}

	if remoteHost == "" {
		return types.NewErrorResult("missing_parameter", "remote_host parameter is required for tunnel_create action").
			WithParameter("remote_host", nil).
			WithExamples([]string{"localhost", "127.0.0.1", "db.internal"}), nil
	}

	if remotePort == 0 {
		return types.NewErrorResult("missing_parameter", "remote_port parameter is required for tunnel_create action").
			WithParameter("remote_port", nil).
			WithExamples([]string{"3306", "5432", "6379", "27017"}), nil
	}

	// Look up host configuration
	hostConfig := t.config.GetHostByName(host)
	if hostConfig == nil {
		availableHosts := t.getHostNames()
		return types.NewErrorResult("invalid_host", fmt.Sprintf("host '%s' not found in configuration", host)).
			WithParameter("host", host).
			WithAvailableValues(availableHosts).
			WithSuggestions([]string{"Use action=hosts to see all configured hosts"}), nil
	}

	// Check if host is enabled
	if !hostConfig.IsHostEnabled() {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("host '%s' is disabled", host),
		}, nil
	}

	// Check if client is available
	if t.client == nil {
		return &types.ToolResult{
			Success: false,
			Error:   "SSH client not connected - tunnels require an active SSH connection",
			Data: map[string]interface{}{
				"host":         host,
				"local_port":   localPort,
				"remote_host":  remoteHost,
				"remote_port":  remotePort,
				"client_ready": false,
			},
		}, nil
	}

	// Get or create SSH client for this host
	// For now, we need to get the underlying SSHClient from the pool
	// This is a simplified implementation - in production, you'd want proper client management
	sshClient, err := t.getSSHClientForHost(host)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to get SSH connection for host '%s': %v", host, err),
		}, nil
	}

	// Create the tunnel
	tunnel, err := t.tunnelManager.CreateTunnel(sshClient, localPort, remoteHost, remotePort)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to create tunnel: %v", err),
			Data: map[string]interface{}{
				"host":        host,
				"local_port":  localPort,
				"remote_host": remoteHost,
				"remote_port": remotePort,
			},
		}, nil
	}

	// Build response
	var content strings.Builder
	content.WriteString("Tunnel created successfully\n\n")
	content.WriteString(fmt.Sprintf("Tunnel ID: %s\n", tunnel.ID))
	content.WriteString(fmt.Sprintf("Local endpoint: 127.0.0.1:%d\n", tunnel.LocalPort))
	content.WriteString(fmt.Sprintf("Remote endpoint: %s:%d (via %s)\n", remoteHost, remotePort, host))
	content.WriteString(fmt.Sprintf("\nConnect to 127.0.0.1:%d to reach %s:%d through the SSH tunnel.", tunnel.LocalPort, remoteHost, remotePort))

	return &types.ToolResult{
		Success: true,
		Content: content.String(),
		Data: map[string]interface{}{
			"tunnel_id":   tunnel.ID,
			"local_port":  tunnel.LocalPort,
			"remote_host": remoteHost,
			"remote_port": remotePort,
			"ssh_host":    host,
			"local_addr":  fmt.Sprintf("127.0.0.1:%d", tunnel.LocalPort),
		},
	}, nil
}

// closeTunnel closes an active tunnel by ID
func (t *SSHTool) closeTunnel(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	tunnelID := t.getStringArg(args, "tunnel_id", "")

	if tunnelID == "" {
		// List available tunnels to help the user
		tunnels := t.tunnelManager.ListTunnels()
		tunnelIDs := make([]string, 0, len(tunnels))
		for _, tunnel := range tunnels {
			tunnelIDs = append(tunnelIDs, tunnel.TunnelID)
		}

		return types.NewErrorResult("missing_parameter", "tunnel_id parameter is required for tunnel_close action").
			WithParameter("tunnel_id", nil).
			WithAvailableValues(tunnelIDs).
			WithSuggestions([]string{"Use action=tunnel_list to see all active tunnels"}), nil
	}

	err := t.tunnelManager.CloseTunnel(tunnelID)
	if err != nil {
		// List available tunnels in the error
		tunnels := t.tunnelManager.ListTunnels()
		tunnelIDs := make([]string, 0, len(tunnels))
		for _, tunnel := range tunnels {
			tunnelIDs = append(tunnelIDs, tunnel.TunnelID)
		}

		return types.NewErrorResult("tunnel_not_found", fmt.Sprintf("tunnel '%s' not found", tunnelID)).
			WithParameter("tunnel_id", tunnelID).
			WithAvailableValues(tunnelIDs).
			WithSuggestions([]string{"Use action=tunnel_list to see all active tunnels"}), nil
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("Tunnel %s closed successfully", tunnelID),
		Data: map[string]interface{}{
			"tunnel_id": tunnelID,
			"closed":    true,
		},
	}, nil
}

// listTunnels lists all active tunnels
func (t *SSHTool) listTunnels(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	tunnels := t.tunnelManager.ListTunnels()

	if len(tunnels) == 0 {
		return &types.ToolResult{
			Success: true,
			Content: "No active tunnels.",
			Data: map[string]interface{}{
				"count":   0,
				"tunnels": []interface{}{},
			},
		}, nil
	}

	var content strings.Builder
	content.WriteString(fmt.Sprintf("%d active tunnel(s):\n\n", len(tunnels)))

	tunnelData := make([]map[string]interface{}, 0, len(tunnels))

	for i, tunnel := range tunnels {
		content.WriteString(fmt.Sprintf("%d. %s\n", i+1, tunnel.TunnelID))
		content.WriteString(fmt.Sprintf("   Local: 127.0.0.1:%d → Remote: %s:%d (via %s)\n",
			tunnel.LocalPort, tunnel.RemoteHost, tunnel.RemotePort, tunnel.SSHHost))
		content.WriteString(fmt.Sprintf("   Active connections: %d, Bytes in/out: %d/%d\n",
			tunnel.ActiveConnections, tunnel.BytesIn, tunnel.BytesOut))
		content.WriteString(fmt.Sprintf("   Created: %s\n", tunnel.CreatedAt.Format("2006-01-02 15:04:05")))

		tunnelData = append(tunnelData, map[string]interface{}{
			"tunnel_id":          tunnel.TunnelID,
			"local_port":         tunnel.LocalPort,
			"remote_host":        tunnel.RemoteHost,
			"remote_port":        tunnel.RemotePort,
			"ssh_host":           tunnel.SSHHost,
			"active_connections": tunnel.ActiveConnections,
			"bytes_in":           tunnel.BytesIn,
			"bytes_out":          tunnel.BytesOut,
			"created_at":         tunnel.CreatedAt,
		})
	}

	return &types.ToolResult{
		Success: true,
		Content: content.String(),
		Data: map[string]interface{}{
			"count":   len(tunnels),
			"tunnels": tunnelData,
		},
	}, nil
}

// getSSHClientForHost gets or creates an SSHClient for a host
// This is a helper that bridges between the Client interface and the actual SSHClient needed for tunnels
func (t *SSHTool) getSSHClientForHost(hostName string) (*SSHClient, error) {
	// If the client is a PoolClient, we can get the underlying SSHClient
	if poolClient, ok := t.client.(*PoolClient); ok {
		return poolClient.GetClient(hostName)
	}

	// For other client types, we need to check if they can provide an SSHClient
	if clientProvider, ok := t.client.(SSHClientProvider); ok {
		return clientProvider.GetSSHClient(hostName)
	}

	return nil, fmt.Errorf("client does not support tunneling - requires SSHClient access")
}

// getHostNames returns the names of all configured hosts
func (t *SSHTool) getHostNames() []string {
	hosts := t.config.Hosts
	names := make([]string, 0, len(hosts))
	for _, host := range hosts {
		names = append(names, host.Name)
	}
	return names
}

// Helper methods for argument parsing
func (t *SSHTool) getStringArg(args map[string]interface{}, key, defaultVal string) string {
	if val, ok := args[key].(string); ok {
		return val
	}
	return defaultVal
}

func (t *SSHTool) getIntArg(args map[string]interface{}, key string, defaultVal int) int {
	if val, ok := args[key].(float64); ok {
		return int(val)
	}
	if val, ok := args[key].(int); ok {
		return val
	}
	return defaultVal
}

// SSHClientProvider is an interface for clients that can provide underlying SSHClient instances
type SSHClientProvider interface {
	GetSSHClient(hostName string) (*SSHClient, error)
}

// PoolClient wraps a Pool to implement the Client interface
type PoolClient struct {
	pool *Pool
}

// NewPoolClient creates a new PoolClient wrapping a Pool
func NewPoolClient(pool *Pool) *PoolClient {
	return &PoolClient{pool: pool}
}

// Execute runs a command on the specified host
func (p *PoolClient) Execute(ctx context.Context, host, command string, timeout time.Duration) (*ExecutionResult, error) {
	result, err := p.pool.ExecWithTimeout(host, command, timeout)
	if err != nil {
		return nil, err
	}

	return &ExecutionResult{
		Host:     host,
		Command:  command,
		ExitCode: result.ExitCode,
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		Duration: timeout, // Approximate - actual duration tracked in ExecResult
	}, nil
}

// GetPoolStatus returns the current connection pool status
func (p *PoolClient) GetPoolStatus() *PoolStatus {
	stats := p.pool.Stats()

	hostStats := make(map[string]int)
	for host, hs := range stats.HostStats {
		hostStats[host] = hs.Total
	}

	active := 0
	idle := 0
	for _, hs := range stats.HostStats {
		active += hs.InUse
		idle += hs.Available
	}

	return &PoolStatus{
		TotalConnections:  stats.TotalConnections,
		ActiveConnections: active,
		IdleConnections:   idle,
		HostStats:         hostStats,
	}
}

// Close closes all connections in the pool
func (p *PoolClient) Close() error {
	p.pool.Close()
	return nil
}

// GetClient gets an SSHClient from the pool for the specified host
func (p *PoolClient) GetClient(hostName string) (*SSHClient, error) {
	return p.pool.Get(hostName)
}

// ValidateParameters implements types.ParameterValidator for rich error messages
func (t *SSHTool) ValidateParameters(ctx context.Context, args map[string]interface{}) *types.ValidationResult {
	result := &types.ValidationResult{Valid: true}

	action := t.getStringArg(args, "action", "")

	// Validate action
	validActions := []string{"exec", "hosts", "status", "session_start", "session_send", "session_close", "session_list", "tunnel_create", "tunnel_close", "tunnel_list"}
	actionValid := false
	for _, a := range validActions {
		if action == a {
			actionValid = true
			break
		}
	}

	if !actionValid {
		result.Valid = false
		result.Errors = append(result.Errors, types.ValidationError{
			Parameter:       "action",
			Message:         fmt.Sprintf("invalid action: %s", action),
			ProvidedValue:   action,
			AvailableValues: validActions,
			ErrorType:       "invalid_value",
		})
		return result
	}

	// Validate exec-specific parameters
	if action == "exec" || action == "session_start" {
		host := t.getStringArg(args, "host", "")

		if host == "" {
			result.Valid = false
			result.Errors = append(result.Errors, types.ValidationError{
				Parameter:       "host",
				Message:         fmt.Sprintf("host is required for %s action", action),
				AvailableValues: t.getHostNames(),
				DiscoveryHint:   "Use action=hosts to list available hosts",
				ErrorType:       "missing",
			})
		} else {
			// Validate host exists
			hostConfig := t.config.GetHostByName(host)
			if hostConfig == nil {
				result.Valid = false
				result.Errors = append(result.Errors, types.ValidationError{
					Parameter:       "host",
					Message:         fmt.Sprintf("host '%s' not found", host),
					ProvidedValue:   host,
					AvailableValues: t.getHostNames(),
					DiscoveryHint:   "Use action=hosts to list available hosts",
					ErrorType:       "invalid_value",
				})
			}
		}
	}

	// Validate command for exec and session_send
	if action == "exec" || action == "session_send" {
		command := t.getStringArg(args, "command", "")
		if command == "" {
			result.Valid = false
			result.Errors = append(result.Errors, types.ValidationError{
				Parameter:   "command",
				Message:     fmt.Sprintf("command is required for %s action", action),
				Examples:    []interface{}{"ls -la", "df -h", "ps aux", "uptime"},
				ErrorType:   "missing",
			})
		}
	}

	// Validate session_id for session_send and session_close
	if action == "session_send" || action == "session_close" {
		sessionID := t.getStringArg(args, "session_id", "")
		if sessionID == "" {
			sessions := t.sessionManager.ListSessions()
			sessionIDs := make([]string, 0, len(sessions))
			for _, s := range sessions {
				sessionIDs = append(sessionIDs, s.ID)
			}
			result.Valid = false
			result.Errors = append(result.Errors, types.ValidationError{
				Parameter:       "session_id",
				Message:         fmt.Sprintf("session_id is required for %s action", action),
				AvailableValues: sessionIDs,
				DiscoveryHint:   "Use action=session_list to see active sessions",
				ErrorType:       "missing",
			})
		} else {
			// Validate session exists
			if !t.sessionManager.HasSession(sessionID) {
				sessions := t.sessionManager.ListSessions()
				sessionIDs := make([]string, 0, len(sessions))
				for _, s := range sessions {
					sessionIDs = append(sessionIDs, s.ID)
				}
				result.Valid = false
				result.Errors = append(result.Errors, types.ValidationError{
					Parameter:       "session_id",
					Message:         fmt.Sprintf("session '%s' not found", sessionID),
					ProvidedValue:   sessionID,
					AvailableValues: sessionIDs,
					DiscoveryHint:   "Use action=session_list to see active sessions",
					ErrorType:       "invalid_value",
				})
			}
		}
	}

	// Validate tunnel_create parameters
	if action == "tunnel_create" {
		host := t.getStringArg(args, "host", "")
		remoteHost := t.getStringArg(args, "remote_host", "")
		remotePort := t.getIntArg(args, "remote_port", 0)

		if host == "" {
			result.Valid = false
			result.Errors = append(result.Errors, types.ValidationError{
				Parameter:       "host",
				Message:         "host is required for tunnel_create action",
				AvailableValues: t.getHostNames(),
				DiscoveryHint:   "Use action=hosts to list available hosts",
				ErrorType:       "missing",
			})
		} else {
			hostConfig := t.config.GetHostByName(host)
			if hostConfig == nil {
				result.Valid = false
				result.Errors = append(result.Errors, types.ValidationError{
					Parameter:       "host",
					Message:         fmt.Sprintf("host '%s' not found", host),
					ProvidedValue:   host,
					AvailableValues: t.getHostNames(),
					DiscoveryHint:   "Use action=hosts to list available hosts",
					ErrorType:       "invalid_value",
				})
			}
		}

		if remoteHost == "" {
			result.Valid = false
			result.Errors = append(result.Errors, types.ValidationError{
				Parameter: "remote_host",
				Message:   "remote_host is required for tunnel_create action",
				Examples:  []interface{}{"localhost", "127.0.0.1", "db.internal"},
				ErrorType: "missing",
			})
		}

		if remotePort == 0 {
			result.Valid = false
			result.Errors = append(result.Errors, types.ValidationError{
				Parameter: "remote_port",
				Message:   "remote_port is required for tunnel_create action",
				Examples:  []interface{}{3306, 5432, 6379, 27017},
				ErrorType: "missing",
			})
		}
	}

	// Validate tunnel_close parameters
	if action == "tunnel_close" {
		tunnelID := t.getStringArg(args, "tunnel_id", "")
		if tunnelID == "" {
			tunnels := t.tunnelManager.ListTunnels()
			tunnelIDs := make([]string, 0, len(tunnels))
			for _, tunnel := range tunnels {
				tunnelIDs = append(tunnelIDs, tunnel.TunnelID)
			}

			result.Valid = false
			result.Errors = append(result.Errors, types.ValidationError{
				Parameter:       "tunnel_id",
				Message:         "tunnel_id is required for tunnel_close action",
				AvailableValues: tunnelIDs,
				DiscoveryHint:   "Use action=tunnel_list to see all active tunnels",
				ErrorType:       "missing",
			})
		}
	}

	return result
}

// GetUsageExamples implements types.UsageExampleProvider
func (t *SSHTool) GetUsageExamples() []types.ToolExample {
	return []types.ToolExample{
		{
			Name:        "List directory",
			Description: "List files in /var/log on a remote host",
			Args: map[string]interface{}{
				"action":  "exec",
				"host":    "web-prod-1",
				"command": "ls -la /var/log",
			},
			Expected: "Directory listing with file permissions and sizes",
		},
		{
			Name:        "Check disk space",
			Description: "Check disk usage on a server",
			Args: map[string]interface{}{
				"action":  "exec",
				"host":    "db-server",
				"command": "df -h",
			},
			Expected: "Disk usage summary for all mounted filesystems",
		},
		{
			Name:        "List hosts",
			Description: "View all configured SSH hosts",
			Args: map[string]interface{}{
				"action": "hosts",
			},
			Expected: "List of configured hosts with connection details",
		},
		{
			Name:        "Pool status",
			Description: "Check SSH connection pool and session status",
			Args: map[string]interface{}{
				"action": "status",
			},
			Expected: "Connection pool statistics, session count, and security configuration",
		},
		{
			Name:        "Start persistent session",
			Description: "Start a persistent shell session on a host",
			Args: map[string]interface{}{
				"action": "session_start",
				"host":   "web-prod-1",
			},
			Expected: "Session ID for subsequent commands",
		},
		{
			Name:        "Send command to session",
			Description: "Execute a command in an existing session",
			Args: map[string]interface{}{
				"action":     "session_send",
				"session_id": "abc12345",
				"command":    "cd /var/log && ls -la",
			},
			Expected: "Command output with exit code",
		},
		{
			Name:        "Close session",
			Description: "Close a persistent session",
			Args: map[string]interface{}{
				"action":     "session_close",
				"session_id": "abc12345",
			},
			Expected: "Session closed confirmation with statistics",
		},
		{
			Name:        "List sessions",
			Description: "View all active persistent sessions",
			Args: map[string]interface{}{
				"action": "session_list",
			},
			Expected: "List of active sessions with host, age, and command count",
		},
		{
			Name:        "Create database tunnel",
			Description: "Create a tunnel to access MySQL on a remote database server",
			Args: map[string]interface{}{
				"action":      "tunnel_create",
				"host":        "db-server",
				"local_port":  3307,
				"remote_host": "localhost",
				"remote_port": 3306,
			},
			Expected: "Tunnel created with local port to connect through",
		},
		{
			Name:        "Create Redis tunnel with auto-port",
			Description: "Create a tunnel to Redis with auto-assigned local port",
			Args: map[string]interface{}{
				"action":      "tunnel_create",
				"host":        "cache-server",
				"local_port":  0,
				"remote_host": "localhost",
				"remote_port": 6379,
			},
			Expected: "Tunnel created with auto-assigned local port",
		},
		{
			Name:        "List active tunnels",
			Description: "View all active SSH tunnels",
			Args: map[string]interface{}{
				"action": "tunnel_list",
			},
			Expected: "List of tunnels with connection stats",
		},
		{
			Name:        "Close tunnel",
			Description: "Close an active SSH tunnel",
			Args: map[string]interface{}{
				"action":    "tunnel_close",
				"tunnel_id": "abc-123-def",
			},
			Expected: "Confirmation that tunnel was closed",
		},
	}
}
