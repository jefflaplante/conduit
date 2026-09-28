//go:build with_ssh

package ssh

import (
	"context"
	"fmt"
	"strings"

	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

// createTunnel creates a new local port forwarding tunnel
func (t *SSHTool) createTunnel(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	host := toolargs.GetString(args, "host", "")
	localPort := toolargs.GetInt(args, "local_port", 0)
	remoteHost := toolargs.GetString(args, "remote_host", "")
	remotePort := toolargs.GetInt(args, "remote_port", 0)

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

	// conduit-enf0: a tunnel exposes the remote host's network (databases,
	// admin ports, container APIs) to every local process and bypasses
	// command classification, so it is dangerous-tier: refused on hosts
	// capped below dangerous and approval-gated under the default policy.
	classification := t.securityEngine.ClassifyOperationForHost("tunnel_create", tunnelTier,
		"tunnels expose remote network services on a local port", hostConfig.SecurityTier)
	if classification.Blocked {
		return operationBlocked(classification, map[string]interface{}{
			"host": host, "local_port": localPort, "remote_host": remoteHost, "remote_port": remotePort,
		}), nil
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

	// Reject bad ports before asking a human to approve them.
	for _, check := range []error{validatePort(localPort, "local"), validatePort(remotePort, "remote")} {
		if check != nil {
			return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to create tunnel: %v", check)}, nil
		}
	}

	if classification.RequiresApproval {
		return t.gate(ctx, t.tunnelOperation(host, localPort, remoteHost, remotePort, classification),
			func(context.Context) (*types.ToolResult, error) {
				return t.runCreateTunnel(host, localPort, remoteHost, remotePort)
			})
	}

	return t.runCreateTunnel(host, localPort, remoteHost, remotePort)
}

// tunnelTier is the fixed security tier of tunnel_create (conduit-enf0).
const tunnelTier = TierDangerous

// runCreateTunnel opens an authorized tunnel. The pooled SSH connection it
// uses goes back to the pool when the tunnel closes.
func (t *SSHTool) runCreateTunnel(host string, localPort int, remoteHost string, remotePort int) (*types.ToolResult, error) {
	sshClient, release, err := t.acquireSSHClient(host)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to get SSH connection for host '%s': %v", host, err),
		}, nil
	}

	// Create the tunnel
	tunnel, err := t.tunnelManager.CreateTunnelWithRelease(sshClient, localPort, remoteHost, remotePort, release)
	if err != nil {
		release()
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
	tunnelID := toolargs.GetString(args, "tunnel_id", "")

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
