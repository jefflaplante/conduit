//go:build with_agentbus

package agentbus

import (
	"context"
	"encoding/json"
	"fmt"

	"conduit/internal/approval"
	"conduit/internal/config"
	"conduit/internal/tools/approvalgate"
	"conduit/internal/tools/types"

	"github.com/jefflaplante/agentbus/a2a"
)

// Approval gating for outgoing agentbus envelopes, following the MQTT
// tool's approval pattern (internal/tools/mqtt/approval.go): a publish
// that acts on the outside world (request/event envelopes from the send,
// request and task actions) is frozen and shown to the owner for a
// "YES <code>" reply; non-interactive turns and a missing approver fail
// closed. Response and error envelopes are exempt — they only answer an
// inbound request, and refusing to answer would strand the other agent.

// approvalKind is the stable machine label for a gated agentbus publish.
const approvalKind = "agentbus.publish"

// needsApproval reports whether an envelope type is a new outbound action
// (request/event) that needs the human gate, as opposed to a response or
// error reply, which only answers an inbound request and is exempt.
func needsApproval(typ a2a.Type) bool {
	switch typ {
	case a2a.TypeResponse, a2a.TypeError:
		return false
	default:
		return true
	}
}

// publishNeedsApproval mirrors internal/tools/mqtt.publishNeedsApproval:
// the zero status (settings unknown) requires approval, so a service that
// leaves the fields unset fails closed. ApprovalExemptTopics carries
// wildcard topic filters like mqtt.approval.exempt (e.g. a pre-approved
// telemetry event namespace).
func publishNeedsApproval(st types.MQTTServiceStatus, topic string) bool {
	if st.ApprovalDisabled {
		return false
	}
	for _, f := range st.ApprovalExemptTopics {
		if config.MQTTTopicMatches(f, topic) {
			return false
		}
	}
	return true
}

// approver returns the approval channel, or nil (fail closed).
func (t *AgentBusTool) approver() approval.Requester {
	if t.services == nil {
		return nil
	}
	return t.services.Approvals
}

// status returns the approval-related status via the MQTT service seam.
// A nil service yields the zero status, which requires approval (fail
// closed): the agentbus gate must not depend on the home-telemetry broker
// being configured to stay safe.
func (t *AgentBusTool) status() types.MQTTServiceStatus {
	if t.services == nil || t.services.MQTTService == nil {
		return types.MQTTServiceStatus{}
	}
	return t.services.MQTTService.Status()
}

// approveOnePublish credits one gated publish. The run closure the owner
// approved takes a credit before calling into the client; BeforePublish
// (transport boundary) requires a credit for gated envelope types, so a
// future code path cannot put a request/event on the wire unapproved.
// The credit is held until the client call returns (publish + reply wait);
// a concurrent gated publish would have its own approval anyway, since
// every tool action goes through gatePublish.
func (t *AgentBusTool) approveOnePublish() (release func()) {
	t.approvedPublishes.Add(1)
	return func() { t.approvedPublishes.Add(-1) }
}

// beforePublish returns the client.Options.BeforePublish hook. Response
// and error envelopes pass (replies to inbound requests); every other
// envelope type must hold an approval credit or the publish is refused —
// fail closed, matching the MQTT tool's rule that unapproved means
// unsent.
func (t *AgentBusTool) beforePublish() func(ctx context.Context, topic string, env *a2a.Envelope) error {
	return func(ctx context.Context, topic string, env *a2a.Envelope) error {
		if !needsApproval(env.Type) {
			return nil
		}
		if t.approvedPublishes.Load() <= 0 {
			return fmt.Errorf("agentbus: %s to %s has no approval credit; conduit refuses unapproved publishes", env.Type, topic)
		}
		// Exemption list applies to approved publishes too: an exempt
		// topic goes out directly (the tool path handles that before the
		// credit is taken), so no extra check is needed here.
		return nil
	}
}

// gatePublish freezes the operation (topic, envelope type, body preview,
// exact params) and hands it to the approval manager, or runs it directly
// when the envelope is an exempt reply or the topic is config-exempt. The
// frozen params and body are what run publishes; run is invoked at most
// once, only after approval.
func (t *AgentBusTool) gatePublish(ctx context.Context, topic string, typ a2a.Type, body any, params map[string]string, run approvalgate.Run) *types.ToolResult {
	if !needsApproval(typ) {
		return runResult(run(ctx))
	}
	st := t.status()
	if !publishNeedsApproval(st, topic) {
		return runResult(run(ctx))
	}
	if t.approver() == nil {
		return &types.ToolResult{
			Success: false,
			Error: fmt.Sprintf("NOT RUN: publishing %s to %s requires human approval, but no approval channel is configured. "+
				"Conduit fails closed: the envelope was not sent.", typ, topic),
			Data: map[string]interface{}{"approval_status": "refused_no_approver", "topic": topic, "envelope_type": string(typ)},
		}
	}

	shownTopic := approvalgate.Clip(approvalgate.RedactCommand(topic), approvalgate.MaxFieldRunes)
	shownBody := approvalgate.Clip(approvalgate.RedactCommand(bodyString(body)), approvalgate.MaxFieldRunes)
	dest := params["to"]
	if dest == "" {
		dest = params["capability"]
	}
	op := approvalgate.Operation{
		Kind:  approvalKind,
		Title: fmt.Sprintf("AgentBus %s to %s", typ, shownTopic),
		Fields: []approval.Field{
			{Name: "Topic", Value: shownTopic},
			{Name: "Destination", Value: approvalgate.Clip(approvalgate.RedactCommand(dest), approvalgate.MaxFieldRunes)},
			{Name: "Body", Value: shownBody},
			{Name: "Risk", Value: "sends a message to another agent on the agent bus"},
		},
		Params:  params,
		Audit:   map[string]string{"topic": approvalgate.Clip(shownTopic, 200), "type": string(typ)},
		Summary: fmt.Sprintf("AgentBus %s to %s", typ, dest),
		Data:    map[string]interface{}{"topic": topic, "envelope_type": string(typ), "requires_approval": true},
	}
	return approvalgate.Request(ctx, t.approver(), op, run)
}

// bodyString renders a message body for the approval prompt; a display
// error must never fail the gate, so failures collapse to a placeholder.
func bodyString(body any) string {
	if body == nil {
		return ""
	}
	if s, ok := body.(string); ok {
		return s
	}
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Sprintf("%v", body)
	}
	return string(b)
}

// verify topic helpers stay consistent with the a2a contract (compile-time).
var _ = a2a.InboxTopic

// runResult unwraps a gated run's (result, error) pair into the result the
// model sees; the error text becomes the result's error (the gate already
// logged it via the approval manager).
func runResult(res *types.ToolResult, err error) *types.ToolResult {
	if err != nil {
		return types.NewErrorResult("agentbus_run_failed", err.Error())
	}
	if res == nil {
		return types.NewErrorResult("agentbus_run_failed", "gated operation returned no result")
	}
	return res
}
