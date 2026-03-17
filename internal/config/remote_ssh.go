package config

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"time"
)

// RemoteSSHConfig contains settings for the SSH remote execution tool
type RemoteSSHConfig struct {
	// Enabled controls whether remote SSH execution is available
	Enabled bool `json:"enabled"`

	// Hosts defines known SSH hosts that can be targeted
	Hosts []SSHHostConfig `json:"hosts,omitempty"`

	// HostGroups defines logical groups of hosts for fan-out execution
	HostGroups []SSHHostGroup `json:"host_groups,omitempty"`

	// Security settings for command classification and approval
	Security SSHSecurityConfig `json:"security"`

	// Connection pool settings
	Pool SSHPoolConfig `json:"pool,omitempty"`

	// Audit logging configuration
	Audit SSHAuditConfig `json:"audit"`

	// Session management settings
	Sessions SSHSessionConfig `json:"sessions,omitempty"`

	// Default settings applied to hosts without explicit configuration
	Defaults SSHHostDefaults `json:"defaults,omitempty"`
}

// SSHHostConfig defines a known SSH host
type SSHHostConfig struct {
	// Name is the unique identifier for this host (e.g., "web-prod-1")
	Name string `json:"name"`

	// Hostname is the DNS name or IP address
	Hostname string `json:"hostname"`

	// Port is the SSH port (default: 22)
	Port int `json:"port,omitempty"`

	// User is the SSH username (default from Defaults or current user)
	User string `json:"user,omitempty"`

	// IdentityFile is the path to the SSH private key
	IdentityFile string `json:"identity_file,omitempty"`

	// Groups lists which host groups this host belongs to
	Groups []string `json:"groups,omitempty"`

	// Tags are arbitrary key-value metadata for the host
	Tags map[string]string `json:"tags,omitempty"`

	// SecurityTier overrides the default security tier for this host
	// Values: "read", "modify", "dangerous", "blocked"
	SecurityTier string `json:"security_tier,omitempty"`

	// Enabled controls whether this host can be targeted (default: true)
	Enabled *bool `json:"enabled,omitempty"`

	// JumpHost specifies a bastion/jump host to use for this connection
	JumpHost string `json:"jump_host,omitempty"`

	// ConnectTimeout overrides the default connection timeout
	ConnectTimeout time.Duration `json:"connect_timeout,omitempty"`
}

// SSHHostGroup defines a logical group of hosts
type SSHHostGroup struct {
	// Name is the group identifier (e.g., "web-servers", "production")
	Name string `json:"name"`

	// Description explains the purpose of this group
	Description string `json:"description,omitempty"`

	// Pattern is a glob pattern to match host names (e.g., "web-prod-*")
	Pattern string `json:"pattern,omitempty"`

	// SecurityTier sets the minimum security tier for commands on this group
	SecurityTier string `json:"security_tier,omitempty"`

	// MaxParallel limits concurrent executions within this group
	MaxParallel int `json:"max_parallel,omitempty"`
}

// SSHSecurityConfig defines command security classification settings
type SSHSecurityConfig struct {
	// DefaultTier is the security tier for unclassified commands
	// MUST be "dangerous" or "blocked" for safety (default: "dangerous")
	DefaultTier string `json:"default_tier"`

	// RequireApproval lists tiers that require human approval
	// Default: ["dangerous", "blocked"]
	RequireApproval []string `json:"require_approval,omitempty"`

	// AllowedCommands is a whitelist of commands at each tier
	AllowedCommands SSHCommandTiers `json:"allowed_commands,omitempty"`

	// BlockedPatterns are regex patterns that always block commands
	BlockedPatterns []string `json:"blocked_patterns,omitempty"`

	// AllowSubshells permits $() and backtick command substitution
	AllowSubshells bool `json:"allow_subshells"`

	// AllowPipes permits pipe chains in commands
	AllowPipes bool `json:"allow_pipes"`

	// MaxCommandLength limits command string length (default: 10000)
	MaxCommandLength int `json:"max_command_length,omitempty"`

	// ApprovalTimeout is how long approval requests are valid
	ApprovalTimeout time.Duration `json:"approval_timeout,omitempty"`

	// ApprovalChannel is the channel to send approval requests to
	ApprovalChannel string `json:"approval_channel,omitempty"`
}

