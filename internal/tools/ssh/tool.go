//go:build with_ssh

// Package ssh implements the SSH remote execution tool with security controls.
package ssh

import (
	"context"
	"fmt"
	"sync"

	"conduit/internal/config"
	"conduit/internal/sandbox"
	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

// SSHTool provides remote command execution via SSH with security controls
type SSHTool struct {
	services         *types.ToolServices
	securityEngine   *SecurityEngine
	client           Client
	config           *config.RemoteSSHConfig
	sessionManager   *SessionManager
	tunnelManager    *TunnelManager
	auditLogger      *AuditLogger
	pool             *Pool             // Connection pool for fan-out execution
	fanoutExecutor   *FanoutExecutor   // Fan-out executor for group commands
	inventoryManager *InventoryManager // Ansible inventory manager

	// sandbox confines SCP local paths (upload source, download destination)
	// to tools.sandbox roots. nil denies all local paths. conduit-31jg.69
	sandbox *sandbox.Sandbox

	closeOnce sync.Once
}

// NewSSHTool creates a new SSH tool with the given services and configuration
func NewSSHTool(services *types.ToolServices, cfg *config.RemoteSSHConfig) (*SSHTool, error) {
	if cfg == nil {
		defaultCfg := config.DefaultRemoteSSHConfig()
		cfg = &defaultCfg
	}

	// Create security engine
	securityEngine, err := NewSecurityEngine(effectiveSecurityConfig(cfg.Security)) // conduit-w3l7
	if err != nil {
		return nil, fmt.Errorf("failed to create security engine: %w", err)
	}

	// Create session manager
	sessionManager := NewSessionManager(cfg.Sessions, cfg.Hosts, cfg.Defaults, cfg.Pool)

	// Create audit logger if enabled
	auditLogger, err := NewAuditLogger(cfg.Audit)
	if err != nil {
		return nil, fmt.Errorf("failed to create audit logger: %w", err)
	}

	// Create connection pool for fan-out execution
	pool := NewPool(cfg.Hosts, cfg.Defaults, cfg.Pool)

	// Create fan-out executor with pool and default max parallel from pool config
	maxParallel := cfg.Pool.MaxConnectionsPerHost
	if maxParallel == 0 {
		maxParallel = 5 // Default
	}
	fanoutExecutor := NewFanoutExecutor(pool, maxParallel)

	// Create inventory manager with config hosts
	inventoryManager := NewInventoryManager(cfg.Hosts)

	return &SSHTool{
		services:         services,
		securityEngine:   securityEngine,
		config:           cfg,
		sessionManager:   sessionManager,
		tunnelManager:    NewTunnelManager(),
		auditLogger:      auditLogger,
		pool:             pool,
		fanoutExecutor:   fanoutExecutor,
		inventoryManager: inventoryManager,
		// client is set via SetClient; the registered tool uses
		// NewPoolClient(pool) (register.go, conduit-enf0).
	}, nil
}

// SetClient sets the SSH client implementation
func (t *SSHTool) SetClient(client Client) {
	t.client = client
}

// Close cleans up the SSH tool resources. It is idempotent: the registry
// calls it on gateway shutdown and tests may call it again. Tunnels close
// first so their pooled connections are released before the pool closes.
func (t *SSHTool) Close() {
	t.closeOnce.Do(t.close)
}

func (t *SSHTool) close() {
	if t.sessionManager != nil {
		t.sessionManager.Close()
	}
	if t.tunnelManager != nil {
		t.tunnelManager.CloseAll()
	}
	if t.auditLogger != nil {
		_ = t.auditLogger.Close()
	}
	if t.pool != nil {
		t.pool.Close()
	}
	if t.inventoryManager != nil {
		t.inventoryManager.StopAutoRefresh()
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
- exec_group: Execute a command on a host group (fan-out)
- hosts: List configured SSH hosts
- status: Show connection pool, session, and tunnel status
- session_start: Start a persistent session on a host (runs no command; send commands with session_send)
- session_send: Send a command to an existing session
- session_close: Close a persistent session
- session_list: List active persistent sessions
- tunnel_create: Create a local port forwarding tunnel
- tunnel_close: Close an active tunnel
- tunnel_list: List all active tunnels
- scp_upload: Upload a local file to a remote host
- scp_download: Download a file from a remote host to local path
- inventory_load: Load an Ansible inventory file (INI, YAML) or dynamic script
- inventory_list: List hosts from inventory (optionally filtered by group)
- inventory_refresh: Refresh all inventory sources

Security:
Commands are classified into security tiers (read, modify, dangerous, blocked).
Blocked commands are rejected. Dangerous commands may require approval.
Tunnels only bind to 127.0.0.1 (localhost) for security.

Persistent Sessions:
Sessions maintain shell state between commands (environment variables, working directory).
Opening a session needs no approval; every command sent into it is classified (and approval-gated) like exec.
Max 5 concurrent sessions and 2 per host by default. Sessions auto-close after 10 minutes of idle time.

Examples:
- One-shot exec: action=exec, host="web-prod-1", command="ls -la /var/log"
- Group exec: action=exec_group, group="web-servers", command="uptime", timeout=60
- Start session: action=session_start, host="web-prod-1"
- Send to session: action=session_send, session_id="abc123", command="cd /var/log"
- Close session: action=session_close, session_id="abc123"
- List sessions: action=session_list
- Create tunnel: action=tunnel_create, host="db-server", local_port=3307, remote_host="localhost", remote_port=3306
- Close tunnel: action=tunnel_close, tunnel_id="abc-123"
- List tunnels: action=tunnel_list
- Upload file: action=scp_upload, host="web-prod-1", local_path="/tmp/data.json", remote_path="/var/www/data.json"
- Download file: action=scp_download, host="web-prod-1", remote_path="/var/log/app.log", local_path="/tmp/app.log"
- Load inventory: action=inventory_load, path="/path/to/inventory.ini" (or .yaml)
- Load dynamic inventory: action=inventory_load, path="/path/to/script.sh", type="dynamic"
- List inventory hosts: action=inventory_list (all hosts) or action=inventory_list, group="webservers"
- Refresh inventory: action=inventory_refresh`
}

// Parameters returns the JSON schema for tool parameters
func (t *SSHTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"exec", "exec_group", "hosts", "status", "session_start", "session_send", "session_close", "session_list", "tunnel_create", "tunnel_close", "tunnel_list", "scp_upload", "scp_download"},
				"description": "SSH operation to perform",
			},
			"host": map[string]interface{}{
				"type":        "string",
				"description": "Target host name (required for exec, session_start, and tunnel_create actions)",
			},
			"group": map[string]interface{}{
				"type":        "string",
				"description": "Target host group name (required for exec_group action)",
			},
			"command": map[string]interface{}{
				"type":        "string",
				"description": "Command to execute (required for exec and session_send actions; not accepted by session_start)",
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
			"max_parallel": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum parallel executions for exec_group (optional override, default from config or group setting)",
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
			"local_path": map[string]interface{}{
				"type":        "string",
				"description": "Local file path (required for scp_upload and scp_download actions)",
			},
			"remote_path": map[string]interface{}{
				"type":        "string",
				"description": "Remote file path (required for scp_upload and scp_download actions)",
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

	action := toolargs.GetString(args, "action", "")

	switch action {
	case "exec":
		return t.executeCommand(ctx, args)
	case "exec_group":
		return t.executeGroupCommand(ctx, args)
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
	case "scp_upload":
		return t.scpUpload(ctx, args)
	case "scp_download":
		return t.scpDownload(ctx, args)
	case "inventory_load":
		return t.inventoryLoad(ctx, args)
	case "inventory_list":
		return t.inventoryList(ctx, args)
	case "inventory_refresh":
		return t.inventoryRefresh(ctx, args)
	default:
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("unknown action: %s (valid actions: exec, exec_group, hosts, status, session_start, session_send, session_close, session_list, tunnel_create, tunnel_close, tunnel_list, scp_upload, scp_download, inventory_load, inventory_list, inventory_refresh)", action),
		}, nil
	}
}

// IncludeDataInModelOutput opts this tool into having ToolResult.Data
// rendered for the model: ids and lists needed for follow-up calls live
// only in Data (conduit-31jg.39).
func (t *SSHTool) IncludeDataInModelOutput() bool { return true }
