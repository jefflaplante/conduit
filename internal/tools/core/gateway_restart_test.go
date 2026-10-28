package core

import (
	"context"
	"testing"

	"conduit/internal/approval"
	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// H14: Gateway restart is approval-gated, failing closed for non-interactive
// turns, instead of restarting on the model's word.

type restartGateway struct {
	types.GatewayService
	restarts int
}

func (g *restartGateway) RestartGateway(context.Context) error {
	g.restarts++
	return nil
}

func TestGatewayRestart_InteractivePendsUntilApproved(t *testing.T) {
	gw := &restartGateway{}
	req := &captureRequester{}
	tool := NewGatewayTool(&types.ToolServices{Gateway: gw, Approvals: req})

	res, err := tool.Execute(context.Background(), map[string]interface{}{"action": "restart"})
	require.NoError(t, err)
	require.True(t, res.Success, res.Error)
	assert.Equal(t, "pending", res.Data["approval_status"])
	assert.Contains(t, res.Content, "NOT RUN YET")
	assert.Zero(t, gw.restarts, "the gateway must not restart before approval")
	assert.Equal(t, 1, req.calls)
	assert.Equal(t, "gateway.restart", req.action.Kind)

	out, err := req.exec(context.Background(), approval.Ticket{Fingerprint: req.action.Fingerprint})
	require.NoError(t, err)
	assert.Equal(t, 1, gw.restarts)
	assert.Contains(t, out, "restart")
}

func TestGatewayRestart_NonInteractiveRefused(t *testing.T) {
	mgr := approval.NewManager(approval.Config{})
	defer mgr.Close()
	for _, src := range []string{"cron", "heartbeat", "subagent", "wake", "mcp"} {
		t.Run(src, func(t *testing.T) {
			gw := &restartGateway{}
			tool := NewGatewayTool(&types.ToolServices{Gateway: gw, Approvals: mgr})
			res, err := tool.Execute(approval.WithNonInteractive(context.Background(), src), map[string]interface{}{"action": "restart"})
			require.NoError(t, err)
			assert.False(t, res.Success)
			assert.Equal(t, "refused_noninteractive", res.Data["approval_status"])
			assert.Zero(t, gw.restarts)
		})
	}
}

func TestGatewayRestart_NoApproverRefused(t *testing.T) {
	gw := &restartGateway{}
	tool := NewGatewayTool(&types.ToolServices{Gateway: gw})
	res, err := tool.Execute(context.Background(), map[string]interface{}{"action": "restart"})
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.Equal(t, "refused_no_approver", res.Data["approval_status"])
	assert.Zero(t, gw.restarts)
}
