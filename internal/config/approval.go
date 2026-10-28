package config

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Human approval for tool writes (H11 follow-up). MQTT publish, PagerDuty
// write actions and Datadog mute/unmute ask the owner for "YES <code>"
// unless the tool's "approval" block says otherwise:
//
//	"mqtt": {..., "approval": {"exempt": ["zigbee2mqtt/lights/#"]}}
//
// required defaults to true; exempt lists MQTT topic filters (mqtt) or
// action names (pagerduty, datadog) that skip approval. update_config can
// change these only with the owner's approval, and the prompt flags any
// change that lowers approval (ApprovalWeakenings).

// ToolApproval is a tool's "approval" config block.
type ToolApproval struct {
	// Required: writes ask a human first. Unset means true.
	Required *bool `json:"required,omitempty"`
	// Exempt lists what skips approval while Required is on: MQTT topic
	// filters (+ and # wildcards) for mqtt, action names otherwise.
	Exempt []string `json:"exempt,omitempty"`
}

// Enabled reports whether required is unset or true.
func (a ToolApproval) Enabled() bool {
	return a.Required == nil || *a.Required
}

// needs reports whether an item must be approved, given a matcher for the
// exempt entries.
func (a ToolApproval) needs(match func(exempt string) bool) bool {
	if !a.Enabled() {
		return false
	}
	for _, e := range a.Exempt {
		if match(e) {
			return false
		}
	}
	return true
}

// PublishNeedsApproval reports whether a publish to topic must be approved
// by a human: approval is on and no exempt filter matches topic.
func (m *MQTTConfig) PublishNeedsApproval(topic string) bool {
	return m.Approval.needs(func(f string) bool { return MQTTTopicMatches(f, topic) })
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
	return p.Approval.needs(func(a string) bool { return a == action })
}

// ActionNeedsApproval reports whether a Datadog monitor action must be
// approved by a human. A nil config requires approval.
func (d *DatadogConfig) ActionNeedsApproval(action string) bool {
	if d == nil {
		return true
	}
	return d.Approval.needs(func(a string) bool { return a == action })
}

// validateExemptActions checks every entry of an approval.exempt list
// against the actions that tool gates.
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
			return fmt.Errorf("%s.approval.exempt: %q is not a gated action (valid: %s)", section, a, strings.Join(valid, ", "))
		}
	}
	return nil
}

// defaultK8sApprovalTiers mirrors k8s.DefaultRequireApproval (absent list).
var defaultK8sApprovalTiers = []string{"dangerous"}

// ApprovalWeakenings lists, in plain words, every way next asks a human
// less often than old: a tool's approval turned off, new exempt entries,
// or Kubernetes / remote SSH approval tiers removed. Empty means next is at
// least as strict. update_config shows these as a warning in its approval
// prompt; the startup check logs them.
func ApprovalWeakenings(old, next *Config) []string {
	var out []string
	tool := func(section string, a, b ToolApproval) {
		switch {
		case a.Enabled() && !b.Enabled():
			out = append(out, section+".approval.required turned off: every write runs without asking")
		case a.Enabled():
			if added := added(a.Exempt, b.Exempt); len(added) > 0 {
				out = append(out, fmt.Sprintf("%s.approval.exempt adds %s: those writes run without asking", section, strings.Join(added, ", ")))
			}
		}
	}
	tool("mqtt", old.MQTT.Approval, next.MQTT.Approval)
	tool("pagerduty", old.PagerDuty.Approval, next.PagerDuty.Approval)
	tool("datadog", old.Datadog.Approval, next.Datadog.Approval)

	k8s := func(t []string) []string {
		if t == nil {
			return defaultK8sApprovalTiers
		}
		return t
	}
	if dropped := added(k8s(next.Kubernetes.RequireApproval), k8s(old.Kubernetes.RequireApproval)); len(dropped) > 0 {
		out = append(out, "kubernetes.require_approval drops "+strings.Join(dropped, ", "))
	}
	ssh := func(t []string) []string {
		if t == nil { // the SSH tool fills in its default tiers
			return DefaultRemoteSSHConfig().Security.RequireApproval
		}
		return t
	}
	if dropped := added(ssh(next.RemoteSSH.Security.RequireApproval), ssh(old.RemoteSSH.Security.RequireApproval)); len(dropped) > 0 {
		out = append(out, "remote_ssh.security.require_approval drops "+strings.Join(dropped, ", "))
	}
	return out
}

// added returns the entries of b that are not in a, sorted.
func added(a, b []string) []string {
	have := make(map[string]bool, len(a))
	for _, s := range a {
		have[s] = true
	}
	var out []string
	for _, s := range b {
		if !have[s] {
			out = append(out, s)
			have[s] = true
		}
	}
	sort.Strings(out)
	return out
}

// ApprovalSettingsChanged reports whether old and next differ in any
// setting that decides whether a tool write needs human approval (the
// mqtt/pagerduty/datadog approval blocks and the kubernetes and
// remote_ssh.security require_approval tiers), stricter or looser.
func ApprovalSettingsChanged(old, next *Config) bool {
	type settings struct {
		On     [3]bool
		Exempt [3][]string
		K8s    []string
		SSH    []string
	}
	of := func(c *Config) settings {
		s := settings{K8s: c.Kubernetes.RequireApproval, SSH: c.RemoteSSH.Security.RequireApproval}
		for i, a := range []ToolApproval{c.MQTT.Approval, c.PagerDuty.Approval, c.Datadog.Approval} {
			s.On[i] = a.Enabled()
			if len(a.Exempt) > 0 { // empty and absent mean the same
				s.Exempt[i] = a.Exempt
			}
		}
		return s
	}
	// Tier lists are compared as-is: the Kubernetes tool reads absent as
	// its default and [] as "no approval".
	return !reflect.DeepEqual(of(old), of(next))
}
