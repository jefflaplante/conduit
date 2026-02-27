package tools

import (
	"context"
	"conduit/internal/config"
	"conduit/internal/tools/types"
	"testing"
)

func TestEnhancedErrorHandling(t *testing.T) {
	cfg := config.ToolsConfig{
		EnabledTools: []string{"Message", "Read", "Write", "Bash"},
		Sandbox: config.SandboxConfig{
			WorkspaceDir: "./testdata",
			AllowedPaths: []string{"./testdata"},
		},
	}

	registry := NewRegistry(cfg)
	registry.SetServices(&types.ToolServices{})

	ctx := context.Background()

	// Test 1: Message tool with missing target parameter
	t.Run("MessageToolMissingTarget", func(t *testing.T) {
		args := map[string]interface{}{
			"action":  "send",
			"message": "test message",
			// Missing "target" parameter
		}

		result, err := registry.ExecuteTool(ctx, "Message", args)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if result.Success {
			t.Error("Expected tool to fail due to missing target")
		}

		if result.ErrorDetails == nil {
			t.Error("Expected rich error details")
		} else {
			// The registry creates validation errors with "invalid_parameter" type
			// when validation fails, regardless of the specific validation error type
			if result.ErrorDetails.Type != "invalid_parameter" {
				t.Errorf("Expected error type 'invalid_parameter', got '%s'", result.ErrorDetails.Type)
			}
			if result.ErrorDetails.Parameter != "target" {
				t.Errorf("Expected parameter 'target', got '%s'", result.ErrorDetails.Parameter)
			}
			if len(result.ErrorDetails.Examples) == 0 {
				t.Error("Expected examples in error details")
			}
			if len(result.ErrorDetails.Suggestions) == 0 {
				t.Error("Expected suggestions in error details")
			}
		}

		t.Logf("Error message: %s", result.Error)
		t.Logf("Suggestions: %v", result.ErrorDetails.Suggestions)
	})

	// Test 2: Read tool with invalid path
	t.Run("ReadToolInvalidPath", func(t *testing.T) {
		args := map[string]interface{}{
			"path": "/invalid/path/that/does/not/exist.txt",
		}

		result, err := registry.ExecuteTool(ctx, "Read", args)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if result.Success {
			t.Error("Expected tool to fail due to invalid path")
		}

		if result.ErrorDetails == nil {
			t.Error("Expected rich error details")
		} else {
			if result.ErrorDetails.Type != "path_not_allowed" {
				t.Errorf("Expected error type 'path_not_allowed', got '%s'", result.ErrorDetails.Type)
			}
			if len(result.ErrorDetails.AvailableValues) == 0 {
				t.Error("Expected available values (allowed paths) in error details")
			}
		}

		t.Logf("Error message: %s", result.Error)
		t.Logf("Available values: %v", result.ErrorDetails.AvailableValues)
	})

	// Test 3: Bash tool with empty command
	t.Run("BashToolEmptyCommand", func(t *testing.T) {
		args := map[string]interface{}{
			"command": "   ", // Empty/whitespace command
		}

		result, err := registry.ExecuteTool(ctx, "Bash", args)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if result.Success {
			t.Error("Expected tool to fail due to empty command")
		}

		if result.ErrorDetails == nil {
			t.Error("Expected rich error details")
		} else {
			if result.ErrorDetails.Type != "invalid_parameter" {
				t.Errorf("Expected error type 'invalid_parameter', got '%s'", result.ErrorDetails.Type)
			}
			if result.ErrorDetails.Parameter != "command" {
				t.Errorf("Expected parameter 'command', got '%s'", result.ErrorDetails.Parameter)
			}
		}

		t.Logf("Error message: %s", result.Error)
	})
}

func TestParameterValidation(t *testing.T) {
	cfg := config.ToolsConfig{
		EnabledTools: []string{"Message"},
		Sandbox: config.SandboxConfig{
			WorkspaceDir: "./testdata",
			AllowedPaths: []string{"./testdata"},
		},
	}

	registry := NewRegistry(cfg)
	registry.SetServices(&types.ToolServices{})

	ctx := context.Background()

	// Test parameter validation for Message tool
	t.Run("MessageToolValidation", func(t *testing.T) {
		// Get Message tool
		tool, exists := registry.tools["Message"]
		if !exists {
			t.Fatal("Message tool not found")
		}

		// Check if it supports parameter validation
		validator, ok := tool.(types.ParameterValidator)
		if !ok {
			t.Fatal("Message tool does not support parameter validation")
		}

		// Test with missing required parameters
		args := map[string]interface{}{
			"action": "send",
			// Missing target and message
		}

		result := validator.ValidateParameters(ctx, args)
		if result.Valid {
			t.Error("Expected validation to fail")
		}

		if len(result.Errors) == 0 {
			t.Error("Expected validation errors")
		}

		for _, err := range result.Errors {
			t.Logf("Validation error: %s: %s", err.Parameter, err.Message)
		}

		if len(result.Suggestions) == 0 {
			t.Error("Expected validation suggestions")
		}

		for _, suggestion := range result.Suggestions {
			t.Logf("Suggestion: %s", suggestion)
		}
	})
}
