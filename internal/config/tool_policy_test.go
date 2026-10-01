package config

import (
	"strings"
	"testing"

	"conduit/internal/policy"
)

func TestToolPolicy_ResolveDefaults(t *testing.T) {
	got := ToolPolicyConfig{}.Resolve([]AlertTarget{
		{Type: "telegram", Config: map[string]string{"chat_id": "123456789"}},
		{Type: "webhook", Config: map[string]string{"url": "https://example.com"}},
	})
	if got.Mode != policy.ModeShadow || got.Default != policy.Allow {
		t.Fatalf("mode/default = %s/%s", got.Mode, got.Default)
	}
	if got.Classes[policy.MessageSelf] != policy.Allow || got.Classes[policy.MessageGroup] != policy.Ask ||
		got.Classes[policy.EmailSendOwner] != policy.Ask {
		t.Fatalf("classes = %v", got.Classes)
	}
	if len(got.OwnerTargets) != 1 || got.OwnerTargets[0] != "telegram:123456789" {
		t.Fatalf("owner targets from alert targets = %v", got.OwnerTargets)
	}
}

func TestToolPolicy_ResolveOverrides(t *testing.T) {
	got := ToolPolicyConfig{
		Mode:         "OFF",
		Default:      "ask",
		Classes:      map[string]string{"Message.DM": "allow", "web.fetch": "deny"},
		OwnerTargets: []string{"telegram:1"},
	}.Resolve([]AlertTarget{{Type: "telegram", Config: map[string]string{"chat_id": "2"}}})
	if got.Mode != policy.ModeOff || got.Default != policy.Ask {
		t.Fatalf("mode/default = %s/%s", got.Mode, got.Default)
	}
	if got.Classes[policy.MessageDM] != policy.Allow || got.Classes["web.fetch"] != policy.Deny || got.Classes[policy.MessageGroup] != policy.Ask {
		t.Fatalf("classes = %v", got.Classes)
	}
	if len(got.OwnerTargets) != 1 || got.OwnerTargets[0] != "telegram:1" {
		t.Fatalf("explicit owner targets must win: %v", got.OwnerTargets)
	}
}

func TestToolPolicy_Validate(t *testing.T) {
	var me multiError
	validateToolPolicy(&me, ToolPolicyConfig{
		Mode:         "enforce",
		Default:      "maybe",
		Classes:      map[string]string{"message": "sometimes", "": "allow"},
		OwnerTargets: []string{"123"},
	})
	msg := me.toError().Error()
	for _, want := range []string{"tool_policy.mode", "tool_policy.default", `tool_policy.classes["message"]`, "empty class name", "owner_targets"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in %s", want, msg)
		}
	}
	var ok multiError
	validateToolPolicy(&ok, ToolPolicyConfig{Mode: "shadow", Classes: map[string]string{"message.group": "ask"}, OwnerTargets: []string{"telegram:1"}})
	if ok.hasErrors() {
		t.Fatalf("valid config rejected: %v", ok.toError())
	}
}
