//go:build with_ssh

package ssh

import (
	"context"
	"fmt"
	"time"

	"conduit/internal/tools/types"
)

// SelfTest performs a functional check of the SSH tool.
// It verifies configuration, security settings, and connection pool status.
func (t *SSHTool) SelfTest(ctx context.Context, opts *types.SelfTestOptions) *types.SelfTestResult {
	start := time.Now()

	if opts == nil {
		opts = types.DefaultSelfTestOptions()
	}

	result := &types.SelfTestResult{
		Status:       types.SelfTestStatusOK,
		Capabilities: []string{},
		TestedAt:     time.Now(),
	}

	deps := []types.DependencyStatus{}

	// Check if SSH is enabled
	configDep := types.DependencyStatus{
		Name:     "SSHConfig",
		Required: true,
	}

	if t.config == nil {
		configDep.Available = false
		configDep.Status = "not_configured"
		configDep.Message = "SSH configuration not loaded"
		result.Status = types.SelfTestStatusFailed
		result.Message = "SSH is not configured"
		result.Suggestions = []string{
			"Add 'remote_ssh' section to config with enabled: true",
			"Configure at least one host in remote_ssh.hosts",
		}
		deps = append(deps, configDep)
		result.Dependencies = deps
		result.TestDuration = time.Since(start)
		return result
	}

	if !t.config.Enabled {
		configDep.Available = false
		configDep.Status = "disabled"
		configDep.Message = "SSH is disabled in configuration"
		result.Status = types.SelfTestStatusFailed
		result.Message = "SSH is disabled in configuration"
		result.Suggestions = []string{
			"Set remote_ssh.enabled to true in config",
		}
		deps = append(deps, configDep)
		result.Dependencies = deps
		result.TestDuration = time.Since(start)
		return result
	}

	configDep.Available = true
	configDep.Status = "enabled"
	deps = append(deps, configDep)

	// Check hosts configuration
	hostsDep := types.DependencyStatus{
		Name:     "SSHHosts",
		Required: true,
	}

	enabledHosts := t.config.GetEnabledHosts()
	if len(enabledHosts) == 0 {
		hostsDep.Available = false
		hostsDep.Status = "none_configured"
		hostsDep.Message = "No SSH hosts configured or enabled"
		result.Status = types.SelfTestStatusFailed
		result.Message = "No SSH hosts available"
		result.Suggestions = []string{
			"Add hosts to remote_ssh.hosts in config",
			"Ensure hosts have enabled: true (or omit for default true)",
		}
		deps = append(deps, hostsDep)
		result.Dependencies = deps
		result.TestDuration = time.Since(start)
		return result
	}

	hostsDep.Available = true
	hostsDep.Status = "configured"
	hostsDep.Message = fmt.Sprintf("%d host(s) enabled", len(enabledHosts))
	deps = append(deps, hostsDep)

	// Check security engine
	securityDep := types.DependencyStatus{
		Name:     "SecurityEngine",
		Required: true,
	}

	if t.securityEngine == nil {
		securityDep.Available = false
		securityDep.Status = "not_initialized"
		securityDep.Message = "Security engine not initialized"
		result.Status = types.SelfTestStatusDegraded
	} else {
		securityDep.Available = true
		securityDep.Status = "active"
		securityDep.Message = fmt.Sprintf("default tier: %s, approval: %v, subshells: %v, pipes: %v",
			t.config.Security.DefaultTier,
			t.securityEngine.config.RequireApproval,
			t.config.Security.AllowSubshells,
			t.config.Security.AllowPipes)
	}
	deps = append(deps, securityDep)

	// Check session manager
	sessionDep := types.DependencyStatus{
		Name:     "SessionManager",
		Required: false, // Sessions are optional
	}

	if t.sessionManager == nil {
		sessionDep.Available = false
		sessionDep.Status = "not_initialized"
	} else {
		sessionDep.Available = true
		sessionDep.Status = "active"
		sessionDep.Message = fmt.Sprintf("%d/%d sessions active",
			t.sessionManager.SessionCount(), t.sessionManager.maxSessions)
	}
	deps = append(deps, sessionDep)

	// Check tunnel manager
	tunnelDep := types.DependencyStatus{
		Name:     "TunnelManager",
		Required: false, // Tunnels are optional
	}

	if t.tunnelManager == nil {
		tunnelDep.Available = false
		tunnelDep.Status = "not_initialized"
	} else {
		tunnels := t.tunnelManager.ListTunnels()
		tunnelDep.Available = true
		tunnelDep.Status = "active"
		tunnelDep.Message = fmt.Sprintf("%d active tunnel(s)", len(tunnels))
	}
	deps = append(deps, tunnelDep)

	// Check connection pool
	poolDep := types.DependencyStatus{
		Name:     "ConnectionPool",
		Required: false, // Pool is for fan-out execution
	}

	if t.pool == nil {
		poolDep.Available = false
		poolDep.Status = "not_initialized"
	} else {
		poolDep.Available = true
		poolDep.Status = "initialized"
	}
	deps = append(deps, poolDep)

	// Check SSH client
	clientDep := types.DependencyStatus{
		Name:     "SSHClient",
		Required: false, // Client enables actual execution
	}

	if t.client == nil {
		clientDep.Available = false
		clientDep.Status = "not_connected"
		clientDep.Message = "SSH client not set (command validation only)"
		result.Capabilities = []string{"hosts", "status", "command_classification"}
		result.UnavailableCapabilities = []string{"exec", "exec_group", "session_start", "session_send", "tunnel_create", "scp_upload", "scp_download"}
		if result.Status == types.SelfTestStatusOK {
			result.Status = types.SelfTestStatusDegraded
			result.Message = "SSH configured but client not connected (command validation only)"
		}
	} else {
		clientDep.Available = true
		poolStatus := t.client.GetPoolStatus()
		clientDep.Status = "connected"
		clientDep.Message = fmt.Sprintf("%d total, %d active, %d idle connections",
			poolStatus.TotalConnections, poolStatus.ActiveConnections, poolStatus.IdleConnections)
		result.Capabilities = []string{"exec", "exec_group", "hosts", "status", "session_start", "session_send", "session_close", "session_list", "tunnel_create", "tunnel_close", "tunnel_list", "scp_upload", "scp_download", "inventory_load", "inventory_list", "inventory_refresh"}
	}
	deps = append(deps, clientDep)

	// Set overall status message if not already set
	if result.Message == "" {
		if t.client != nil {
			result.Status = types.SelfTestStatusOK
			result.Message = fmt.Sprintf("SSH ready — %d host(s), security tier: %s",
				len(enabledHosts), t.config.Security.DefaultTier)
		} else {
			result.Status = types.SelfTestStatusDegraded
			result.Message = fmt.Sprintf("SSH configured — %d host(s), client not connected",
				len(enabledHosts))
		}
	}

	// Add verbose details
	if opts.Verbose {
		hostDetails := make([]map[string]interface{}, 0, len(enabledHosts))
		for _, host := range enabledHosts {
			hostDetails = append(hostDetails, map[string]interface{}{
				"name":          host.Name,
				"hostname":      host.Hostname,
				"port":          host.GetPort(t.config.Defaults),
				"user":          host.GetUser(t.config.Defaults),
				"security_tier": host.SecurityTier,
				"groups":        host.Groups,
			})
		}

		result.Details = map[string]interface{}{
			"enabled_hosts": len(enabledHosts),
			"host_groups":   len(t.config.HostGroups),
			"hosts":         hostDetails,
			"security": map[string]interface{}{
				"default_tier":     t.config.Security.DefaultTier,
				"require_approval": t.securityEngine.config.RequireApproval,
				"allow_subshells":  t.config.Security.AllowSubshells,
				"allow_pipes":      t.config.Security.AllowPipes,
			},
			"pool_config": map[string]interface{}{
				"max_connections_per_host": t.config.Pool.MaxConnectionsPerHost,
				"idle_timeout":             t.config.Pool.IdleTimeout,
				"connect_timeout":          t.config.Pool.ConnectTimeout,
			},
		}
	}

	result.Dependencies = deps
	result.TestDuration = time.Since(start)

	// Include examples if requested and tool is functional
	if opts.IncludeExamples && result.IsFunctional() {
		result.Examples = []types.ToolExample{
			{
				Name:        "List hosts",
				Description: "Show configured SSH hosts",
				Args: map[string]interface{}{
					"action": "hosts",
				},
				Expected: "List of hosts with connection details and security tiers",
			},
			{
				Name:        "Execute command",
				Description: "Run a command on a remote host",
				Args: map[string]interface{}{
					"action":  "exec",
					"host":    "web-prod-1",
					"command": "uptime",
				},
				Expected: "Command output with exit code",
			},
			{
				Name:        "Check status",
				Description: "View connection pool and session status",
				Args: map[string]interface{}{
					"action": "status",
				},
				Expected: "Pool statistics and security configuration",
			},
		}
	}

	return result
}
