package gateway

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"conduit/internal/config"
	"conduit/internal/mcp"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conduit-31jg.8: MCP token file lifecycle and mode resolution.

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestResolveMCPAuth_GeneratesTokenAndExportsEnv(t *testing.T) {
	t.Setenv(mcp.TokenEnvVar, "")
	t.Setenv("CONDUIT_DATA_DIR", "")
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir}

	mode, token, ok := resolveMCPAuth(cfg, quietLogger())
	require.True(t, ok)
	assert.Equal(t, mcp.AuthWarn, mode, "unset require_auth => warn-only transition mode")
	require.Len(t, token, 64)
	assert.Equal(t, token, os.Getenv(mcp.TokenEnvVar), "token exported for the claude -p subprocess")

	path := filepath.Join(dir, "auth", MCPTokenFilename)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())

	// Stable across restarts.
	_, again, ok := resolveMCPAuth(cfg, quietLogger())
	require.True(t, ok)
	assert.Equal(t, token, again)
}

func TestResolveMCPAuth_Modes(t *testing.T) {
	t.Setenv(mcp.TokenEnvVar, "")
	yes, no := true, false

	cfg := &config.Config{MCP: config.MCPConfig{RequireAuth: &yes, TokenFile: filepath.Join(t.TempDir(), "tok")}}
	mode, token, ok := resolveMCPAuth(cfg, quietLogger())
	require.True(t, ok)
	assert.Equal(t, mcp.AuthEnforce, mode)
	assert.NotEmpty(t, token)

	cfg = &config.Config{MCP: config.MCPConfig{RequireAuth: &no}}
	mode, token, ok = resolveMCPAuth(cfg, quietLogger())
	require.True(t, ok)
	assert.Equal(t, mcp.AuthDisabled, mode)
	assert.Empty(t, token)
}

// Enforce mode with an unusable token file fails closed (MCP not started).
func TestResolveMCPAuth_EnforceUnusableTokenFailsClosed(t *testing.T) {
	t.Setenv(mcp.TokenEnvVar, "")
	yes := true
	path := filepath.Join(t.TempDir(), "tok")
	require.NoError(t, os.WriteFile(path, []byte("secret\n"), 0644)) // world-readable

	_, _, ok := resolveMCPAuth(&config.Config{MCP: config.MCPConfig{RequireAuth: &yes, TokenFile: path}}, quietLogger())
	assert.False(t, ok)

	mode, token, ok := resolveMCPAuth(&config.Config{MCP: config.MCPConfig{TokenFile: path}}, quietLogger())
	assert.True(t, ok, "warn mode keeps serving")
	assert.Equal(t, mcp.AuthWarn, mode)
	assert.Empty(t, token)
}
