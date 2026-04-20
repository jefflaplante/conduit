package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefault(t *testing.T) {
	cfg := Default()

	// Test default values
	if cfg.Port != 18789 {
		t.Errorf("Expected default port 18789, got %d", cfg.Port)
	}

	if cfg.Database.Path != "gateway.db" {
		t.Errorf("Expected default database path 'gateway.db', got %s", cfg.Database.Path)
	}

	if cfg.AI.DefaultProvider != "anthropic" {
		t.Errorf("Expected default provider 'anthropic', got %s", cfg.AI.DefaultProvider)
	}

	// Test that we have at least one provider
	if len(cfg.AI.Providers) == 0 {
		t.Error("Expected at least one AI provider in default config")
	}

	// Test anthropic provider exists
	found := false
	for _, provider := range cfg.AI.Providers {
		if provider.Name == "anthropic" && provider.Type == "anthropic" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected anthropic provider in default config")
	}
}

func TestConfigSaveAndLoad(t *testing.T) {
	// Create a temporary directory for test
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test-config.json")

	// Create a test config
	originalConfig := &Config{
		Port: 19999,
		Database: DatabaseConfig{
			Path: "./test.db",
		},
		AI: AIConfig{
			DefaultProvider: "test-provider",
			Providers: []ProviderConfig{
				{
					Name:   "test",
					Type:   "anthropic",
					APIKey: "test-key",
					Model:  "test-model",
				},
			},
		},
		Tools: ToolsConfig{
			EnabledTools:  []string{"read"},
			MaxToolChains: 25,
		},
		Heartbeat:      DefaultHeartbeatConfig(),
		AgentHeartbeat: DefaultAgentHeartbeatConfig(),
	}

	// Save config
	err := originalConfig.Save(configPath)
	if err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Fatal("Config file was not created")
	}

	// Load config
	loadedConfig, err := Load(configPath)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Verify loaded config matches original
	if loadedConfig.Port != originalConfig.Port {
		t.Errorf("Port mismatch: expected %d, got %d", originalConfig.Port, loadedConfig.Port)
	}

	if loadedConfig.AI.DefaultProvider != originalConfig.AI.DefaultProvider {
		t.Errorf("DefaultProvider mismatch: expected %s, got %s",
			originalConfig.AI.DefaultProvider, loadedConfig.AI.DefaultProvider)
	}

	if len(loadedConfig.AI.Providers) != len(originalConfig.AI.Providers) {
		t.Errorf("Providers count mismatch: expected %d, got %d",
			len(originalConfig.AI.Providers), len(loadedConfig.AI.Providers))
	}
}

func TestEnvironmentVariableExpansion(t *testing.T) {
	// Set test environment variables
	os.Setenv("TEST_API_KEY", "test-api-key-123")
	os.Setenv("TEST_BOT_TOKEN", "test-bot-token-456")
	defer func() {
		os.Unsetenv("TEST_API_KEY")
		os.Unsetenv("TEST_BOT_TOKEN")
	}()

	// Create config with environment variables
	cfg := &Config{
		AI: AIConfig{
			Providers: []ProviderConfig{
				{
					Name:   "test",
					Type:   "anthropic",
					APIKey: "${TEST_API_KEY}",
					Model:  "test-model",
				},
			},
		},
		Channels: []ChannelConfig{
			{
				Name: "test-channel",
				Type: "telegram",
				Config: map[string]interface{}{
					"bot_token": "${TEST_BOT_TOKEN}",
				},
			},
		},
	}

	// Expand environment variables
	err := cfg.expandEnvVars()
	if err != nil {
		t.Fatalf("Failed to expand environment variables: %v", err)
	}

	// Verify expansion
	if cfg.AI.Providers[0].APIKey != "test-api-key-123" {
		t.Errorf("API key not expanded correctly: got %s", cfg.AI.Providers[0].APIKey)
	}

	if cfg.Channels[0].Config["bot_token"] != "test-bot-token-456" {
		t.Errorf("Bot token not expanded correctly: got %s", cfg.Channels[0].Config["bot_token"])
	}
}

