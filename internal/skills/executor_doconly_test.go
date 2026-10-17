package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeExecutable writes an executable file (test helper).
func writeExecutable(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o755)
}

// getSystemPath returns the test process PATH so tests can prepend fake
// binary directories without losing system lookups.
func getSystemPath() string {
	return os.Getenv("PATH")
}

// docOnlySkill builds an in-memory skill with no scripts and no frontmatter
// actions — the shape of skills like t5t/beads that are pure documentation
// (conduit-7ny5). The integrator still advertises the fallback
// [status help] actions for these (integration.go getCommonActionsForSkill),
// so the executor must give those advertised verbs an honest, non-error path.
func docOnlySkill(t *testing.T, location string) Skill {
	t.Helper()
	loader := NewSkillLoader()
	skill, err := loader.LoadSkillFromContent("---\nname: doconly\ndescription: doc-only skill\n---\n# Doc-only skill\n\nProse only, no code blocks, no scripts, no actions.\n")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	skill.Location = location
	return *skill
}

// TestExecutor_DocOnlySkillStatusHelpShortCircuit verifies that help/status
// on a doc-only skill (no scripts, no actions frontmatter) returns success
// with a pointer to SKILL.md instead of the guaranteed
// no-command-found-for-action error round-trip. The advertised fallback
// actions [status help] (getCommonActionsForSkill) had no execution path:
// determineExecutionMethod → subprocess, buildShellCommand → no builder →
// empty command → error. Now the model gets a self-correcting pointer to
// Read the skill file.
func TestExecutor_DocOnlySkillStatusHelpShortCircuit(t *testing.T) {
	dir := t.TempDir()
	skill := docOnlySkill(t, dir)
	if len(skill.Scripts) != 0 || len(skill.Actions) != 0 {
		t.Fatalf("fixture must be doc-only; got scripts=%d actions=%d", len(skill.Scripts), len(skill.Actions))
	}

	e := NewExecutor(ExecutionConfig{TimeoutSeconds: 10})
	for _, action := range []string{"status", "help"} {
		result, err := e.ExecuteSkill(context.Background(), skill, action, map[string]interface{}{})
		if err != nil {
			t.Fatalf("action %q: ExecuteSkill error: %v", action, err)
		}
		if !result.Success {
			t.Errorf("action %q must short-circuit to success for doc-only skills; got error: %s", action, result.Error)
		}
		if !strings.Contains(result.Output, "SKILL.md") {
			t.Errorf("action %q output should point at SKILL.md for self-correction; got: %q", action, result.Output)
		}
	}
}

// TestExecutor_DocOnlySkillRealActionsStillFailHonestly verifies the
// short-circuit does not resurrect the echo fallback: arbitrary actions on a
// doc-only skill still fail honestly with no-command-found-for-action rather
// than reporting fake success.
func TestExecutor_DocOnlySkillRealActionsStillFailHonestly(t *testing.T) {
	dir := t.TempDir()
	skill := docOnlySkill(t, dir)

	e := NewExecutor(ExecutionConfig{TimeoutSeconds: 10})
	result, err := e.ExecuteSkill(context.Background(), skill, "generate_report", map[string]interface{}{})
	if err != nil {
		t.Fatalf("ExecuteSkill error: %v", err)
	}
	if result.Success {
		t.Fatalf("non-status/help action on doc-only skill must fail honestly; got success with output %q", result.Output)
	}
	if !strings.Contains(result.Error, "no command found for action") {
		t.Errorf("expected honest no-command error; got: %q", result.Error)
	}
}

// TestExecutor_ScriptSkillsKeepRealStatus verifies the short-circuit never
// masks skills with real scripts: a script-bound skill's status action must
// still execute the script, not return a doc-only pointer.
func TestExecutor_ScriptSkillsKeepRealStatus(t *testing.T) {
	dir := t.TempDir()
	skill := writeScript(t, dir, "dispatch.sh", "#!/bin/bash\ncat >/dev/null\necho REAL_SCRIPT_RAN\n")

	e := NewExecutor(ExecutionConfig{TimeoutSeconds: 10})
	result, err := e.ExecuteSkill(context.Background(), skill, "status", map[string]interface{}{})
	if err != nil {
		t.Fatalf("ExecuteSkill error: %v", err)
	}
	if !result.Success {
		t.Fatalf("script skill failed: %s (output: %s)", result.Error, result.Output)
	}
	if !strings.Contains(result.Output, "REAL_SCRIPT_RAN") {
		t.Errorf("script-bound skill status must run the script; got: %q", result.Output)
	}
}

