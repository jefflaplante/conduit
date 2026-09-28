//go:build with_ssh

package ssh

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"conduit/internal/approval"
	"conduit/internal/config"
	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type execCall struct {
	host, command string
	timeout       time.Duration
}

// sshApprovalHarness wires a real approval.Manager and a recording client
// into an SSHTool (conduit-w3l7).
type sshApprovalHarness struct {
	tool   *SSHTool
	mgr    *approval.Manager
	origin approval.Origin

	mu      sync.Mutex
	calls   []execCall
	notices []approval.Notice
}

func newSSHApprovalHarness(t *testing.T, cfg *config.RemoteSSHConfig) *sshApprovalHarness {
	t.Helper()
	h := &sshApprovalHarness{mgr: approval.NewManager(approval.Config{})}
	t.Cleanup(h.mgr.Close)
	tool, err := NewSSHTool(&types.ToolServices{Approvals: h.mgr}, cfg)
	require.NoError(t, err)
	t.Cleanup(tool.Close)
	tool.SetClient(&mockClient{executeFunc: func(_ context.Context, host, command string, timeout time.Duration) (*ExecutionResult, error) {
		h.mu.Lock()
		h.calls = append(h.calls, execCall{host, command, timeout})
		h.mu.Unlock()
		return &ExecutionResult{Host: host, Command: command, Stdout: "ran " + command}, nil
	}})
	h.tool = tool
	h.origin = approval.Origin{
		Source: "tui", ChannelID: "tui", UserID: "owner", SessionKey: "s1",
		Notify: func(_ context.Context, n approval.Notice) error {
			h.mu.Lock()
			h.notices = append(h.notices, n)
			h.mu.Unlock()
			return nil
		},
	}
	return h
}

func (h *sshApprovalHarness) interactive() context.Context {
	return approval.WithInteractiveOrigin(context.Background(), h.origin)
}

func (h *sshApprovalHarness) reply(text string) {
	h.mgr.HandleReply(context.Background(), approval.Inbound{
		ChannelID: h.origin.ChannelID, UserID: h.origin.UserID, SessionKey: h.origin.SessionKey,
		Text: text, Notify: h.origin.Notify,
	})
	h.mgr.Wait()
}

func (h *sshApprovalHarness) snapshot() ([]execCall, []approval.Notice) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]execCall(nil), h.calls...), append([]approval.Notice(nil), h.notices...)
}

var sshCodeRe = regexp.MustCompile(`APPROVAL NEEDED \[([A-Z0-9]{6})\]`)

func (h *sshApprovalHarness) prompt(t *testing.T) (code, text string) {
	t.Helper()
	_, notices := h.snapshot()
	for _, n := range notices {
		if m := sshCodeRe.FindStringSubmatch(n.Text); m != nil {
			return m[1], n.Text
		}
	}
	t.Fatal("no approval prompt was delivered")
	return "", ""
}

func execArgs(command string) map[string]interface{} {
	return map[string]interface{}{"action": "exec", "host": "test-host", "command": command, "timeout": float64(45)}
}

func TestSSHApproval_DangerousNeverRunsWithoutApprovedCode(t *testing.T) {
	h := newSSHApprovalHarness(t, testSSHConfig())

	res, err := h.tool.Execute(h.interactive(), execArgs("rm /tmp/app.lock"))
	require.NoError(t, err)
	assert.True(t, res.Success)
	assert.Contains(t, res.Content, "NOT RUN YET")
	assert.Equal(t, "pending", res.Data["approval_status"])
	assert.Equal(t, true, res.Data["requires_approval"])
	code, prompt := h.prompt(t)
	assert.NotContains(t, res.Content, code)

	h.reply("YES ZZZ222") // wrong code
	h.reply("NO " + code) // deny
	h.reply("YES " + code)
	calls, _ := h.snapshot()
	assert.Empty(t, calls, "dangerous command ran without an approved code")

	for _, want := range []string{
		"Run SSH command on test-host (admin@192.168.1.100:22)",
		"Host: test-host (admin@192.168.1.100:22)",
		"Command: rm /tmp/app.lock",
		"Timeout: 45s",
		"Risk: dangerous tier",
	} {
		assert.Contains(t, prompt, want)
	}
}