func TestOAuthConfigIntegration(t *testing.T) {
	// Test OAuth configuration structure
	authConfig := &AuthConfig{
		Type:         "oauth",
		OAuthToken:   "test-oauth-token",
		RefreshToken: "test-refresh-token",
		ExpiresAt:    time.Now().Add(1 * time.Hour).Unix(),
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
	}

	provider := ProviderConfig{
		Name:   "test-oauth-provider",
		Type:   "anthropic",
		APIKey: "fallback-api-key",
		Model:  "test-model",
		Auth:   authConfig,
	}

	// Test JSON serialization
	data, err := json.Marshal(provider)
	if err != nil {
		t.Fatalf("Failed to marshal OAuth provider config: %v", err)
	}

	// Test JSON deserialization
	var unmarshaled ProviderConfig
	err = json.Unmarshal(data, &unmarshaled)
	if err != nil {
		t.Fatalf("Failed to unmarshal OAuth provider config: %v", err)
	}

	// Verify OAuth config is preserved
	if unmarshaled.Auth == nil {
		t.Fatal("OAuth config was lost during serialization")
	}

	if unmarshaled.Auth.Type != "oauth" {
		t.Errorf("OAuth type mismatch: expected 'oauth', got %s", unmarshaled.Auth.Type)
	}

	if unmarshaled.Auth.OAuthToken != "test-oauth-token" {
		t.Errorf("OAuth token mismatch: expected 'test-oauth-token', got %s", unmarshaled.Auth.OAuthToken)
	}
}

func TestLoadNonExistentConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "non-existent-config.json")

	// Should create default config if file doesn't exist
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Expected Load to create default config, got error: %v", err)
	}

	// Should be default config
	defaultCfg := Default()
	if cfg.Port != defaultCfg.Port {
		t.Errorf("Expected default port %d, got %d", defaultCfg.Port, cfg.Port)
	}

	// File should now exist
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Error("Config file should have been created")
	}
}

func TestInvalidConfigFile(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "invalid-config.json")

	// Write invalid JSON
	err := os.WriteFile(configPath, []byte("invalid json {"), 0644)
	if err != nil {
		t.Fatalf("Failed to write invalid config file: %v", err)
	}

	// Should return error when loading
	_, err = Load(configPath)
	if err == nil {
		t.Error("Expected error when loading invalid JSON config")
	}
}

func TestOAuthEnvironmentVariableExpansion(t *testing.T) {
	// Set OAuth environment variables
	os.Setenv("TEST_OAUTH_TOKEN", "at_oauth_token_123")
	os.Setenv("TEST_REFRESH_TOKEN", "rt_refresh_token_456")
	os.Setenv("TEST_CLIENT_ID", "client_id_789")
	os.Setenv("TEST_CLIENT_SECRET", "client_secret_abc")
	defer func() {
		os.Unsetenv("TEST_OAUTH_TOKEN")
		os.Unsetenv("TEST_REFRESH_TOKEN")
		os.Unsetenv("TEST_CLIENT_ID")
		os.Unsetenv("TEST_CLIENT_SECRET")
	}()

	cfg := &Config{
		AI: AIConfig{
			Providers: []ProviderConfig{
				{
					Name:   "oauth-test",
					Type:   "anthropic",
					APIKey: "${TEST_API_KEY}",
					Model:  "test-model",
					Auth: &AuthConfig{
						Type:         "oauth",
						OAuthToken:   "${TEST_OAUTH_TOKEN}",
						RefreshToken: "${TEST_REFRESH_TOKEN}",
						ClientID:     "${TEST_CLIENT_ID}",
						ClientSecret: "${TEST_CLIENT_SECRET}",
					},
				},
			},
		},
	}

	// Expand environment variables
	err := cfg.expandEnvVars()
	if err != nil {
		t.Fatalf("Failed to expand OAuth environment variables: %v", err)
	}

	// Verify OAuth token expansion
	auth := cfg.AI.Providers[0].Auth
	if auth.OAuthToken != "at_oauth_token_123" {
		t.Errorf("OAuth token not expanded: expected 'at_oauth_token_123', got %s", auth.OAuthToken)
	}

	if auth.RefreshToken != "rt_refresh_token_456" {
		t.Errorf("Refresh token not expanded: expected 'rt_refresh_token_456', got %s", auth.RefreshToken)
	}

	if auth.ClientID != "client_id_789" {
		t.Errorf("Client ID not expanded: expected 'client_id_789', got %s", auth.ClientID)
	}

	if auth.ClientSecret != "client_secret_abc" {
		t.Errorf("Client secret not expanded: expected 'client_secret_abc', got %s", auth.ClientSecret)
	}
}

func TestExpandTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}

	cfg := &Config{
		DataDir:     "~/mydata",
		SecretsFile: "~/.secrets.env",
		Database:    DatabaseConfig{Path: "~/db/gw.db"},
		SSH: SSHServerConfig{
			HostKeyPath:        "~/.conduit/ssh_host_key",
			AuthorizedKeysPath: "~/.conduit/authorized_keys",
		},
	}

	cfg.expandTilde()

	if cfg.DataDir != filepath.Join(home, "mydata") {
		t.Errorf("DataDir: got %s, want %s", cfg.DataDir, filepath.Join(home, "mydata"))
	}
	if cfg.SecretsFile != filepath.Join(home, ".secrets.env") {
		t.Errorf("SecretsFile: got %s, want %s", cfg.SecretsFile, filepath.Join(home, ".secrets.env"))
	}
	if cfg.Database.Path != filepath.Join(home, "db/gw.db") {
		t.Errorf("Database.Path: got %s, want %s", cfg.Database.Path, filepath.Join(home, "db/gw.db"))
	}
	if cfg.SSH.HostKeyPath != filepath.Join(home, ".conduit/ssh_host_key") {
		t.Errorf("SSH.HostKeyPath: got %s", cfg.SSH.HostKeyPath)
	}
	if cfg.SSH.AuthorizedKeysPath != filepath.Join(home, ".conduit/authorized_keys") {
		t.Errorf("SSH.AuthorizedKeysPath: got %s", cfg.SSH.AuthorizedKeysPath)
	}
}

func TestExpandTilde_NoTilde(t *testing.T) {
	cfg := &Config{
		DataDir:     "/absolute/path",
		SecretsFile: "",
	}
	cfg.expandTilde()

	if cfg.DataDir != "/absolute/path" {
		t.Errorf("absolute path should be unchanged: got %s", cfg.DataDir)
	}
	if cfg.SecretsFile != "" {
		t.Errorf("empty string should be unchanged: got %s", cfg.SecretsFile)
	}
}

