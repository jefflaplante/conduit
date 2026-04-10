package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSetup_CreatesNewFile(t *testing.T) {
	dir := t.TempDir()
	mgr := NewMCPConfigManager(dir, 18790)

	if err := mgr.Setup(); err != nil {
		t.Fatalf("Setup() error: %v", err)
	}

	data, err := os.ReadFile(mgr.ConfigPath())
	if err != nil {
		t.Fatalf("failed to read created file: %v", err)
	}

	var content mcpFileContent
	if err := json.Unmarshal(data, &content); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	entry, ok := content.MCPServers["conduit"]
	if !ok {
		t.Fatal("expected 'conduit' key in mcpServers")
	}
	if entry.Type != "http" {
		t.Errorf("expected type 'http', got %q", entry.Type)
	}
	if entry.URL != "http://127.0.0.1:18790" {
		t.Errorf("expected url 'http://127.0.0.1:18790', got %q", entry.URL)
	}
}

func TestSetup_MergesIntoExistingFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".mcp.json")

	// Write an existing .mcp.json with another server
	existing := mcpFileContent{
		MCPServers: map[string]mcpServerEntry{
			"other-tool": {Type: "http", URL: "http://127.0.0.1:9999"},
		},
	}
	existingData, _ := json.MarshalIndent(existing, "", "  ")
	if err := os.WriteFile(configPath, existingData, 0644); err != nil {
		t.Fatalf("failed to write existing file: %v", err)
	}

	mgr := NewMCPConfigManager(dir, 18790)
	if err := mgr.Setup(); err != nil {
		t.Fatalf("Setup() error: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read merged file: %v", err)
	}

	var content mcpFileContent
	if err := json.Unmarshal(data, &content); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	// Both entries should exist
	if _, ok := content.MCPServers["other-tool"]; !ok {
		t.Error("existing 'other-tool' entry was lost during merge")
	}
	conduit, ok := content.MCPServers["conduit"]
	if !ok {
		t.Fatal("expected 'conduit' key after merge")
	}
	if conduit.URL != "http://127.0.0.1:18790" {
		t.Errorf("expected conduit URL 'http://127.0.0.1:18790', got %q", conduit.URL)
	}
}

func TestCleanup_DeletesCreatedFile(t *testing.T) {
	dir := t.TempDir()
	mgr := NewMCPConfigManager(dir, 18790)

	if err := mgr.Setup(); err != nil {
		t.Fatalf("Setup() error: %v", err)
	}

	// File should exist
	if _, err := os.Stat(mgr.ConfigPath()); err != nil {
		t.Fatalf("file should exist after Setup: %v", err)
	}

	if err := mgr.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error: %v", err)
	}

	// File should be gone
	if _, err := os.Stat(mgr.ConfigPath()); !os.IsNotExist(err) {
		t.Error("file should be removed after Cleanup of a created file")
	}
}

func TestCleanup_RestoresModifiedFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".mcp.json")

	// Write an existing .mcp.json
	original := `{
  "mcpServers": {
    "other-tool": {
      "type": "http",
      "url": "http://127.0.0.1:9999"
    }
  }
}
`
	if err := os.WriteFile(configPath, []byte(original), 0644); err != nil {
		t.Fatalf("failed to write original file: %v", err)
	}

	mgr := NewMCPConfigManager(dir, 18790)
	if err := mgr.Setup(); err != nil {
		t.Fatalf("Setup() error: %v", err)
	}

	// Verify file was modified (should contain "conduit")
	modified, _ := os.ReadFile(configPath)
	var modContent mcpFileContent
	json.Unmarshal(modified, &modContent)
	if _, ok := modContent.MCPServers["conduit"]; !ok {
		t.Fatal("conduit entry should exist after Setup")
	}

	// Cleanup should restore original
	if err := mgr.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error: %v", err)
	}

	restored, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read restored file: %v", err)
	}

	if string(restored) != original {
		t.Errorf("restored content does not match original.\nGot:\n%s\nWant:\n%s", string(restored), original)
	}
}

func TestSetup_ErrorWhenDirDoesNotExist(t *testing.T) {
	mgr := NewMCPConfigManager("/nonexistent/path/that/should/not/exist", 18790)

	err := mgr.Setup()
	if err == nil {
		t.Fatal("Setup() should return error for non-existent directory")
	}
}

func TestConfigPath(t *testing.T) {
	mgr := NewMCPConfigManager("/some/dir", 18790)
	expected := filepath.Join("/some/dir", ".mcp.json")
	if mgr.ConfigPath() != expected {
		t.Errorf("ConfigPath() = %q, want %q", mgr.ConfigPath(), expected)
	}
}

func TestCleanup_NopWhenSetupNotCalled(t *testing.T) {
	dir := t.TempDir()
	mgr := NewMCPConfigManager(dir, 18790)

	// Cleanup without Setup should be a no-op
	if err := mgr.Cleanup(); err != nil {
		t.Fatalf("Cleanup() without Setup should not error: %v", err)
	}
}

func TestSetup_CustomPort(t *testing.T) {
	dir := t.TempDir()
	mgr := NewMCPConfigManager(dir, 19999)

	if err := mgr.Setup(); err != nil {
		t.Fatalf("Setup() error: %v", err)
	}

	data, _ := os.ReadFile(mgr.ConfigPath())
	var content mcpFileContent
	json.Unmarshal(data, &content)

	if content.MCPServers["conduit"].URL != "http://127.0.0.1:19999" {
		t.Errorf("expected port 19999 in URL, got %q", content.MCPServers["conduit"].URL)
	}
}
