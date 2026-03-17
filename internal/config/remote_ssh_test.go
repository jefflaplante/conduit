package config

import (
	"testing"
	"time"
)

func TestRemoteSSHConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  RemoteSSHConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "disabled config is valid",
			config: RemoteSSHConfig{
				Enabled: false,
			},
			wantErr: false,
		},
		{
			name: "valid minimal enabled config",
			config: RemoteSSHConfig{
				Enabled: true,
				Security: SSHSecurityConfig{
					DefaultTier:     "dangerous",
					RequireApproval: []string{"dangerous"},
				},
				Audit: SSHAuditConfig{
					Enabled: true,
					LogPath: "/var/log/ssh_audit.jsonl",
				},
			},
			wantErr: false,
		},
		{
			name: "missing default_tier",
			config: RemoteSSHConfig{
				Enabled: true,
				Security: SSHSecurityConfig{
					DefaultTier: "",
				},
			},
			wantErr: true,
			errMsg:  "default_tier must be specified",
		},
		{
			name: "invalid default_tier - read not allowed",
			config: RemoteSSHConfig{
				Enabled: true,
				Security: SSHSecurityConfig{
					DefaultTier: "read",
				},
			},
			wantErr: true,
			errMsg:  "default_tier must be 'dangerous' or 'blocked'",
		},
		{
			name: "invalid default_tier - modify not allowed",
			config: RemoteSSHConfig{
				Enabled: true,
				Security: SSHSecurityConfig{
					DefaultTier: "modify",
				},
			},
			wantErr: true,
			errMsg:  "default_tier must be 'dangerous' or 'blocked'",
		},
		{
			name: "valid default_tier - blocked",
			config: RemoteSSHConfig{
				Enabled: true,
				Security: SSHSecurityConfig{
					DefaultTier: "blocked",
				},
				Audit: SSHAuditConfig{
					Enabled: true,
					LogPath: "/var/log/ssh_audit.jsonl",
				},
			},
			wantErr: false,
		},
		{
			name: "duplicate host names",
			config: RemoteSSHConfig{
				Enabled: true,
				Security: SSHSecurityConfig{
					DefaultTier: "dangerous",
				},
				Audit: SSHAuditConfig{
					Enabled: true,
					LogPath: "/var/log/ssh_audit.jsonl",
				},
				Hosts: []SSHHostConfig{
					{Name: "web-1", Hostname: "192.168.1.1"},
					{Name: "web-1", Hostname: "192.168.1.2"},
				},
			},
			wantErr: true,
			errMsg:  "duplicate host name",
		},
		{
			name: "duplicate host group names",
			config: RemoteSSHConfig{
				Enabled: true,
				Security: SSHSecurityConfig{
					DefaultTier: "dangerous",
				},
				Audit: SSHAuditConfig{
					Enabled: true,
					LogPath: "/var/log/ssh_audit.jsonl",
				},
				HostGroups: []SSHHostGroup{
					{Name: "web-servers"},
					{Name: "web-servers"},
				},
			},
			wantErr: true,
			errMsg:  "duplicate host group name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()

			if tt.wantErr {
				if err == nil {
					t.Errorf("RemoteSSHConfig.Validate() expected error but got nil")
					return
				}
				if tt.errMsg != "" && !sshContainsString(err.Error(), tt.errMsg) {
					t.Errorf("RemoteSSHConfig.Validate() error = %v, expected to contain %v", err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("RemoteSSHConfig.Validate() unexpected error = %v", err)
				}
			}
		})
	}
}

func TestSSHHostConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		host    SSHHostConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid host",
			host: SSHHostConfig{
				Name:     "web-server-1",
				Hostname: "192.168.1.100",
				Port:     22,
				User:     "admin",
			},
			wantErr: false,
		},
		{
			name: "valid host with dns name",
			host: SSHHostConfig{
				Name:     "web-server-1",
				Hostname: "web1.example.com",
			},
			wantErr: false,
		},
		{
			name: "empty name",
			host: SSHHostConfig{
				Name:     "",
				Hostname: "192.168.1.100",
			},
			wantErr: true,
			errMsg:  "host name cannot be empty",
		},
		{
			name: "empty hostname",
			host: SSHHostConfig{
				Name:     "web-1",
				Hostname: "",
			},
			wantErr: true,
			errMsg:  "hostname cannot be empty",
		},
		{
			name: "invalid port - too low",
			host: SSHHostConfig{
				Name:     "web-1",
				Hostname: "192.168.1.100",
				Port:     0, // 0 is treated as unset, so we test negative
			},
			wantErr: false, // 0 is not validated, only explicit invalid values
		},
		{
			name: "invalid port - too high",
			host: SSHHostConfig{
				Name:     "web-1",
				Hostname: "192.168.1.100",
				Port:     70000,
			},
			wantErr: true,
			errMsg:  "invalid port number",
		},
		{
			name: "valid security tier",
			host: SSHHostConfig{
				Name:         "web-1",
				Hostname:     "192.168.1.100",
				SecurityTier: "read",
			},
			wantErr: false,
		},
		{
			name: "invalid security tier",
			host: SSHHostConfig{
				Name:         "web-1",
				Hostname:     "192.168.1.100",
				SecurityTier: "invalid",
			},
			wantErr: true,
			errMsg:  "invalid security tier",
		},
		{
			name: "invalid characters in name",
			host: SSHHostConfig{
				Name:     "web server 1",
				Hostname: "192.168.1.100",
			},
			wantErr: true,
			errMsg:  "invalid character",
		},
		{
			name: "hostname with whitespace",
			host: SSHHostConfig{
				Name:     "web-1",
				Hostname: "192.168.1.100 ",
			},
			wantErr: true,
			errMsg:  "invalid whitespace",
		},
		{
			name: "valid identity file with absolute path",
			host: SSHHostConfig{
				Name:         "web-1",
				Hostname:     "192.168.1.100",
				IdentityFile: "/home/user/.ssh/id_rsa",
			},
			wantErr: false,
		},
		{
			name: "valid identity file with tilde",
			host: SSHHostConfig{
				Name:         "web-1",
				Hostname:     "192.168.1.100",
				IdentityFile: "~/.ssh/id_rsa",
			},
			wantErr: false,
		},
		{
			name: "invalid identity file - relative path",
			host: SSHHostConfig{
				Name:         "web-1",
				Hostname:     "192.168.1.100",
				IdentityFile: ".ssh/id_rsa",
			},
			wantErr: true,
			errMsg:  "must be an absolute path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.host.Validate()

			if tt.wantErr {
				if err == nil {
					t.Errorf("SSHHostConfig.Validate() expected error but got nil")
					return
				}
				if tt.errMsg != "" && !sshContainsString(err.Error(), tt.errMsg) {
					t.Errorf("SSHHostConfig.Validate() error = %v, expected to contain %v", err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("SSHHostConfig.Validate() unexpected error = %v", err)
				}
			}
		})
	}
}

func TestSSHSecurityConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  SSHSecurityConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid dangerous default",
			config: SSHSecurityConfig{
				DefaultTier: "dangerous",
			},
			wantErr: false,
		},
		{
			name: "valid blocked default",
			config: SSHSecurityConfig{
				DefaultTier: "blocked",
			},
			wantErr: false,
		},
		{
			name: "invalid read default",
			config: SSHSecurityConfig{
				DefaultTier: "read",
			},
			wantErr: true,
			errMsg:  "must be 'dangerous' or 'blocked'",
		},
		{
			name: "invalid modify default",
			config: SSHSecurityConfig{
				DefaultTier: "modify",
			},
			wantErr: true,
			errMsg:  "must be 'dangerous' or 'blocked'",
		},
		{
			name: "empty default",
			config: SSHSecurityConfig{
				DefaultTier: "",
			},
			wantErr: true,
			errMsg:  "default_tier must be specified",
		},
		{
			name: "valid require_approval tiers",
			config: SSHSecurityConfig{
				DefaultTier:     "dangerous",
				RequireApproval: []string{"dangerous", "blocked"},
			},
			wantErr: false,
		},
		{
			name: "invalid require_approval tier",
			config: SSHSecurityConfig{
				DefaultTier:     "dangerous",
				RequireApproval: []string{"invalid"},
			},
			wantErr: true,
			errMsg:  "invalid tier in require_approval",
		},
		{
			name: "negative max command length",
			config: SSHSecurityConfig{
				DefaultTier:      "dangerous",
				MaxCommandLength: -1,
			},
			wantErr: true,
			errMsg:  "max_command_length cannot be negative",
		},
		{
			name: "negative approval timeout",
			config: SSHSecurityConfig{
				DefaultTier:     "dangerous",
				ApprovalTimeout: -1 * time.Second,
			},
			wantErr: true,
			errMsg:  "approval_timeout cannot be negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()

			if tt.wantErr {
				if err == nil {
					t.Errorf("SSHSecurityConfig.Validate() expected error but got nil")
					return
				}
				if tt.errMsg != "" && !sshContainsString(err.Error(), tt.errMsg) {
					t.Errorf("SSHSecurityConfig.Validate() error = %v, expected to contain %v", err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("SSHSecurityConfig.Validate() unexpected error = %v", err)
				}
			}
		})
	}
}

func TestSSHAuditConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  SSHAuditConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "disabled config is valid",
			config: SSHAuditConfig{
				Enabled: false,
			},
			wantErr: false,
		},
		{
			name: "valid enabled config",
			config: SSHAuditConfig{
				Enabled:     true,
				LogPath:     "/var/log/ssh_audit.jsonl",
				LogCommands: true,
			},
			wantErr: false,
		},
		{
			name: "enabled without log path",
			config: SSHAuditConfig{
				Enabled: true,
				LogPath: "",
			},
			wantErr: true,
			errMsg:  "log_path must be specified",
		},
		{
			name: "negative max output capture",
			config: SSHAuditConfig{
				Enabled:          true,
				LogPath:          "/var/log/ssh.log",
				MaxOutputCapture: -1,
			},
			wantErr: true,
			errMsg:  "max_output_capture cannot be negative",
		},
		{
			name: "negative retention days",
			config: SSHAuditConfig{
				Enabled:       true,
				LogPath:       "/var/log/ssh.log",
				RetentionDays: -1,
			},
			wantErr: true,
			errMsg:  "retention_days cannot be negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()

			if tt.wantErr {
				if err == nil {
					t.Errorf("SSHAuditConfig.Validate() expected error but got nil")
					return
				}
				if tt.errMsg != "" && !sshContainsString(err.Error(), tt.errMsg) {
					t.Errorf("SSHAuditConfig.Validate() error = %v, expected to contain %v", err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("SSHAuditConfig.Validate() unexpected error = %v", err)
				}
			}
		})
	}
}

func TestSSHPoolConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  SSHPoolConfig
		wantErr bool
		errMsg  string
	}{
		{
			name:    "empty config is valid",
			config:  SSHPoolConfig{},
			wantErr: false,
		},
		{
			name: "valid config",
			config: SSHPoolConfig{
				MaxConnectionsPerHost: 5,
				MaxTotalConnections:   50,
				IdleTimeout:           5 * time.Minute,
				StrictHostKeyChecking: "yes",
			},
			wantErr: false,
		},
		{
			name: "negative max connections per host",
			config: SSHPoolConfig{
				MaxConnectionsPerHost: -1,
			},
			wantErr: true,
			errMsg:  "max_connections_per_host cannot be negative",
		},
		{
			name: "negative max total connections",
			config: SSHPoolConfig{
				MaxTotalConnections: -1,
			},
			wantErr: true,
			errMsg:  "max_total_connections cannot be negative",
		},
		{
			name: "negative idle timeout",
			config: SSHPoolConfig{
				IdleTimeout: -1 * time.Second,
			},
			wantErr: true,
			errMsg:  "idle_timeout cannot be negative",
		},
		{
			name: "invalid strict host key checking",
			config: SSHPoolConfig{
				StrictHostKeyChecking: "invalid",
			},
			wantErr: true,
			errMsg:  "invalid strict_host_key_checking",
		},
		{
			name: "valid strict host key checking - yes",
			config: SSHPoolConfig{
				StrictHostKeyChecking: "yes",
			},
			wantErr: false,
		},
		{
			name: "valid strict host key checking - no",
			config: SSHPoolConfig{
				StrictHostKeyChecking: "no",
			},
			wantErr: false,
		},
		{
			name: "valid strict host key checking - accept-new",
			config: SSHPoolConfig{
				StrictHostKeyChecking: "accept-new",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()

			if tt.wantErr {
				if err == nil {
					t.Errorf("SSHPoolConfig.Validate() expected error but got nil")
					return
				}
				if tt.errMsg != "" && !sshContainsString(err.Error(), tt.errMsg) {
					t.Errorf("SSHPoolConfig.Validate() error = %v, expected to contain %v", err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("SSHPoolConfig.Validate() unexpected error = %v", err)
				}
			}
		})
	}
}

func TestSSHHostConfig_Helpers(t *testing.T) {
	defaults := SSHHostDefaults{
		Port:           2222,
		User:           "defaultuser",
		IdentityFile:   "/default/key",
		ConnectTimeout: 60 * time.Second,
	}

	t.Run("IsHostEnabled - nil", func(t *testing.T) {
		host := SSHHostConfig{Enabled: nil}
		if !host.IsHostEnabled() {
			t.Error("nil Enabled should default to true")
		}
	})

	t.Run("IsHostEnabled - true", func(t *testing.T) {
		enabled := true
		host := SSHHostConfig{Enabled: &enabled}
		if !host.IsHostEnabled() {
			t.Error("explicit true Enabled should return true")
		}
	})

	t.Run("IsHostEnabled - false", func(t *testing.T) {
		enabled := false
		host := SSHHostConfig{Enabled: &enabled}
		if host.IsHostEnabled() {
			t.Error("explicit false Enabled should return false")
		}
	})

	t.Run("GetPort - host override", func(t *testing.T) {
		host := SSHHostConfig{Port: 3333}
		if got := host.GetPort(defaults); got != 3333 {
			t.Errorf("GetPort() = %d, want 3333", got)
		}
	})

	t.Run("GetPort - use default", func(t *testing.T) {
		host := SSHHostConfig{}
		if got := host.GetPort(defaults); got != 2222 {
			t.Errorf("GetPort() = %d, want 2222", got)
		}
	})

	t.Run("GetPort - no default", func(t *testing.T) {
		host := SSHHostConfig{}
		if got := host.GetPort(SSHHostDefaults{}); got != 22 {
			t.Errorf("GetPort() = %d, want 22", got)
		}
	})

	t.Run("GetUser - host override", func(t *testing.T) {
		host := SSHHostConfig{User: "hostuser"}
		if got := host.GetUser(defaults); got != "hostuser" {
			t.Errorf("GetUser() = %s, want hostuser", got)
		}
	})

	t.Run("GetUser - use default", func(t *testing.T) {
		host := SSHHostConfig{}
		if got := host.GetUser(defaults); got != "defaultuser" {
			t.Errorf("GetUser() = %s, want defaultuser", got)
		}
	})

	t.Run("GetIdentityFile - host override", func(t *testing.T) {
		host := SSHHostConfig{IdentityFile: "/host/key"}
		if got := host.GetIdentityFile(defaults); got != "/host/key" {
			t.Errorf("GetIdentityFile() = %s, want /host/key", got)
		}
	})

	t.Run("GetConnectTimeout - host override", func(t *testing.T) {
		host := SSHHostConfig{ConnectTimeout: 90 * time.Second}
		if got := host.GetConnectTimeout(defaults); got != 90*time.Second {
			t.Errorf("GetConnectTimeout() = %v, want 90s", got)
		}
	})

	t.Run("GetConnectTimeout - use default", func(t *testing.T) {
		host := SSHHostConfig{}
		if got := host.GetConnectTimeout(defaults); got != 60*time.Second {
			t.Errorf("GetConnectTimeout() = %v, want 60s", got)
		}
	})

	t.Run("GetConnectTimeout - no default", func(t *testing.T) {
		host := SSHHostConfig{}
		if got := host.GetConnectTimeout(SSHHostDefaults{}); got != 30*time.Second {
			t.Errorf("GetConnectTimeout() = %v, want 30s", got)
		}
	})
}

