package config

import (
	"strings"

	"conduit/internal/policy"
)

// ToolPolicyConfig is the tool action policy (conduit-25lt.2, tool_policy in
// config.json). Every field is optional: an absent block means shadow mode
// with DefaultToolPolicyClasses. The agent cannot change this block: the
// Gateway tool's update_config refuses tool_policy keys.
type ToolPolicyConfig struct {
	// Mode is "shadow" (record decisions, block nothing; the default) or
	// "off". Enforcement arrives with phase 2.
	Mode string `json:"mode,omitempty"`

	// Default is the decision for classes no entry matches. Default: allow.
	Default string `json:"default,omitempty"`

	// Classes maps an action class or class prefix ("message",
	// "email.send.owner") to allow, ask or deny; the most specific entry
	// wins. Entries here are merged over DefaultToolPolicyClasses.
	Classes map[string]string `json:"classes,omitempty"`

	// Recipients lists targets an ask-class action may reach without asking,
	// per class or prefix, e.g. {"email.send.agent": ["owner@example.com"]}.
	Recipients map[string][]string `json:"recipients,omitempty"`

	// OwnerTargets are the owner's own chats as "channel:id"
	// ("telegram:123456789"); a message to one is message.self. Empty means
	// the telegram chat IDs of agent_heartbeat.alert_targets.
	OwnerTargets []string `json:"owner_targets,omitempty"`
}

// DefaultToolPolicyClasses are the owner's decisions of 2026-10-01: messages
// to the owner are allowed; to anyone else, and all email, ask (email to
// listed recipients is allowed through Recipients).
var DefaultToolPolicyClasses = map[string]string{
	policy.MessageSelf:    "allow",
	policy.MessageDM:      "ask",
	policy.MessageGroup:   "ask",
	policy.EmailSendAgent: "ask",
	policy.EmailSendOwner: "ask",
}

// Resolve applies defaults and returns the engine config. Call Validate
// first; invalid entries are skipped here.
func (c ToolPolicyConfig) Resolve(alertTargets []AlertTarget) policy.Config {
	out := policy.Config{
		Mode:       policy.ModeShadow,
		Default:    policy.Allow,
		Classes:    map[string]policy.Decision{},
		Recipients: c.Recipients,
	}
	if m := strings.ToLower(strings.TrimSpace(c.Mode)); m != "" {
		out.Mode = m
	}
	if d, err := policy.ParseDecision(c.Default); err == nil {
		out.Default = d
	}
	for k, v := range DefaultToolPolicyClasses {
		out.Classes[k] = policy.Decision(v)
	}
	for k, v := range c.Classes {
		if d, err := policy.ParseDecision(v); err == nil {
			out.Classes[strings.ToLower(strings.TrimSpace(k))] = d
		}
	}
	out.OwnerTargets = c.OwnerTargets
	if len(out.OwnerTargets) == 0 {
		for _, t := range alertTargets {
			if t.Type == "telegram" {
				if id := strings.TrimSpace(t.Config["chat_id"]); id != "" {
					out.OwnerTargets = append(out.OwnerTargets, "telegram:"+id)
				}
			}
		}
	}
	return out
}

func validateToolPolicy(me *multiError, c ToolPolicyConfig) {
	switch strings.ToLower(strings.TrimSpace(c.Mode)) {
	case "", policy.ModeShadow, policy.ModeOff:
	default:
		me.add("tool_policy.mode %q is invalid (want %q or %q)", c.Mode, policy.ModeShadow, policy.ModeOff)
	}
	if c.Default != "" {
		if _, err := policy.ParseDecision(c.Default); err != nil {
			me.add("tool_policy.default: %v", err)
		}
	}
	for k, v := range c.Classes {
		if strings.TrimSpace(k) == "" {
			me.add("tool_policy.classes: empty class name")
		}
		if _, err := policy.ParseDecision(v); err != nil {
			me.add("tool_policy.classes[%q]: %v", k, err)
		}
	}
	for _, t := range c.OwnerTargets {
		if !strings.Contains(t, ":") {
			me.add("tool_policy.owner_targets: %q must be \"channel:id\"", t)
		}
	}
}
