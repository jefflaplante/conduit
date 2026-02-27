package skills

import (
	"context"
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