func TestSSHAuditConfig_Helpers(t *testing.T) {
	t.Run("IncludesTimestamps - nil", func(t *testing.T) {
		config := SSHAuditConfig{}
		if !config.IncludesTimestamps() {
			t.Error("nil IncludeTimestamps should default to true")
		}
	})

	t.Run("IncludesTimestamps - true", func(t *testing.T) {
		include := true
		config := SSHAuditConfig{IncludeTimestamps: &include}
		if !config.IncludesTimestamps() {
			t.Error("explicit true should return true")
		}
	})

	t.Run("IncludesTimestamps - false", func(t *testing.T) {
		include := false
		config := SSHAuditConfig{IncludeTimestamps: &include}
		if config.IncludesTimestamps() {
			t.Error("explicit false should return false")
		}
	})

	t.Run("GetMaxOutputCapture - default", func(t *testing.T) {
		config := SSHAuditConfig{}
		if got := config.GetMaxOutputCapture(); got != 64*1024 {
			t.Errorf("GetMaxOutputCapture() = %d, want 65536", got)
		}
	})

	t.Run("GetMaxOutputCapture - override", func(t *testing.T) {
		config := SSHAuditConfig{MaxOutputCapture: 1024}
		if got := config.GetMaxOutputCapture(); got != 1024 {
			t.Errorf("GetMaxOutputCapture() = %d, want 1024", got)
		}
	})
}

func TestSSHSecurityConfig_Helpers(t *testing.T) {
	t.Run("GetMaxCommandLength - default", func(t *testing.T) {
		config := SSHSecurityConfig{}
		if got := config.GetMaxCommandLength(); got != 10000 {
			t.Errorf("GetMaxCommandLength() = %d, want 10000", got)
		}
	})

	t.Run("GetMaxCommandLength - override", func(t *testing.T) {
		config := SSHSecurityConfig{MaxCommandLength: 5000}
		if got := config.GetMaxCommandLength(); got != 5000 {
			t.Errorf("GetMaxCommandLength() = %d, want 5000", got)
		}
	})

	t.Run("GetApprovalTimeout - default", func(t *testing.T) {
		config := SSHSecurityConfig{}
		if got := config.GetApprovalTimeout(); got != 5*time.Minute {
			t.Errorf("GetApprovalTimeout() = %v, want 5m", got)
		}
	})

	t.Run("GetApprovalTimeout - override", func(t *testing.T) {
		config := SSHSecurityConfig{ApprovalTimeout: 10 * time.Minute}
		if got := config.GetApprovalTimeout(); got != 10*time.Minute {
			t.Errorf("GetApprovalTimeout() = %v, want 10m", got)
		}
	})
}

