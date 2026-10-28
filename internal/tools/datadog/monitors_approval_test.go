//go:build with_datadog

package datadog

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"

	"conduit/internal/approval"
	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for H11: mute_monitor / unmute_monitor ran on the
// model's own confirmed=true flag, including from cron and heartbeat turns.

type ddApprovalHarness struct {
	tool   *MonitorTool
	mgr    *approval.Manager
	origin approval.Origin

	mu      sync.Mutex
	writes  []string // "METHOD path?query body"
	notices []approval.Notice
}

func newDDApprovalHarness(t *testing.T) *ddApprovalHarness {
	t.Helper()
	h := &ddApprovalHarness{mgr: approval.NewManager(approval.Config{})}
	t.Cleanup(h.mgr.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			body, _ := io.ReadAll(r.Body)
			h.mu.Lock()
			h.writes = append(h.writes, r.Method+" "+r.URL.String()+" "+string(body))
			h.mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]interface{}{})
	}))
	t.Cleanup(srv.Close)
	h.tool = createTestTool(t, srv)
	h.tool.services = &types.ToolServices{Approvals: h.mgr}
	h.origin = approval.Origin{
		Source: "telegram", ChannelID: "telegram", UserID: "owner", SessionKey: "s1",
		Notify: func(_ context.Context, n approval.Notice) error {
			h.mu.Lock()
			h.notices = append(h.notices, n)
			h.mu.Unlock()
			return nil
		},
	}
	return h
}

func (h *ddApprovalHarness) interactive() context.Context {
	return approval.WithInteractiveOrigin(context.Background(), h.origin)
}

func (h *ddApprovalHarness) sent() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.writes...)
}

var ddCodeRe = regexp.MustCompile(`APPROVAL NEEDED \[([A-Z0-9]{6})\]`)

func (h *ddApprovalHarness) approve(t *testing.T) string {
	t.Helper()
	h.mu.Lock()
	var code, prompt string
	for _, n := range h.notices {
		if m := ddCodeRe.FindStringSubmatch(n.Text); m != nil {
			code, prompt = m[1], n.Text
		}
	}
	h.mu.Unlock()
	require.NotEmpty(t, code, "no approval prompt was delivered")
	for i := 0; i < 2; i++ { // a replayed approval is void
		h.mgr.HandleReply(context.Background(), approval.Inbound{
			ChannelID: h.origin.ChannelID, UserID: h.origin.UserID, SessionKey: h.origin.SessionKey,
			Text: "YES " + code, Notify: h.origin.Notify,
		})
		h.mgr.Wait()
	}
	return prompt
}

func ddWriteCases() map[string]map[string]interface{} {
	return map[string]map[string]interface{}{
		"mute_monitor":   {"action": "mute_monitor", "monitor_id": float64(123), "scope": "host:web-1", "end": float64(1712345678), "confirmed": true},
		"unmute_monitor": {"action": "unmute_monitor", "monitor_id": float64(123), "scope": "host:web-1", "confirmed": true},
	}
}

func TestMonitorApproval_WritesPendUntilApprovedThenRunOnce(t *testing.T) {
	for name, args := range ddWriteCases() {
		t.Run(name, func(t *testing.T) {
			h := newDDApprovalHarness(t)
			res, err := h.tool.Execute(h.interactive(), args)
			require.NoError(t, err)
			assert.True(t, res.Success, res.Error)
			assert.Equal(t, "pending", res.Data["approval_status"])
			assert.Empty(t, h.sent(), "%s ran before approval", name)

			args["monitor_id"] = float64(999) // must not affect the approved op
			args["scope"] = "*"

			prompt := h.approve(t)
			assert.Contains(t, prompt, "Monitor: 123")
			assert.Contains(t, prompt, "Scope: host:web-1")
			sent := h.sent()
			require.Len(t, sent, 1, "approved %s must run exactly once", name)
			assert.Contains(t, sent[0], "/api/v1/monitor/123/")
			assert.Contains(t, sent[0], "host")
			assert.NotContains(t, sent[0], "999")
		})
	}
}

