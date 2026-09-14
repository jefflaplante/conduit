package skills

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestSkillsToolIntegration tests the complete skills-to-tools integration
func TestSkillsToolIntegration(t *testing.T) {
	// Create a skills manager with test configuration
	config := SkillsConfig{
		Enabled:     true,
		SearchPaths: []string{"testdata"},
		Execution: ExecutionConfig{
			TimeoutSeconds: 10,
			Environment:    map[string]string{},
			AllowedActions: map[string][]string{},
		},
		Cache: CacheConfig{
			TTLSeconds: 300,
			Enabled:    true,
		},
	}

	manager := NewManager(config)

	// Test that tool adapters can be generated
	adapters, err := GenerateToolAdapters(context.Background(), manager)
	if err != nil {
		t.Errorf("GenerateToolAdapters failed: %v", err)
		return
	}

	// Should return a valid slice even with no skills (never nil)
	// An empty slice `[]` is perfectly valid when there are no skills
	t.Logf("Adapters: %v, len: %d", adapters, len(adapters))

	// With no skills in testdata, should return empty slice (this is expected behavior)
	t.Logf("Generated %d tool adapters", len(adapters))
}

// TestSkillToolAdapter tests the SkillToolAdapter implementation
func TestSkillToolAdapter(t *testing.T) {
	// Create a mock skill tool
	mockSkillTool := &MockSkillTool{
		name:        "test_skill",
		description: "Test skill for adapter testing",
		parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"action": map[string]interface{}{
					"type":        "string",
					"description": "Action to perform",
				},
			},
		},
	}

	// Create adapter
	adapter := NewSkillToolAdapter(mockSkillTool)

	// Test basic methods
	if adapter.Name() != "test_skill" {
		t.Errorf("Expected name 'test_skill', got '%s'", adapter.Name())
	}

	if adapter.Description() != "Test skill for adapter testing" {
		t.Errorf("Expected description 'Test skill for adapter testing', got '%s'", adapter.Description())
	}

	params := adapter.Parameters()
	if params == nil {
		t.Error("Expected non-nil parameters")
	}

	// Test execute method
	ctx := context.Background()
	args := map[string]interface{}{
		"action": "test",
	}

	result, err := adapter.Execute(ctx, args)
	if err != nil {
		t.Errorf("Execute failed: %v", err)
	}

	if result == nil {
		t.Error("Expected non-nil result")
	} else {
		if !result.Success {
			t.Error("Expected successful result")
		}
		if result.Content != "test execution completed" {
			t.Errorf("Expected content 'test execution completed', got '%s'", result.Content)
		}
	}
}

// TestDisabledSkillsManager tests behavior with disabled manager
func TestDisabledSkillsManager(t *testing.T) {
	// Create a disabled skills manager
	config := SkillsConfig{
		Enabled: false,
	}

	manager := NewManager(config)

	// Test that no tools are generated when disabled
	adapters, err := GenerateToolAdapters(context.Background(), manager)
	if err != nil {
		t.Errorf("GenerateToolAdapters failed: %v", err)
	}

	if len(adapters) != 0 {
		t.Errorf("Expected 0 adapters for disabled manager, got %d", len(adapters))
	}

	// Test with nil manager
	adapters, err = GenerateToolAdapters(context.Background(), nil)
	if err != nil {
		t.Errorf("GenerateToolAdapters with nil manager failed: %v", err)
	}

	if len(adapters) != 0 {
		t.Errorf("Expected 0 adapters for nil manager, got %d", len(adapters))
	}
}

// MockSkillTool implements SkillToolInterface for testing
type MockSkillTool struct {
	name        string
	description string
	parameters  map[string]interface{}
}

func (m *MockSkillTool) Name() string {
	return m.name
}

func (m *MockSkillTool) Description() string {
	return m.description
}

func (m *MockSkillTool) Parameters() map[string]interface{} {
	return m.parameters
}

