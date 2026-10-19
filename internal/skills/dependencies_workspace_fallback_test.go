package skills

import (
	"os"
	"path/filepath"
	"testing"
)

// bd-3vr2: SKILL.md dependency bullets declare paths relative to the
// workspace root (skills/<name>/scripts/x.py, memory/foo.json).
// resolveDependencies used to try the skill dir only, so those deps were
// always flagged Missing even though the files exist. New behavior: skill dir
// first, then workspace-root fallback (parent of skills/<name>).

func writeTree(t *testing.T) (ws, skillDir string) {
	t.Helper()
	ws = t.TempDir()
	skillDir = filepath.Join(ws, "skills", "testskill")
	scripts := filepath.Join(skillDir, "scripts")
	if err := os.MkdirAll(scripts, 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(scripts, "helper.py")
	if err := os.WriteFile(script, []byte("print('hi')\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	mem := filepath.Join(ws, "memory")
	if err := os.MkdirAll(mem, 0o755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(mem, "testskill-state.json")
	if err := os.WriteFile(state, []byte(`{"ok":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return ws, skillDir
}

func TestDependencyResolutionFallsBackToWorkspaceRoot(t *testing.T) {
	_, skillDir := writeTree(t)

	deps := resolveDependencies(skillDir, []string{"skills/testskill/scripts/helper.py"})
	if len(deps) != 1 {
		t.Fatalf("expected 1 dep, got %d", len(deps))
	}
	d := deps[0]
	if d.Missing || d.Skipped {
		t.Fatalf("dep should resolve via workspace fallback: missing=%v skipped=%v (%s) resolved=%q",
			d.Missing, d.Skipped, d.SkipReason, d.ResolvedPath)
	}
	if d.Content == "" {
		t.Fatal("content should be populated after fallback resolution")
	}
}

func TestDependencyResolutionWorkspaceRootStateDir(t *testing.T) {
	_, skillDir := writeTree(t)

	deps := resolveDependencies(skillDir, []string{"memory/testskill-state.json"})
	if len(deps) != 1 {
		t.Fatalf("expected 1 dep, got %d", len(deps))
	}
	if deps[0].Missing {
		t.Fatalf("workspace-root memory/ dep should resolve, resolved=%q", deps[0].ResolvedPath)
	}
	if deps[0].Content != `{"ok":true}` {
		t.Fatalf("content = %q", deps[0].Content)
	}
}

func TestDependencyResolutionSkillRelativeStillPreferred(t *testing.T) {
	_, skillDir := writeTree(t)

	// A dep that exists under the skill dir resolves there as before.
	deps := resolveDependencies(skillDir, []string{"scripts/helper.py"})
	if len(deps) != 1 || deps[0].Missing || deps[0].Content == "" {
		t.Fatalf("skill-relative dep should resolve unchanged: %+v", deps)
	}
}

func TestDependencyResolutionFallbackContainedToWorkspace(t *testing.T) {
	_, skillDir := writeTree(t)

	// Traversal must stay rejected under the fallback too.
	deps := resolveDependencies(skillDir, []string{"../../etc/passwd"})
	if len(deps) != 1 {
		t.Fatalf("expected 1 dep, got %d", len(deps))
	}
	if !deps[0].Skipped {
		t.Fatalf("traversal dep should be skipped: %+v", deps[0])
	}
}

// bd-3vr2: script/state/template bullets are operational notes, not
// prompt-autoloadable reference files. They must not become deps.
func TestParseDependencyPathsSkipsScriptStateLabels(t *testing.T) {
	content := "## 📎 Dependencies\n\n" +
		"- **Script:** `skills/testskill/scripts/helper.py`\n" +
		"- **State:** `memory/testskill-state.json`\n" +
		"- **Templates:** `skills/x/templates/questions.md`\n" +
		"- **References:** `reference/real.md`\n"
	got := parseDependencyPaths(content)
	want := []string{"reference/real.md"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("parseDependencyPaths = %v, want %v", got, want)
	}
}
