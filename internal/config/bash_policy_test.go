package config

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSandboxConfig_DenylistMode(t *testing.T) {
	var zero SandboxConfig
	mode, ok := zero.EffectiveDenylistMode()
	assert.Equal(t, DenylistModeLegacy, mode)
	assert.True(t, ok)
	assert.True(t, zero.StrictAutonomousEnabled(), "strict_autonomous defaults to true")

	var tc ToolsConfig
	require.NoError(t, json.Unmarshal([]byte(`{"sandbox":{"denylist_mode":"Command_Position","strict_autonomous":false}}`), &tc))
	mode, ok = tc.Sandbox.EffectiveDenylistMode()
	assert.Equal(t, DenylistModeCommandPosition, mode)
	assert.True(t, ok)
	assert.False(t, tc.Sandbox.StrictAutonomousEnabled())

	mode, ok = SandboxConfig{DenylistMode: "substring"}.EffectiveDenylistMode()
	assert.Equal(t, DenylistModeLegacy, mode)
	assert.False(t, ok)
}
