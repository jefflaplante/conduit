package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conduit-31jg.8: the gateway-written .mcp.json carries the Authorization
// header (env-var reference, never the literal token), and merging keeps
// other servers' fields intact.

func TestSetup_WritesAuthHeader(t *testing.T) {
	dir := t.TempDir()
	mgr := NewMCPConfigManager(dir, 18790)
	mgr.SetAuthHeader(true)
	require.NoError(t, mgr.Setup())

	data, err := os.ReadFile(mgr.ConfigPath())
	require.NoError(t, err)
	var content mcpFileContent
	require.NoError(t, json.Unmarshal(data, &content))
	e := content.MCPServers["conduit"]
	assert.Equal(t, "http", e.Type)
	assert.Equal(t, "http://127.0.0.1:18790/mcp", e.URL)
	assert.Equal(t, "Bearer ${CONDUIT_MCP_TOKEN:-}", e.Headers["Authorization"])
}

func TestSetup_NoAuthHeaderWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	mgr := NewMCPConfigManager(dir, 18790)
	require.NoError(t, mgr.Setup())
	data, err := os.ReadFile(mgr.ConfigPath())
	require.NoError(t, err)
	assert.NotContains(t, string(data), "headers")
}

func TestSetup_MergePreservesOtherServerFields(t *testing.T) {
	dir := t.TempDir()
	orig := `{
  "mcpServers": {
    "other": {"command": "npx", "args": ["-y", "srv"], "env": {"K": "V"}},
    "conduit": {"type": "http", "url": "http://127.0.0.1:1/mcp"}
  },
  "extra": true
}`
	path := filepath.Join(dir, ".mcp.json")
	require.NoError(t, os.WriteFile(path, []byte(orig), 0644))

	mgr := NewMCPConfigManager(dir, 18790)
	mgr.SetAuthHeader(true)
	require.NoError(t, mgr.Setup())

	var got map[string]interface{}
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, true, got["extra"])
	servers := got["mcpServers"].(map[string]interface{})
	other := servers["other"].(map[string]interface{})
	assert.Equal(t, "npx", other["command"])
	assert.Equal(t, []interface{}{"-y", "srv"}, other["args"])
	assert.Equal(t, map[string]interface{}{"K": "V"}, other["env"])
	conduit := servers["conduit"].(map[string]interface{})
	assert.Equal(t, "http://127.0.0.1:18790/mcp", conduit["url"])

	require.NoError(t, mgr.Cleanup())
	restored, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, orig, string(restored))
}
