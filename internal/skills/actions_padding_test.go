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

// Skills with no declared actions keep the curated fallback (status/help).
func TestExtractAvailableActionsFallbackWhenUndeclared(t *testing.T) {
	i := &SkillIntegrator{loader: NewSkillLoader()}
	skill := Skill{Name: "testskill"}

	got := i.extractAvailableActions(skill)
	if len(got) != 2 || got[0] != "status" || got[1] != "help" {
		t.Fatalf("fallback actions = %v, want [status help]", got)
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
