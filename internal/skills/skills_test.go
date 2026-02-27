package skills

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSkillLoader_LoadSkillFromContent(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantName    string
		wantDesc    string
		wantEmoji   string
		expectError bool
	}{
		{
			name:        "Valid skill with frontmatter",
			content:     "---\nname: test-skill\ndescription: A test skill for unit testing\nemoji: 🧪\nrequires:\n  anyBins: [\"curl\", \"jq\"]\n  env: [\"TEST_VAR\"]\n---\n\n# Test Skill\n\nThis is a test skill for validation.\n",
			wantName:    "test-skill",
			wantDesc:    "A test skill for unit testing",
			wantEmoji:   "🧪",
			expectError: false,
		},
		{
			name: "Skill without frontmatter",
			content: `# Simple Skill

This skill has no frontmatter.`,
			expectError: true,
		},
		{
			name: "Invalid YAML frontmatter",
			content: `---
name: test-skill
description: Invalid YAML
invalid: [unclosed
---

Content here.`,
			expectError: true,
		},
	}

	loader := NewSkillLoader()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			skill, err := loader.LoadSkillFromContent(tt.content)

			if tt.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if skill.Name != tt.wantName {
				t.Errorf("expected name %q, got %q", tt.wantName, skill.Name)
			}

			if skill.Description != tt.wantDesc {
				t.Errorf("expected description %q, got %q", tt.wantDesc, skill.Description)
			}

			if skill.Metadata.Conduit.Emoji != tt.wantEmoji {
				t.Errorf("expected emoji %q, got %q", tt.wantEmoji, skill.Metadata.Conduit.Emoji)
			}
		})
	}
}

func TestSkillValidator_ValidateRequirements(t *testing.T) {
	validator := NewSkillValidator()

	// Create a test skill with requirements
	skill := Skill{
		Name: "test-skill",
		Metadata: SkillMetadata{
			Conduit: SkillConduitMeta{
				Requires: SkillRequirements{
					AllBins: []string{"ls"}, // Should exist on Unix systems
					AnyBins: []string{"nonexistent-binary-12345"},
					Env:     []string{"NONEXISTENT_ENV_VAR"},
				},
			},
		},
	}

	err := validator.ValidateRequirements(skill)
	if err == nil {
		t.Error("expected validation to fail due to missing requirements")
	}

	// Test with no requirements
	emptySkill := Skill{
		Name: "empty-skill",
		Metadata: SkillMetadata{
			Conduit: SkillConduitMeta{
				Requires: SkillRequirements{},
			},
		},
	}

	err = validator.ValidateRequirements(emptySkill)
	if err != nil {
		t.Errorf("expected empty requirements to pass, got: %v", err)
	}
}

func TestSkillDiscovery_DiscoverSkills(t *testing.T) {
	// Create a temporary test directory
	tempDir := t.TempDir()

	// Create a test skill
	skillDir := filepath.Join(tempDir, "test-skill")
	err := os.MkdirAll(skillDir, 0755)
	if err != nil {
		t.Fatalf("failed to create test skill directory: %v", err)
	}

	skillContent := `---
name: test-skill
description: A test skill
---

# Test Skill

This is for testing.`

	skillFile := filepath.Join(skillDir, "SKILL.md")
	err = os.WriteFile(skillFile, []byte(skillContent), 0644)
	if err != nil {
		t.Fatalf("failed to write test skill file: %v", err)
	}

	// Test discovery
	discovery := NewSkillDiscovery([]string{tempDir})
	skills, err := discovery.DiscoverSkills(context.Background())
	if err != nil {
		t.Fatalf("skill discovery failed: %v", err)
	}

	if len(skills) != 1 {
		t.Errorf("expected 1 skill, found %d", len(skills))
		return
	}

	skill := skills[0]
	if skill.Name != "test-skill" {
		t.Errorf("expected skill name 'test-skill', got %q", skill.Name)
	}

	if skill.Location != skillDir {
		t.Errorf("expected skill location %q, got %q", skillDir, skill.Location)
	}
}

