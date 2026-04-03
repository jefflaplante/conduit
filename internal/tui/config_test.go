package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTUIConfig_Save(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test-tui.json")

	cfg := &TUIConfig{
		GatewayURL:    "ws://localhost:18789/ws",
		Token:         "test-token-123",
		ClientName:    "test-client",
		DatabasePath:  "/path/to/db",
		UserID:        "testuser",
		AssistantName: "Claude",
	}

	err := cfg.save(configPath)
	require.NoError(t, err)

	// Verify file was created
	_, err = os.Stat(configPath)
	assert.NoError(t, err)

	// Read and verify content
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)

	var loaded TUIConfig
	err = json.Unmarshal(data, &loaded)
	require.NoError(t, err)

	assert.Equal(t, cfg.GatewayURL, loaded.GatewayURL)
	assert.Equal(t, cfg.Token, loaded.Token)
	assert.Equal(t, cfg.ClientName, loaded.ClientName)
	assert.Equal(t, cfg.DatabasePath, loaded.DatabasePath)
	assert.Equal(t, cfg.UserID, loaded.UserID)
	assert.Equal(t, cfg.AssistantName, loaded.AssistantName)
}

func TestTUIConfig_SavePermissions(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test-tui.json")

	cfg := &TUIConfig{
		GatewayURL: "ws://localhost:18789/ws",
		Token:      "secret-token",
	}

	err := cfg.save(configPath)
	require.NoError(t, err)

	// Verify file permissions (0600)
	info, err := os.Stat(configPath)
	require.NoError(t, err)
	// Note: On Windows permissions work differently
	if os.Getenv("GOOS") != "windows" {
		assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
	}
}

func TestLoadSavedToken_FileNotExists(t *testing.T) {
	// Use a unique temp directory
	tmpDir := t.TempDir()
	oldDataDirConfig := DataDirConfig
	DataDirConfig = tmpDir
	defer func() { DataDirConfig = oldDataDirConfig }()

	// Token should not be found
	token, found := LoadSavedToken()
	assert.False(t, found)
	assert.Empty(t, token)
}

func TestLoadSavedToken_EmptyToken(t *testing.T) {
	tmpDir := t.TempDir()
	oldDataDirConfig := DataDirConfig
	DataDirConfig = tmpDir
	defer func() { DataDirConfig = oldDataDirConfig }()

	// Create config with empty token
	configPath := filepath.Join(tmpDir, "tui.json")
	cfg := TUIConfig{
		GatewayURL: "ws://localhost:18789/ws",
		Token:      "",
	}
	data, _ := json.Marshal(cfg)
	err := os.WriteFile(configPath, data, 0600)
	require.NoError(t, err)

	token, found := LoadSavedToken()
	assert.False(t, found)
	assert.Empty(t, token)
}

func TestLoadSavedToken_WithToken(t *testing.T) {
	tmpDir := t.TempDir()
	oldDataDirConfig := DataDirConfig
	DataDirConfig = tmpDir
	defer func() { DataDirConfig = oldDataDirConfig }()

	// Create config with token
	configPath := filepath.Join(tmpDir, "tui.json")
	cfg := TUIConfig{
		GatewayURL: "ws://localhost:18789/ws",
		Token:      "saved-token-abc123",
	}
	data, _ := json.Marshal(cfg)
	err := os.WriteFile(configPath, data, 0600)
	require.NoError(t, err)

	token, found := LoadSavedToken()
	assert.True(t, found)
	assert.Equal(t, "saved-token-abc123", token)
}

func TestLoadSavedToken_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	oldDataDirConfig := DataDirConfig
	DataDirConfig = tmpDir
	defer func() { DataDirConfig = oldDataDirConfig }()

	// Create invalid JSON file
	configPath := filepath.Join(tmpDir, "tui.json")
	err := os.WriteFile(configPath, []byte("not valid json{"), 0600)
	require.NoError(t, err)

	token, found := LoadSavedToken()
	assert.False(t, found)
	assert.Empty(t, token)
}

func TestLoadOrCreateConfig_DefaultURL(t *testing.T) {
	tmpDir := t.TempDir()
	oldDataDirConfig := DataDirConfig
	DataDirConfig = tmpDir
	defer func() { DataDirConfig = oldDataDirConfig }()

	// With a token but no URL, should use default
	cfg, err := LoadOrCreateConfig("", "my-token", "")
	require.NoError(t, err)

	assert.Equal(t, "ws://localhost:18789/ws", cfg.GatewayURL)
	assert.Equal(t, "my-token", cfg.Token)
}