// SSHCommandTiers defines commands allowed at each security tier
type SSHCommandTiers struct {
	// Read commands that only read/display information
	// Examples: ls, cat, ps, df, free, uptime, whoami, hostname
	Read []string `json:"read,omitempty"`

	// Modify commands that change state but are generally safe
	// Examples: touch, mkdir, cp, mv (with restrictions)
	Modify []string `json:"modify,omitempty"`

	// Dangerous commands that could cause harm but may be needed
	// Examples: rm, kill, systemctl, apt, yum
	Dangerous []string `json:"dangerous,omitempty"`

	// Blocked commands that should never be executed
	// Examples: rm -rf /, dd, mkfs, shutdown, reboot, init
	Blocked []string `json:"blocked,omitempty"`
}

// SSHPoolConfig defines connection pool settings
type SSHPoolConfig struct {
	// MaxConnectionsPerHost limits connections to each host
	MaxConnectionsPerHost int `json:"max_connections_per_host,omitempty"`

	// MaxTotalConnections limits total pool connections
	MaxTotalConnections int `json:"max_total_connections,omitempty"`

	// IdleTimeout closes idle connections after this duration
	IdleTimeout time.Duration `json:"idle_timeout,omitempty"`

	// ConnectTimeout is the default timeout for new connections
	ConnectTimeout time.Duration `json:"connect_timeout,omitempty"`

	// HealthCheckInterval is how often to verify connection health
	HealthCheckInterval time.Duration `json:"health_check_interval,omitempty"`

	// KnownHostsFile is the path to the SSH known_hosts file
	KnownHostsFile string `json:"known_hosts_file,omitempty"`

	// StrictHostKeyChecking controls host key verification
	// Values: "yes", "no", "accept-new" (default: "yes")
	StrictHostKeyChecking string `json:"strict_host_key_checking,omitempty"`
}

// SSHAuditConfig defines audit logging settings
type SSHAuditConfig struct {
	// Enabled controls whether audit logging is active
	Enabled bool `json:"enabled"`

	// LogPath is the path to the audit log file (JSONL format)
	LogPath string `json:"log_path"`

	// LogCommands records all executed commands
	LogCommands bool `json:"log_commands"`

	// LogOutput records command output (bounded by MaxOutputCapture)
	LogOutput bool `json:"log_output"`

	// MaxOutputCapture limits captured output size in bytes
	MaxOutputCapture int `json:"max_output_capture,omitempty"`

	// RetentionDays is how long to keep audit logs
	RetentionDays int `json:"retention_days,omitempty"`

	// IncludeTimestamps adds timestamps to all entries (default: true)
	IncludeTimestamps *bool `json:"include_timestamps,omitempty"`

	// RedactSecrets attempts to redact sensitive data from logs
	RedactSecrets bool `json:"redact_secrets"`
}

// SSHSessionConfig defines persistent session settings
type SSHSessionConfig struct {
	// MaxConcurrentSessions limits active persistent sessions
	MaxConcurrentSessions int `json:"max_concurrent_sessions,omitempty"`

	// SessionIdleTimeout closes idle sessions after this duration
	SessionIdleTimeout time.Duration `json:"session_idle_timeout,omitempty"`

	// DefaultShell is the shell to use for sessions (default: /bin/sh)
	DefaultShell string `json:"default_shell,omitempty"`

	// OutputBoundaryMarker is used to delimit command output
	OutputBoundaryMarker string `json:"output_boundary_marker,omitempty"`
}

