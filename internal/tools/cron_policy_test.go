package tools

import (
	"context"
	"testing"

	"conduit/internal/approval"
	"conduit/internal/config"
	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// C2: the registry wires the Bash denylist into Cron jobType=system.

// schedOnlyGateway implements just ScheduleJob; any other call panics.
type schedOnlyGateway struct {
	types.GatewayService
	jobs []*types.SchedulerJob
}

func (g *schedOnlyGateway) ScheduleJob(j *types.SchedulerJob) error {
	g.jobs = append(g.jobs, j)
	return nil
}

type recordingRequester struct{ calls int }

func (r *recordingRequester) Request(context.Context, approval.Action, approval.ExecuteFunc) (*approval.Ticket, error) {
	r.calls++
	return &approval.Ticket{ID: "apr_test", Code: "ABC234"}, nil
}

func TestCronSystemJob_RegistryAppliesBashDenylist(t *testing.T) {
	for _, mode := range []string{"", config.DenylistModeCommandPosition} {
		t.Run("mode="+mode, func(t *testing.T) {
			dir := t.TempDir()
			r := NewRegistry(config.ToolsConfig{
				EnabledTools: []string{"Cron"},
				Sandbox:      config.SandboxConfig{WorkspaceDir: dir, AllowedPaths: []string{dir}, DenylistMode: mode},
			})
			gw := &schedOnlyGateway{}
			req := &recordingRequester{}
			r.SetServices(&types.ToolServices{Gateway: gw, Approvals: req})
			cron, ok := r.getTool("Cron")
			require.True(t, ok)

			ctx := approval.WithInteractiveOrigin(context.Background(), approval.Origin{
				Source: "tui", ChannelID: "tui", UserID: "u1", SessionKey: "tui_u1",
				Notify: func(context.Context, approval.Notice) error { return nil },
			})
			for _, cmd := range []string{"rm -rf /", "sudo reboot", "cd /tmp && shutdown -h now"} {
				res, err := cron.Execute(ctx, map[string]interface{}{
					"action": "schedule", "jobType": "system", "schedule": "0 2 * * *", "command": cmd,
				})
				require.NoError(t, err)
				assert.False(t, res.Success, "%q must be refused", cmd)
				assert.Contains(t, res.Error, "Bash command policy", cmd)
			}
			assert.Zero(t, req.calls, "denied commands never reach the approval prompt")
			assert.Empty(t, gw.jobs)

			res, err := cron.Execute(ctx, map[string]interface{}{
				"action": "schedule", "jobType": "system", "schedule": "0 2 * * *", "command": "/usr/local/bin/backup.sh",
			})
			require.NoError(t, err)
			assert.True(t, res.Success, res.Error)
			assert.Equal(t, 1, req.calls, "an allowed command goes to approval")
			assert.Empty(t, gw.jobs, "and is not installed before approval")
		})
	}
}