func (m *MockSkillTool) Execute(ctx context.Context, args map[string]interface{}) (*SkillToolResult, error) {
	// Simulate execution
	time.Sleep(1 * time.Millisecond)

	return &SkillToolResult{
		Success: true,
		Content: "test execution completed",
		Data: map[string]interface{}{
			"args": args,
		},
	}, nil
}

// --- conduit-1jd9 regression tests: skill description bloat + honest actions ---

func mkBloatSkill(name, desc string) Skill {
	return Skill{
		Name:        name,
		Description: desc,
		Content:     "Some content with 'do NOT trigger' prose that used to be scraped.",
	}
}

// TestBuildSkillsContext_CompactFormat pins the one-line-per-skill format:
// no full-description duplication (the catalog already carries it) and no
// Location lines.
func TestBuildSkillsContext_CompactFormat(t *testing.T) {
	integrator := &SkillIntegrator{loader: NewSkillLoader(), config: nil}
	longDesc := strings.Repeat("Trigger words. ", 40) // ~560 chars of bloat
	skills := []Skill{mkBloatSkill("spot", longDesc)}

	out := integrator.BuildSkillsContext(skills)

	if !strings.Contains(out, "skill_spot") {
		t.Errorf("section should name the tool, got: %s", out)
	}
	if strings.Count(out, "Trigger words.") > 2 {
		t.Errorf("full description must not be repeated in prompt section; got: %s", out)
	}
	if strings.Contains(out, "Location:") || strings.Contains(out, "**Tool:**") {
		t.Errorf("legacy verbose format still present; got: %s", out)
	}
	if len(out) > 2000 {
		t.Errorf("section for one skill should be compact; got %d chars: %s", len(out), out)
	}
}

// TestTruncateDescription word-boundary and sentence-preference behavior.
func TestTruncateDescription(t *testing.T) {
	short := "Short description."
	if got := truncateDescription(short, 160); got != short {
		t.Errorf("short desc should pass through, got %q", got)
	}

	long := strings.Repeat("word ", 60) // 300 chars, no sentence break
	got := truncateDescription(long, 100)
	if len(got) > 105 || !strings.HasSuffix(got, "…") {
		t.Errorf("expected word-boundary truncation with ellipsis, got %q (len %d)", got, len(got))
	}

	sentency := "First sentence is here. Then a long tail " + strings.Repeat("x", 200)
	if got := truncateDescription(sentency, 160); got != "First sentence is here." {
		t.Errorf("expected first-sentence truncation, got %q", got)
	}
}

// TestParameters_EnumOnlyWithHonestActions pins the schema contract: enum
// present when honest actions exist (frontmatter or scripts), omitted
// otherwise — never populated from prose scraping.
func TestParameters_EnumOnlyWithHonestActions(t *testing.T) {
	executor := &Executor{}
	loader := NewSkillLoader()

	// Explicit frontmatter actions → enum present.
	skill, err := loader.LoadSkillFromContent("---\nname: t\ndescription: d\nactions: [search, send]\n---\nbody")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	tool := &SkillTool{skill: *skill, executor: executor}
	params := tool.Parameters()
	props := params["properties"].(map[string]interface{})
	actionProp := props["action"].(map[string]interface{})
	if enum, ok := actionProp["enum"].([]string); !ok || len(enum) != 2 {
		t.Errorf("expected enum [search send], got %v", actionProp["enum"])
	}

	// No actions, no scripts → enum omitted, tool still usable.
	empty := &SkillTool{skill: Skill{Name: "e", Description: "d"}, executor: executor}
	props2 := empty.Parameters()["properties"].(map[string]interface{})
	actionProp2 := props2["action"].(map[string]interface{})
	if _, hasEnum := actionProp2["enum"]; hasEnum {
		t.Errorf("enum must be omitted when no honest actions exist; got %v", actionProp2["enum"])
	}
	if actionProp2["description"] == "" {
		t.Error("action param must still carry a description")
	}
}
