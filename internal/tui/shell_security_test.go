package tui

import (
	"testing"
)

func TestValidateShellCommand_NoRestrictions(t *testing.T) {
	m := &Model{
		config: ModelConfig{
			ShellSecurity: ShellSecurityConfig{
				Enabled: true,
				// No allowlist or blocklist
			},
		},
	}

	// All commands should be allowed
	tests := []string{
		"ls -la",
		"git status",
		"echo hello",
		"cat /etc/passwd",
	}

	for _, cmd := range tests {
		if err := m.validateShellCommand(cmd); err != nil {
			t.Errorf("Expected command %q to be allowed, got error: %v", cmd, err)
		}
	}
}

func TestValidateShellCommand_Allowlist(t *testing.T) {
	m := &Model{
		config: ModelConfig{
			ShellSecurity: ShellSecurityConfig{
				Enabled:          true,
				CommandAllowlist: []string{"git ", "ls", "cat "},
			},
		},
	}

	// Allowed commands
	allowed := []string{
		"git status",
		"git commit -m 'test'",
		"ls",
		"ls -la",
		"cat file.txt",
	}

	for _, cmd := range allowed {
		if err := m.validateShellCommand(cmd); err != nil {
			t.Errorf("Expected command %q to be allowed, got error: %v", cmd, err)
		}
	}

	// Blocked commands (not in allowlist)
	blocked := []string{
		"rm -rf /",
		"echo hello",
		"sudo apt install",
		"wget http://example.com",
	}

	for _, cmd := range blocked {
		if err := m.validateShellCommand(cmd); err == nil {
			t.Errorf("Expected command %q to be blocked (not in allowlist)", cmd)
		}
	}
}

func TestValidateShellCommand_Blocklist(t *testing.T) {
	m := &Model{
		config: ModelConfig{
			ShellSecurity: ShellSecurityConfig{
				Enabled:          true,
				CommandBlocklist: []string{"rm -rf", "sudo ", "dangerous"},
			},
		},
	}

	// Allowed commands (not in blocklist)
	allowed := []string{
		"ls -la",
		"git status",
		"cat file.txt",
		"rm file.txt", // rm without -rf is allowed
	}

	for _, cmd := range allowed {
		if err := m.validateShellCommand(cmd); err != nil {
			t.Errorf("Expected command %q to be allowed, got error: %v", cmd, err)
		}
	}

	// Blocked commands
	blocked := []string{
		"rm -rf /",
		"rm -rf ~",
		"sudo apt install",
		"dangerous command here",
	}

	for _, cmd := range blocked {
		if err := m.validateShellCommand(cmd); err == nil {
			t.Errorf("Expected command %q to be blocked", cmd)
		}
	}
}

func TestValidateShellCommand_CaseInsensitive(t *testing.T) {
	m := &Model{
		config: ModelConfig{
			ShellSecurity: ShellSecurityConfig{
				Enabled:          true,
				CommandBlocklist: []string{"sudo "},
			},
		},
	}

	// Should block regardless of case
	blocked := []string{
		"sudo apt install",
		"SUDO apt install",
		"Sudo Apt Install",
	}

	for _, cmd := range blocked {
		if err := m.validateShellCommand(cmd); err == nil {
			t.Errorf("Expected command %q to be blocked (case-insensitive)", cmd)
		}
	}
}

func TestValidateShellCommand_AllowlistThenBlocklist(t *testing.T) {
	// When both allowlist and blocklist are set, allowlist is checked first
	m := &Model{
		config: ModelConfig{
			ShellSecurity: ShellSecurityConfig{
				Enabled:          true,
				CommandAllowlist: []string{"git "},
				CommandBlocklist: []string{"git push --force"},
			},
		},
	}

	// Allowed - in allowlist and not in blocklist
	if err := m.validateShellCommand("git status"); err != nil {
		t.Errorf("Expected 'git status' to be allowed: %v", err)
	}

	// Blocked - not in allowlist
	if err := m.validateShellCommand("ls -la"); err == nil {
		t.Error("Expected 'ls -la' to be blocked (not in allowlist)")
	}

	// Blocked - in allowlist but also in blocklist
	if err := m.validateShellCommand("git push --force"); err == nil {
		t.Error("Expected 'git push --force' to be blocked (in blocklist)")
	}
}

func TestValidateShellCommand_EmptyCommand(t *testing.T) {
	m := &Model{
		config: ModelConfig{
			ShellSecurity: ShellSecurityConfig{
				Enabled:          true,
				CommandBlocklist: []string{"rm"},
			},
		},
	}

	// Empty command should be allowed (it won't match any blocklist)
	if err := m.validateShellCommand(""); err != nil {
		t.Errorf("Empty command should be allowed: %v", err)
	}
}
