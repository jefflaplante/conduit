package core

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"conduit/internal/approval"
	"conduit/internal/tools/approvalgate"
	"conduit/internal/tools/types"
)

// update_config approval policy (conduit-rmho).
//
// Every update_config call is model-originated, and config controls the
// sandbox, credentials, provider endpoints and remote access, so every
// change (not only the security-sensitive ones) goes through the human
// approval primitive, exactly like gated K8s/SSH operations:
//
//   - the patch is validated and classified first (PlanConfigUpdate); an
//     invalid patch is rejected at once, with nothing changed and no prompt;
//   - a valid plan is frozen (canonical JSON, fingerprinted) and the owner is
//     asked on the originating channel; the change is applied only after a
//     "YES <code>" reply, and is re-validated against the file at that time;
//   - non-interactive turns (cron, heartbeat, sub-agents, wakes, MCP) and a
//     gateway without an approval channel fail closed: nothing is applied.
//
// The model never sees the approval code, and nothing it emits can approve.

// maxShownChanges bounds the per-key fields in one approval prompt.
const maxShownChanges = 20

func (t *GatewayTool) updateConfig(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	patch, ok := args["config"].(map[string]interface{})
	if !ok || len(patch) == 0 {
		return &types.ToolResult{
			Success: false,
			Error:   "config parameter is required for update_config action and must be a non-empty object",
		}, nil
	}
	updater, ok := t.services.Gateway.(types.ConfigUpdater)
	if !ok {
		return &types.ToolResult{Success: false, Error: "this gateway does not support update_config"}, nil
	}

	// Freeze the patch: the approval is bound to these exact bytes, and the
	// deferred apply decodes its own copy of them.
	frozen, err := json.Marshal(patch)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("config is not valid JSON: %v", err)}, nil
	}
	thaw := func() (map[string]interface{}, error) {
		var p map[string]interface{}
		err := json.Unmarshal(frozen, &p)
		return p, err
	}

	planPatch, err := thaw()
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("config is not valid JSON: %v", err)}, nil
	}
	plan, err := updater.PlanConfigUpdate(ctx, planPatch)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("configuration update rejected (nothing changed): %v", err),
		}, nil
	}
	if len(plan.Changes) == 0 {
		return &types.ToolResult{
			Success: true,
			Content: "No change: every requested key already has the requested value (" + strings.Join(plan.Unchanged, ", ") + ").",
			Data:    map[string]interface{}{"action": "update_config", "unchanged": plan.Unchanged},
		}, nil
	}

	run := func(execCtx context.Context) (*types.ToolResult, error) {
		p, err := thaw()
		if err != nil {
			return nil, err
		}
		res, err := updater.ApplyConfigUpdate(execCtx, p)
		if err != nil {
			return &types.ToolResult{Success: false, Error: fmt.Sprintf("configuration update failed (nothing changed): %v", err)}, nil
		}
		return &types.ToolResult{Success: true, Content: formatConfigUpdate(res)}, nil
	}

	return approvalgate.Request(ctx, t.approver(), configOperation(plan, string(frozen)), run), nil
}

func (t *GatewayTool) approver() approval.Requester {
	if t.services == nil {
		return nil
	}
	return t.services.Approvals
}

// configOperation describes a planned update for the approval prompt.
func configOperation(plan *types.ConfigUpdateResult, frozen string) approvalgate.Operation {
	keys := make([]string, 0, len(plan.Changes))
	fields := make([]approval.Field, 0, len(plan.Changes)+1)
	for i, c := range plan.Changes {
		keys = append(keys, c.Path)
		if i >= maxShownChanges {
			continue
		}
		note := "live"
		if c.Mode == types.ConfigChangeRestart {
			note = "saved, needs restart"
		}
		if c.SecuritySensitive {
			note += ", SECURITY-SENSITIVE"
		}
		fields = append(fields, approval.Field{
			Name:  c.Path,
			Value: approvalgate.Clip(c.Value, 300) + " (" + note + ")",
		})
	}
	if len(plan.Changes) > maxShownChanges {
		fields = append(fields, approval.Field{
			Name:  "More",
			Value: fmt.Sprintf("%d more keys: %s", len(plan.Changes)-maxShownChanges, strings.Join(keys[maxShownChanges:], ", ")),
		})
	}
	sort.Strings(keys)
	summary := "the configuration change to " + approvalgate.Clip(strings.Join(keys, ", "), 200)
	return approvalgate.Operation{
		Kind:   "gateway.update_config",
		Title:  fmt.Sprintf("Change gateway configuration (%d key(s))", len(plan.Changes)),
		Fields: fields,
		Params: map[string]string{"patch": frozen},
		Audit: map[string]string{
			"keys":           approvalgate.Clip(strings.Join(keys, ","), 500),
			"live_keys":      approvalgate.Clip(strings.Join(plan.LiveKeys(), ","), 500),
			"restart_keys":   approvalgate.Clip(strings.Join(plan.RestartKeys(), ","), 500),
			"security_keys":  approvalgate.Clip(strings.Join(plan.SecurityKeys(), ","), 500),
			"requested_via":  "Gateway.update_config",
			"approval_scope": "all update_config changes",
		},
		Summary: summary,
		Data: map[string]interface{}{
			"action":             "update_config",
			"requires_approval":  true,
			"live":               plan.LiveKeys(),
			"requires_restart":   plan.RestartKeys(),
			"security_sensitive": plan.SecurityKeys(),
			"unchanged":          plan.Unchanged,
		},
	}
}

// formatConfigUpdate renders an applied update for the owner/model. Values
// come from the redacted readback; secrets never appear.
func formatConfigUpdate(res *types.ConfigUpdateResult) string {
	var b strings.Builder
	b.WriteString("Configuration updated")
	if res.ConfigPath != "" {
		fmt.Fprintf(&b, " and saved to %s", res.ConfigPath)
	}
	if res.BackupPath != "" {
		fmt.Fprintf(&b, " (previous version: %s)", res.BackupPath)
	}
	b.WriteString(".\n")
	if live := res.LiveKeys(); len(live) > 0 {
		b.WriteString("Applied live (no restart needed):\n")
		for _, k := range live {
			fmt.Fprintf(&b, "  %s = %s\n", k, readbackString(res.Readback, k))
		}
	}
	if restart := res.RestartKeys(); len(restart) > 0 {
		b.WriteString("Saved; REQUIRES A RESTART to take effect (not applied to the running gateway):\n")
		for _, k := range restart {
			fmt.Fprintf(&b, "  %s = %s\n", k, readbackString(res.Readback, k))
		}
	}
	if len(res.Unchanged) > 0 {
		fmt.Fprintf(&b, "Already set (unchanged): %s\n", strings.Join(res.Unchanged, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

func readbackString(rb map[string]interface{}, key string) string {
	v, ok := rb[key]
	if !ok {
		return "(removed)"
	}
	s, err := json.Marshal(v)
	if err != nil {
		return "?"
	}
	return approvalgate.Clip(string(s), 300)
}
