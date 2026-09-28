package config

import (
	"encoding/json"
	"testing"
)

func TestClaudeCodeConfig_Defaults(t *testing.T) {
	cfg := DefaultClaudeCodeConfig()

	if cfg.ClaudePath != "claude" {
		t.Errorf("ClaudePath: got %q, want %q", cfg.ClaudePath, "claude")
	}
	if cfg.MCPPort != 18790 {
		t.Errorf("MCPPort: got %d, want %d", cfg.MCPPort, 18790)
	}
	if cfg.PermissionMode != "acceptEdits" {
		t.Errorf("PermissionMode: got %q, want %q", cfg.PermissionMode, "acceptEdits")
	}
	if cfg.MaxTurns != 25 {
		t.Errorf("MaxTurns: got %d, want %d", cfg.MaxTurns, 25)
	}
	if cfg.TimeoutSeconds != 300 {
		t.Errorf("TimeoutSeconds: got %d, want %d", cfg.TimeoutSeconds, 300)
	}
	expectedTools := []string{"Read", "Edit", "Bash", "Glob", "Grep", "Write"}
	if len(cfg.AllowedTools) != len(expectedTools) {
		t.Fatalf("AllowedTools length: got %d, want %d", len(cfg.AllowedTools), len(expectedTools))
	}
	for i, tool := range expectedTools {
		if cfg.AllowedTools[i] != tool {
			t.Errorf("AllowedTools[%d]: got %q, want %q", i, cfg.AllowedTools[i], tool)
		}
	}
	if cfg.WorkingDir != "" {
		t.Errorf("WorkingDir should be empty by default, got %q", cfg.WorkingDir)
	}
}

func TestClaudeCodeOrDefault_NilClaudeCode(t *testing.T) {
	p := ProviderConfig{
		Name: "test",
		Type: "claude-code",
	}
	cfg := p.ClaudeCodeOrDefault()
	defaults := DefaultClaudeCodeConfig()

	if cfg.ClaudePath != defaults.ClaudePath {
		t.Errorf("ClaudePath: got %q, want %q", cfg.ClaudePath, defaults.ClaudePath)
	}
	if cfg.MCPPort != defaults.MCPPort {
		t.Errorf("MCPPort: got %d, want %d", cfg.MCPPort, defaults.MCPPort)
	}
	if cfg.MaxTurns != defaults.MaxTurns {
		t.Errorf("MaxTurns: got %d, want %d", cfg.MaxTurns, defaults.MaxTurns)
	}
}

func TestClaudeCodeOrDefault_PartialConfig(t *testing.T) {
	p := ProviderConfig{
		Name: "test",
		Type: "claude-code",
		ClaudeCode: &ClaudeCodeConfig{
			ClaudePath: "/usr/local/bin/claude",
			WorkingDir: "/home/user/project",
			// All other fields left at zero values — should get defaults
		},
	}
	cfg := p.ClaudeCodeOrDefault()

	if cfg.ClaudePath != "/usr/local/bin/claude" {
		t.Errorf("ClaudePath: got %q, want %q", cfg.ClaudePath, "/usr/local/bin/claude")
	}
	if cfg.WorkingDir != "/home/user/project" {
		t.Errorf("WorkingDir: got %q, want %q", cfg.WorkingDir, "/home/user/project")
	}
	// Defaulted fields
	if cfg.MCPPort != 18790 {
		t.Errorf("MCPPort should default to 18790, got %d", cfg.MCPPort)
	}
	if cfg.PermissionMode != "acceptEdits" {
		t.Errorf("PermissionMode should default to 'acceptEdits', got %q", cfg.PermissionMode)
	}
	if cfg.MaxTurns != 25 {
		t.Errorf("MaxTurns should default to 25, got %d", cfg.MaxTurns)
	}
	if cfg.TimeoutSeconds != 300 {
		t.Errorf("TimeoutSeconds should default to 300, got %d", cfg.TimeoutSeconds)
	}
	if len(cfg.AllowedTools) != 6 {
		t.Errorf("AllowedTools should default to 6 tools, got %d", len(cfg.AllowedTools))
	}
}