// SSHHostDefaults defines default settings for hosts
type SSHHostDefaults struct {
	// Port is the default SSH port (default: 22)
	Port int `json:"port,omitempty"`

	// User is the default SSH username
	User string `json:"user,omitempty"`

	// IdentityFile is the default path to SSH private key
	IdentityFile string `json:"identity_file,omitempty"`

	// ConnectTimeout is the default connection timeout
	ConnectTimeout time.Duration `json:"connect_timeout,omitempty"`
}

// Validate validates the entire RemoteSSH configuration
func (c *RemoteSSHConfig) Validate() error {
	if !c.Enabled {
		return nil // No validation needed if disabled
	}

	// Validate security configuration (most critical)
	if err := c.Security.Validate(); err != nil {
		return fmt.Errorf("invalid security configuration: %w", err)
	}

	// Validate audit configuration
	if err := c.Audit.Validate(); err != nil {
		return fmt.Errorf("invalid audit configuration: %w", err)
	}

	// Validate hosts
	hostNames := make(map[string]bool)
	for i, host := range c.Hosts {
		if err := host.Validate(); err != nil {
			return fmt.Errorf("invalid host %d (%s): %w", i, host.Name, err)
		}
		if hostNames[host.Name] {
			return fmt.Errorf("duplicate host name: %s", host.Name)
		}
		hostNames[host.Name] = true
	}

	// Validate host groups
	groupNames := make(map[string]bool)
	for i, group := range c.HostGroups {
		if err := group.Validate(); err != nil {
			return fmt.Errorf("invalid host group %d (%s): %w", i, group.Name, err)
		}
		if groupNames[group.Name] {
			return fmt.Errorf("duplicate host group name: %s", group.Name)
		}
		groupNames[group.Name] = true
	}

	// Validate pool configuration
	if err := c.Pool.Validate(); err != nil {
		return fmt.Errorf("invalid pool configuration: %w", err)
	}

	// Validate session configuration
	if err := c.Sessions.Validate(); err != nil {
		return fmt.Errorf("invalid session configuration: %w", err)
	}

	return nil
}

// Validate validates SSHHostConfig
func (h *SSHHostConfig) Validate() error {
	if h.Name == "" {
		return fmt.Errorf("host name cannot be empty")
	}

	// Name must be a valid identifier (alphanumeric, hyphens, underscores)
	for _, c := range h.Name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return fmt.Errorf("host name contains invalid character: %c", c)
		}
	}

	if h.Hostname == "" {
		return fmt.Errorf("hostname cannot be empty")
	}

	// Validate hostname is a valid DNS name or IP
	if net.ParseIP(h.Hostname) == nil {
		// Not an IP, check if it looks like a valid hostname
		if strings.ContainsAny(h.Hostname, " \t\n") {
			return fmt.Errorf("hostname contains invalid whitespace")
		}
	}

	// Validate port if specified
	if h.Port != 0 && (h.Port < 1 || h.Port > 65535) {
		return fmt.Errorf("invalid port number: %d (must be 1-65535)", h.Port)
	}

	// Validate security tier if specified
	if h.SecurityTier != "" {
		validTiers := []string{"read", "modify", "dangerous", "blocked"}
		valid := false
		for _, tier := range validTiers {
			if h.SecurityTier == tier {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("invalid security tier: %s (must be one of: %s)", h.SecurityTier, strings.Join(validTiers, ", "))
		}
	}

	// Validate identity file path if specified
	if h.IdentityFile != "" && !filepath.IsAbs(h.IdentityFile) {
		// Allow ~ prefix for home directory
		if !strings.HasPrefix(h.IdentityFile, "~") {
			return fmt.Errorf("identity_file must be an absolute path or start with ~")
		}
	}

	return nil
}

