package config

import (
	"testing"
)

func TestShellEscapeConfig_Defaults(t *testing.T) {
	cfg := &ShellEscapeConfig{}

	// Local TUI should be enabled by default
	if !cfg.IsShellEscapeEnabled(false) {
		t.Error("Shell escape should be enabled by default for local TUI")
	}

	// SSH should be disabled by default
	if cfg.IsShellEscapeEnabled(true) {
		t.Error("Shell escape should be disabled by default for SSH")
	}

	// Default blocklist should be used by default
	if !cfg.ShouldUseDefaultBlocklist() {
		t.Error("Default blocklist should be enabled by default")
	}
}

func TestShellEscapeConfig_ExplicitEnabled(t *testing.T) {
	enabled := true
	cfg := &ShellEscapeConfig{Enabled: &enabled}

	if !cfg.IsShellEscapeEnabled(false) {
		t.Error("Shell escape should be enabled when explicitly set to true")
	}

	disabled := false
	cfg2 := &ShellEscapeConfig{Enabled: &disabled}

	if cfg2.IsShellEscapeEnabled(false) {
		t.Error("Shell escape should be disabled when explicitly set to false")
	}
}

func TestShellEscapeConfig_AllowSSH(t *testing.T) {
	cfg := &ShellEscapeConfig{AllowSSH: true}

	if !cfg.IsShellEscapeEnabled(true) {
		t.Error("Shell escape should be enabled for SSH when AllowSSH is true")
	}

	cfg2 := &ShellEscapeConfig{AllowSSH: false}

	if cfg2.IsShellEscapeEnabled(true) {
		t.Error("Shell escape should be disabled for SSH when AllowSSH is false")
	}
}

func TestShellEscapeConfig_GetEffectiveBlocklist(t *testing.T) {
	// Default blocklist only
	cfg := &ShellEscapeConfig{}
	blocklist := cfg.GetEffectiveBlocklist()

	if len(blocklist) == 0 {
		t.Error("Expected non-empty default blocklist")
	}

	// Check some expected default blocklist entries
	defaultList := DefaultShellBlocklist()
	for _, entry := range defaultList {
		found := false
		for _, b := range blocklist {
			if b == entry {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected default blocklist entry %q in effective blocklist", entry)
		}
	}

	// Custom blocklist combined with default
	cfg2 := &ShellEscapeConfig{
		CommandBlocklist: []string{"custom_bad_cmd"},
	}
	blocklist2 := cfg2.GetEffectiveBlocklist()

	foundCustom := false
	for _, b := range blocklist2 {
		if b == "custom_bad_cmd" {
			foundCustom = true
			break
		}
	}
	if !foundCustom {
		t.Error("Expected custom blocklist entry in effective blocklist")
	}

	// Disable default blocklist
	disabled := false
	cfg3 := &ShellEscapeConfig{
		UseDefaultBlocklist: &disabled,
		CommandBlocklist:    []string{"only_custom"},
	}
	blocklist3 := cfg3.GetEffectiveBlocklist()

	if len(blocklist3) != 1 || blocklist3[0] != "only_custom" {
		t.Errorf("Expected only custom blocklist when default is disabled, got %v", blocklist3)
	}
}

func TestDefaultShellBlocklist(t *testing.T) {
	blocklist := DefaultShellBlocklist()

	if len(blocklist) == 0 {
		t.Error("Default blocklist should not be empty")
	}

	// Check that dangerous commands are blocked
	expectedDangerous := []string{"rm -rf /", "sudo ", "su "}
	for _, dangerous := range expectedDangerous {
		found := false
		for _, entry := range blocklist {
			if entry == dangerous {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected %q in default blocklist", dangerous)
		}
	}
}
