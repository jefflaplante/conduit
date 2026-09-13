package skills

import (
	"strings"
	"testing"
)

// mkGatingSkill builds a minimal skill whose name maps to common actions
// ("email" → search/read/send/cleanup/list), all of which are key actions,
// so the legacy path would emit one action tool per action.
func mkGatingSkill() Skill {
	return Skill{
		Name:        "email",
		Description: "Email skill used for gating tests",
		Content:     "## Search\nSearch the inbox.",
	}
}

func collectToolNames(integrator *SkillIntegrator, skills []Skill) map[string]bool {
	tools := integrator.GenerateToolsFromSkills(skills)
	names := make(map[string]bool, len(tools))
	for _, tl := range tools {
		names[tl.Name()] = true
	}
	return names
}

// TestActionToolsLegacyDefault verifies that with no config (nil) — the
// legacy behavior — per-action wrapper tools ARE generated alongside the
// parent tool. This pins the backward-compatible default.
func TestActionToolsLegacyDefault(t *testing.T) {
	integrator := &SkillIntegrator{loader: NewSkillLoader(), config: nil}
	names := collectToolNames(integrator, []Skill{mkGatingSkill()})

	if !names["skill_email"] {
		t.Fatalf("parent tool skill_email missing; got %v", names)
	}
	// email maps to common actions search/read/send/cleanup/list, but only
	// search/cleanup/list are in the legacy keyActions list — pin that exact set.
	for _, action := range []string{"search", "cleanup", "list"} {
		want := "email_" + action
		if !names[want] {
			t.Errorf("legacy default: action tool %q missing; got %v", want, names)
		}
	}
}

// TestActionToolsGatedOff verifies that generate_action_tools=false emits
// ONLY the parent skill tool — no per-action duplicates.
func TestActionToolsGatedOff(t *testing.T) {
	disabled := false
	cfg := &SkillsConfig{GenerateActionTools: &disabled}
	integrator := &SkillIntegrator{loader: NewSkillLoader(), config: cfg}
	names := collectToolNames(integrator, []Skill{mkGatingSkill()})

	if !names["skill_email"] {
		t.Fatalf("parent tool skill_email missing; got %v", names)
	}
	for name := range names {
		if name != "skill_email" && !strings.HasPrefix(name, "skill_") {
			t.Errorf("gated off: unexpected action tool %q leaked through; got %v", name, names)
		}
	}
	if len(names) != 1 {
		t.Errorf("gated off: want exactly 1 tool (skill_email), got %d: %v", len(names), names)
	}
}

// TestActionToolsExplicitOn verifies generate_action_tools=true preserves
// the legacy behavior when set explicitly.
func TestActionToolsExplicitOn(t *testing.T) {
	enabled := true
	cfg := &SkillsConfig{GenerateActionTools: &enabled}
	integrator := &SkillIntegrator{loader: NewSkillLoader(), config: cfg}
	names := collectToolNames(integrator, []Skill{mkGatingSkill()})

	if !names["email_search"] {
		t.Errorf("explicit on: action tool email_search missing; got %v", names)
	}
}

// TestActionToolsEnabledMethod covers the nil-receiver and pointer semantics.
func TestActionToolsEnabledMethod(t *testing.T) {
	var nilCfg *SkillsConfig
	if !nilCfg.ActionToolsEnabled() {
		t.Error("nil config must default to action tools enabled (legacy)")
	}

	disabled := false
	if (&SkillsConfig{GenerateActionTools: &disabled}).ActionToolsEnabled() {
		t.Error("explicit false must disable action tools")
	}

	enabled := true
	if !(&SkillsConfig{GenerateActionTools: &enabled}).ActionToolsEnabled() {
		t.Error("explicit true must enable action tools")
	}
}

// TestRefreshPathHonorsGate ensures the refresh path (used by Gateway
// reload_skills) flows through the same gate — it calls GenerateTools via
// the manager, which routes to GenerateToolsFromSkills.
func TestRefreshPathHonorsGate(t *testing.T) {
	disabled := false
	cfg := SkillsConfig{
		Enabled:             true,
		SearchPaths:         []string{"testdata"},
		GenerateActionTools: &disabled,
		Execution: ExecutionConfig{
			TimeoutSeconds: 10,
			Environment:    map[string]string{},
			AllowedActions: map[string][]string{},
		},
		Cache: CacheConfig{TTLSeconds: 300, Enabled: true},
	}
	manager := NewManager(cfg)

	tools, err := manager.GenerateTools(nil)
	if err != nil {
		t.Fatalf("GenerateTools failed: %v", err)
	}
	for _, tl := range tools {
		name := tl.Name()
		if !strings.HasPrefix(name, "skill_") {
			t.Errorf("gate off via manager: action tool %q leaked through", name)
		}
	}
}
