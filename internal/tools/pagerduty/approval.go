//go:build with_pagerduty

package pagerduty

import (
	"context"
	"fmt"
	"strings"

	"conduit/internal/approval"
	"conduit/internal/tools/approvalgate"
	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

// Write actions (acknowledge, snooze, add_note, resolve, trigger) change a
// live incident, so they run only after a human approves them (H11). The
// model's confirmed=true flag is still accepted but is not sufficient:
// incident or log text can talk the model into setting it. Non-interactive
// turns (cron, heartbeat, sub-agents) and a missing approver fail closed.

// isWriteAction reports whether action changes PagerDuty state.
func isWriteAction(action string) bool {
	return ClassifyAction(action) != TierRead
}

func (t *PagerDutyTool) approver() approval.Requester {
	if t.services == nil {
		return nil
	}
	return t.services.Approvals
}

// gateWrite validates a write action, freezes the arguments it uses, and
// hands it to the approval manager. The frozen arguments are what runs.
func (t *PagerDutyTool) gateWrite(ctx context.Context, action string, args map[string]interface{}) (*types.ToolResult, error) {
	frozen, bad := t.freezeWriteArgs(action, args)
	if bad != nil {
		return bad, nil
	}

	params := map[string]string{"action": action}
	audit := map[string]string{"action": action, "tier": string(ClassifyAction(action))}
	fields := []approval.Field{{Name: "Action", Value: action}}
	for _, key := range []string{"incident_id", "snooze_duration", "title", "service_id", "escalation_policy_id", "note", "details"} {
		v, ok := frozen[key]
		if !ok {
			continue
		}
		s := fmt.Sprint(v)
		params[key] = s
		if s == "" {
			continue
		}
		shown := approvalgate.Clip(approvalgate.RedactCommand(s), approvalgate.MaxFieldRunes)
		fields = append(fields, approval.Field{Name: fieldLabel(key), Value: shown})
		audit[key] = approvalgate.Clip(shown, 200)
	}
	fields = append(fields, approval.Field{Name: "Risk", Value: fmt.Sprintf("%s tier (changes a live PagerDuty incident)", ClassifyAction(action))})

	target := "a new incident"
	data := map[string]interface{}{"action": action, "requires_approval": true}
	if id, _ := frozen["incident_id"].(string); id != "" {
		target = "incident " + id
		data["incident_id"] = id
	}
	op := approvalgate.Operation{
		Kind:    "pagerduty." + action,
		Title:   fmt.Sprintf("PagerDuty %s %s", strings.ToUpper(strings.ReplaceAll(action, "_", " ")), target),
		Fields:  fields,
		Params:  params,
		Audit:   audit,
		Summary: fmt.Sprintf("PagerDuty %s of %s", action, target),
		Data:    data,
	}
	return approvalgate.Request(ctx, t.approver(), op, func(ctx context.Context) (*types.ToolResult, error) {
		run := make(map[string]interface{}, len(frozen))
		for k, v := range frozen {
			run[k] = v
		}
		return t.runWrite(ctx, action, run)
	}), nil
}

// freezeWriteArgs extracts and validates the arguments action uses,
// resolving configured defaults so the approval shows what will be sent.
func (t *PagerDutyTool) freezeWriteArgs(action string, args map[string]interface{}) (map[string]interface{}, *types.ToolResult) {
	fail := func(msg string) (map[string]interface{}, *types.ToolResult) {
		return nil, &types.ToolResult{Success: false, Error: msg}
	}
	frozen := map[string]interface{}{}
	if action != "trigger" {
		id := toolargs.GetString(args, "incident_id", "")
		if id == "" {
			return fail(fmt.Sprintf("incident_id is required for %s action", action))
		}
		frozen["incident_id"] = id
	}
	switch action {
	case "snooze":
		d := toolargs.GetInt(args, "snooze_duration", 0)
		if d <= 0 {
			return fail("snooze_duration (in seconds) is required and must be positive")
		}
		frozen["snooze_duration"] = d
	case "add_note":
		note := toolargs.GetString(args, "note", "")
		if note == "" {
			return fail("note content is required for add_note action")
		}
		frozen["note"] = note
	case "trigger":
		title := toolargs.GetString(args, "title", "")
		if title == "" {
			return fail("title is required for trigger action")
		}
		serviceID := toolargs.GetString(args, "service_id", "")
		if serviceID == "" {
			serviceID = t.config.DefaultServiceID
		}
		if serviceID == "" {
			return fail("service_id is required (not specified and no default configured)")
		}
		policyID := toolargs.GetString(args, "escalation_policy_id", "")
		if policyID == "" {
			policyID = t.config.DefaultEscalationPolicyID
		}
		frozen["title"] = title
		frozen["service_id"] = serviceID
		frozen["escalation_policy_id"] = policyID
		frozen["details"] = toolargs.GetString(args, "details", "")
	}
	return frozen, nil
}

// runWrite performs an approved write action.
func (t *PagerDutyTool) runWrite(ctx context.Context, action string, args map[string]interface{}) (*types.ToolResult, error) {
	switch action {
	case "acknowledge":
		return t.acknowledgeIncident(ctx, args)
	case "resolve":
		return t.resolveIncident(ctx, args)
	case "snooze":
		return t.snoozeIncident(ctx, args)
	case "add_note":
		return t.addNote(ctx, args)
	case "trigger":
		return t.triggerIncident(ctx, args)
	}
	return &types.ToolResult{Success: false, Error: fmt.Sprintf("unknown action: %s", action)}, nil
}

func fieldLabel(key string) string {
	switch key {
	case "incident_id":
		return "Incident"
	case "snooze_duration":
		return "Snooze (seconds)"
	case "service_id":
		return "Service"
	case "escalation_policy_id":
		return "Escalation policy"
	}
	s := strings.ReplaceAll(key, "_", " ")
	return strings.ToUpper(s[:1]) + s[1:]
}