func TestLoadSecretsFile(t *testing.T) {
	tmpDir := t.TempDir()
	secretsPath := filepath.Join(tmpDir, "test.env")

	content := `# This is a comment
KEY_ONE=value1
KEY_TWO="value with spaces"
KEY_THREE='single quoted'

BARE_KEY=bare
`
	if err := os.WriteFile(secretsPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	// Make sure the keys don't exist yet.
	os.Unsetenv("KEY_ONE")
	os.Unsetenv("KEY_TWO")
	os.Unsetenv("KEY_THREE")
	os.Unsetenv("BARE_KEY")
	defer func() {
		os.Unsetenv("KEY_ONE")
		os.Unsetenv("KEY_TWO")
		os.Unsetenv("KEY_THREE")
		os.Unsetenv("BARE_KEY")
	}()

	cfg := &Config{SecretsFile: secretsPath}
	if err := cfg.loadSecretsFile(); err != nil {
		t.Fatalf("loadSecretsFile: %v", err)
	}

	tests := map[string]string{
		"KEY_ONE":   "value1",
		"KEY_TWO":   "value with spaces",
		"KEY_THREE": "single quoted",
		"BARE_KEY":  "bare",
	}
	for key, want := range tests {
		if got := os.Getenv(key); got != want {
			t.Errorf("%s: got %q, want %q", key, got, want)
		}
	}
}

func TestLoadSecretsFile_NoOverride(t *testing.T) {
	tmpDir := t.TempDir()
	secretsPath := filepath.Join(tmpDir, "test.env")

	os.WriteFile(secretsPath, []byte("EXISTING_KEY=new_value\n"), 0600)

	// Set existing value.
	os.Setenv("EXISTING_KEY", "original")
	defer os.Unsetenv("EXISTING_KEY")

	cfg := &Config{SecretsFile: secretsPath}
	cfg.loadSecretsFile()

	if got := os.Getenv("EXISTING_KEY"); got != "original" {
		t.Errorf("existing env var was overridden: got %q, want %q", got, "original")
	}
}

func TestLoadSecretsFile_MissingFile(t *testing.T) {
	cfg := &Config{SecretsFile: "/nonexistent/path/secrets.env"}
	if err := cfg.loadSecretsFile(); err != nil {
		t.Errorf("missing file should be a no-op, got error: %v", err)
	}
}

func TestLoadSecretsFile_Empty(t *testing.T) {
	cfg := &Config{SecretsFile: ""}
	if err := cfg.loadSecretsFile(); err != nil {
		t.Errorf("empty path should be a no-op, got error: %v", err)
	}
}

func TestDataDirAndSecretsFileInJSON(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "cfg.json")

	cfgJSON := `{
		"port": 18789,
		"data_dir": "/custom/datadir",
		"secrets_file": "/custom/secrets.env",
		"database": {"path": "test.db"},
		"ai": {"default_provider": "anthropic", "providers": [{"name": "anthropic", "type": "anthropic", "api_key": "test", "model": "test"}]},
		"tools": {"enabled_tools": ["read"], "max_tool_chains": 25},
		"heartbeat": {"enabled": false},
		"agent_heartbeat": {"enabled": false}
	}`

	os.WriteFile(configPath, []byte(cfgJSON), 0644)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.DataDir != "/custom/datadir" {
		t.Errorf("DataDir: got %s, want /custom/datadir", cfg.DataDir)
	}
	if cfg.SecretsFile != "/custom/secrets.env" {
		t.Errorf("SecretsFile: got %s, want /custom/secrets.env", cfg.SecretsFile)
	}
}

func TestDiagnosticsConfig_Defaults(t *testing.T) {
	// Test that DefaultDiagnosticsConfig returns secure defaults
	cfg := DefaultDiagnosticsConfig()

	if !cfg.RequireAuth {
		t.Error("RequireAuth should be true by default for security")
	}

	// HealthPublic should be nil (defaults to true)
	if cfg.HealthPublic != nil {
		t.Errorf("HealthPublic should be nil (default), got %v", *cfg.HealthPublic)
	}

	// IsHealthPublic should return true when nil
	if !cfg.IsHealthPublic() {
		t.Error("IsHealthPublic() should return true when HealthPublic is nil")
	}
}

