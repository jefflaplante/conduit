package agent

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"conduit/internal/ai"
	"conduit/internal/sessions"
	"conduit/internal/skills"
	"conduit/internal/workspace"
)

// conduit-331g: per-topic prompt profiles. The gateway stamps a topic's
// profile into the turn's session context (sessions.ContextSkillFilter,
// sessions.ContextSkip); the prompt builder narrows the Skills and Project
// Context sections from those keys. Without them the prompt must not change
// by a single byte.

var updateProfileGolden = flag.Bool("update-profile-golden", false, "rewrite testdata/topic_profile_noprofile_golden.txt")

var profileFixtureSkills = []string{"alpha-skill", "beta-skill", "gamma-skill"}

// profileFixtureFiles are the workspace context files of the fixture. Each
// body carries a unique marker so a test can tell which files were loaded.
var profileFixtureFiles = map[string]string{
	"SOUL.md":      "soul-marker: be kind and safe\n",
	"USER.md":      "user-marker: synthetic owner\n",
	"AGENTS.md":    "agents-marker: agent rules\n",
	"TOOLS.md":     "tools-marker: tool notes\n",
	"IDENTITY.md":  "identity-marker: Conduit\n",
	"BOOTSTRAP.md": "bootstrap-marker: first run\n",
	"MEMORY.md":    "memory-marker: long-term facts\n",
}

