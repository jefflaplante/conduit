package config

import (
	"strings"
	"testing"
)

func boolp(b bool) *bool { return &b }

func TestApproval_DefaultsOn(t *testing.T) {
	cfg, err := Parse([]byte(`{"ai":{},"port":19999,"tools":{"max_tool_chains":25,"enabled_tools":["Read"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.MQTT.PublishNeedsApproval("zigbee2mqtt/lights/set") {
		t.Error("mqtt: unset require_approval must require approval")
	}
	for _, a := range PagerDutyWriteActions {
		if !cfg.PagerDuty.ActionNeedsApproval(a) {
			t.Errorf("pagerduty %s: unset require_approval must require approval", a)
		}
	}
	for _, a := range DatadogApprovalActions {
		if !cfg.Datadog.ActionNeedsApproval(a) {
			t.Errorf("datadog %s: unset require_approval must require approval", a)
		}
	}
	var nilPD *PagerDutyConfig
	var nilDD *DatadogConfig
	if !nilPD.ActionNeedsApproval("resolve") || !nilDD.ActionNeedsApproval("mute_monitor") {
		t.Error("nil config must require approval")
	}
}

func TestApproval_ExplicitOff(t *testing.T) {
	cfg, err := Parse([]byte(`{"ai":{},"port":19999,"tools":{"max_tool_chains":25,"enabled_tools":["Read"]},
		"mqtt":{"require_approval":false},
		"pagerduty":{"require_approval":false},
		"datadog":{"require_approval":false}}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MQTT.PublishNeedsApproval("a/b") || cfg.MQTT.ApprovalEnabled() {
		t.Error("mqtt: require_approval=false still requires approval")
	}
	if cfg.PagerDuty.ActionNeedsApproval("trigger") || cfg.Datadog.ActionNeedsApproval("mute_monitor") {
		t.Error("require_approval=false still requires approval")
	}
}

func TestApproval_MQTTExemptTopics(t *testing.T) {
	m := MQTTConfig{RequireApproval: boolp(true), ApprovalExemptTopics: []string{"zigbee2mqtt/lights/#", "home/+/lamp"}}
	for topic, want := range map[string]bool{
		"zigbee2mqtt/lights":           false,
		"zigbee2mqtt/lights/kitchen":   false,
		"zigbee2mqtt/lights/a/b/c":     false,
		"home/den/lamp":                false,
		"zigbee2mqtt/lightsout":        true,
		"zigbee2mqtt/locks/front/set":  true,
		"home/den/lamp/set":            true,
		"home/lamp":                    true,
		"ZIGBEE2MQTT/lights/kitchen":   true,
		"other/zigbee2mqtt/lights/set": true,
	} {
		if got := m.PublishNeedsApproval(topic); got != want {
			t.Errorf("PublishNeedsApproval(%q) = %v, want %v", topic, got, want)
		}
	}
}

func TestApproval_ExemptActions(t *testing.T) {
	p := PagerDutyConfig{ApprovalExemptActions: []string{"acknowledge"}}
	if p.ActionNeedsApproval("acknowledge") || !p.ActionNeedsApproval("resolve") {
		t.Error("pagerduty exempt list not applied per action")
	}
	d := DatadogConfig{ApprovalExemptActions: []string{"unmute_monitor"}}
	if d.ActionNeedsApproval("unmute_monitor") || !d.ActionNeedsApproval("mute_monitor") {
		t.Error("datadog exempt list not applied per action")
	}
}

func TestApproval_Validation(t *testing.T) {
	for name, tc := range map[string]struct {
		err  func() error
		want string
	}{
		"mqtt hash mid": {func() error {
			m := MQTTConfig{Enabled: true, BrokerURL: "tcp://broker.example:1883", Topics: []string{"#"}, ApprovalExemptTopics: []string{"a/#/b"}}
			return m.Validate()
		}, "approval_exempt_topics"},
		"mqtt partial plus": {func() error {
			m := MQTTConfig{Enabled: true, BrokerURL: "tcp://broker.example:1883", Topics: []string{"#"}, ApprovalExemptTopics: []string{"a/b+"}}
			return m.Validate()
		}, "approval_exempt_topics"},
		"mqtt empty": {func() error {
			m := MQTTConfig{Enabled: true, BrokerURL: "tcp://broker.example:1883", Topics: []string{"#"}, ApprovalExemptTopics: []string{""}}
			return m.Validate()
		}, "approval_exempt_topics"},
		"pagerduty read action": {func() error {
			p := PagerDutyConfig{Enabled: true, APIToken: "x", ApprovalExemptActions: []string{"list_incidents"}}
			return p.Validate()
		}, "pagerduty.approval_exempt_actions"},
		"datadog unknown": {func() error {
			d := DatadogConfig{Enabled: true, APIKey: "x", AppKey: "y", ApprovalExemptActions: []string{"mute"}}
			return d.Validate()
		}, "datadog.approval_exempt_actions"},
	} {
		err := tc.err()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
	ok := MQTTConfig{Enabled: true, BrokerURL: "tcp://broker.example:1883", Topics: []string{"#"}, ApprovalExemptTopics: []string{"a/+/b", "c/#", "#"}}
	if err := ok.Validate(); err != nil {
		t.Errorf("valid filters rejected: %v", err)
	}
}

func TestApprovalSettingsChanged(t *testing.T) {
	base := func() *Config {
		c := &Config{}
		c.Kubernetes.RequireApproval = []string{"dangerous"}
		return c
	}
	if ApprovalSettingsChanged(base(), base()) {
		t.Fatal("identical configs reported as changed")
	}
	same := base()
	same.MQTT.RequireApproval = boolp(true) // explicit true == unset
	same.MQTT.ApprovalExemptTopics = []string{}
	same.MQTT.QoS = 2
	if ApprovalSettingsChanged(base(), same) {
		t.Error("no-op approval change or unrelated key reported as changed")
	}
	for name, mut := range map[string]func(c *Config){
		"mqtt off":       func(c *Config) { c.MQTT.RequireApproval = boolp(false) },
		"mqtt exempt":    func(c *Config) { c.MQTT.ApprovalExemptTopics = []string{"#"} },
		"pd off":         func(c *Config) { c.PagerDuty.RequireApproval = boolp(false) },
		"pd exempt":      func(c *Config) { c.PagerDuty.ApprovalExemptActions = []string{"resolve"} },
		"dd off":         func(c *Config) { c.Datadog.RequireApproval = boolp(false) },
		"dd exempt":      func(c *Config) { c.Datadog.ApprovalExemptActions = []string{"mute_monitor"} },
		"k8s empty":      func(c *Config) { c.Kubernetes.RequireApproval = []string{} },
		"k8s absent":     func(c *Config) { c.Kubernetes.RequireApproval = nil },
		"ssh tiers":      func(c *Config) { c.RemoteSSH.Security.RequireApproval = []string{"blocked"} },
		"k8s add modify": func(c *Config) { c.Kubernetes.RequireApproval = []string{"dangerous", "modify"} },
	} {
		next := base()
		mut(next)
		if !ApprovalSettingsChanged(base(), next) {
			t.Errorf("%s: change not detected", name)
		}
	}
}