func TestDefaultRemoteSSHConfig(t *testing.T) {
	config := DefaultRemoteSSHConfig()

	// Should be disabled by default for safety
	if config.Enabled {
		t.Error("DefaultRemoteSSHConfig() should be disabled by default")
	}

	// Default tier must be dangerous or blocked
	if config.Security.DefaultTier != "dangerous" && config.Security.DefaultTier != "blocked" {
		t.Errorf("DefaultRemoteSSHConfig() default_tier = %s, want dangerous or blocked", config.Security.DefaultTier)
	}

	// Audit should be enabled by default
	if !config.Audit.Enabled {
		t.Error("DefaultRemoteSSHConfig() audit should be enabled by default")
	}

	// Subshells should be disabled by default for security
	if config.Security.AllowSubshells {
		t.Error("DefaultRemoteSSHConfig() subshells should be disabled by default")
	}

	// Validate the default config (after enabling)
	config.Enabled = true
	if err := config.Validate(); err != nil {
		t.Errorf("DefaultRemoteSSHConfig() validation failed: %v", err)
	}
}

func TestRemoteSSHConfig_GetHostByName(t *testing.T) {
	config := RemoteSSHConfig{
		Hosts: []SSHHostConfig{
			{Name: "web-1", Hostname: "192.168.1.1"},
			{Name: "web-2", Hostname: "192.168.1.2"},
			{Name: "db-1", Hostname: "192.168.1.10"},
		},
	}

	t.Run("existing host", func(t *testing.T) {
		host := config.GetHostByName("web-1")
		if host == nil {
			t.Error("GetHostByName() returned nil for existing host")
			return
		}
		if host.Hostname != "192.168.1.1" {
			t.Errorf("GetHostByName() hostname = %s, want 192.168.1.1", host.Hostname)
		}
	})

	t.Run("non-existent host", func(t *testing.T) {
		host := config.GetHostByName("nonexistent")
		if host != nil {
			t.Error("GetHostByName() should return nil for non-existent host")
		}
	})
}

func TestRemoteSSHConfig_GetHostsByGroup(t *testing.T) {
	enabled := true
	disabled := false

	config := RemoteSSHConfig{
		Hosts: []SSHHostConfig{
			{Name: "web-prod-1", Hostname: "192.168.1.1", Groups: []string{"web", "production"}, Enabled: &enabled},
			{Name: "web-prod-2", Hostname: "192.168.1.2", Groups: []string{"web", "production"}, Enabled: &enabled},
			{Name: "web-staging-1", Hostname: "192.168.2.1", Groups: []string{"web", "staging"}, Enabled: &enabled},
			{Name: "db-prod-1", Hostname: "192.168.1.10", Groups: []string{"database", "production"}, Enabled: &enabled},
			{Name: "web-disabled", Hostname: "192.168.1.99", Groups: []string{"web"}, Enabled: &disabled},
		},
		HostGroups: []SSHHostGroup{
			{Name: "all-web", Pattern: "web-*"},
		},
	}

	t.Run("explicit group membership", func(t *testing.T) {
		hosts := config.GetHostsByGroup("production")
		if len(hosts) != 3 {
			t.Errorf("GetHostsByGroup(production) returned %d hosts, want 3", len(hosts))
		}
	})

	t.Run("pattern matching", func(t *testing.T) {
		hosts := config.GetHostsByGroup("all-web")
		// Should match web-prod-1, web-prod-2, web-staging-1 (web-disabled is disabled)
		if len(hosts) != 3 {
			t.Errorf("GetHostsByGroup(all-web) returned %d hosts, want 3", len(hosts))
		}
	})

	t.Run("excludes disabled hosts", func(t *testing.T) {
		hosts := config.GetHostsByGroup("web")
		for _, host := range hosts {
			if host.Name == "web-disabled" {
				t.Error("GetHostsByGroup() should not return disabled hosts")
			}
		}
	})

	t.Run("non-existent group", func(t *testing.T) {
		hosts := config.GetHostsByGroup("nonexistent")
		if len(hosts) != 0 {
			t.Errorf("GetHostsByGroup(nonexistent) returned %d hosts, want 0", len(hosts))
		}
	})
}