func TestSkillManager_Integration(t *testing.T) {
	// Create test configuration
	config := SkillsConfig{
		Enabled: true,
		SearchPaths: []string{
			"/nonexistent/path", // This should be handled gracefully
		},
		Execution: ExecutionConfig{
			TimeoutSeconds: 5,
			Environment:    map[string]string{"TEST": "true"},
		},
		Cache: CacheConfig{
			Enabled:    true,
			TTLSeconds: 10,
		},
	}

	manager := NewManager(config)

	// Test initialization
	err := manager.Initialize(context.Background())
	if err != nil {
		t.Fatalf("manager initialization failed: %v", err)
	}

	if !manager.IsInitialized() {
		t.Error("manager should be initialized")
	}

	// Test getting skills (should be empty due to nonexistent path)
	skills, err := manager.GetAvailableSkills(context.Background())
	if err != nil {
		t.Errorf("getting skills failed: %v", err)
	}

	if len(skills) != 0 {
		t.Logf("found %d skills (may be from actual system paths)", len(skills))
	}

	// Test skill status
	statuses, err := manager.GetSkillStatus(context.Background())
	if err != nil {
		t.Errorf("getting skill status failed: %v", err)
	}

	t.Logf("skill statuses: %d", len(statuses))
}

func TestSkillCache(t *testing.T) {
	config := SkillsConfig{
		Enabled:     true,
		SearchPaths: []string{"/nonexistent"},
		Cache: CacheConfig{
			Enabled:    true,
			TTLSeconds: 1, // Short TTL for testing
		},
	}

	manager := NewManager(config)

	// First call should populate cache
	skills1, err := manager.GetAvailableSkills(context.Background())
	if err != nil {
		t.Errorf("first call failed: %v", err)
	}

	// Second call should use cache
	skills2, err := manager.GetAvailableSkills(context.Background())
	if err != nil {
		t.Errorf("second call failed: %v", err)
	}

	// Should be the same
	if len(skills1) != len(skills2) {
		t.Errorf("cache inconsistency: %d vs %d skills", len(skills1), len(skills2))
	}

	// Wait for cache to expire
	time.Sleep(2 * time.Second)

	// Third call should refresh cache
	skills3, err := manager.GetAvailableSkills(context.Background())
	if err != nil {
		t.Errorf("third call failed: %v", err)
	}

	// Should still be consistent
	if len(skills1) != len(skills3) {
		t.Errorf("post-expiry inconsistency: %d vs %d skills", len(skills1), len(skills3))
	}
}

func TestSkillActionExtraction(t *testing.T) {
	content := "# Email Skill\n\nThis skill manages email.\n\n## Search\nSearch for emails using queries.\n\n## Read\nRead specific email threads.\n\n## Commands\n\n### Current Status\nCheck status.\n\n### Send Email\nSend an email.\n"

	loader := NewSkillLoader()
	actions := loader.ExtractActionsFromContent(content)

	expectedActions := []string{"search", "read"} // Only test core actions that will be extracted

	for _, expected := range expectedActions {
		found := false
		for _, action := range actions {
			if action == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected action %q not found in extracted actions: %v", expected, actions)
		}
	}
}

func TestSkillIntegrator_GenerateTools(t *testing.T) {
	skill := Skill{
		Name:        "test-skill",
		Description: "A test skill",
		Content:     "## Search\nSearch for things.\n## Status\nGet status.",
		Metadata: SkillMetadata{
			Conduit: SkillConduitMeta{
				Emoji: "🧪",
			},
		},
	}

	config := ExecutionConfig{TimeoutSeconds: 30}
	executor := NewExecutor(config)
	integrator := NewSkillIntegrator(executor)

	tools := integrator.GenerateToolsFromSkills([]Skill{skill})

	if len(tools) == 0 {
		t.Error("expected at least one tool to be generated")
		return
	}

	// Check primary skill tool
	primaryTool := tools[0]
	if primaryTool.Name() != "skill_test-skill" {
		t.Errorf("expected tool name 'skill_test-skill', got %q", primaryTool.Name())
	}

	description := primaryTool.Description()
	if description == "" {
		t.Error("tool description should not be empty")
	}

	params := primaryTool.Parameters()
	if params == nil {
		t.Error("tool parameters should not be nil")
	}
}
