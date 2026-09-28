//go:build with_ssh

package ssh

import (
	"context"
	"fmt"
	"strings"

	"conduit/internal/config"
	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

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
	content.WriteString(fmt.Sprintf("  Require approval: %v\n", t.securityEngine.config.RequireApproval))
	content.WriteString(fmt.Sprintf("  Allow subshells: %v\n", t.config.Security.AllowSubshells))
	content.WriteString(fmt.Sprintf("  Allow pipes: %v\n", t.config.Security.AllowPipes))

	data := map[string]interface{}{
		"enabled": t.config.Enabled,
		"security": map[string]interface{}{
			"default_tier":     t.config.Security.DefaultTier,
			"require_approval": t.securityEngine.config.RequireApproval,
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

// getHostNames returns the names of all configured hosts
func (t *SSHTool) getHostNames() []string {
	hosts := t.config.Hosts
	names := make([]string, 0, len(hosts))
	for _, host := range hosts {
		names = append(names, host.Name)
	}
	return names
}

// inventoryLoad loads an inventory file
func (t *SSHTool) inventoryLoad(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	path := toolargs.GetString(args, "path", "")
	inventoryType := toolargs.GetString(args, "type", "file") // "file" or "dynamic"

	if path == "" {
		return &types.ToolResult{
			Success: false,
			Error:   "path parameter is required",
		}, nil
	}

	var err error
	if inventoryType == "dynamic" {
		err = t.inventoryManager.LoadDynamic(path)
	} else {
		err = t.inventoryManager.LoadFile(path)
	}

	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to load inventory: %v", err),
		}, nil
	}

	// Get stats
	hosts := t.inventoryManager.GetHosts()
	groups := t.inventoryManager.GetGroups()
	sources := t.inventoryManager.GetSources()

	return &types.ToolResult{
		Success: true,
		Data: map[string]interface{}{
			"message":     fmt.Sprintf("Successfully loaded inventory from %s", path),
			"type":        inventoryType,
			"path":        path,
			"hosts_count": len(hosts),
			"groups":      groups,
			"sources":     sources,
		},
	}, nil
}

// inventoryList lists hosts from inventory
func (t *SSHTool) inventoryList(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	group := toolargs.GetString(args, "group", "")

	var hosts []config.SSHHostConfig
	if group != "" {
		hosts = t.inventoryManager.GetHostsByGroup(group)
	} else {
		hosts = t.inventoryManager.GetHosts()
	}

	// Format hosts for output
	hostList := make([]map[string]interface{}, 0, len(hosts))
	for _, host := range hosts {
		hostList = append(hostList, map[string]interface{}{
			"name":          host.Name,
			"hostname":      host.Hostname,
			"user":          host.User,
			"port":          host.Port,
			"identity_file": host.IdentityFile,
			"groups":        host.Groups,
			"enabled":       host.IsHostEnabled(),
		})
	}

	result := map[string]interface{}{
		"hosts": hostList,
		"count": len(hosts),
	}

	if group != "" {
		result["group"] = group
	} else {
		result["groups"] = t.inventoryManager.GetGroups()
	}

	return &types.ToolResult{
		Success: true,
		Data:    result,
	}, nil
}

// inventoryRefresh forces a refresh of all inventory sources
func (t *SSHTool) inventoryRefresh(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	err := t.inventoryManager.Refresh()
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("refresh failed: %v", err),
		}, nil
	}

	hosts := t.inventoryManager.GetHosts()
	sources := t.inventoryManager.GetSources()

	return &types.ToolResult{
		Success: true,
		Data: map[string]interface{}{
			"message":     "Inventory refreshed successfully",
			"hosts_count": len(hosts),
			"sources":     sources,
		},
	}, nil
}
