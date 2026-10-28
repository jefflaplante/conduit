//go:build with_mqtt

package mqtt

import (
	"context"
	"regexp"
	"sync"
	"testing"

	"conduit/internal/approval"
	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for H11: publish sent any payload to any topic (retained
// allowed) on the model's say-so, including from cron and heartbeat turns.

type published struct {
	topic, payload string
	qos            byte
	retained       bool
}

type recordingMQTT struct {
	mockMQTTService
	mu   sync.Mutex
	sent []published
}

func (r *recordingMQTT) Publish(ctx context.Context, topic string, payload []byte, qos byte, retained bool) (*types.MQTTPublishResult, error) {
	r.mu.Lock()
	r.sent = append(r.sent, published{topic, string(payload), qos, retained})
	r.mu.Unlock()
	return r.mockMQTTService.Publish(ctx, topic, payload, qos, retained)
}

func (r *recordingMQTT) published() []published {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]published(nil), r.sent...)
}

type mqttApprovalHarness struct {
	tool   *MQTTTool
	svc    *recordingMQTT
	mgr    *approval.Manager
	origin approval.Origin

	mu      sync.Mutex
	notices []approval.Notice
}

func newMQTTApprovalHarness(t *testing.T) *mqttApprovalHarness {
	t.Helper()
	h := &mqttApprovalHarness{mgr: approval.NewManager(approval.Config{})}
	t.Cleanup(h.mgr.Close)
	h.svc = &recordingMQTT{mockMQTTService: mockMQTTService{status: types.MQTTServiceStatus{Connected: true, PublishAllowed: true}}}
	h.tool = NewMQTTTool(&types.ToolServices{MQTTService: h.svc, Approvals: h.mgr})
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

func (h *mqttApprovalHarness) interactive() context.Context {
	return approval.WithInteractiveOrigin(context.Background(), h.origin)
}

var mqttCodeRe = regexp.MustCompile(`APPROVAL NEEDED \[([A-Z0-9]{6})\]`)

func (h *mqttApprovalHarness) approve(t *testing.T) string {
	t.Helper()
	h.mu.Lock()
	var code, prompt string
	for _, n := range h.notices {
		if m := mqttCodeRe.FindStringSubmatch(n.Text); m != nil {
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

func publishArgs() map[string]interface{} {
	return map[string]interface{}{
		"action": "publish", "topic": "zigbee2mqtt/Lamp/set", "payload": `{"state":"ON"}`,
		"qos": float64(1), "retained": true,
	}
}

func TestMQTTApproval_PublishPendsUntilApprovedThenRunsOnce(t *testing.T) {
	h := newMQTTApprovalHarness(t)
	args := publishArgs()
	res, err := h.tool.Execute(h.interactive(), args)
	require.NoError(t, err)
	assert.True(t, res.Success, res.Error)
	assert.Equal(t, "pending", res.Data["approval_status"])
	assert.Contains(t, res.Content, "NOT RUN YET")
	assert.Empty(t, h.svc.published(), "published before approval")

	args["topic"] = "zigbee2mqtt/Lock/set" // must not affect the approved op
	args["payload"] = "UNLOCK"

	prompt := h.approve(t)
	assert.Contains(t, prompt, "Topic: zigbee2mqtt/Lamp/set")
	assert.Contains(t, prompt, "Retained: true")
	assert.Equal(t, []published{{"zigbee2mqtt/Lamp/set", `{"state":"ON"}`, 1, true}}, h.svc.published())
}

func TestMQTTApproval_NonInteractiveRefused(t *testing.T) {
	for origin, ctx := range map[string]context.Context{
		"cron":      approval.WithNonInteractive(context.Background(), "cron"),
		"heartbeat": approval.WithNonInteractive(context.Background(), "heartbeat"),
		"unknown":   context.Background(),
	} {
		t.Run(origin, func(t *testing.T) {
			h := newMQTTApprovalHarness(t)
			res, err := h.tool.Execute(ctx, publishArgs())
			require.NoError(t, err)
			assert.False(t, res.Success)
			assert.Equal(t, "refused_noninteractive", res.Data["approval_status"])
			assert.Empty(t, h.svc.published())
			assert.Empty(t, h.mgr.Pending("s1"))
		})
	}
}

func TestMQTTApproval_NoApproverFailsClosed(t *testing.T) {
	svc := &recordingMQTT{mockMQTTService: mockMQTTService{status: types.MQTTServiceStatus{PublishAllowed: true}}}
	tool := NewMQTTTool(&types.ToolServices{MQTTService: svc})
	res, err := tool.Execute(context.Background(), publishArgs())
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.Equal(t, "refused_no_approver", res.Data["approval_status"])
	assert.Empty(t, svc.published())
}

func TestMQTTApproval_ReadsAndRefusedPublishNeedNoApproval(t *testing.T) {
	h := newMQTTApprovalHarness(t)
	cron := approval.WithNonInteractive(context.Background(), "cron")
	for _, action := range []string{"status", "topics", "recent", "devices"} {
		res, err := h.tool.Execute(cron, map[string]interface{}{"action": action})
		require.NoError(t, err)
		assert.True(t, res.Success, "%s: %s", action, res.Error)
	}

	// Missing payload, or publishing disabled in config: refused up front.
	res, err := h.tool.Execute(h.interactive(), map[string]interface{}{"action": "publish", "topic": "a/b"})
	require.NoError(t, err)
	assert.False(t, res.Success)
	h.svc.status.PublishAllowed = false
	res, err = h.tool.Execute(h.interactive(), publishArgs())
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.Contains(t, res.Error, "publish failed")
	assert.Empty(t, h.mgr.Pending("s1"))
	assert.Empty(t, h.svc.published())
}