// Validate validates SSHHostGroup
func (g *SSHHostGroup) Validate() error {
	if g.Name == "" {
		return fmt.Errorf("group name cannot be empty")
	}

	// Validate security tier if specified
	if g.SecurityTier != "" {
		validTiers := []string{"read", "modify", "dangerous", "blocked"}
		valid := false
		for _, tier := range validTiers {
			if g.SecurityTier == tier {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("invalid security tier: %s", g.SecurityTier)
		}
	}

	// Validate max parallel if specified
	if g.MaxParallel < 0 {
		return fmt.Errorf("max_parallel cannot be negative")
	}

	return nil
}

// Validate validates SSHSecurityConfig
func (s *SSHSecurityConfig) Validate() error {
	// Default tier MUST be "dangerous" or "blocked" for safety
	if s.DefaultTier == "" {
		return fmt.Errorf("default_tier must be specified (use 'dangerous' or 'blocked')")
	}

	validDefaultTiers := []string{"dangerous", "blocked"}
	valid := false
	for _, tier := range validDefaultTiers {
		if s.DefaultTier == tier {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("default_tier must be 'dangerous' or 'blocked' for safety (got: %s)", s.DefaultTier)
	}

	// Validate require_approval tiers
	validTiers := []string{"read", "modify", "dangerous", "blocked"}
	for _, tier := range s.RequireApproval {
		valid := false
		for _, validTier := range validTiers {
			if tier == validTier {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("invalid tier in require_approval: %s", tier)
		}
	}

	// Validate max command length
	if s.MaxCommandLength < 0 {
		return fmt.Errorf("max_command_length cannot be negative")
	}

	// Validate approval timeout
	if s.ApprovalTimeout < 0 {
		return fmt.Errorf("approval_timeout cannot be negative")
	}

	return nil
}

// Validate validates SSHPoolConfig
func (p *SSHPoolConfig) Validate() error {
	if p.MaxConnectionsPerHost < 0 {
		return fmt.Errorf("max_connections_per_host cannot be negative")
	}

	if p.MaxTotalConnections < 0 {
		return fmt.Errorf("max_total_connections cannot be negative")
	}

	if p.IdleTimeout < 0 {
		return fmt.Errorf("idle_timeout cannot be negative")
	}

	if p.ConnectTimeout < 0 {
		return fmt.Errorf("connect_timeout cannot be negative")
	}

	// Validate strict host key checking
	if p.StrictHostKeyChecking != "" {
		validValues := []string{"yes", "no", "accept-new"}
		valid := false
		for _, v := range validValues {
			if p.StrictHostKeyChecking == v {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("invalid strict_host_key_checking: %s (must be one of: %s)", p.StrictHostKeyChecking, strings.Join(validValues, ", "))
		}
	}

	return nil
}

// Validate validates SSHAuditConfig
func (a *SSHAuditConfig) Validate() error {
	if !a.Enabled {
		return nil // No validation needed if disabled
	}

	if a.LogPath == "" {
		return fmt.Errorf("log_path must be specified when audit is enabled")
	}

	if a.MaxOutputCapture < 0 {
		return fmt.Errorf("max_output_capture cannot be negative")
	}

	if a.RetentionDays < 0 {
		return fmt.Errorf("retention_days cannot be negative")
	}

	return nil
}

// Validate validates SSHSessionConfig
func (s *SSHSessionConfig) Validate() error {
	if s.MaxConcurrentSessions < 0 {
		return fmt.Errorf("max_concurrent_sessions cannot be negative")
	}

	if s.SessionIdleTimeout < 0 {
		return fmt.Errorf("session_idle_timeout cannot be negative")
	}

	return nil
}

// IsHostEnabled checks if a host is enabled (handles nil pointer)
func (h *SSHHostConfig) IsHostEnabled() bool {
	if h.Enabled == nil {
		return true // Default to enabled
	}
	return *h.Enabled
}

// GetPort returns the host port or default
func (h *SSHHostConfig) GetPort(defaults SSHHostDefaults) int {
	if h.Port != 0 {
		return h.Port
	}
	if defaults.Port != 0 {
		return defaults.Port
	}
	return 22
}

// GetUser returns the host user or default
func (h *SSHHostConfig) GetUser(defaults SSHHostDefaults) string {
	if h.User != "" {
		return h.User
	}
	return defaults.User
}

// GetIdentityFile returns the host identity file or default
func (h *SSHHostConfig) GetIdentityFile(defaults SSHHostDefaults) string {
	if h.IdentityFile != "" {
		return h.IdentityFile
	}
	return defaults.IdentityFile
}

// GetConnectTimeout returns the host connect timeout or default
func (h *SSHHostConfig) GetConnectTimeout(defaults SSHHostDefaults) time.Duration {
	if h.ConnectTimeout != 0 {
		return h.ConnectTimeout
	}
	if defaults.ConnectTimeout != 0 {
		return defaults.ConnectTimeout
	}
	return 30 * time.Second
}

// IncludesTimestamps returns whether timestamps should be included
func (a *SSHAuditConfig) IncludesTimestamps() bool {
	if a.IncludeTimestamps == nil {
		return true // Default to including timestamps
	}
	return *a.IncludeTimestamps
}

// GetMaxOutputCapture returns the max output capture size or default
func (a *SSHAuditConfig) GetMaxOutputCapture() int {
	if a.MaxOutputCapture != 0 {
		return a.MaxOutputCapture
	}
	return 64 * 1024 // 64KB default
}

// GetMaxCommandLength returns the max command length or default
func (s *SSHSecurityConfig) GetMaxCommandLength() int {
	if s.MaxCommandLength != 0 {
		return s.MaxCommandLength
	}
	return 10000 // 10KB default
}

// GetApprovalTimeout returns the approval timeout or default
func (s *SSHSecurityConfig) GetApprovalTimeout() time.Duration {
	if s.ApprovalTimeout != 0 {
		return s.ApprovalTimeout
	}
	return 5 * time.Minute // 5 minute default
}

// DefaultRemoteSSHConfig returns secure default configuration
func DefaultRemoteSSHConfig() RemoteSSHConfig {
	return RemoteSSHConfig{
		Enabled: false, // Disabled by default for safety
		Hosts:   []SSHHostConfig{},
		Security: SSHSecurityConfig{
			DefaultTier:     "dangerous", // Unknown commands are dangerous by default
			RequireApproval: []string{"dangerous", "blocked"},
			AllowSubshells:  false, // Disable by default for security
			AllowPipes:      true,  // Pipes are common and useful
			AllowedCommands: SSHCommandTiers{
				Read: []string{
					"ls", "cat", "head", "tail", "grep", "find", "which", "whereis",
					"ps", "top", "htop", "df", "du", "free", "uptime", "uname",
					"whoami", "hostname", "id", "groups", "date", "cal",
					"pwd", "env", "printenv", "echo", "wc", "sort", "uniq",
					"file", "stat", "lsof", "netstat", "ss", "ip", "ifconfig",
					"dig", "nslookup", "host", "ping", "traceroute", "curl", "wget",
					"journalctl", "dmesg", "last", "lastlog", "w", "who",
				},
				Modify: []string{
					"touch", "mkdir", "cp", "mv", "ln", "chmod", "chown",
					"tar", "gzip", "gunzip", "zip", "unzip",
					"git", "npm", "yarn", "pip", "go", "cargo", "make",
					"docker", "docker-compose", "kubectl",
				},
				Dangerous: []string{
					"rm", "rmdir", "kill", "killall", "pkill",
					"systemctl", "service", "init",
					"apt", "apt-get", "yum", "dnf", "pacman", "brew",
					"useradd", "userdel", "usermod", "groupadd", "groupdel",
					"crontab", "at",
				},
				Blocked: []string{
					"rm -rf /", "rm -rf /*", "dd", "mkfs", "fdisk", "parted",
					"shutdown", "reboot", "halt", "poweroff", "init 0", "init 6",
					":(){ :|:& };:", // Fork bomb
					"> /dev/sda", "cat /dev/zero", "cat /dev/random",
					"chmod -R 777 /", "chown -R",
					"wget | sh", "curl | sh", "wget | bash", "curl | bash",
				},
			},
			BlockedPatterns: []string{
				`rm\s+(-[rf]+\s+)*/$`,     // rm -rf /
				`>\s*/dev/[sh]d[a-z]`,     // overwrite disk
				`dd\s+.*of=/dev/[sh]d`,    // dd to disk
				`mkfs`,                    // format filesystem
				`:\(\)\{\s*:\|:&\s*\};:`,  // fork bomb
				`chmod\s+-R\s+777\s+/`,    // recursive 777 on root
				`curl.*\|\s*(ba)?sh`,      // pipe to shell
				`wget.*\|\s*(ba)?sh`,      // pipe to shell
				`/etc/shadow`,             // shadow file access
				`/etc/passwd.*>`,          // passwd file modification
			},
		},
		Pool: SSHPoolConfig{
			MaxConnectionsPerHost: 5,
			MaxTotalConnections:   50,
			IdleTimeout:           5 * time.Minute,
			ConnectTimeout:        30 * time.Second,
			HealthCheckInterval:   1 * time.Minute,
			StrictHostKeyChecking: "yes",
		},
		Audit: SSHAuditConfig{
			Enabled:          true, // Audit enabled by default for security
			LogPath:          "logs/ssh_audit.jsonl",
			LogCommands:      true,
			LogOutput:        true,
			MaxOutputCapture: 64 * 1024, // 64KB
			RetentionDays:    90,
			RedactSecrets:    true,
		},
		Sessions: SSHSessionConfig{
			MaxConcurrentSessions: 5,
			SessionIdleTimeout:    10 * time.Minute,
			DefaultShell:          "/bin/sh",
			OutputBoundaryMarker:  "___CONDUIT_OUTPUT_BOUNDARY___",
		},
		Defaults: SSHHostDefaults{
			Port:           22,
			ConnectTimeout: 30 * time.Second,
		},
	}
}

// GetHostByName returns a host configuration by name
func (c *RemoteSSHConfig) GetHostByName(name string) *SSHHostConfig {
	for i := range c.Hosts {
		if c.Hosts[i].Name == name {
			return &c.Hosts[i]
		}
	}
	return nil
}

// GetHostsByGroup returns all hosts belonging to a group
func (c *RemoteSSHConfig) GetHostsByGroup(groupName string) []*SSHHostConfig {
	var hosts []*SSHHostConfig

	// First check for pattern-based matching in the group
	var group *SSHHostGroup
	for i := range c.HostGroups {
		if c.HostGroups[i].Name == groupName {
			group = &c.HostGroups[i]
			break
		}
	}

	for i := range c.Hosts {
		host := &c.Hosts[i]
		if !host.IsHostEnabled() {
			continue
		}

		// Check explicit group membership
		for _, g := range host.Groups {
			if g == groupName {
				hosts = append(hosts, host)
				break
			}
		}

		// Check pattern matching if group has a pattern
		if group != nil && group.Pattern != "" {
			matched, _ := filepath.Match(group.Pattern, host.Name)
			if matched {
				// Avoid duplicates
				found := false
				for _, h := range hosts {
					if h.Name == host.Name {
						found = true
						break
					}
				}
				if !found {
					hosts = append(hosts, host)
				}
			}
		}
	}

	return hosts
}

// GetEnabledHosts returns all enabled hosts
func (c *RemoteSSHConfig) GetEnabledHosts() []*SSHHostConfig {
	var hosts []*SSHHostConfig
	for i := range c.Hosts {
		if c.Hosts[i].IsHostEnabled() {
			hosts = append(hosts, &c.Hosts[i])
		}
	}
	return hosts
}
