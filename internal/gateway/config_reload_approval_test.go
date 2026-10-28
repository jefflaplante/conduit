package gateway

import (
	"context"
	"strings"
	"testing"
)

// H11 follow-up: whether MQTT publish, PagerDuty writes, Datadog mutes (and
// the Kubernetes / remote SSH approval tiers) ask a human first is
// config-only. update_config refuses any change to it, whatever key
// spelling, nesting or parent delete the patch uses, so the model (or an
// automation token driving it) cannot switch off its own approval prompt.
func TestConfigUpdate_RefusesApprovalSettings(t *testing.T) {
	f := newReloadFixture(t)
	for _, patch := range []map[string]interface{}{
		{"mqtt.require_approval": false},
		{"mqtt.approval_exempt_topics": []interface{}{"#"}},
		{"MQTT.Require_Approval": false},
		{"mqtt": map[string]interface{}{"require_approval": false}},
		{"pagerduty.require_approval": false},
		{"pagerduty.approval_exempt_actions": []interface{}{"resolve"}},
		{"datadog.require_approval": false},
		{"datadog.approval_exempt_actions": []interface{}{"mute_monitor"}},
		{"kubernetes.require_approval": []interface{}{}},
		{"remote_ssh.security.require_approval": []interface{}{"blocked"}},
		{"ai.providers.z-ai.timeout_seconds": 900.0, "pagerduty.require_approval": false},
	} {
		if _, err := f.gw.PlanConfigUpdate(context.Background(), patch); err == nil || !strings.Contains(err.Error(), "approval settings") {
			t.Errorf("plan %v: err = %v, want approval refusal", patch, err)
		}
		if _, err := f.gw.ApplyConfigUpdate(context.Background(), patch); err == nil {
			t.Errorf("apply %v succeeded", patch)
		}
	}
	if f.file(t) != f.orig {
		t.Fatal("config file changed")
	}
	if !f.gw.currentConfig().MQTT.PublishNeedsApproval("zigbee2mqtt/lights/set") {
		t.Fatal("running config no longer requires approval")
	}
}

// Setting require_approval to its current effective value (true, the
// default) or changing unrelated keys in the same sections is allowed.
func TestConfigUpdate_ApprovalNoOpAndNeighboursAllowed(t *testing.T) {
	f := newReloadFixture(t)
	for _, patch := range []map[string]interface{}{
		{"mqtt.require_approval": true},
		{"pagerduty.rate_limit_rps": 2.0},
	} {
		if _, err := f.gw.PlanConfigUpdate(context.Background(), patch); err != nil {
			t.Errorf("plan %v: %v", patch, err)
		}
	}
}

// An owner who relaxed approval in the file cannot have it reset (or
// relaxed further) by the model either: removing the section is a change.
func TestConfigUpdate_RefusesDeletingRelaxedApproval(t *testing.T) {
	doc := strings.Replace(reloadConfigJSON, `"tools": {`,
		`"mqtt": {"enabled": false, "require_approval": true, "approval_exempt_topics": ["zigbee2mqtt/lights/#"]},
  "tools": {`, 1)
	f := newReloadFixtureFrom(t, doc)
	for _, patch := range []map[string]interface{}{
		{"mqtt": nil},
		{"mqtt.approval_exempt_topics": nil},
		{"mqtt.approval_exempt_topics": []interface{}{"zigbee2mqtt/#"}},
	} {
		if _, err := f.gw.PlanConfigUpdate(context.Background(), patch); err == nil || !strings.Contains(err.Error(), "approval settings") {
			t.Errorf("plan %v: err = %v, want approval refusal", patch, err)
		}
	}
	if _, err := f.gw.PlanConfigUpdate(context.Background(), map[string]interface{}{"mqtt.qos": 1.0}); err != nil {
		t.Errorf("unrelated mqtt key refused: %v", err)
	}
}
