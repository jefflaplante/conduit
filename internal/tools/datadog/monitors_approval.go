//go:build with_datadog

package datadog

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"conduit/internal/approval"
	"conduit/internal/tools/approvalgate"
	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

// mute_monitor and unmute_monitor silence or re-enable alerting, so they
// run only after a human approves them (H11). confirmed=true is still
// required but is not sufficient: the model sets it itself, and log or
// alert text can talk it into that. Non-interactive turns (cron,
// heartbeat, sub-agents) and a missing approver fail closed.

func (t *MonitorTool) approver() approval.Requester {
	if t.services == nil {
		return nil
	}
	return t.services.Approvals
}

// gateMonitorWrite validates a mute/unmute call, freezes its arguments and
// hands it to the approval manager. The frozen arguments are what runs.
func (t *MonitorTool) gateMonitorWrite(ctx context.Context, action string, args map[string]interface{}) (*types.ToolResult, error) {
	if result := checkConfirmation(action, args); result != nil {
		return result, nil
	}
	monitorID := toolargs.GetInt(args, "monitor_id", 0)
	if monitorID == 0 {
		return &types.ToolResult{Success: false, Error: "monitor_id parameter is required"}, nil
	}

	frozen := map[string]interface{}{"monitor_id": monitorID, "confirmed": true}
	params := map[string]string{"action": action, "monitor_id": strconv.Itoa(monitorID)}
	fields := []approval.Field{{Name: "Monitor", Value: strconv.Itoa(monitorID)}}
	scope := toolargs.GetString(args, "scope", "")
	frozen["scope"] = scope
	params["scope"] = scope
	if scope != "" {
		fields = append(fields, approval.Field{Name: "Scope", Value: approvalgate.Clip(approvalgate.RedactCommand(scope), approvalgate.MaxFieldRunes)})
	} else {
		fields = append(fields, approval.Field{Name: "Scope", Value: "all"})
	}
	if action == "mute_monitor" {
		end := toolargs.GetInt64(args, "end", 0)
		frozen["end"] = end
		params["end"] = strconv.FormatInt(end, 10)
		until := "indefinitely"
		if end > 0 {
			until = time.Unix(end, 0).UTC().Format(time.RFC3339)
		}
		fields = append(fields, approval.Field{Name: "Until", Value: until})
	}
	fields = append(fields, approval.Field{Name: "Risk", Value: "modify tier (changes Datadog alerting)"})

	verb := strings.TrimSuffix(action, "_monitor")
	audit := make(map[string]string, len(params))
	for k, v := range params {
		audit[k] = approvalgate.Clip(approvalgate.RedactCommand(v), 200)
	}
	op := approvalgate.Operation{
		Kind:    "datadog." + action,
		Title:   fmt.Sprintf("Datadog %s monitor %d", strings.ToUpper(verb), monitorID),
		Fields:  fields,
		Params:  params,
		Audit:   audit,
		Summary: fmt.Sprintf("Datadog %s of monitor %d", verb, monitorID),
		Data:    map[string]interface{}{"action": action, "monitor_id": monitorID, "requires_approval": true},
	}
	return approvalgate.Request(ctx, t.approver(), op, func(ctx context.Context) (*types.ToolResult, error) {
		run := make(map[string]interface{}, len(frozen))
		for k, v := range frozen {
			run[k] = v
		}
		if action == "mute_monitor" {
			return t.executeMuteMonitor(ctx, run)
		}
		return t.executeUnmuteMonitor(ctx, run)
	}), nil
}