func TestDiagnosticsConfig_IsHealthPublic(t *testing.T) {
	tests := []struct {
		name         string
		healthPublic *bool
		expected     bool
	}{
		{
			name:         "nil defaults to true (for load balancer compatibility)",
			healthPublic: nil,
			expected:     true,
		},
		{
			name:         "explicit true",
			healthPublic: boolPtr(true),
			expected:     true,
		},
		{
			name:         "explicit false requires auth",
			healthPublic: boolPtr(false),
			expected:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DiagnosticsConfig{
				HealthPublic: tt.healthPublic,
			}

			if got := cfg.IsHealthPublic(); got != tt.expected {
				t.Errorf("IsHealthPublic() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestDiagnosticsConfig_InDefaultConfig(t *testing.T) {
	// Verify that Default() includes secure DiagnosticsConfig
	cfg := Default()

	if !cfg.Diagnostics.RequireAuth {
		t.Error("Default config should have Diagnostics.RequireAuth=true")
	}
}

func TestDiagnosticsConfig_JSONSerialization(t *testing.T) {
	// Test that DiagnosticsConfig serializes correctly
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "cfg.json")

	cfgJSON := `{
		"port": 18789,
		"database": {"path": "test.db"},
		"ai": {"default_provider": "anthropic", "providers": [{"name": "anthropic", "type": "anthropic", "api_key": "test", "model": "test"}]},
		"tools": {"enabled_tools": ["read"], "max_tool_chains": 25},
		"diagnostics": {
			"require_auth": true,
			"health_public": false
		},
		"heartbeat": {"enabled": false},
		"agent_heartbeat": {"enabled": false}
	}`

	os.WriteFile(configPath, []byte(cfgJSON), 0644)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if !cfg.Diagnostics.RequireAuth {
		t.Error("RequireAuth should be true from JSON")
	}

	if cfg.Diagnostics.HealthPublic == nil {
		t.Error("HealthPublic should not be nil after loading from JSON")
	} else if *cfg.Diagnostics.HealthPublic {
		t.Error("HealthPublic should be false from JSON")
	}

	if cfg.Diagnostics.IsHealthPublic() {
		t.Error("IsHealthPublic() should return false when HealthPublic is false")
	}
}

func TestDiagnosticsConfig_LegacyBehavior(t *testing.T) {
	// Test that require_auth=false makes all endpoints public (legacy behavior)
	cfg := DiagnosticsConfig{
		RequireAuth:  false,
		HealthPublic: nil,
	}

	// With RequireAuth=false, all diagnostic endpoints should be public
	if cfg.RequireAuth {
		t.Error("RequireAuth should be false for legacy behavior")
	}

	// Health should still be public regardless
	if !cfg.IsHealthPublic() {
		t.Error("Health should be public regardless of require_auth setting")
	}
}

// boolPtr returns a pointer to a bool value
func boolPtr(b bool) *bool {
	return &b
}

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

func TestBrainConfigApplyDefaults_MinimalConfig(t *testing.T) {
	// Regression test: minimal brain config with just enabled:true should pass validation.
	// Previously, omitempty JSON fields deserialized as zero values and failed validation.
	raw := `{"brain": {"enabled": true}}`
	var cfg struct {
		Brain BrainConfig `json:"brain"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if err := cfg.Brain.Validate(); err != nil {
		t.Fatalf("Validate() should succeed with minimal config, got: %v", err)
	}

	defaults := DefaultBrainConfig()
	if cfg.Brain.MaxLTMEntries != defaults.MaxLTMEntries {
		t.Errorf("MaxLTMEntries: got %d, want %d", cfg.Brain.MaxLTMEntries, defaults.MaxLTMEntries)
	}
	if cfg.Brain.AccessCountCap != defaults.AccessCountCap {
		t.Errorf("AccessCountCap: got %d, want %d", cfg.Brain.AccessCountCap, defaults.AccessCountCap)
	}
	if cfg.Brain.AccessWeight != defaults.AccessWeight {
		t.Errorf("AccessWeight: got %f, want %f", cfg.Brain.AccessWeight, defaults.AccessWeight)
	}
}

func TestBrainConfigApplyDefaults_PartialOverride(t *testing.T) {
	raw := `{"brain": {"enabled": true, "max_ltm_entries": 500}}`
	var cfg struct {
		Brain BrainConfig `json:"brain"`
	}
	json.Unmarshal([]byte(raw), &cfg)
	cfg.Brain.Validate()

	if cfg.Brain.MaxLTMEntries != 500 {
		t.Errorf("MaxLTMEntries should be user-specified 500, got %d", cfg.Brain.MaxLTMEntries)
	}
	defaults := DefaultBrainConfig()
	if cfg.Brain.AccessCountCap != defaults.AccessCountCap {
		t.Errorf("AccessCountCap should get default %d, got %d", defaults.AccessCountCap, cfg.Brain.AccessCountCap)
	}
}

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
