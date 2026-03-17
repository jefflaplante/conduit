package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"conduit/internal/config"
	"conduit/internal/tools/types"
)

func TestEditTool_RejectsPathOutsideSandbox(t *testing.T) {
	tmpDir := t.TempDir()
	sandboxDir := filepath.Join(tmpDir, "workspace")
	if err := os.MkdirAll(sandboxDir, 0755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Tools: config.ToolsConfig{
			Sandbox: config.SandboxConfig{
				WorkspaceDir: sandboxDir,
				AllowedPaths: []string{sandboxDir},
			},
		},
	}
	services := &types.ToolServices{
		ConfigMgr: cfg,
	}

	tool := NewEditTool(services)

	// Attempt to edit /etc/passwd (outside sandbox)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"path":       "/etc/passwd",
		"old_string": "root",
		"new_string": "hacked",
	})

	if err != nil {
		t.Fatalf("Execute should not return error, got: %v", err)
	}

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	if result.Success {
		t.Error("Should reject path outside sandbox, but got success")
	}

	if result.ErrorDetails == nil || result.ErrorDetails.Type != "path_not_allowed" {
		errType := ""
		if result.ErrorDetails != nil {
			errType = result.ErrorDetails.Type
		}
		t.Errorf("Expected error type 'path_not_allowed', got '%s'", errType)
	}
}

func TestEditTool_AllowsPathInsideSandbox(t *testing.T) {
	tmpDir := t.TempDir()
	sandboxDir := filepath.Join(tmpDir, "workspace")
	if err := os.MkdirAll(sandboxDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a file inside sandbox
	testFile := filepath.Join(sandboxDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("hello world"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Tools: config.ToolsConfig{
			Sandbox: config.SandboxConfig{
				WorkspaceDir: sandboxDir,
				AllowedPaths: []string{sandboxDir},
			},
		},
	}
	services := &types.ToolServices{
		ConfigMgr: cfg,
	}

	tool := NewEditTool(services)

	// Edit file inside sandbox should succeed
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"path":       testFile,
		"old_string": "hello",
		"new_string": "goodbye",
	})

	if err != nil {
		t.Fatalf("Execute should not return error, got: %v", err)
	}

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	if !result.Success {
		errType := ""
		if result.ErrorDetails != nil {
			errType = result.ErrorDetails.Type
		}
		t.Errorf("Should allow path inside sandbox, but got error: %s - %s", errType, result.Content)
	}

	// Verify file was actually edited
	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "goodbye world" {
		t.Errorf("Expected 'goodbye world', got '%s'", string(data))
	}
}
