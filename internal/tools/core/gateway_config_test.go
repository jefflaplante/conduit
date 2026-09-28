package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"conduit/internal/approval"
	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeConfigGateway implements the ConfigUpdater part of the gateway; the
// embedded (nil) GatewayService is never called by update_config.
type fakeConfigGateway struct {
	types.GatewayService
	plan      *types.ConfigUpdateResult
	planErr   error
	applied   []map[string]interface{}
	applyRes  *types.ConfigUpdateResult
	planCalls int
}

func (f *fakeConfigGateway) PlanConfigUpdate(_ context.Context, patch map[string]interface{}) (*types.ConfigUpdateResult, error) {
	f.planCalls++
	return f.plan, f.planErr
}

func (f *fakeConfigGateway) ApplyConfigUpdate(_ context.Context, patch map[string]interface{}) (*types.ConfigUpdateResult, error) {
	f.applied = append(f.applied, patch)
	return f.applyRes, nil
}

type captureRequester struct {
	action approval.Action
	exec   approval.ExecuteFunc
	err    error
	calls  int
}

func (c *captureRequester) Request(_ context.Context, a approval.Action, e approval.ExecuteFunc) (*approval.Ticket, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	c.action, c.exec = a, e
	return &approval.Ticket{ID: "apr_1", Code: "SECRET1", Fingerprint: a.Fingerprint, ExpiresAt: time.Now().Add(time.Minute)}, nil
}

func samplePlan() *types.ConfigUpdateResult {
	return &types.ConfigUpdateResult{Changes: []types.ConfigChange{
		{Path: "ai.providers.z-ai.timeout_seconds", Value: "600", Mode: types.ConfigChangeLive},
		{Path: "tools.sandbox.allowed_paths", Value: `["/tmp","/srv"]`, Mode: types.ConfigChangeRestart, SecuritySensitive: true},
	}}
}

func newConfigTool(gw types.GatewayService, r approval.Requester) *GatewayTool {
	return NewGatewayTool(&types.ToolServices{Gateway: gw, Approvals: r})
}

func TestUpdateConfig_ApprovedRunAppliesFrozenPatch(t *testing.T) {
	gw := &fakeConfigGateway{
		plan: samplePlan(),
		applyRes: &types.ConfigUpdateResult{
			Changes:    samplePlan().Changes,
			Applied:    true,
			ConfigPath: "/etc/conduit/config.json",
			BackupPath: "/etc/conduit/config.json.bak",
			Readback: map[string]interface{}{
				"ai.providers.z-ai.timeout_seconds": 600.0,
				"tools.sandbox.allowed_paths":       []interface{}{"/tmp", "/srv"},
			},
		},
	}
	req := &captureRequester{}
	tool := newConfigTool(gw, req)
	args := map[string]interface{}{"action": "update_config", "config": map[string]interface{}{
		"ai.providers.z-ai.timeout_seconds": 600.0,
		"tools.sandbox.allowed_paths":       []interface{}{"/tmp", "/srv"},
	}}

	res, err := tool.Execute(context.Background(), args)
	require.NoError(t, err)
	require.True(t, res.Success, res.Error)
	assert.Contains(t, res.Content, "NOT RUN YET")
	assert.NotContains(t, res.Content, "SECRET1", "approval code must not reach the model")
	assert.Empty(t, gw.applied, "nothing may apply before approval")
	assert.Equal(t, "gateway.update_config", req.action.Kind)

	var shown strings.Builder
	for _, f := range req.action.Fields {
		shown.WriteString(f.Name + "=" + f.Value + "\n")
	}
	assert.Contains(t, shown.String(), "tools.sandbox.allowed_paths")
	assert.Contains(t, shown.String(), "SECURITY-SENSITIVE")
	assert.Contains(t, shown.String(), "saved, needs restart")
	assert.Contains(t, req.action.Audit["security_keys"], "tools.sandbox.allowed_paths")

	// The model's args map changing afterwards cannot change what runs.
	args["config"].(map[string]interface{})["tools.sandbox.allowed_paths"] = []interface{}{"/"}

	out, err := req.exec(context.Background(), approval.Ticket{Fingerprint: req.action.Fingerprint})
	require.NoError(t, err)
	require.Len(t, gw.applied, 1)
	assert.Equal(t, []interface{}{"/tmp", "/srv"}, gw.applied[0]["tools.sandbox.allowed_paths"])
	assert.Contains(t, out, "Applied live")
	assert.Contains(t, out, "ai.providers.z-ai.timeout_seconds = 600")
	assert.Contains(t, out, "REQUIRES A RESTART")
	assert.Contains(t, out, "config.json.bak")
}

func TestUpdateConfig_NonInteractiveFailsClosed(t *testing.T) {
	gw := &fakeConfigGateway{plan: samplePlan()}
	req := &captureRequester{err: &approval.NonInteractiveError{Source: "cron"}}
	res, err := newConfigTool(gw, req).Execute(context.Background(), map[string]interface{}{
		"action": "update_config", "config": map[string]interface{}{"ai.providers.z-ai.timeout_seconds": 600.0},
	})
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.Equal(t, "refused_noninteractive", res.Data["approval_status"])
	assert.Contains(t, res.Error, "NOT RUN")
	assert.Empty(t, gw.applied)
}

func TestUpdateConfig_NoApproverFailsClosed(t *testing.T) {
	gw := &fakeConfigGateway{plan: samplePlan()}
	res, err := newConfigTool(gw, nil).Execute(context.Background(), map[string]interface{}{
		"action": "update_config", "config": map[string]interface{}{"ai.providers.z-ai.timeout_seconds": 600.0},
	})
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.Equal(t, "refused_no_approver", res.Data["approval_status"])
	assert.Empty(t, gw.applied)
}

func TestUpdateConfig_InvalidRejectedWithoutPrompt(t *testing.T) {
	gw := &fakeConfigGateway{planErr: errors.New("port 80 is out of range")}
	req := &captureRequester{}
	res, err := newConfigTool(gw, req).Execute(context.Background(), map[string]interface{}{
		"action": "update_config", "config": map[string]interface{}{"port": 80.0},
	})
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.Contains(t, res.Error, "nothing changed")
	assert.Contains(t, res.Error, "port 80")
	assert.Equal(t, 0, req.calls, "an invalid update must not prompt the owner")
	assert.Empty(t, gw.applied)
}

func TestUpdateConfig_NoChangeNeedsNoApproval(t *testing.T) {
	gw := &fakeConfigGateway{plan: &types.ConfigUpdateResult{Unchanged: []string{"port"}}}
	req := &captureRequester{}
	res, err := newConfigTool(gw, req).Execute(context.Background(), map[string]interface{}{
		"action": "update_config", "config": map[string]interface{}{"port": 18789.0},
	})
	require.NoError(t, err)
	assert.True(t, res.Success)
	assert.Contains(t, res.Content, "No change")
	assert.Equal(t, 0, req.calls)
}

func TestUpdateConfig_BadArgs(t *testing.T) {
	gw := &fakeConfigGateway{plan: samplePlan()}
	for _, args := range []map[string]interface{}{
		{"action": "update_config"},
		{"action": "update_config", "config": "port=1"},
		{"action": "update_config", "config": map[string]interface{}{}},
	} {
		res, err := newConfigTool(gw, &captureRequester{}).Execute(context.Background(), args)
		require.NoError(t, err)
		assert.False(t, res.Success)
	}
	assert.Equal(t, 0, gw.planCalls)
}
