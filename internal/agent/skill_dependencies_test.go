package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"conduit/internal/ai"
	"conduit/internal/config"
	"conduit/internal/sessions"
	"conduit/internal/skills"
)

// TestPromptBuilder_IncludesSkillDependencies verifies the end-to-end flow:
// a skill with a declared Dependencies section causes its reference file
// contents to appear inside the assembled system prompt.
func TestPromptBuilder_IncludesSkillDependencies(t *testing.T) {
	// Build a real skill tree on disk with one dep file.
	root := t.TempDir()
	skillDir := filepath.Join(root, "hass")
	refDir := filepath.Join(skillDir, "reference")
	if err := os.MkdirAll(refDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	skillMd := "---\nname: hass\ndescription: Home Assistant skill\n---\n\n" +
		"# HA\n\n## Dependencies\n\n" +
		"- **References:** `reference/ha-entities.md`\n\n" +
		"## Usage\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillMd), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	const depBody = "HA_ENTITIES_FIXTURE_BODY"
	if err := os.WriteFile(filepath.Join(refDir, "ha-entities.md"), []byte(depBody), 0o644); err != nil {
		t.Fatalf("write dep: %v", err)
	}

	mgr := skills.NewManager(skills.SkillsConfig{
		Enabled:     true,
		SearchPaths: []string{root},
		Cache:       skills.CacheConfig{Enabled: true, TTLSeconds: 60},
	})
	if err := mgr.Initialize(context.Background()); err != nil {
		t.Fatalf("init manager: %v", err)
	}

	pb := NewPromptBuilder(
		"conduit", "helpful assistant",
		config.AgentEmail{},
		IdentityConfig{APIKeyIdentity: "You are Conduit."},
		AgentCapabilities{SkillsIntegration: true},
		[]ai.Tool{{Name: "Read", Description: "Read a file"}},
		nil, // workspace
		nil, // summary
		mgr,
		nil, nil, "", "", nil,
	)

	session := &sessions.Session{
		Key:       "test-session",
		ChannelID: "telegram-123",
		UserID:    "user1",
		Context:   map[string]string{"model": "claude-sonnet-4-20250514"},
	}

	prompt := pb.buildFullPrompt(context.Background(), session, false)

	if !strings.Contains(prompt, depBody) {
		t.Errorf("expected dep content %q in assembled prompt; prompt sample:\n%s",
			depBody, truncate(prompt, 2000))
	}
	if !strings.Contains(prompt, "Skill Dependencies (auto-loaded)") {
		t.Error("expected Skill Dependencies header in prompt")
	}
	if !strings.Contains(prompt, "reference/ha-entities.md") {
		t.Error("expected dep path label in prompt")
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}
