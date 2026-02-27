package tools

import (
	"testing"

	"conduit/internal/config"
)

func TestAllToolsRegistration(t *testing.T) {
	// Test configuration
	cfg := config.ToolsConfig{
		EnabledTools: []string{
			"Read", "Write", "Bash", "Glob",
		},
		Sandbox: config.SandboxConfig{
			WorkspaceDir: "./workspace",
			AllowedPaths: []string{"./workspace", "/tmp"},
		},
	}

	// Create registry (advanced tools require full service dependencies)
	registry := NewRegistry(cfg)

	// Set services to trigger tool registration
	registry.SetServices(&ToolServices{})

	// Verify basic tools are registered
	availableTools := registry.GetAvailableTools()

	expectedTools := []string{
		"Read", "Write", "Bash", "Glob",
	}

	for _, expectedTool := range expectedTools {
		if _, exists := availableTools[expectedTool]; !exists {
			t.Errorf("Expected tool '%s' not found in registry", expectedTool)
		}
	}

	t.Logf("Successfully registered %d tools", len(availableTools))
}

func TestCoreToolsCreation(t *testing.T) {
	t.Skip("Skipping: Core tools require full service implementations that are tested at integration level")
}

func TestWebToolsCreation(t *testing.T) {
	t.Skip("Skipping: Web tools require full service implementations that are tested at integration level")
}

func TestCommunicationToolsCreation(t *testing.T) {
	t.Skip("Skipping: Communication tools require full service implementations that are tested at integration level")
}

func TestSchedulingToolsCreation(t *testing.T) {
	t.Skip("Skipping: Scheduling tools require full service implementations that are tested at integration level")
}

func TestVisionToolsCreation(t *testing.T) {
	t.Skip("Skipping: Vision tools require full service implementations that are tested at integration level")
}

func TestToolSchemas(t *testing.T) {
	cfg := config.ToolsConfig{
		EnabledTools: []string{
			"Read", "Write", "Bash", "Glob",
		},
		Sandbox: config.SandboxConfig{
			WorkspaceDir: "./workspace",
			AllowedPaths: []string{"./workspace"},
		},
	}

	registry := NewRegistry(cfg)
	registry.SetServices(&ToolServices{})

	schemas := registry.GetToolSchemas()

	if len(schemas) == 0 {
		t.Error("No tool schemas generated")
	}

	for _, schema := range schemas {
		name, ok := schema["name"].(string)
		if !ok {
			t.Error("Tool schema missing name")
			continue
		}

		description, ok := schema["description"].(string)
		if !ok {
			t.Errorf("Tool %s missing description", name)
		}

		parameters, ok := schema["parameters"].(map[string]interface{})
		if !ok {
			t.Errorf("Tool %s missing parameters", name)
		}

		t.Logf("Tool %s: %s (params: %d)", name, description, len(parameters))
	}
}

// Benchmarks for performance testing
func BenchmarkRegistryCreation(b *testing.B) {
	cfg := config.ToolsConfig{
		EnabledTools: []string{
			"Read", "Write", "Bash", "Glob",
		},
		Sandbox: config.SandboxConfig{
			WorkspaceDir: "./workspace",
			AllowedPaths: []string{"./workspace"},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := NewRegistry(cfg)
		r.SetServices(&ToolServices{})
	}
}
