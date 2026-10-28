//go:build with_mqtt

package mqtt

import (
	"context"
	"fmt"
	"strconv"

	"conduit/internal/approval"
	"conduit/internal/config"
	"conduit/internal/tools/approvalgate"
	"conduit/internal/tools/types"
)

// A publish switches real devices (lights, locks, relays), and a retained
// message is replayed to every subscriber, so it runs only after a human
// approves it (H11). Non-interactive turns (cron, heartbeat, sub-agents)
// and a missing approver fail closed. The owner can exempt topics, or turn
// approval off, in config (mqtt.require_approval,
// mqtt.approval_exempt_topics); an exempt publish runs directly from any
// origin.

// publishNeedsApproval applies the service's approval settings to topic.
// The zero status (settings unknown) requires approval.
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

func (t *MQTTTool) approver() approval.Requester {
	if t.services == nil {
		return nil
	}
	return t.services.Approvals
}

// gatePublish freezes a validated publish and hands it to the approval
// manager, or sends it directly when config exempts it. The frozen topic,
// payload, QoS and retain flag are what is sent.
func (t *MQTTTool) gatePublish(ctx context.Context, st types.MQTTServiceStatus, topic, payload string, qos byte, retained bool) (*types.ToolResult, error) {
	if !publishNeedsApproval(st, topic) {
		return t.runPublish(ctx, topic, payload, qos, retained)
	}
	q := strconv.Itoa(int(qos))
	r := strconv.FormatBool(retained)
	shownTopic := approvalgate.Clip(approvalgate.RedactCommand(topic), approvalgate.MaxFieldRunes)
	shownPayload := approvalgate.Clip(approvalgate.RedactCommand(payload), approvalgate.MaxFieldRunes)
	risk := "controls a device"
	if retained {
		risk += "; retained: the broker keeps it and replays it to every new subscriber"
	}
	op := approvalgate.Operation{
		Kind:  "mqtt.publish",
		Title: fmt.Sprintf("MQTT PUBLISH to %s", shownTopic),
		Fields: []approval.Field{
			{Name: "Topic", Value: shownTopic},
			{Name: "Payload", Value: shownPayload},
			{Name: "QoS", Value: q},
			{Name: "Retained", Value: r},
			{Name: "Risk", Value: risk},
		},
		Params: map[string]string{"topic": topic, "payload": payload, "qos": q, "retained": r},
		Audit: map[string]string{
			"topic":    approvalgate.Clip(shownTopic, 200),
			"payload":  approvalgate.Clip(shownPayload, 200),
			"qos":      q,
			"retained": r,
		},
		Summary: fmt.Sprintf("MQTT publish to %s", shownTopic),
		Data:    map[string]interface{}{"topic": topic, "retained": retained, "requires_approval": true},
	}
	return approvalgate.Request(ctx, t.approver(), op, func(ctx context.Context) (*types.ToolResult, error) {
		return t.runPublish(ctx, topic, payload, qos, retained)
	}), nil
}
