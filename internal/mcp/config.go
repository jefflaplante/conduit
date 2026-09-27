package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// mcpFileContent represents the top-level structure of a .mcp.json file.
type mcpFileContent struct {
	MCPServers map[string]mcpServerEntry `json:"mcpServers"`
}

// mcpServerEntry represents a single MCP server entry.
type mcpServerEntry struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

// AuthHeaderValue is the Authorization header written into .mcp.json when MCP
// auth is on (conduit-31jg.8). Claude Code expands ${VAR:-default} in headers;
// the empty default keeps the config parseable when the variable is unset
// (the server then sees an unauthenticated request instead of a config error).
const AuthHeaderValue = "Bearer ${" + TokenEnvVar + ":-}"

// MCPConfigManager manages the .mcp.json file lifecycle.
// It writes a "conduit" entry during Setup and restores the previous state
// during Cleanup (deleting the file if we created it, or restoring a backup
// if we merged into an existing file).
type MCPConfigManager struct {
	workingDir string
	port       int
	created    bool   // true if we created the file (vs merging into existing)
	backup     []byte // backup of pre-existing file contents
	authHeader bool   // conduit-31jg.8: write the Authorization header
}

// SetAuthHeader makes Setup write the Authorization header referencing
// ${CONDUIT_MCP_TOKEN} (conduit-31jg.8).
func (m *MCPConfigManager) SetAuthHeader(on bool) { m.authHeader = on }

// NewMCPConfigManager creates a config manager for the given working directory
// and MCP server port.
func NewMCPConfigManager(workingDir string, port int) *MCPConfigManager {
	return &MCPConfigManager{
		workingDir: workingDir,
		port:       port,
	}
}

// ConfigPath returns the full path to the .mcp.json file.
func (m *MCPConfigManager) ConfigPath() string {
	return filepath.Join(m.workingDir, ".mcp.json")
}

// Setup writes (or merges into) the .mcp.json file.
// If the file exists, it backs up the original content and merges the conduit
// entry. If the file does not exist, it creates a fresh one.
func (m *MCPConfigManager) Setup() error {
	// Verify working directory exists
	info, err := os.Stat(m.workingDir)
	if err != nil {
		return fmt.Errorf("working directory does not exist: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("working directory path is not a directory: %s", m.workingDir)
	}

	configPath := m.ConfigPath()
	conduitEntry := mcpServerEntry{
		Type: "http",
		URL:  fmt.Sprintf("http://127.0.0.1:%d/mcp", m.port),
	}
	if m.authHeader {
		conduitEntry.Headers = map[string]string{"Authorization": AuthHeaderValue}
	}
	conduitRaw, err := json.Marshal(conduitEntry)
	if err != nil {
		return fmt.Errorf("failed to marshal conduit entry: %w", err)
	}

	existing, err := os.ReadFile(configPath)
	if err == nil {
		// File exists: back it up and merge
		m.backup = existing
		m.created = false

		// conduit-31jg.8: merge via raw JSON so other servers' fields
		// (command, args, env, headers) and other top-level keys survive;
		// the typed struct used to drop everything but type/url.
		var top map[string]json.RawMessage
		if err := json.Unmarshal(existing, &top); err != nil {
			return fmt.Errorf("failed to parse existing .mcp.json: %w", err)
		}
		if top == nil {
			top = make(map[string]json.RawMessage)
		}
		servers := make(map[string]json.RawMessage)
		if raw, ok := top["mcpServers"]; ok && len(raw) > 0 && string(raw) != "null" {
			if err := json.Unmarshal(raw, &servers); err != nil {
				return fmt.Errorf("failed to parse mcpServers in existing .mcp.json: %w", err)
			}
		}
		servers["conduit"] = conduitRaw
		serversRaw, err := json.Marshal(servers)
		if err != nil {
			return fmt.Errorf("failed to marshal mcpServers: %w", err)
		}
		top["mcpServers"] = serversRaw

		return m.writeConfig(configPath, top)
	}

	if !os.IsNotExist(err) {
		return fmt.Errorf("failed to read .mcp.json: %w", err)
	}

	// File does not exist: create fresh
	m.created = true
	m.backup = nil

	content := map[string]interface{}{
		"mcpServers": map[string]json.RawMessage{"conduit": conduitRaw},
	}

	return m.writeConfig(configPath, content)
}

// Cleanup restores the previous state of the .mcp.json file.
// If we created the file, it is deleted. If we merged into an existing file,
// the backup is restored.
func (m *MCPConfigManager) Cleanup() error {
	configPath := m.ConfigPath()

	if m.created {
		// We created the file — remove it
		err := os.Remove(configPath)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove .mcp.json: %w", err)
		}
		return nil
	}

	if m.backup != nil {
		// We modified an existing file — restore the backup
		if err := os.WriteFile(configPath, m.backup, 0644); err != nil {
			return fmt.Errorf("failed to restore .mcp.json backup: %w", err)
		}
		return nil
	}

	// Nothing to clean up (Setup was never called or failed)
	return nil
}

// writeConfig marshals the content with indentation and writes it to path.
func (m *MCPConfigManager) writeConfig(path string, content interface{}) error {
	data, err := json.MarshalIndent(content, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal .mcp.json: %w", err)
	}
	data = append(data, '\n')

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write .mcp.json: %w", err)
	}
	return nil
}
