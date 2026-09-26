//go:build unix

package tools

import (
	"context"
	"testing"
	"time"

	"conduit/internal/ai"
	"conduit/internal/config"
	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newBashRegistryForTest(t *testing.T) *Registry {
	t.Helper()
	dir := t.TempDir()
	reg := NewRegistry(config.ToolsConfig{
		EnabledTools: []string{"Bash", "Read"},
		Sandbox:      config.SandboxConfig{WorkspaceDir: dir, AllowedPaths: []string{dir}},
	})
	reg.SetServices(&types.ToolServices{})
	return reg
}

// conduit-31jg.20/.39: a Bash call's `timeout` sizes that call's deadline;
// the engine-wide default no longer caps it (nor is it shared by a batch).
func TestExecutionEngine_PerCallTimeoutExtendsDeadline(t *testing.T) {
	reg := newBashRegistryForTest(t)
	engine := NewExecutionEngine(reg, 4, 300*time.Millisecond, 5)

	results, err := engine.ExecuteToolCalls(context.Background(), []ai.ToolCall{
		{ID: "1", Name: "Bash", Args: map[string]interface{}{"command": "sleep 1; echo done", "timeout": float64(5000)}},
		{ID: "2", Name: "Bash", Args: map[string]interface{}{"command": "sleep 5"}},
	})
	require.NoError(t, err)
	require.Len(t, results, 2)

	require.NotNil(t, results[0].Result)
	assert.True(t, results[0].Result.Success, "call with timeout=5000ms should finish: %s", results[0].Result.Error)
	assert.Contains(t, results[0].Result.Content, "done")

	require.NotNil(t, results[1].Result)
	assert.False(t, results[1].Result.Success, "call without timeout uses the engine default")
	require.NotNil(t, results[1].Result.ErrorDetails)
	assert.Equal(t, "timeout_error", results[1].Result.ErrorDetails.Type)
}

func TestRegistry_CallTimeout(t *testing.T) {
	reg := newBashRegistryForTest(t)
	d, ok := reg.CallTimeout("Bash", map[string]interface{}{"timeout": float64(1500)})
	assert.True(t, ok)
	assert.Equal(t, 1500*time.Millisecond, d)
	_, ok = reg.CallTimeout("Read", map[string]interface{}{"timeout": float64(1500)})
	assert.False(t, ok)
}
