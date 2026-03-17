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

	return &SSHTool{
		services:       services,
		securityEngine: securityEngine,
		config:         cfg,
		// client will be set via SetClient when the real implementation is available
	}, nil
}

// SetClient sets the SSH client implementation
func (t *SSHTool) SetClient(client Client) {
	t.client = client
}

// Name returns the tool name
func (t *SSHTool) Name() string {
	return "Ssh"
}

// Description returns the tool description with usage examples
func (t *SSHTool) Description() string {
	return `Execute commands on remote hosts via SSH with security controls.

Actions:
- exec: Execute a command on a remote host
- hosts: List configured SSH hosts
- status: Show connection pool status

Security:
Commands are classified into security tiers (read, modify, dangerous, blocked).
Blocked commands are rejected. Dangerous commands may require approval.

Examples:
- List files: action=exec, host="web-prod-1", command="ls -la /var/log"
- Check disk: action=exec, host="db-server", command="df -h"
- View hosts: action=hosts
- Pool status: action=status`
}

// Parameters returns the JSON schema for tool parameters
func (t *SSHTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"exec", "hosts", "status"},
				"description": "SSH operation to perform",
			},
			"host": map[string]interface{}{
				"type":        "string",
				"description": "Target host name (required for exec action)",
			},
			"command": map[string]interface{}{
				"type":        "string",
				"description": "Command to execute on the remote host (required for exec action)",
			},
			"timeout": map[string]interface{}{
				"type":        "integer",
				"description": "Command execution timeout in seconds (default: 30)",
				"default":     30,
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
	default:
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("unknown action: %s (valid actions: exec, hosts, status)", action),
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

	return &types.ToolResult{
		Success: true,
		Content: content.String(),
		Data:    data,
	}, nil
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

// ValidateParameters implements types.ParameterValidator for rich error messages
func (t *SSHTool) ValidateParameters(ctx context.Context, args map[string]interface{}) *types.ValidationResult {
	result := &types.ValidationResult{Valid: true}

	action := t.getStringArg(args, "action", "")

	// Validate action
	validActions := []string{"exec", "hosts", "status"}
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
	if action == "exec" {
		host := t.getStringArg(args, "host", "")
		command := t.getStringArg(args, "command", "")

		if host == "" {
			result.Valid = false
			result.Errors = append(result.Errors, types.ValidationError{
				Parameter:       "host",
				Message:         "host is required for exec action",
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

		if command == "" {
			result.Valid = false
			result.Errors = append(result.Errors, types.ValidationError{
				Parameter:   "command",
				Message:     "command is required for exec action",
				Examples:    []interface{}{"ls -la", "df -h", "ps aux", "uptime"},
				ErrorType:   "missing",
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
			Description: "Check SSH connection pool status",
			Args: map[string]interface{}{
				"action": "status",
			},
			Expected: "Connection pool statistics and security configuration",
		},
	}
}