func TestSSHApproval_ApprovedCodeRunsExactlyFrozenCommand(t *testing.T) {
	h := newSSHApprovalHarness(t, testSSHConfig())

	args := execArgs("systemctl restart app")
	res, err := h.tool.Execute(h.interactive(), args)
	require.NoError(t, err)
	require.Equal(t, "pending", res.Data["approval_status"])
	args["command"] = "rm /etc/passwd" // must not affect the approved op
	args["host"] = "prod-web"

	code, _ := h.prompt(t)
	h.reply("YES " + code)
	h.reply("YES " + code) // replay is void

	calls, notices := h.snapshot()
	require.Len(t, calls, 1)
	assert.Equal(t, execCall{"test-host", "systemctl restart app", 45 * time.Second}, calls[0])
	assert.Contains(t, notices[len(notices)-2].Text, "ran systemctl restart app")
}

func TestSSHApproval_NonInteractiveFailsClosed(t *testing.T) {
	for name, ctx := range map[string]context.Context{
		"cron":     approval.WithNonInteractive(context.Background(), "cron"),
		"subagent": approval.WithNonInteractive(context.Background(), "subagent"),
		"unknown":  context.Background(),
	} {
		t.Run(name, func(t *testing.T) {
			h := newSSHApprovalHarness(t, testSSHConfig())
			res, err := h.tool.Execute(ctx, execArgs("rm /tmp/app.lock"))
			require.NoError(t, err)
			assert.False(t, res.Success)
			assert.Equal(t, "refused_noninteractive", res.Data["approval_status"])
			calls, notices := h.snapshot()
			assert.Empty(t, calls)
			assert.Empty(t, notices)
			assert.Empty(t, h.mgr.Pending("s1"))
		})
	}
}

func TestSSHApproval_ReadTierUnaffected(t *testing.T) {
	h := newSSHApprovalHarness(t, testSSHConfig())
	res, err := h.tool.Execute(approval.WithNonInteractive(context.Background(), "cron"), execArgs("uptime"))
	require.NoError(t, err)
	assert.True(t, res.Success, res.Error)
	calls, notices := h.snapshot()
	require.Len(t, calls, 1)
	assert.Equal(t, "uptime", calls[0].command)
	assert.Empty(t, notices)
}

func TestSSHApproval_AbsentRequireApprovalDefaultsToGated(t *testing.T) {
	cfg := testSSHConfig()
	cfg.Security.RequireApproval = nil
	h := newSSHApprovalHarness(t, cfg)
	res, err := h.tool.Execute(h.interactive(), execArgs("rm /tmp/app.lock"))
	require.NoError(t, err)
	assert.Equal(t, "pending", res.Data["approval_status"], "absent require_approval must not disable gating")

	cfg = testSSHConfig()
	cfg.Security.RequireApproval = []string{}
	h = newSSHApprovalHarness(t, cfg)
	res, err = h.tool.Execute(h.interactive(), execArgs("rm /tmp/app.lock"))
	require.NoError(t, err)
	assert.True(t, res.Success, res.Error)
	calls, _ := h.snapshot()
	assert.Len(t, calls, 1, "explicit [] disables gating")
}

func TestSSHApproval_PromptRedactsSecrets(t *testing.T) {
	h := newSSHApprovalHarness(t, testSSHConfig())
	_, err := h.tool.Execute(h.interactive(), execArgs("systemctl set-environment API_TOKEN=tok_0123456789abcdef"))
	require.NoError(t, err)
	_, prompt := h.prompt(t)
	assert.NotContains(t, prompt, "tok_0123456789abcdef")
	assert.Contains(t, prompt, "API_TOKEN=[redacted]")
}

func TestSSHApproval_GroupFreezesHostList(t *testing.T) {
	cfg := testSSHConfig()
	cfg.Hosts[0].Groups = []string{"ops"}
	h := newSSHApprovalHarness(t, cfg)

	res, err := h.tool.Execute(h.interactive(), map[string]interface{}{
		"action": "exec_group", "group": "ops", "command": "systemctl restart app",
	})
	require.NoError(t, err)
	assert.Equal(t, "pending", res.Data["approval_status"])
	assert.Equal(t, "ops", res.Data["group"])
	_, prompt := h.prompt(t)
	assert.Contains(t, prompt, "Run SSH command on 1 host(s) in group ops")
	assert.Contains(t, prompt, "Hosts: test-host (admin@192.168.1.100:22)")
	assert.True(t, strings.Contains(prompt, "Command: systemctl restart app"))

	res, err = h.tool.Execute(approval.WithNonInteractive(context.Background(), "heartbeat"), map[string]interface{}{
		"action": "exec_group", "group": "ops", "command": "systemctl restart app",
	})
	require.NoError(t, err)
	assert.Equal(t, "refused_noninteractive", res.Data["approval_status"])
}
