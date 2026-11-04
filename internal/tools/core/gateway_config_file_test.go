package core

import (
	"context"
	"strings"
	"testing"

	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeFileGateway adds full-file editing to fakeConfigGateway.
type fakeFileGateway struct {
	fakeConfigGateway
	redacted string
	patch    map[string]interface{}
	proposed []string
}

func (f *fakeFileGateway) RedactedConfigFile(context.Context) (string, error) { return f.redacted, nil }

func (f *fakeFileGateway) ConfigPatchFromFile(_ context.Context, proposed string) (map[string]interface{}, error) {
	f.proposed = append(f.proposed, proposed)
	return f.patch, nil
}

func TestGatewayConfig_RawReturnsRedactedFile(t *testing.T) {
	gw := &fakeFileGateway{redacted: `{"api_key": "[redacted]", "port": 19999}`}
	res, err := newConfigTool(gw, &captureRequester{}).Execute(context.Background(), map[string]interface{}{"action": "config", "raw": true})
	require.NoError(t, err)
	require.True(t, res.Success, res.Error)
	assert.Contains(t, res.Content, `"api_key": "[redacted]"`)
	assert.Contains(t, res.Content, "config_file")
}

// config_file is diffed into a patch and planned and approved like any
// other update; the approval prompt shows old -> new values.
func TestUpdateConfig_ConfigFileGoesThroughApproval(t *testing.T) {
	gw := &fakeFileGateway{patch: map[string]interface{}{"tools": map[string]interface{}{"max_tool_chains": 30.0}}}
	gw.plan = &types.ConfigUpdateResult{Changes: []types.ConfigChange{{
		Path: "tools.max_tool_chains", OldValue: "25", Value: "30", Mode: types.ConfigChangeRestart,
		SecuritySensitive: true, Risk: "tool enablement, sandbox or command policy",
	}}}
	req := &captureRequester{}
	res, err := newConfigTool(gw, req).Execute(context.Background(), map[string]interface{}{
		"action": "update_config", "config_file": `{"tools": {"max_tool_chains": 30}}`,
	})
	require.NoError(t, err)
	require.True(t, res.Success, res.Error)
	require.Len(t, gw.proposed, 1)
	assert.Equal(t, 1, gw.planCalls)
	assert.Empty(t, gw.applied, "nothing may apply before approval")
	var shown strings.Builder
	for _, f := range req.action.Fields {
		shown.WriteString(f.Name + "=" + f.Value + "\n")
	}
	assert.Contains(t, shown.String(), "25 -> 30")
	assert.Contains(t, shown.String(), "SECURITY-SENSITIVE: tool enablement")
}

func TestUpdateConfig_ConfigFileArgErrors(t *testing.T) {
	gw := &fakeFileGateway{patch: map[string]interface{}{}}
	for _, args := range []map[string]interface{}{
		{"action": "update_config", "config_file": "{}", "config": map[string]interface{}{"port": 1.0}},
	} {
		res, err := newConfigTool(gw, &captureRequester{}).Execute(context.Background(), args)
		require.NoError(t, err)
		assert.False(t, res.Success)
	}
	// An empty diff needs no approval and writes nothing.
	req := &captureRequester{}
	res, err := newConfigTool(gw, req).Execute(context.Background(), map[string]interface{}{"action": "update_config", "config_file": "{}"})
	require.NoError(t, err)
	assert.Contains(t, res.Content+res.Error, "No change")
	assert.Equal(t, 0, req.calls)
	assert.Equal(t, 0, gw.planCalls)

	// A gateway without full-file support refuses config_file.
	plain := &fakeConfigGateway{plan: samplePlan()}
	res, err = newConfigTool(plain, &captureRequester{}).Execute(context.Background(), map[string]interface{}{"action": "update_config", "config_file": "{}"})
	require.NoError(t, err)
	assert.False(t, res.Success)
}

// A change that lowers human approval is allowed but leads the prompt with
// a warning and marks the title.
func TestUpdateConfig_ApprovalWeakeningWarnsInPrompt(t *testing.T) {
	gw := &fakeConfigGateway{plan: &types.ConfigUpdateResult{
		Changes: []types.ConfigChange{{
			Path: "mqtt.approval.required", OldValue: "(absent)", Value: "false", Mode: types.ConfigChangeRestart,
			SecuritySensitive: true, Risk: "human approval",
		}},
		Warnings: []string{"mqtt.approval.required turned off: every write runs without asking"},
	}}
	req := &captureRequester{}
	res, err := newConfigTool(gw, req).Execute(context.Background(), map[string]interface{}{
		"action": "update_config", "config": map[string]interface{}{"mqtt.approval.required": false},
	})
	require.NoError(t, err)
	require.True(t, res.Success, res.Error)
	require.NotEmpty(t, req.action.Fields)
	assert.Equal(t, "WARNING", req.action.Fields[0].Name)
	assert.Contains(t, req.action.Fields[0].Value, "LOWERS HUMAN APPROVAL")
	assert.Contains(t, req.action.Fields[0].Value, "mqtt.approval.required turned off")
	assert.Contains(t, req.action.Title, "LOWERS HUMAN APPROVAL")
	assert.Contains(t, req.action.Audit["warnings"], "mqtt.approval.required turned off")
}