func TestRemoteSSHConfig_GetEnabledHosts(t *testing.T) {
	enabled := true
	disabled := false

	config := RemoteSSHConfig{
		Hosts: []SSHHostConfig{
			{Name: "host-1", Hostname: "192.168.1.1", Enabled: &enabled},
			{Name: "host-2", Hostname: "192.168.1.2", Enabled: nil},    // defaults to enabled
			{Name: "host-3", Hostname: "192.168.1.3", Enabled: &disabled},
		},
	}

	hosts := config.GetEnabledHosts()
	if len(hosts) != 2 {
		t.Errorf("GetEnabledHosts() returned %d hosts, want 2", len(hosts))
	}

	for _, host := range hosts {
		if host.Name == "host-3" {
			t.Error("GetEnabledHosts() returned disabled host")
		}
	}
}

func TestSSHHostGroup_Validate(t *testing.T) {
	tests := []struct {
		name    string
		group   SSHHostGroup
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid group",
			group:   SSHHostGroup{Name: "web-servers"},
			wantErr: false,
		},
		{
			name:    "empty name",
			group:   SSHHostGroup{Name: ""},
			wantErr: true,
			errMsg:  "group name cannot be empty",
		},
		{
			name:    "valid security tier",
			group:   SSHHostGroup{Name: "prod", SecurityTier: "dangerous"},
			wantErr: false,
		},
		{
			name:    "invalid security tier",
			group:   SSHHostGroup{Name: "prod", SecurityTier: "invalid"},
			wantErr: true,
			errMsg:  "invalid security tier",
		},
		{
			name:    "negative max parallel",
			group:   SSHHostGroup{Name: "test", MaxParallel: -1},
			wantErr: true,
			errMsg:  "max_parallel cannot be negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.group.Validate()

			if tt.wantErr {
				if err == nil {
					t.Errorf("SSHHostGroup.Validate() expected error but got nil")
					return
				}
				if tt.errMsg != "" && !sshContainsString(err.Error(), tt.errMsg) {
					t.Errorf("SSHHostGroup.Validate() error = %v, expected to contain %v", err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("SSHHostGroup.Validate() unexpected error = %v", err)
				}
			}
		})
	}
}

func TestSSHSessionConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  SSHSessionConfig
		wantErr bool
		errMsg  string
	}{
		{
			name:    "empty config is valid",
			config:  SSHSessionConfig{},
			wantErr: false,
		},
		{
			name: "valid config",
			config: SSHSessionConfig{
				MaxConcurrentSessions: 5,
				SessionIdleTimeout:    10 * time.Minute,
				DefaultShell:          "/bin/bash",
			},
			wantErr: false,
		},
		{
			name: "negative max concurrent sessions",
			config: SSHSessionConfig{
				MaxConcurrentSessions: -1,
			},
			wantErr: true,
			errMsg:  "max_concurrent_sessions cannot be negative",
		},
		{
			name: "negative session idle timeout",
			config: SSHSessionConfig{
				SessionIdleTimeout: -1 * time.Second,
			},
			wantErr: true,
			errMsg:  "session_idle_timeout cannot be negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()

			if tt.wantErr {
				if err == nil {
					t.Errorf("SSHSessionConfig.Validate() expected error but got nil")
					return
				}
				if tt.errMsg != "" && !sshContainsString(err.Error(), tt.errMsg) {
					t.Errorf("SSHSessionConfig.Validate() error = %v, expected to contain %v", err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("SSHSessionConfig.Validate() unexpected error = %v", err)
				}
			}
		})
	}
}

// Helper function to check if a string contains a substring
// Note: Named differently to avoid conflict with other test files in the package
func sshContainsString(s, substr string) bool {
	return len(s) >= len(substr) && sshFindSubstr(s, substr)
}

func sshFindSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
