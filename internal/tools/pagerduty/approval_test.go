//go:build with_pagerduty

package pagerduty

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"sync"
	"testing"

	"conduit/internal/approval"
	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for H11: PagerDuty write actions ran on the model's own
// confirmed=true flag (acknowledge, snooze and add_note on nothing at all),
// including from cron and heartbeat turns with no human present.

type pdRequest struct{ method, path, body string }

type pdApprovalHarness struct {
	tool   *PagerDutyTool
	mgr    *approval.Manager
	origin approval.Origin

	mu       sync.Mutex
	requests []pdRequest
	notices  []approval.Notice
}

func newPDApprovalHarness(t *testing.T) *pdApprovalHarness {
	t.Helper()
	h := &pdApprovalHarness{mgr: approval.NewManager(approval.Config{})}
	t.Cleanup(h.mgr.Close)

	tool, server := setupTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.requests = append(h.requests, pdRequest{r.Method, r.URL.Path, string(body)})
		h.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"incidents": []interface{}{}, "incident": map[string]interface{}{"id": "PINC001"}})
		case r.URL.Path == "/incidents" || r.URL.Path == "/incidents/PINC001/notes":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"incident": map[string]interface{}{"id": "PNEW", "status": "triggered"}, "note": map[string]interface{}{"id": "N1"}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"incident": map[string]interface{}{"id": "PINC001", "status": "resolved"}})
		}
	})
	t.Cleanup(server.Close)
	tool.services = &types.ToolServices{Approvals: h.mgr}
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

func (h *pdApprovalHarness) interactive() context.Context {
	return approval.WithInteractiveOrigin(context.Background(), h.origin)
}

func (h *pdApprovalHarness) writes() []pdRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []pdRequest
	for _, r := range h.requests {
		if r.method != http.MethodGet {
			out = append(out, r)
		}
	}
	return out
}

var pdCodeRe = regexp.MustCompile(`APPROVAL NEEDED \[([A-Z0-9]{6})\]`)