func TestMonitorApproval_NonInteractiveRefused(t *testing.T) {
	for name, args := range ddWriteCases() {
		for origin, ctx := range map[string]context.Context{
			"cron":      approval.WithNonInteractive(context.Background(), "cron"),
			"heartbeat": approval.WithNonInteractive(context.Background(), "heartbeat"),
			"unknown":   context.Background(),
		} {
			t.Run(name+"/"+origin, func(t *testing.T) {
				h := newDDApprovalHarness(t)
				res, err := h.tool.Execute(ctx, args)
				require.NoError(t, err)
				assert.False(t, res.Success)
				assert.Equal(t, "refused_noninteractive", res.Data["approval_status"])
				assert.Empty(t, h.sent())
				assert.Empty(t, h.mgr.Pending("s1"))
			})
		}
	}
}

func TestMonitorApproval_NoApproverFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no request may reach Datadog, got %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()
	tool := createTestTool(t, srv)
	res, err := tool.Execute(context.Background(), map[string]interface{}{"action": "mute_monitor", "monitor_id": 123, "confirmed": true})
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.Equal(t, "refused_no_approver", res.Data["approval_status"])
}

func TestMonitorApproval_ReadsAndInvalidArgsNeedNoApproval(t *testing.T) {
	h := newDDApprovalHarness(t)
	res, err := h.tool.Execute(approval.WithNonInteractive(context.Background(), "cron"),
		map[string]interface{}{"action": "list_monitors"})
	require.NoError(t, err)
	assert.True(t, res.Success, res.Error)

	// Without confirmed=true, or without monitor_id, nothing is requested.
	res, err = h.tool.Execute(h.interactive(), map[string]interface{}{"action": "mute_monitor", "monitor_id": 123})
	require.NoError(t, err)
	assert.Contains(t, res.Error, "requires confirmation")
	res, err = h.tool.Execute(h.interactive(), map[string]interface{}{"action": "unmute_monitor", "confirmed": true})
	require.NoError(t, err)
	assert.Contains(t, res.Error, "monitor_id parameter is required")
	assert.Empty(t, h.mgr.Pending("s1"))
	assert.Empty(t, h.sent())
}

// approval.required / approval.exempt (H11 follow-up).

func TestMonitorApproval_ConfigDisabledRunsDirectly(t *testing.T) {
	for name, args := range ddWriteCases() {
		t.Run(name, func(t *testing.T) {
			h := newDDApprovalHarness(t)
			off := false
			h.tool.config.Approval.Required = &off
			res, err := h.tool.Execute(approval.WithNonInteractive(context.Background(), "cron"), args)
			require.NoError(t, err)
			assert.True(t, res.Success, res.Error)
			assert.Nil(t, res.Data["approval_status"])
			assert.Len(t, h.sent(), 1)
			assert.Empty(t, h.mgr.Pending("s1"))

			// confirmed=true is still required.
			noConfirm := map[string]interface{}{}
			for k, v := range args {
				noConfirm[k] = v
			}
			delete(noConfirm, "confirmed")
			res, err = h.tool.Execute(h.interactive(), noConfirm)
			require.NoError(t, err)
			assert.False(t, res.Success)
			assert.Len(t, h.sent(), 1)
		})
	}
}

func TestMonitorApproval_ExemptActionSkipsOtherStillPrompts(t *testing.T) {
	h := newDDApprovalHarness(t)
	h.tool.config.Approval.Exempt = []string{"unmute_monitor"}
	cases := ddWriteCases()
	res, err := h.tool.Execute(h.interactive(), cases["unmute_monitor"])
	require.NoError(t, err)
	assert.True(t, res.Success, res.Error)
	assert.Nil(t, res.Data["approval_status"])
	assert.Len(t, h.sent(), 1)

	res, err = h.tool.Execute(h.interactive(), cases["mute_monitor"])
	require.NoError(t, err)
	assert.Equal(t, "pending", res.Data["approval_status"])
	assert.Len(t, h.sent(), 1, "mute ran without approval")
}