// TestExtractAvailableActions_DocOnlySkillsAdvertiseNothing verifies the
// integrator stops advertising the fallback [status help] verbs for doc-only
// skills (no scripts, no frontmatter actions): those verbs have no execution
// path, so advertising them guarantees an error round-trip. With no honest
// actions the tool schema omits the enum (conduit-1jd9) and the model is
// steered to Read the SKILL.md. Script-bound and curated skills keep their
// current advertised lists.
func TestExtractAvailableActions_DocOnlySkillsAdvertiseNothing(t *testing.T) {
	i := &SkillIntegrator{}

	docOnly := docOnlySkill(t, t.TempDir())
	if got := i.extractAvailableActions(docOnly); len(got) != 0 {
		t.Errorf("doc-only skill must not advertise fallback actions; got %v", got)
	}

	scriptBound := docOnly
	scriptBound.Scripts = []SkillScript{{Name: "run", Path: "run.sh", Language: "bash"}}
	if got := i.extractAvailableActions(scriptBound); len(got) != 1 || got[0] != "run" {
		t.Errorf("script-bound skill should advertise exactly its script names; got %v", got)
	}

	curations := map[string][]string{
		"briefing": {"generate", "morning", "evening"},
		"email":    {"search", "read", "send", "cleanup", "list"},
	}
	for name, want := range curations {
		curated := docOnly
		curated.Name = name
		if got := i.extractAvailableActions(curated); len(got) != len(want) {
			t.Errorf("curated skill %q must keep its action list even with no scripts/actions; got %v want %v", name, got, want)
		}
	}
}

// TestDocOnlySkillSteering verifies the doc-only surfaces say so: the tool
// description points at SKILL.md instead of listing dead verbs, and the
// skills context line replaces "actions: none" with a Read pointer.
func TestDocOnlySkillSteering(t *testing.T) {
	docOnly := docOnlySkill(t, t.TempDir())

	tool := &SkillTool{skill: docOnly, executor: &Executor{}}
	desc := tool.Description()
	if !strings.Contains(desc, "SKILL.md") || strings.Contains(desc, "Available actions") {
		t.Errorf("doc-only tool description should steer to SKILL.md; got %q", desc)
	}

	integrator := &SkillIntegrator{loader: NewSkillLoader(), config: nil}
	ctxLine := integrator.BuildSkillsContext([]Skill{docOnly})
	if !strings.Contains(ctxLine, "doc-only — Read skills/doconly/SKILL.md") {
		t.Errorf("skills context should carry a doc-only Read pointer; got %q", ctxLine)
	}
}

// TestExecutor_CuratedSubprocessSkillsKeepRealStatus verifies the doc-only
// short-circuit never masks skills with curated subprocess builders
// (email/gog): even with no scripts and no frontmatter actions, their status
// action must still build and run the real gog command rather than a
// doc-only pointer.
func TestExecutor_CuratedSubprocessSkillsKeepRealStatus(t *testing.T) {
	dir := t.TempDir()
	loader := NewSkillLoader()
	skill, err := loader.LoadSkillFromContent("---\nname: email\ndescription: mail skill\n---\nbody")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	skill.Location = dir
	if len(skill.Scripts) != 0 || len(skill.Actions) != 0 {
		t.Fatalf("fixture must have no scripts/actions; got scripts=%d actions=%d", len(skill.Scripts), len(skill.Actions))
	}

	// A fake gog on PATH that records the invocation and succeeds.
	fakeBin := t.TempDir()
	script := "#!/bin/sh\necho \"GOG_INVOKED $*\"\n"
	if err := writeExecutable(filepath.Join(fakeBin, "gog"), script); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+":"+getSystemPath())

	e := NewExecutor(ExecutionConfig{TimeoutSeconds: 10})
	result, err := e.ExecuteSkill(context.Background(), *skill, "status", map[string]interface{}{})
	if err != nil {
		t.Fatalf("ExecuteSkill error: %v", err)
	}
	if !result.Success {
		t.Fatalf("email skill status failed: %s (output: %s)", result.Error, result.Output)
	}
	if !strings.Contains(result.Output, "GOG_INVOKED") {
		t.Errorf("curated subprocess skill status must run the real builder; got: %q", result.Output)
	}
}