func (h *pdApprovalHarness) approve(t *testing.T) string {
	t.Helper()
	h.mu.Lock()
	var code, prompt string
	for _, n := range h.notices {
		if m := pdCodeRe.FindStringSubmatch(n.Text); m != nil {
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

func pdWriteCases() map[string]map[string]interface{} {
	return map[string]map[string]interface{}{
		"acknowledge": {"action": "acknowledge", "incident_id": "PINC001"},
		"snooze":      {"action": "snooze", "incident_id": "PINC001", "snooze_duration": float64(3600)},
		"add_note":    {"action": "add_note", "incident_id": "PINC001", "note": "looking"},
		"resolve":     {"action": "resolve", "incident_id": "PINC001", "confirmed": true},
		"trigger":     {"action": "trigger", "title": "Disk full", "confirmed": true},
	}
}

func TestPagerDutyApproval_WritesPendUntilApprovedThenRunOnce(t *testing.T) {
	for name, args := range pdWriteCases() {
		t.Run(name, func(t *testing.T) {
			h := newPDApprovalHarness(t)
			res, err := h.tool.Execute(h.interactive(), args)
			require.NoError(t, err)
			assert.True(t, res.Success, res.Error)
			assert.Equal(t, "pending", res.Data["approval_status"])
			assert.Contains(t, res.Content, "NOT RUN YET")
			assert.Empty(t, h.writes(), "%s ran before approval", name)

			// Changing the caller's args after the request has no effect.
			args["incident_id"] = "POTHER"
			args["title"] = "changed"

			prompt := h.approve(t)
			assert.Contains(t, prompt, "PagerDuty")
			writes := h.writes()
			require.Len(t, writes, 1, "approved %s must run exactly once", name)
			assert.NotContains(t, writes[0].path, "POTHER")
			assert.NotContains(t, writes[0].body, "changed")
		})
	}
}

func TestPagerDutyApproval_NonInteractiveRefused(t *testing.T) {
	for name, args := range pdWriteCases() {
		for origin, ctx := range map[string]context.Context{
			"cron":      approval.WithNonInteractive(context.Background(), "cron"),
			"heartbeat": approval.WithNonInteractive(context.Background(), "heartbeat"),
			"unknown":   context.Background(),
		} {
			t.Run(name+"/"+origin, func(t *testing.T) {
				h := newPDApprovalHarness(t)
				res, err := h.tool.Execute(ctx, args)
				require.NoError(t, err)
				assert.False(t, res.Success)
				assert.Equal(t, "refused_noninteractive", res.Data["approval_status"])
				assert.Empty(t, h.writes())
				assert.Empty(t, h.mgr.Pending("s1"))
			})
		}
	}
}

func TestPagerDutyApproval_NoApproverFailsClosed(t *testing.T) {
	tool, server := setupTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no request may reach PagerDuty, got %s %s", r.Method, r.URL.Path)
	})
	defer server.Close()
	res, err := tool.Execute(context.Background(), map[string]interface{}{"action": "acknowledge", "incident_id": "PINC001"})
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.Equal(t, "refused_no_approver", res.Data["approval_status"])
}

func TestPagerDutyApproval_ConfirmedFlagIsNotEnough(t *testing.T) {
	h := newPDApprovalHarness(t)
	res, err := h.tool.Execute(approval.WithNonInteractive(context.Background(), "cron"),
		map[string]interface{}{"action": "resolve", "incident_id": "PINC001", "confirmed": true})
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.Empty(t, h.writes())

	// Dangerous actions still also want confirmed=true, as before.
	res, err = h.tool.Execute(h.interactive(), map[string]interface{}{"action": "resolve", "incident_id": "PINC001"})
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.Contains(t, res.Error, "confirmed=true")
}

func TestPagerDutyApproval_ReadsAndInvalidArgsNeedNoApproval(t *testing.T) {
	h := newPDApprovalHarness(t)
	cron := approval.WithNonInteractive(context.Background(), "cron")
	res, err := h.tool.Execute(cron, map[string]interface{}{"action": "list_incidents"})
	require.NoError(t, err)
	assert.True(t, res.Success, res.Error)
	res, err = h.tool.Execute(cron, map[string]interface{}{"action": "get_incident", "incident_id": "PINC001"})
	require.NoError(t, err)
	assert.True(t, res.Success, res.Error)

	// Invalid writes are rejected up front, without asking anyone.
	res, err = h.tool.Execute(h.interactive(), map[string]interface{}{"action": "snooze", "incident_id": "PINC001"})
	require.NoError(t, err)
	assert.Contains(t, res.Error, "snooze_duration")
	assert.Empty(t, h.mgr.Pending("s1"))
	assert.Empty(t, h.writes())
}

func TestPagerDutyApproval_PromptRedactsSecrets(t *testing.T) {
	h := newPDApprovalHarness(t)
	_, err := h.tool.Execute(h.interactive(), map[string]interface{}{
		"action": "add_note", "incident_id": "PINC001", "note": "rotated API_TOKEN=tok_0123456789abcdef",
	})
	require.NoError(t, err)
	prompt := h.approve(t)
	assert.NotContains(t, prompt, "tok_0123456789abcdef")
	assert.Contains(t, prompt, "Incident: PINC001")
}

// approval.required / approval.exempt (H11 follow-up).

func TestPagerDutyApproval_ConfigDisabledRunsDirectly(t *testing.T) {
	for name, args := range pdWriteCases() {
		t.Run(name, func(t *testing.T) {
			h := newPDApprovalHarness(t)
			off := false
			h.tool.config.Approval.Required = &off
			res, err := h.tool.Execute(approval.WithNonInteractive(context.Background(), "cron"), args)
			require.NoError(t, err)
			assert.True(t, res.Success, res.Error)
			assert.Nil(t, res.Data["approval_status"])
			assert.Len(t, h.writes(), 1)
			assert.Empty(t, h.mgr.Pending("s1"))
		})
	}
}

func TestPagerDutyApproval_ExemptActionsSkipOthersStillPrompt(t *testing.T) {
	h := newPDApprovalHarness(t)
	h.tool.config.Approval.Exempt = []string{"acknowledge", "add_note"}
	cases := pdWriteCases()
	for _, name := range []string{"acknowledge", "add_note"} {
		res, err := h.tool.Execute(h.interactive(), cases[name])
		require.NoError(t, err)
		assert.True(t, res.Success, "%s: %s", name, res.Error)
		assert.Nil(t, res.Data["approval_status"], name)
	}
	assert.Len(t, h.writes(), 2)
	for _, name := range []string{"snooze", "resolve", "trigger"} {
		res, err := h.tool.Execute(h.interactive(), cases[name])
		require.NoError(t, err)
		assert.Equal(t, "pending", res.Data["approval_status"], name)
	}
	assert.Len(t, h.writes(), 2, "a non-exempt action ran without approval")

	// Invalid args are still refused before anything is sent.
	res, err := h.tool.Execute(h.interactive(), map[string]interface{}{"action": "add_note", "incident_id": "PINC001"})
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.Len(t, h.writes(), 2)
}
