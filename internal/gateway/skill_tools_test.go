package gateway

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"conduit/internal/config"
	"conduit/internal/skills"
	"conduit/internal/tools"
	"conduit/internal/tools/types"
)

// TestRegisterSkillTools_NilManagerReturnsZero documents the nil-guard behavior in
// registerSkillTools: when ToolServices.SkillsManager is nil, no skill tools are registered.
// Regression for conduit-3qph.
func TestRegisterSkillTools_NilManagerReturnsZero(t *testing.T) {
	registry := tools.NewRegistry(config.ToolsConfig{})
	registry.SetServices(&types.ToolServices{SkillsManager: nil})

	count := registry.RefreshSkillTools()
	if count != 0 {
		t.Errorf("expected 0 skill tools with nil SkillsManager, got %d", count)
	}
}

// TestRegisterSkillTools_WithSkillsRegistersTools is a regression test for conduit-3qph.
// For ~1 month the SkillsManager field was missing from the ToolServices wiring in gateway.go,
// causing registerSkillTools to silently return 0 tools even when skills were configured.
// This test asserts that a non-nil, enabled SkillsManager results in >0 registered skill tools.
func TestRegisterSkillTools_WithSkillsRegistersTools(t *testing.T) {
	// Build a minimal skill in a temp directory so discovery finds exactly one skill.
	tempDir := t.TempDir()
	skillDir := filepath.Join(tempDir, "test-skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatalf("failed to create skill dir: %v", err)
	}
	skillContent := "---\nname: test-skill\ndescription: Regression test fixture skill\n---\n\n# Test Skill\n\nThis fixture exists solely for the conduit-3qph regression test.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillContent), 0644); err != nil {
		t.Fatalf("failed to write SKILL.md: %v", err)
	}

	manager := skills.NewManager(skills.SkillsConfig{
		Enabled:     true,
		SearchPaths: []string{tempDir},
	})
	if err := manager.Initialize(context.Background()); err != nil {
		t.Fatalf("failed to initialize skills manager: %v", err)
	}

	registry := tools.NewRegistry(config.ToolsConfig{})
	registry.SetServices(&types.ToolServices{SkillsManager: manager})

	count := registry.RefreshSkillTools()
	if count == 0 {
		t.Error("expected >0 skill tools when SkillsManager is non-nil and has skills; got 0 — SkillsManager may not be wired into ToolServices (see conduit-3qph)")
	}
}
