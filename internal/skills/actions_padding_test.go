package skills

import "testing"

// bd-3vr2 evidence: registered tool description showed "[trmnl status help]"
// — frontmatter actions + getCommonActionsForSkill fallback concatenated.
// When a skill declares explicit actions, the fallback must not pad the enum.
func TestExtractAvailableActionsNoPaddingWhenDeclared(t *testing.T) {
	i := &SkillIntegrator{loader: NewSkillLoader()}
	skill := Skill{Name: "testskill", Actions: []string{"alert", "show", "html", "note", "status", "clear"}}

	got := i.extractAvailableActions(skill)
	for _, want := range []string{"alert", "show", "html", "note", "clear"} {
		if !contains(got, want) {
			t.Fatalf("declared action %q missing from %v", want, got)
		}
	}
	for _, banned := range []string{"help", "trmnl", "testskill"} {
		if contains(got, banned) {
			t.Fatalf("padding action %q leaked into enum %v", banned, got)
		}
	}
}

// Skills with no declared actions get NO padded fallback verbs: the generic
// [status help] had no execution path for doc-only skills (conduit-7ny5,
// conduit-1jd9), so the tool schema omits the enum entirely and the model is
// steered to Read SKILL.md instead.
func TestExtractAvailableActionsNoFallbackWhenUndeclared(t *testing.T) {
	i := &SkillIntegrator{loader: NewSkillLoader()}
	skill := Skill{Name: "testskill"}

	if got := i.extractAvailableActions(skill); len(got) != 0 {
		t.Fatalf("doc-only skill actions = %v, want empty (no padded fallback)", got)
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
