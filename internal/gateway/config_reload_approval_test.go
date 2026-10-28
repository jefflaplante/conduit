package gateway

import (
	"context"
	"strings"
	"testing"
)

// A change that makes a tool ask a human less often is allowed through
// update_config (the owner approves every update) but the plan must carry
// a warning, whatever key spelling, nesting or parent delete the patch
// uses, so the approval prompt flags it.
func TestConfigPlan_FlagsApprovalWeakenings(t *testing.T) {
	f := newReloadFixture(t)
	for _, tc := range []struct {
		patch map[string]interface{}
		want  string
	}{
		{map[string]interface{}{"mqtt.approval.required": false}, "mqtt.approval.required turned off"},
		{map[string]interface{}{"mqtt.approval.exempt": []interface{}{"#"}}, "mqtt.approval.exempt adds #"},
		{map[string]interface{}{"MQTT.Approval.Required": false}, "mqtt.approval.required turned off"},
		{map[string]interface{}{"mqtt": map[string]interface{}{"approval": map[string]interface{}{"required": false}}}, "mqtt.approval.required turned off"},
		{map[string]interface{}{"pagerduty.approval.exempt": []interface{}{"resolve"}}, "pagerduty.approval.exempt adds resolve"},
		{map[string]interface{}{"datadog.approval.required": false}, "datadog.approval.required turned off"},
		{map[string]interface{}{"kubernetes.require_approval": []interface{}{}}, "kubernetes.require_approval drops dangerous"},
		{map[string]interface{}{"remote_ssh.security.require_approval": []interface{}{"blocked"}}, "remote_ssh.security.require_approval drops dangerous"},
		{map[string]interface{}{"ai.providers.z-ai.timeout_seconds": 900.0, "pagerduty.approval.required": false}, "pagerduty.approval.required turned off"},
	} {
		plan, err := f.gw.PlanConfigUpdate(context.Background(), tc.patch)
		if err != nil {
			t.Errorf("plan %v: %v", tc.patch, err)
			continue
		}
		if w := strings.Join(plan.Warnings, "; "); !strings.Contains(w, tc.want) {
			t.Errorf("plan %v: warnings %q, want %q", tc.patch, w, tc.want)
		}
		for _, c := range plan.Changes {
			if strings.Contains(c.Path, "approval") && (!c.SecuritySensitive || c.Risk == "") {
				t.Errorf("plan %v: %s not marked security-sensitive", tc.patch, c.Path)
			}
		}
	}
	if f.file(t) != f.orig {
		t.Fatal("planning changed the config file")
	}
}

// Stricter or unrelated changes carry no approval warning; every change
// shows its old value.
func TestConfigPlan_NoWarningForStricterChanges(t *testing.T) {
	doc := strings.Replace(reloadConfigJSON, `"tools": {`,
		`"mqtt": {"enabled": false, "approval": {"exempt": ["zigbee2mqtt/lights/#"]}},
  "tools": {`, 1)
	f := newReloadFixtureFrom(t, doc)
	for _, patch := range []map[string]interface{}{
		{"mqtt.approval.required": true},
		{"mqtt.approval.exempt": nil},
		{"mqtt": nil},
		{"mqtt.qos": 1.0},
		{"pagerduty.rate_limit_rps": 2.0},
	} {
		plan, err := f.gw.PlanConfigUpdate(context.Background(), patch)
		if err != nil {
			t.Errorf("plan %v: %v", patch, err)
			continue
		}
		if len(plan.Warnings) != 0 {
			t.Errorf("plan %v: unexpected warnings %v", patch, plan.Warnings)
		}
	}
	plan, err := f.gw.PlanConfigUpdate(context.Background(), map[string]interface{}{"mqtt.approval.exempt": []interface{}{"zigbee2mqtt/#"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 1 || plan.Changes[0].OldValue != `["zigbee2mqtt/lights/#"]` || plan.Changes[0].Value != `["zigbee2mqtt/#"]` {
		t.Errorf("change = %+v, want old and new value", plan.Changes)
	}
	if len(plan.Warnings) != 1 {
		t.Errorf("widening an exempt filter: warnings %v", plan.Warnings)
	}
}

// tool_policy stays refused outright (conduit-25lt.2), even nested.
func TestConfigPlan_ToolPolicyStillRefused(t *testing.T) {
	f := newReloadFixture(t)
	if _, err := f.gw.PlanConfigUpdate(context.Background(), map[string]interface{}{"tool_policy": map[string]interface{}{"mode": "off"}}); err == nil {
		t.Fatal("tool_policy change planned")
	}
}
