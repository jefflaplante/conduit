//go:build with_ssh

package ssh

import (
	"context"
	"fmt"
	"strings"
	"time"

	"conduit/internal/config"
	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

// executeCommand executes a command on a remote host
func (t *SSHTool) executeCommand(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	host := toolargs.GetString(args, "host", "")
	command := toolargs.GetString(args, "command", "")
	timeout := toolargs.GetInt(args, "timeout", 30)

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
				"tier":         string(classification.Tier),
				"reason":       classification.Reason,
				"base_cmd":     classification.BaseCommand,
				"warnings":     classification.Warnings,
				"has_subshell": classification.HasSubshell,
			},
		}, nil
	}

	// conduit-w3l7: approval-tier commands are frozen and run only after a
	// human "YES <code>" reply; non-interactive turns fail closed.
	if classification.RequiresApproval {
		return t.gate(ctx, t.execOperation(host, command, timeout, classification),
			func(ctx context.Context) (*types.ToolResult, error) {
				return t.runCommand(ctx, host, command, timeout, classification, approvedBy)
			})
	}

	return t.runCommand(ctx, host, command, timeout, classification, "")
}

// runCommand executes an authorized command on host. approver is recorded in
// the audit log ("" when no human approval was required).
func (t *SSHTool) runCommand(ctx context.Context, host, command string, timeout int, classification *ClassificationResult, approver string) (*types.ToolResult, error) {
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
		// Log failed execution to audit
		if t.auditLogger != nil && t.config.Audit.LogCommands {
			_ = t.auditLogger.LogExecution(&AuditEntry{
				Host:         host,
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
			Error:   fmt.Sprintf("execution failed: %v", err),
			Data: map[string]interface{}{
				"host":    host,
				"command": command,
				"tier":    string(classification.Tier),
			},
		}, nil
	}

	// Log successful execution to audit
	if t.auditLogger != nil && t.config.Audit.LogCommands {
		_ = t.auditLogger.LogExecution(&AuditEntry{
			Host:         result.Host,
			Command:      result.Command,
			SecurityTier: string(classification.Tier),
			Approved:     true, // Command was approved by security check
			ApprovedBy:   approver,
			ExitCode:     result.ExitCode,
			Duration:     result.Duration.String(),
			Stdout:       result.Stdout,
			Stderr:       result.Stderr,
			Error:        result.Error,
			TimedOut:     result.TimedOut,
		})
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
			"host":      result.Host,
			"command":   result.Command,
			"exit_code": result.ExitCode,
			"stdout":    result.Stdout,
			"stderr":    result.Stderr,
			"duration":  result.Duration.String(),
			"timed_out": result.TimedOut,
			"tier":      string(classification.Tier),
			"base_cmd":  classification.BaseCommand,
		},
	}, nil
}

