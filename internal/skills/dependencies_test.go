package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseDependencyPaths(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{
			name:    "no dependencies section",
			content: "# Skill\n\nNo deps here.",
			want:    nil,
		},
		{
			name: "emoji heading with references bullet",
			content: "# Solar\n\n## 📎 Dependencies\n\n" +
				"- **Env:** Source `.ocgo-secrets.env`\n" +
				"- **Skills:** `ha`, `ev-tracking`\n" +
				"- **References:** `reference/ha-entities.md` and `reference/scripts.md`\n\n" +
				"## Data Sources\n",
			want: []string{"reference/ha-entities.md", "reference/scripts.md"},
		},
		{
			name: "plain heading with local references",
			content: "## Dependencies\n\n" +
				"- **References:** `reference/briefing.md`\n" +
				"- **Local:** `references/morning.md`, `references/midday.md`\n\n" +
				"## Next\n",
			want: []string{"reference/briefing.md", "references/morning.md", "references/midday.md"},
		},
		{
			name: "unlabelled bullet with backticked file",
			content: "## Dependencies\n" +
				"- `docs/guide.md`\n" +
				"- `config.yaml`\n" +
				"## Other\n",
			want: []string{"docs/guide.md", "config.yaml"},
		},
		{
			name: "env vars and skill names are not treated as paths",
			content: "## Dependencies\n" +
				"- **Env:** `$HA_TOKEN`\n" +
				"- **Skills:** `ha`, `solar`\n" +
				"## End\n",
			want: nil,
		},
		{
			name: "bold dependencies label without heading",
			content: "**Dependencies:**\n" +
				"- `ref.md`\n" +
				"## Usage\n",
			want: []string{"ref.md"},
		},
		{
			name: "section closes at next heading",
			content: "## Dependencies\n" +
				"- `first.md`\n" +
				"## Usage\n" +
				"- `should-not-load.md`\n",
			want: []string{"first.md"},
		},
		{
			name: "dedupe repeated paths",
			content: "## Dependencies\n" +
				"- **References:** `a.md`, `a.md`\n" +
				"- `a.md`\n" +
				"## End\n",
			want: []string{"a.md"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseDependencyPaths(tc.content)
			if !stringSlicesEqual(got, tc.want) {
				t.Errorf("parseDependencyPaths() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResolveDependencies_Success(t *testing.T) {
	skillDir := t.TempDir()
	refDir := filepath.Join(skillDir, "reference")
	if err := os.MkdirAll(refDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	const body = "# HA Entities\n\n- light.kitchen\n- switch.porch\n"
	refFile := filepath.Join(refDir, "ha-entities.md")
	if err := os.WriteFile(refFile, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	deps := resolveDependencies(skillDir, []string{"reference/ha-entities.md"})
	if len(deps) != 1 {
		t.Fatalf("expected 1 dep, got %d", len(deps))
	}
	dep := deps[0]
	if dep.Missing || dep.Skipped {
		t.Fatalf("dep should be loaded, got missing=%v skipped=%v reason=%q",
			dep.Missing, dep.Skipped, dep.SkipReason)
	}
	if dep.Content != body {
		t.Errorf("content mismatch: got %q, want %q", dep.Content, body)
	}
	if dep.Size != int64(len(body)) {
		t.Errorf("size mismatch: got %d, want %d", dep.Size, len(body))
	}
}

func TestResolveDependencies_Missing(t *testing.T) {
	skillDir := t.TempDir()

	deps := resolveDependencies(skillDir, []string{"reference/does-not-exist.md"})
	if len(deps) != 1 {
		t.Fatalf("expected 1 dep entry, got %d", len(deps))
	}
	if !deps[0].Missing {
		t.Errorf("dep should be marked missing, got %+v", deps[0])
	}
	if deps[0].Content != "" {
		t.Errorf("missing dep should have empty content, got %q", deps[0].Content)
	}
}

func TestResolveDependencies_RejectsEscape(t *testing.T) {
	skillDir := t.TempDir()

	cases := []string{
		"../etc/passwd",
		"/etc/passwd",
		"sub/../../escape.md",
	}
	for _, p := range cases {
		deps := resolveDependencies(skillDir, []string{p})
		if len(deps) != 1 || !deps[0].Skipped {
			t.Errorf("path %q should be skipped for safety, got %+v", p, deps)
		}
	}
}

func TestResolveDependencies_OversizedSkipped(t *testing.T) {
	skillDir := t.TempDir()
	big := filepath.Join(skillDir, "big.md")
	data := make([]byte, MaxDependencyFileBytes+1)
	for i := range data {
		data[i] = 'x'
	}
	if err := os.WriteFile(big, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	deps := resolveDependencies(skillDir, []string{"big.md"})
	if len(deps) != 1 {
		t.Fatalf("expected 1 dep, got %d", len(deps))
	}
	if !deps[0].Skipped {
		t.Errorf("oversized dep should be skipped, got %+v", deps[0])
	}
	if deps[0].Content != "" {
		t.Errorf("skipped dep should have no content, got %d bytes", len(deps[0].Content))
	}
}

func TestResolveDependencies_Directory(t *testing.T) {
	skillDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(skillDir, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// docs has no extension; looksLikeFilePath would reject it, but we still
	// want resolveDependencies to handle directories gracefully when given a
	// path that happens to have an extension-like suffix.
	sub := filepath.Join(skillDir, "notes.md")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	deps := resolveDependencies(skillDir, []string{"notes.md"})
	if len(deps) != 1 || !deps[0].Skipped {
		t.Errorf("directory should be skipped, got %+v", deps)
	}
}

func TestDiscovery_LoadsDependenciesOnDiscover(t *testing.T) {
	tmp := t.TempDir()
	skillDir := filepath.Join(tmp, "hass")
	if err := os.MkdirAll(filepath.Join(skillDir, "reference"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	skillMd := `---
name: hass
description: Home Assistant skill
---

# HA

## Dependencies

- **References:** ` + "`reference/ha-entities.md`" + `

## Usage
`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillMd), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	depBody := "# HA Entities\nlight.kitchen\n"
	if err := os.WriteFile(filepath.Join(skillDir, "reference", "ha-entities.md"), []byte(depBody), 0o644); err != nil {
		t.Fatalf("write dep: %v", err)
	}

	d := NewSkillDiscovery([]string{tmp})
	skills, err := d.DiscoverSkills(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	s := skills[0]
	if len(s.Dependencies) != 1 {
		t.Fatalf("expected 1 dep, got %d", len(s.Dependencies))
	}
	if s.Dependencies[0].Content != depBody {
		t.Errorf("dep content mismatch: got %q want %q", s.Dependencies[0].Content, depBody)
	}
}

func TestDiscovery_LoadsWithMissingDependency(t *testing.T) {
	tmp := t.TempDir()
	skillDir := filepath.Join(tmp, "hass")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	skillMd := `---
name: hass
description: Home Assistant skill
---

## Dependencies

- **References:** ` + "`reference/missing.md`" + `
`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillMd), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	d := NewSkillDiscovery([]string{tmp})
	skills, err := d.DiscoverSkills(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected skill to load despite missing dep, got %d skills", len(skills))
	}
	if len(skills[0].Dependencies) != 1 || !skills[0].Dependencies[0].Missing {
		t.Errorf("expected one missing dep entry, got %+v", skills[0].Dependencies)
	}
}

func TestBuildDependencyContext_IncludesContent(t *testing.T) {
	skills := []Skill{
		{
			Name: "hass",
			Dependencies: []SkillDependency{
				{Path: "reference/ha-entities.md", Content: "ENTITIES_BODY"},
				{Path: "reference/gone.md", Missing: true},
				{Path: "reference/huge.md", Skipped: true, SkipReason: "too big"},
			},
		},
	}

	got := BuildDependencyContext(skills)
	if !strings.Contains(got, "ENTITIES_BODY") {
		t.Errorf("expected dep content inlined, got: %s", got)
	}
	if !strings.Contains(got, "reference/ha-entities.md") {
		t.Errorf("expected dep path header, got: %s", got)
	}
	if !strings.Contains(got, "reference/gone.md") || !strings.Contains(got, "not found") {
		t.Errorf("expected missing-dep note, got: %s", got)
	}
	if !strings.Contains(got, "too big") {
		t.Errorf("expected skipped-dep reason, got: %s", got)
	}
}

func TestBuildSkillsContext_IncludesDependencies(t *testing.T) {
	integrator := NewSkillIntegrator(NewExecutor(ExecutionConfig{TimeoutSeconds: 5}))
	skills := []Skill{
		{
			Name:        "hass",
			Description: "home automation",
			Dependencies: []SkillDependency{
				{Path: "reference/ha-entities.md", Content: "ENTITIES_BODY"},
			},
		},
	}
	out := integrator.BuildSkillsContext(skills)
	if !strings.Contains(out, "ENTITIES_BODY") {
		t.Errorf("BuildSkillsContext should include dep content, got: %s", out)
	}
	if !strings.Contains(out, "Skill Dependencies (auto-loaded)") {
		t.Errorf("BuildSkillsContext should label dep section, got: %s", out)
	}
}

// TestParseDependencyPaths_RealSolarSkill pins the parser behavior against the
// actual solar skill declaration shape used in Jeff's workspace.
func TestParseDependencyPaths_RealSolarSkill(t *testing.T) {
	content := "# Solar Reporting\n\n" +
		"## 📎 Dependencies\n\n" +
		"- **Env:** Source `.ocgo-secrets.env` before commands\n" +
		"- **Tools:** `lux` CLI (`~/.local/bin/lux`) — direct inverter communication via Modbus/TCP\n" +
		"- **Fallback:** Home Assistant API (SolarAssistant MQTT → HA entities)\n" +
		"- **Skills:** `ha` (sensor access), `ev-tracking` (EV/lab data for combined reports)\n" +
		"- **References:** `reference/ha-entities.md` (solar/battery sensor entities), `reference/scripts.md` (building blocks catalog)\n\n" +
		"## Data Sources\n"

	got := parseDependencyPaths(content)
	want := []string{"reference/ha-entities.md", "reference/scripts.md"}
	if !stringSlicesEqual(got, want) {
		t.Errorf("real solar skill paths: got %v, want %v", got, want)
	}
}

// stringSlicesEqual returns true if a and b contain the same strings in order.
func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