// newProfilePromptBuilder returns a prompt builder with three skills and a
// workspace holding every core context file, plus the skills directory (so
// tests can normalize it out of the output).
func newProfilePromptBuilder(t *testing.T) (*PromptBuilder, string) {
	t.Helper()
	skillsDir := t.TempDir()
	for _, name := range profileFixtureSkills {
		dir := filepath.Join(skillsDir, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		md := "---\nname: " + name + "\ndescription: Fixture skill " + name + "\n---\nbody of " + name + "\n"
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sm := skills.NewManager(skills.SkillsConfig{Enabled: true, SearchPaths: []string{skillsDir}})
	if err := sm.Initialize(context.Background()); err != nil {
		t.Fatalf("skills init: %v", err)
	}

	wsDir := t.TempDir()
	for name, body := range profileFixtureFiles {
		if err := os.WriteFile(filepath.Join(wsDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	pb := newTestPromptBuilder()
	pb.capabilities.SkillsIntegration = true
	pb.skillsManager = sm
	pb.workspaceContext = workspace.NewWorkspaceContext(wsDir)
	pb.SetClock(func() time.Time { return time.Date(2026, 9, 28, 14, 30, 0, 0, time.UTC) })
	return pb, skillsDir
}

func profileSession(ctx map[string]string) *sessions.Session {
	c := map[string]string{"model": "claude-sonnet-4-6"}
	for k, v := range ctx {
		c[k] = v
	}
	return &sessions.Session{Key: "topic_abc123", ChannelID: "telegram", UserID: "123456789", Context: c}
}

func buildProfileStatic(t *testing.T, pb *PromptBuilder, s *sessions.Session) string {
	t.Helper()
	blocks, err := pb.Build(context.Background(), s, false)
	if err != nil {
		t.Fatal(err)
	}
	return blocks[0].Text
}

// skillsSection returns the "## Skills (mandatory)" section of a prompt.
func skillsSection(prompt string) string {
	i := strings.Index(prompt, "## Skills (mandatory)")
	if i < 0 {
		return ""
	}
	rest := prompt[i+len("## Skills (mandatory)"):]
	if j := strings.Index(rest, "\n## "); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

func TestTopicProfile_SkillsAndContextNarrowed(t *testing.T) {
	pb, _ := newProfilePromptBuilder(t)
	full := buildProfileStatic(t, pb, profileSession(nil))
	lean := buildProfileStatic(t, pb, profileSession(map[string]string{
		sessions.ContextSkillFilter: "alpha-skill,gamma-skill",
		sessions.ContextSkip:        "AGENTS.md,tools.md,SOUL.md,MEMORY.md,NOPE.md",
	}))

	if len(lean) >= len(full) {
		t.Fatalf("profile did not shrink the prompt: %d >= %d chars", len(lean), len(full))
	}

	fullSkills, leanSkills := skillsSection(full), skillsSection(lean)
	for _, name := range profileFixtureSkills {
		if !strings.Contains(fullSkills, name) {
			t.Fatalf("full prompt lacks skill %s", name)
		}
	}
	if !strings.Contains(leanSkills, "alpha-skill") || !strings.Contains(leanSkills, "gamma-skill") {
		t.Fatalf("allowlisted skills missing from lean skills section:\n%s", leanSkills)
	}
	if strings.Contains(leanSkills, "beta-skill") {
		t.Fatalf("beta-skill not allowlisted but present:\n%s", leanSkills)
	}

	for _, gone := range []string{"agents-marker", "tools-marker", "memory-marker", "## AGENTS.md", "## TOOLS.md", "## MEMORY.md"} {
		if strings.Contains(lean, gone) {
			t.Errorf("skipped context %q still in prompt", gone)
		}
	}
	// SOUL.md is pinned: listing it in skip_context does nothing.
	for _, kept := range []string{"## SOUL.md", "soul-marker", "user-marker", "identity-marker", "bootstrap-marker"} {
		if !strings.Contains(lean, kept) {
			t.Errorf("context %q missing from lean prompt", kept)
		}
	}
}

// A profile with only one of the two keys narrows only that section.
func TestTopicProfile_IndependentKeys(t *testing.T) {
	pb, _ := newProfilePromptBuilder(t)
	full := buildProfileStatic(t, pb, profileSession(nil))

	onlySkip := buildProfileStatic(t, pb, profileSession(map[string]string{sessions.ContextSkip: "USER.md"}))
	if skillsSection(onlySkip) != skillsSection(full) {
		t.Errorf("context skip changed the skills section")
	}
	if strings.Contains(onlySkip, "user-marker") {
		t.Errorf("USER.md not skipped")
	}

	onlySkills := buildProfileStatic(t, pb, profileSession(map[string]string{sessions.ContextSkillFilter: "beta-skill"}))
	for _, m := range []string{"soul-marker", "user-marker", "agents-marker", "tools-marker", "memory-marker"} {
		if !strings.Contains(onlySkills, m) {
			t.Errorf("skill filter dropped context %q", m)
		}
	}
}

// No-profile invariant: a session without profile keys (home, or a topic
// with no profile) builds exactly the pre-conduit-331g prompt. The golden
// file was generated from the code before profiles existed; empty profile
// keys must match it too.
func TestTopicProfile_NoProfileByteIdentical(t *testing.T) {
	pb, skillsDir := newProfilePromptBuilder(t)
	hostname, _ := os.Hostname()
	normalize := func(s string) string {
		s = strings.ReplaceAll(s, skillsDir, "<SKILLS>")
		s = strings.ReplaceAll(s, "host="+hostname+" |", "host=<HOST> |")
		s = strings.ReplaceAll(s, fmt.Sprintf("os=%s (%s) |", runtime.GOOS, runtime.GOARCH), "os=<OS> |")
		s = strings.ReplaceAll(s, "node="+runtime.Version()+" |", "node=<GOVERSION> |")
		return s
	}
	render := func(s *sessions.Session) string {
		blocks, err := pb.Build(context.Background(), s, false)
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		for i, blk := range blocks {
			fmt.Fprintf(&b, "===== block %d dynamic=%v =====\n%s\n", i, blk.Dynamic, normalize(blk.Text))
		}
		return b.String()
	}

	got := render(profileSession(nil))
	path := filepath.Join("testdata", "topic_profile_noprofile_golden.txt")
	if *updateProfileGolden {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Fatalf("no-profile prompt differs from the pre-profile golden")
	}

	home := &sessions.Session{Key: "telegram_123456789", ChannelID: "telegram", UserID: "123456789",
		Context: map[string]string{"model": "claude-sonnet-4-6"}}
	homeEmpty := &sessions.Session{Key: "telegram_123456789", ChannelID: "telegram", UserID: "123456789",
		Context: map[string]string{"model": "claude-sonnet-4-6", sessions.ContextSkillFilter: "", sessions.ContextSkip: ""}}
	if render(home) != render(homeEmpty) {
		t.Fatalf("empty profile keys changed the prompt")
	}
}

// The agent's prompt cache keys on the profile, so changing a topic's
// profile takes effect on the next turn instead of after the cache TTL.
func TestTopicProfile_AgentCacheKeysOnProfile(t *testing.T) {
	pb, _ := newProfilePromptBuilder(t)
	a := &ConduitAgentWithIntegration{promptBuilder: pb, promptCacheTTL: time.Hour, capabilities: pb.capabilities, skillsManager: pb.skillsManager}

	build := func(s *sessions.Session) []ai.SystemBlock {
		t.Helper()
		blocks, err := a.BuildSystemPrompt(context.Background(), s)
		if err != nil {
			t.Fatal(err)
		}
		return blocks
	}
	full := build(profileSession(nil))[0].Text
	lean := build(profileSession(map[string]string{sessions.ContextSkillFilter: "alpha-skill"}))[0].Text
	if lean == full || strings.Contains(skillsSection(lean), "beta-skill") {
		t.Fatalf("cached full prompt served for a profiled turn")
	}
	if again := build(profileSession(nil))[0].Text; again != full {
		t.Fatalf("profile leaked into the unprofiled cache entry")
	}
}