func TestClaudeCodeOrDefault_FullConfig(t *testing.T) {
	p := ProviderConfig{
		Name: "test",
		Type: "claude-code",
		ClaudeCode: &ClaudeCodeConfig{
			ClaudePath:     "/opt/claude",
			MCPPort:        19000,
			AllowedTools:   []string{"Read", "Write"},
			PermissionMode: "bypassPermissions",
			MaxTurns:       50,
			TimeoutSeconds: 600,
			WorkingDir:     "/srv/project",
		},
	}
	cfg := p.ClaudeCodeOrDefault()

	if cfg.ClaudePath != "/opt/claude" {
		t.Errorf("ClaudePath: got %q, want %q", cfg.ClaudePath, "/opt/claude")
	}
	if cfg.MCPPort != 19000 {
		t.Errorf("MCPPort: got %d, want %d", cfg.MCPPort, 19000)
	}
	if len(cfg.AllowedTools) != 2 {
		t.Errorf("AllowedTools: got %d tools, want 2", len(cfg.AllowedTools))
	}
	if cfg.PermissionMode != "bypassPermissions" {
		t.Errorf("PermissionMode: got %q, want %q", cfg.PermissionMode, "bypassPermissions")
	}
	if cfg.MaxTurns != 50 {
		t.Errorf("MaxTurns: got %d, want %d", cfg.MaxTurns, 50)
	}
	if cfg.TimeoutSeconds != 600 {
		t.Errorf("TimeoutSeconds: got %d, want %d", cfg.TimeoutSeconds, 600)
	}
	if cfg.WorkingDir != "/srv/project" {
		t.Errorf("WorkingDir: got %q, want %q", cfg.WorkingDir, "/srv/project")
	}
}

func TestClaudeCodeConfig_JSONRoundTrip(t *testing.T) {
	original := ProviderConfig{
		Name:  "claude-code-provider",
		Type:  "claude-code",
		Model: "claude-sonnet-4-6",
		ClaudeCode: &ClaudeCodeConfig{
			ClaudePath:     "/usr/bin/claude",
			MCPPort:        18791,
			AllowedTools:   []string{"Read", "Edit", "Bash"},
			PermissionMode: "acceptEdits",
			MaxTurns:       30,
			TimeoutSeconds: 120,
			WorkingDir:     "/tmp/work",
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var decoded ProviderConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if decoded.ClaudeCode == nil {
		t.Fatal("ClaudeCode should not be nil after round-trip")
	}
	if decoded.ClaudeCode.ClaudePath != "/usr/bin/claude" {
		t.Errorf("ClaudePath: got %q", decoded.ClaudeCode.ClaudePath)
	}
	if decoded.ClaudeCode.MCPPort != 18791 {
		t.Errorf("MCPPort: got %d", decoded.ClaudeCode.MCPPort)
	}
	if len(decoded.ClaudeCode.AllowedTools) != 3 {
		t.Errorf("AllowedTools: got %d, want 3", len(decoded.ClaudeCode.AllowedTools))
	}
	if decoded.ClaudeCode.WorkingDir != "/tmp/work" {
		t.Errorf("WorkingDir: got %q", decoded.ClaudeCode.WorkingDir)
	}
}

func TestClaudeCodeConfig_OmittedFromJSON(t *testing.T) {
	// When ClaudeCode is nil, it should be omitted from JSON
	p := ProviderConfig{
		Name:  "anthropic",
		Type:  "anthropic",
		Model: "claude-sonnet-4-6",
	}

	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	// Should not contain "claude_code" key
	var raw map[string]interface{}
	json.Unmarshal(data, &raw)
	if _, ok := raw["claude_code"]; ok {
		t.Error("claude_code should be omitted from JSON when nil")
	}
}
