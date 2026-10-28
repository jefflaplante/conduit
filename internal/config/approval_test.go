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
		t.Error("mqtt: unset approval.required must require approval")
	}
	for _, a := range PagerDutyWriteActions {
		if !cfg.PagerDuty.ActionNeedsApproval(a) {
			t.Errorf("pagerduty %s: unset approval.required must require approval", a)
		}
	}
	for _, a := range DatadogApprovalActions {
		if !cfg.Datadog.ActionNeedsApproval(a) {
			t.Errorf("datadog %s: unset approval.required must require approval", a)
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
		"mqtt":{"approval":{"required":false}},
		"pagerduty":{"approval":{"required":false}},
		"datadog":{"approval":{"required":false}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MQTT.PublishNeedsApproval("a/b") || cfg.MQTT.Approval.Enabled() {
		t.Error("mqtt: approval.required=false still requires approval")
	}
	if cfg.PagerDuty.ActionNeedsApproval("trigger") || cfg.Datadog.ActionNeedsApproval("mute_monitor") {
		t.Error("approval.required=false still requires approval")
	}
}

func TestApproval_MQTTExemptTopics(t *testing.T) {
	m := MQTTConfig{Approval: ToolApproval{Required: boolp(true), Exempt: []string{"zigbee2mqtt/lights/#", "home/+/lamp"}}}
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
	p := PagerDutyConfig{Approval: ToolApproval{Exempt: []string{"acknowledge"}}}
	if p.ActionNeedsApproval("acknowledge") || !p.ActionNeedsApproval("resolve") {
		t.Error("pagerduty exempt list not applied per action")
	}
	d := DatadogConfig{Approval: ToolApproval{Exempt: []string{"unmute_monitor"}}}
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
			m := MQTTConfig{Enabled: true, BrokerURL: "tcp://broker.example:1883", Topics: []string{"#"}, Approval: ToolApproval{Exempt: []string{"a/#/b"}}}
			return m.Validate()
		}, "approval.exempt"},
		"mqtt partial plus": {func() error {
			m := MQTTConfig{Enabled: true, BrokerURL: "tcp://broker.example:1883", Topics: []string{"#"}, Approval: ToolApproval{Exempt: []string{"a/b+"}}}
			return m.Validate()
		}, "approval.exempt"},
		"mqtt empty": {func() error {
			m := MQTTConfig{Enabled: true, BrokerURL: "tcp://broker.example:1883", Topics: []string{"#"}, Approval: ToolApproval{Exempt: []string{""}}}
			return m.Validate()
		}, "approval.exempt"},
		"pagerduty read action": {func() error {
			p := PagerDutyConfig{Enabled: true, APIToken: "x", Approval: ToolApproval{Exempt: []string{"list_incidents"}}}
			return p.Validate()
		}, "pagerduty.approval.exempt"},
		"datadog unknown": {func() error {
			d := DatadogConfig{Enabled: true, APIKey: "x", AppKey: "y", Approval: ToolApproval{Exempt: []string{"mute"}}}
			return d.Validate()
		}, "datadog.approval.exempt"},
	} {
		err := tc.err()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
	ok := MQTTConfig{Enabled: true, BrokerURL: "tcp://broker.example:1883", Topics: []string{"#"}, Approval: ToolApproval{Exempt: []string{"a/+/b", "c/#", "#"}}}
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
	same.MQTT.Approval.Required = boolp(true) // explicit true == unset
	same.MQTT.Approval.Exempt = []string{}
	same.MQTT.QoS = 2
	if ApprovalSettingsChanged(base(), same) {
		t.Error("no-op approval change or unrelated key reported as changed")
	}
	for name, mut := range map[string]func(c *Config){
		"mqtt off":       func(c *Config) { c.MQTT.Approval.Required = boolp(false) },
		"mqtt exempt":    func(c *Config) { c.MQTT.Approval.Exempt = []string{"#"} },
		"pd off":         func(c *Config) { c.PagerDuty.Approval.Required = boolp(false) },
		"pd exempt":      func(c *Config) { c.PagerDuty.Approval.Exempt = []string{"resolve"} },
		"dd off":         func(c *Config) { c.Datadog.Approval.Required = boolp(false) },
		"dd exempt":      func(c *Config) { c.Datadog.Approval.Exempt = []string{"mute_monitor"} },
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

func TestApprovalWeakenings(t *testing.T) {
	base := func() *Config {
		c := &Config{}
		c.MQTT.Approval.Exempt = []string{"zigbee2mqtt/lights/#"}
		c.RemoteSSH.Security.RequireApproval = []string{"dangerous", "blocked"}
		return c
	}
	if w := ApprovalWeakenings(base(), base()); len(w) != 0 {
		t.Fatalf("identical configs: %v", w)
	}
	for name, tc := range map[string]struct {
		mut  func(c *Config)
		want string // "" = not a weakening
	}{
		"mqtt off":            {func(c *Config) { c.MQTT.Approval.Required = boolp(false) }, "mqtt.approval.required turned off"},
		"mqtt exempt added":   {func(c *Config) { c.MQTT.Approval.Exempt = append(c.MQTT.Approval.Exempt, "#") }, "mqtt.approval.exempt adds #"},
		"mqtt exempt removed": {func(c *Config) { c.MQTT.Approval.Exempt = nil }, ""},
		"pd exempt":           {func(c *Config) { c.PagerDuty.Approval.Exempt = []string{"resolve"} }, "pagerduty.approval.exempt adds resolve"},
		"dd off":              {func(c *Config) { c.Datadog.Approval.Required = boolp(false) }, "datadog.approval.required turned off"},
		"k8s disabled":        {func(c *Config) { c.Kubernetes.RequireApproval = []string{} }, "kubernetes.require_approval drops dangerous"},
		"k8s stricter":        {func(c *Config) { c.Kubernetes.RequireApproval = []string{"dangerous", "modify"} }, ""},
		"ssh drops blocked":   {func(c *Config) { c.RemoteSSH.Security.RequireApproval = []string{"dangerous"} }, "remote_ssh.security.require_approval drops blocked"},
	} {
		next := base()
		tc.mut(next)
		w := strings.Join(ApprovalWeakenings(base(), next), "; ")
		if tc.want == "" && w != "" {
			t.Errorf("%s: stricter change reported as weakening: %s", name, w)
		}
		if tc.want != "" && !strings.Contains(w, tc.want) {
			t.Errorf("%s: weakenings %q, want %q", name, w, tc.want)
		}
	}
	// Turning approval back on is never a weakening, whatever it exempts.
	off := base()
	off.MQTT.Approval.Required = boolp(false)
	on := base()
	on.MQTT.Approval.Exempt = []string{"#"}
	if w := ApprovalWeakenings(off, on); len(w) != 0 {
		t.Errorf("re-enabling approval reported as weakening: %v", w)
	}
}
