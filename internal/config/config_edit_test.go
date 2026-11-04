package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const editCurrent = `{
  "port": 19999,
  "ai": {"providers": [{"name": "acme", "api_key": "sk-acme-SECRET", "model": "m1"}]},
  "mqtt": {"enabled": true, "broker_url": "tcp://broker.example.com:1883", "password": "hunter2-SECRET"},
  "old_key": 1
}`

// Unedited redacted values keep the current secret; only the edited keys
// are in the patch.
func TestPatchFromFiles_KeepsRedactedSecrets(t *testing.T) {
	redacted, err := RedactedFile([]byte(editCurrent))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal([]byte(redacted), &doc); err != nil {
		t.Fatal(err)
	}
	doc["port"] = 19998.0
	delete(doc, "old_key")
	doc["mqtt"].(map[string]interface{})["approval"] = map[string]interface{}{"exempt": []interface{}{"lights/#"}}
	proposed, _ := json.Marshal(doc)

	patch, err := PatchFromFiles([]byte(editCurrent), proposed)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]interface{}{
		"port":    19998.0,
		"old_key": nil,
		"mqtt":    map[string]interface{}{"approval": map[string]interface{}{"exempt": []interface{}{"lights/#"}}},
	}
	if !reflect.DeepEqual(patch, want) {
		t.Fatalf("patch = %v\nwant %v", patch, want)
	}
}

// A redacted placeholder inside a changed value (an array with an edited
// element, a new key) would overwrite the secret with "[redacted]".
func TestPatchFromFiles_RefusesRedactedPlaceholder(t *testing.T) {
	for _, proposed := range []string{
		`{"port": 19999, "ai": {"providers": [{"name": "acme", "api_key": "[redacted]", "model": "m2"}]},
		  "mqtt": {"enabled": true, "broker_url": "tcp://broker.example.com:1883", "password": "[redacted]"}, "old_key": 1}`,
		`{"port": 19999, "ai": {"providers": [{"name": "acme", "api_key": "sk-acme-SECRET", "model": "m1"}]},
		  "mqtt": {"enabled": true, "broker_url": "tcp://broker.example.com:1883", "password": "[redacted]"}, "old_key": 1,
		  "new": {"token": "[redacted]"}}`,
	} {
		if _, err := PatchFromFiles([]byte(editCurrent), []byte(proposed)); err == nil || !strings.Contains(err.Error(), RedactedValue) {
			t.Errorf("err = %v, want a refusal naming %s", err, RedactedValue)
		}
	}
}

func TestPatchFromFiles_NoChangeAndBadInput(t *testing.T) {
	if p, err := PatchFromFiles([]byte(editCurrent), []byte(editCurrent)); err != nil || len(p) != 0 {
		t.Fatalf("same file: patch %v err %v", p, err)
	}
	for _, bad := range []string{"", "[]", "null", "{"} {
		if _, err := PatchFromFiles([]byte(editCurrent), []byte(bad)); err == nil {
			t.Errorf("proposed %q accepted", bad)
		}
	}
}

func TestKeyRisk(t *testing.T) {
	for _, tc := range []struct {
		path      string
		sensitive bool
	}{
		{"port", false},
		{"ai.default_provider", false},
		{"ai.providers.acme.model", false},
		{"ai.providers.acme.timeout_seconds", false},
		{"mqtt.qos", false},
		{"pagerduty.rate_limit_rps", false},
		{"ai.providers.acme.api_key", true},
		{"ai.providers.acme.base_url", true},
		{"mqtt.broker_url", true},
		{"mqtt.password", true},
		{"mqtt.enabled", true},
		{"mqtt.approval.required", true},
		{"mqtt.approval.exempt", true},
		{"pagerduty.approval.exempt", true},
		{"datadog.site", true},
		{"kubernetes.require_approval", true},
		{"remote_ssh.security.require_approval", true},
		{"tools.enabled_tools", true},
		{"tools.sandbox.allowed_paths", true},
		{"tool_policy.mode", true},
		{"channels.telegram.allowed_users", true},
		{"auth.oauth.client_id", true},
		{"secrets_file", true},
		{"agent_heartbeat.alert_targets", true},
		{"TOOLS.Enabled_Tools", true},
	} {
		got := KeyRisk(strings.Split(tc.path, "."))
		if (got != "") != tc.sensitive {
			t.Errorf("KeyRisk(%s) = %q, want sensitive=%v", tc.path, got, tc.sensitive)
		}
	}
}