func TestLoadOrCreateConfig_OverridesConfig(t *testing.T) {
	tmpDir := t.TempDir()
	oldDataDirConfig := DataDirConfig
	DataDirConfig = tmpDir
	defer func() { DataDirConfig = oldDataDirConfig }()

	// Create existing config
	configPath := filepath.Join(tmpDir, "tui.json")
	existingCfg := TUIConfig{
		GatewayURL: "ws://old-server:1234/ws",
		Token:      "old-token",
	}
	data, _ := json.Marshal(existingCfg)
	err := os.WriteFile(configPath, data, 0600)
	require.NoError(t, err)

	// Load with overrides
	cfg, err := LoadOrCreateConfig("ws://new-server:5678/ws", "new-token", "/path/to/db")
	require.NoError(t, err)

	assert.Equal(t, "ws://new-server:5678/ws", cfg.GatewayURL)
	assert.Equal(t, "new-token", cfg.Token)
	assert.Equal(t, "/path/to/db", cfg.DatabasePath)
}

func TestLoadOrCreateConfig_UsesExistingConfig(t *testing.T) {
	tmpDir := t.TempDir()
	oldDataDirConfig := DataDirConfig
	DataDirConfig = tmpDir
	defer func() { DataDirConfig = oldDataDirConfig }()

	// Create existing config
	configPath := filepath.Join(tmpDir, "tui.json")
	existingCfg := TUIConfig{
		GatewayURL: "ws://saved-server:1234/ws",
		Token:      "saved-token",
	}
	data, _ := json.Marshal(existingCfg)
	err := os.WriteFile(configPath, data, 0600)
	require.NoError(t, err)

	// Load without overrides
	cfg, err := LoadOrCreateConfig("", "", "")
	require.NoError(t, err)

	assert.Equal(t, "ws://saved-server:1234/ws", cfg.GatewayURL)
	assert.Equal(t, "saved-token", cfg.Token)
}

func TestLoadOrCreateConfig_NoToken(t *testing.T) {
	tmpDir := t.TempDir()
	oldDataDirConfig := DataDirConfig
	DataDirConfig = tmpDir
	defer func() { DataDirConfig = oldDataDirConfig }()

	// No token provided and none saved
	_, err := LoadOrCreateConfig("", "", "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "token")
}

func TestLoadOrCreateConfig_SetsUserID(t *testing.T) {
	tmpDir := t.TempDir()
	oldDataDirConfig := DataDirConfig
	DataDirConfig = tmpDir
	defer func() { DataDirConfig = oldDataDirConfig }()

	cfg, err := LoadOrCreateConfig("", "token", "")
	require.NoError(t, err)

	// UserID should be set to hostname (or fallback)
	assert.NotEmpty(t, cfg.UserID)
}

func TestLoadOrCreateConfig_SetsClientName(t *testing.T) {
	tmpDir := t.TempDir()
	oldDataDirConfig := DataDirConfig
	DataDirConfig = tmpDir
	defer func() { DataDirConfig = oldDataDirConfig }()

	cfg, err := LoadOrCreateConfig("", "token", "")
	require.NoError(t, err)

	// ClientName should be set
	assert.NotEmpty(t, cfg.ClientName)
	assert.Contains(t, cfg.ClientName, "tui-")
}

func TestAutoGenerateToken(t *testing.T) {
	// autoGenerateToken currently returns an error indicating it's not available
	_, err := autoGenerateToken("/path/to/db", "client-name")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "auto-generation not available")
}

func TestShellSecurityConfig_Fields(t *testing.T) {
	cfg := ShellSecurityConfig{
		Enabled:          true,
		CommandAllowlist: []string{"git ", "ls"},
		CommandBlocklist: []string{"rm -rf"},
	}

	assert.True(t, cfg.Enabled)
	assert.Len(t, cfg.CommandAllowlist, 2)
	assert.Len(t, cfg.CommandBlocklist, 1)
}

func TestTUIConfig_ShellSecurityNotPersisted(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test-tui.json")

	cfg := &TUIConfig{
		GatewayURL: "ws://localhost:18789/ws",
		Token:      "test-token",
		ShellSecurity: ShellSecurityConfig{
			Enabled:          true,
			CommandAllowlist: []string{"git"},
		},
	}

	err := cfg.save(configPath)
	require.NoError(t, err)

	// Read file and verify ShellSecurity is not persisted
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)

	// ShellSecurity has json:"-" tag, so it shouldn't be in the file
	assert.NotContains(t, string(data), "shell_security")
	assert.NotContains(t, string(data), "CommandAllowlist")
}
