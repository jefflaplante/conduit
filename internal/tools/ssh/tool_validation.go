//go:build with_ssh

package ssh

import (
	"context"
	"fmt"

	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

// ValidateParameters implements types.ParameterValidator for rich error messages
func (t *SSHTool) ValidateParameters(ctx context.Context, args map[string]interface{}) *types.ValidationResult {
	result := &types.ValidationResult{Valid: true}

	action := toolargs.GetString(args, "action", "")

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
		host := toolargs.GetString(args, "host", "")

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

	// session_start never runs a command (conduit-1kxf)
	if action == "session_start" {
		if command := toolargs.GetString(args, "command", ""); command != "" {
			result.Valid = false
			result.Errors = append(result.Errors, types.ValidationError{
				Parameter:     "command",
				Message:       "session_start does not run commands; send them with action=session_send",
				ProvidedValue: command,
				DiscoveryHint: "Start the session with only host, then use action=session_send",
				ErrorType:     "invalid_value",
			})
		}
	}

	// Validate command for exec and session_send
	if action == "exec" || action == "session_send" {
		command := toolargs.GetString(args, "command", "")
		if command == "" {
			result.Valid = false
			result.Errors = append(result.Errors, types.ValidationError{
				Parameter: "command",
				Message:   fmt.Sprintf("command is required for %s action", action),
				Examples:  []interface{}{"ls -la", "df -h", "ps aux", "uptime"},
				ErrorType: "missing",
			})
		}
	}

	// Validate session_id for session_send and session_close
	if action == "session_send" || action == "session_close" {
		sessionID := toolargs.GetString(args, "session_id", "")
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
		host := toolargs.GetString(args, "host", "")
		remoteHost := toolargs.GetString(args, "remote_host", "")
		remotePort := toolargs.GetInt(args, "remote_port", 0)

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
		tunnelID := toolargs.GetString(args, "tunnel_id", "")
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
		{
			Name:        "Upload file",
			Description: "Upload a local file to a remote host",
			Args: map[string]interface{}{
				"action":      "scp_upload",
				"host":        "web-prod-1",
				"local_path":  "/tmp/data.json",
				"remote_path": "/var/www/html/data.json",
			},
			Expected: "File uploaded confirmation with size and duration",
		},
		{
			Name:        "Download file",
			Description: "Download a file from a remote host",
			Args: map[string]interface{}{
				"action":      "scp_download",
				"host":        "web-prod-1",
				"remote_path": "/var/log/application.log",
				"local_path":  "/tmp/application.log",
			},
			Expected: "File downloaded confirmation with size and duration",
		},
	}
}
