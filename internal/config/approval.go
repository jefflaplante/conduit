package config

import (
	"fmt"
	"reflect"
	"strings"
)

// Human approval for tool writes (H11 follow-up). MQTT publish, PagerDuty
// write actions and Datadog mute/unmute ask the owner for "YES <code>"
// unless config says otherwise. require_approval defaults to true when
// unset; the exempt lists narrow it (MQTT topic filters, tool actions).
// These keys are config-only: update_config refuses any change to them
// (ApprovalSettingsChanged), so the model cannot switch off its own
// approval prompt.

// approvalRequired resolves an optional require_approval flag; unset is true.
func approvalRequired(v *bool) bool {
	return v == nil || *v
}

// ApprovalEnabled reports whether require_approval is unset or true.
func (m *MQTTConfig) ApprovalEnabled() bool {
	return approvalRequired(m.RequireApproval)
}

// PublishNeedsApproval reports whether a publish to topic must be approved
// by a human: require_approval is unset or true and no
// approval_exempt_topics filter matches topic.
func (m *MQTTConfig) PublishNeedsApproval(topic string) bool {
	if !m.ApprovalEnabled() {
		return false
	}
	for _, f := range m.ApprovalExemptTopics {
		if MQTTTopicMatches(f, topic) {
			return false
		}
	}
	return true
}

// MQTTTopicMatches implements MQTT filter matching (+ one level, # rest).
func MQTTTopicMatches(filter, topic string) bool {
	return mqttTopicMatches(filter, topic)
}

// validateMQTTFilter checks one MQTT topic filter: no empty filter, '#'
// only as the whole last level, '+' only as a whole level.
func validateMQTTFilter(f string) error {
	if f == "" {
		return fmt.Errorf("empty topic filter")
	}
	levels := strings.Split(f, "/")
	for i, l := range levels {
		if strings.Contains(l, "#") && (l != "#" || i != len(levels)-1) {
			return fmt.Errorf("%q: '#' must be the whole last level", f)
		}
		if strings.Contains(l, "+") && l != "+" {
			return fmt.Errorf("%q: '+' must be a whole level", f)
		}
	}
	return nil
}

// PagerDutyWriteActions are the PagerDuty tool actions that change an
// incident and so go through approval.
var PagerDutyWriteActions = []string{"acknowledge", "snooze", "add_note", "resolve", "trigger"}

// DatadogApprovalActions are the Datadog monitor actions that go through
// approval.
var DatadogApprovalActions = []string{"mute_monitor", "unmute_monitor"}

// ActionNeedsApproval reports whether a PagerDuty write action must be
// approved by a human. A nil config requires approval.
func (p *PagerDutyConfig) ActionNeedsApproval(action string) bool {
	if p == nil {
		return true
	}
	return actionNeedsApproval(p.RequireApproval, p.ApprovalExemptActions, action)
}

// ActionNeedsApproval reports whether a Datadog monitor action must be
// approved by a human. A nil config requires approval.
func (d *DatadogConfig) ActionNeedsApproval(action string) bool {
	if d == nil {
		return true
	}
	return actionNeedsApproval(d.RequireApproval, d.ApprovalExemptActions, action)
}

func actionNeedsApproval(require *bool, exempt []string, action string) bool {
	if !approvalRequired(require) {
		return false
	}
	for _, a := range exempt {
		if a == action {
			return false
		}
	}
	return true
}

// validateExemptActions checks every entry of an approval_exempt_actions
// list against the actions that tool gates.
func validateExemptActions(section string, exempt, valid []string) error {
	for _, a := range exempt {
		ok := false
		for _, v := range valid {
			if a == v {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("%s.approval_exempt_actions: %q is not a gated action (valid: %s)", section, a, strings.Join(valid, ", "))
		}
	}
	return nil
}

// approvalSettings is everything that decides whether a tool write asks a
// human first. Two configs with equal approvalSettings gate identically.
type approvalSettings struct {
	MQTTRequire      bool
	MQTTExempt       []string
	PagerDutyRequire bool
	PagerDutyExempt  []string
	DatadogRequire   bool
	DatadogExempt    []string
	K8sTiers         []string
	SSHTiers         []string
}

func approvalSettingsOf(c *Config) approvalSettings {
	return approvalSettings{
		MQTTRequire:      approvalRequired(c.MQTT.RequireApproval),
		MQTTExempt:       c.MQTT.ApprovalExemptTopics,
		PagerDutyRequire: approvalRequired(c.PagerDuty.RequireApproval),
		PagerDutyExempt:  c.PagerDuty.ApprovalExemptActions,
		DatadogRequire:   approvalRequired(c.Datadog.RequireApproval),
		DatadogExempt:    c.Datadog.ApprovalExemptActions,
		K8sTiers:         c.Kubernetes.RequireApproval,
		SSHTiers:         c.RemoteSSH.Security.RequireApproval,
	}
}

// ApprovalSettingsChanged reports whether old and next differ in any
// setting that decides whether a tool write needs human approval
// (mqtt/pagerduty/datadog require_approval and exempt lists, kubernetes
// and remote_ssh.security require_approval tiers). update_config uses it
// to refuse such changes whichever key path the patch used.
func ApprovalSettingsChanged(old, next *Config) bool {
	a, b := approvalSettingsOf(old), approvalSettingsOf(next)
	norm := func(s []string) []string {
		if len(s) == 0 {
			return nil
		}
		return s
	}
	// Empty and absent exempt lists mean the same; tier lists are compared
	// as-is because the Kubernetes tool reads absent as its default and []
	// as "no approval".
	for _, p := range []*[]string{&a.MQTTExempt, &a.PagerDutyExempt, &a.DatadogExempt,
		&b.MQTTExempt, &b.PagerDutyExempt, &b.DatadogExempt} {
		*p = norm(*p)
	}
	return !reflect.DeepEqual(a, b)
}
