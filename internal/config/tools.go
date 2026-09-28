package config

// ShellEscapeConfig controls the shell escape (! prefix) feature in the TUI
type ShellEscapeConfig struct {
	// Enabled controls whether shell escape is available (default: true for local TUI, false for SSH)
	Enabled *bool `json:"enabled,omitempty"`

	// AllowSSH controls whether shell escape is allowed over SSH connections (default: false)
	AllowSSH bool `json:"allow_ssh,omitempty"`

	// CommandAllowlist, if non-empty, restricts shell commands to only those matching these prefixes.
	// Example: ["git ", "ls", "cat "] allows git commands, ls, and cat.
	CommandAllowlist []string `json:"command_allowlist,omitempty"`

	// CommandBlocklist blocks commands matching these prefixes. Applied after allowlist.
	// Default includes dangerous commands like "rm -rf", "sudo", "su ", etc.
	CommandBlocklist []string `json:"command_blocklist,omitempty"`

	// UseDefaultBlocklist includes the default blocklist of dangerous commands (default: true)
	UseDefaultBlocklist *bool `json:"use_default_blocklist,omitempty"`
}

// DefaultShellBlocklist returns the default list of blocked command prefixes
func DefaultShellBlocklist() []string {
	return []string{
		"rm -rf /",
		"rm -rf ~",
		"rm -rf .",
		"sudo ",
		"su ",
		"chmod 777",
		"dd if=",
		"mkfs",
		"> /dev/",
		":(){ :|:& };:", // fork bomb
		"curl | sh",
		"curl | bash",
		"wget | sh",
		"wget | bash",
	}
}

// IsShellEscapeEnabled returns whether shell escape is enabled, with defaults based on context
func (c *ShellEscapeConfig) IsShellEscapeEnabled(isSSH bool) bool {
	// SSH has shell escape disabled by default unless explicitly allowed
	if isSSH {
		return c.AllowSSH
	}

	// Local TUI has shell escape enabled by default
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

// ShouldUseDefaultBlocklist returns whether to use the default blocklist
func (c *ShellEscapeConfig) ShouldUseDefaultBlocklist() bool {
	if c.UseDefaultBlocklist == nil {
		return true
	}
	return *c.UseDefaultBlocklist
}

// GetEffectiveBlocklist returns the combined blocklist (default + custom)
func (c *ShellEscapeConfig) GetEffectiveBlocklist() []string {
	var result []string
	if c.ShouldUseDefaultBlocklist() {
		result = append(result, DefaultShellBlocklist()...)
	}
	result = append(result, c.CommandBlocklist...)
	return result
}

// ToolsConfig contains tool execution settings
type ToolsConfig struct {
	EnabledTools       []string                          `json:"enabled_tools"`
	MaxToolChains      int                               `json:"max_tool_chains,omitempty"`       // Maximum tool calls in a chain before stopping
	MaxToolResultChars int                               `json:"max_tool_result_chars,omitempty"` // Maximum chars in tool result content (default 8192)
	Sandbox            SandboxConfig                     `json:"sandbox"`
	Web                WebToolsConfig                    `json:"web,omitempty"` // conduit-31jg.7: outbound fetch (SSRF) policy
	Services           map[string]map[string]interface{} `json:"services,omitempty"`
}

// SandboxConfig contains sandboxing settings for tool execution
type SandboxConfig struct {
	WorkspaceDir    string   `json:"workspace_dir"`
	AllowedPaths    []string `json:"allowed_paths"`
	CommandDenylist []string `json:"command_denylist,omitempty"`
	// conduit-23hg: how CommandDenylist is matched ("legacy" | "command_position")
	// and whether autonomous sessions keep literal matching. See bash_policy.go.
	DenylistMode     string `json:"denylist_mode,omitempty"`
	StrictAutonomous *bool  `json:"strict_autonomous,omitempty"`
}

// WebToolsConfig controls the SSRF guard on WebFetch/Image URL fetches
// (conduit-31jg.7). Link-local (incl. 169.254.169.254) is always blocked;
// loopback is blocked unless listed in AllowedHosts; RFC1918/ULA is allowed
// unless BlockPrivateNetworks is set.
type WebToolsConfig struct {
	BlockPrivateNetworks bool     `json:"block_private_networks,omitempty"`
	AllowedHosts         []string `json:"allowed_hosts,omitempty"` // "ip:port" or "localhost:port"; port may be "*"
}