// executeGroupCommand executes a command on a host group (fan-out execution)
func (t *SSHTool) executeGroupCommand(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	groupName := toolargs.GetString(args, "group", "")
	command := toolargs.GetString(args, "command", "")
	timeout := toolargs.GetInt(args, "timeout", 30)
	maxParallel := toolargs.GetInt(args, "max_parallel", 0)

	// Validate required parameters
	if groupName == "" {
		return types.NewErrorResult("missing_parameter", "group parameter is required for exec_group action").
			WithParameter("group", nil).
			WithSuggestions([]string{"Use action=hosts to see configured host groups"}), nil
	}

	if command == "" {
		return types.NewErrorResult("missing_parameter", "command parameter is required for exec_group action").
			WithParameter("command", nil).
			WithExamples([]string{"uptime", "df -h", "ps aux"}), nil
	}

	// Resolve hosts from group
	hosts := t.config.GetHostsByGroup(groupName)
	if len(hosts) == 0 {
		return types.NewErrorResult("invalid_group", fmt.Sprintf("group '%s' has no hosts or does not exist", groupName)).
			WithParameter("group", groupName).
			WithSuggestions([]string{"Use action=hosts to see configured host groups"}), nil
	}

	// Find the group config to check for security tier and max parallel
	var groupConfig *config.SSHHostGroup
	for i := range t.config.HostGroups {
		if t.config.HostGroups[i].Name == groupName {
			groupConfig = &t.config.HostGroups[i]
			break
		}
	}

	// Determine the strictest security tier from the group or any host
	securityTier := ""
	if groupConfig != nil && groupConfig.SecurityTier != "" {
		securityTier = groupConfig.SecurityTier
	}

	// Check each host for the most restrictive tier
	for _, host := range hosts {
		if host.SecurityTier != "" {
			if securityTier == "" || isMoreRestrictive(host.SecurityTier, securityTier) {
				securityTier = host.SecurityTier
			}
		}
	}

	// Classify the command for security using the strictest tier
	classification := t.securityEngine.ValidateCommandForHost(command, securityTier)

	// Block if command is blocked
	if classification.Blocked {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("command blocked: %s", classification.Reason),
			Data: map[string]interface{}{
				"group":        groupName,
				"tier":         string(classification.Tier),
				"reason":       classification.Reason,
				"base_cmd":     classification.BaseCommand,
				"warnings":     classification.Warnings,
				"has_subshell": classification.HasSubshell,
			},
		}, nil
	}

	// Determine max parallel from: parameter > group config > default
	if maxParallel == 0 && groupConfig != nil && groupConfig.MaxParallel > 0 {
		maxParallel = groupConfig.MaxParallel
	}
	if maxParallel == 0 {
		maxParallel = t.config.Pool.MaxConnectionsPerHost
		if maxParallel == 0 {
			maxParallel = 5
		}
	}

	// Extract host names
	hostNames := make([]string, len(hosts))
	for i, host := range hosts {
		hostNames[i] = host.Name
	}

	// conduit-w3l7: gate on the resolved host list, so an approval covers
	// exactly these hosts even if group membership changes before approval.
	if classification.RequiresApproval {
		return t.gate(ctx, t.groupOperation(groupName, hostNames, command, timeout, maxParallel, classification),
			func(ctx context.Context) (*types.ToolResult, error) {
				return t.runGroupCommand(ctx, groupName, hostNames, command, timeout, maxParallel, classification, approvedBy)
			})
	}

	return t.runGroupCommand(ctx, groupName, hostNames, command, timeout, maxParallel, classification, "")
}

// runGroupCommand fans an authorized command out to hostNames.
func (t *SSHTool) runGroupCommand(ctx context.Context, groupName string, hostNames []string, command string, timeout, maxParallel int, classification *ClassificationResult, approver string) (*types.ToolResult, error) {
	// Create fan-out executor with specified max parallel
	executor := NewFanoutExecutor(t.pool, maxParallel)

	// Execute on all hosts
	execTimeout := time.Duration(timeout) * time.Second
	fanoutResult := executor.Execute(ctx, hostNames, command, execTimeout)

	// Log to audit
	if t.auditLogger != nil && t.config.Audit.LogCommands {
		for hostName, result := range fanoutResult.Results {
			_ = t.auditLogger.LogExecution(&AuditEntry{
				Host:         hostName,
				Command:      command,
				SecurityTier: string(classification.Tier),
				Approved:     true,
				ApprovedBy:   approver,
				ExitCode:     result.ExitCode,
				Duration:     result.Duration.String(),
				Stdout:       result.Stdout,
				Stderr:       result.Stderr,
				Error:        result.Error,
				TimedOut:     result.TimedOut,
			})
		}
	}

	// Format results for display
	includeOutput := true // Always include output for group executions
	content := executor.FormatResults(fanoutResult, includeOutput)

	// Determine overall success (all hosts succeeded)
	success := len(fanoutResult.Failed) == 0

	return &types.ToolResult{
		Success: success,
		Content: content,
		Data: map[string]interface{}{
			"group":     groupName,
			"command":   command,
			"total":     len(hostNames),
			"succeeded": len(fanoutResult.Succeeded),
			"failed":    len(fanoutResult.Failed),
			"duration":  fanoutResult.Duration.String(),
			"results":   fanoutResult.Results,
			"tier":      string(classification.Tier),
			"base_cmd":  classification.BaseCommand,
		},
	}, nil
}

// isMoreRestrictive returns true if tier1 is more restrictive than tier2
func isMoreRestrictive(tier1, tier2 string) bool {
	order := map[string]int{
		"read":      0,
		"modify":    1,
		"dangerous": 2,
		"blocked":   3,
	}
	return order[tier1] > order[tier2]
}
