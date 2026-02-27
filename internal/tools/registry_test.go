package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"conduit/internal/config"
	"conduit/internal/tools/types"
)

// setupTestRegistry creates a registry with test configuration
func setupTestRegistry(t *testing.T, workspaceContextDir, sandboxWorkspaceDir string) (*Registry, string) {
	// Create temporary directory for testing
	tempDir := t.TempDir()

	// Create workspace context directory if specified
	if workspaceContextDir != "" {
		workspaceContextDir = filepath.Join(tempDir, workspaceContextDir)
		err := os.MkdirAll(workspaceContextDir, 0755)
		if err != nil {
			t.Fatalf("Failed to create workspace context dir: %v", err)
		}
	}

	// Create sandbox workspace directory
	if sandboxWorkspaceDir == "" {
		sandboxWorkspaceDir = filepath.Join(tempDir, "sandbox")
	} else {
		sandboxWorkspaceDir = filepath.Join(tempDir, sandboxWorkspaceDir)
	}
	err := os.MkdirAll(sandboxWorkspaceDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create sandbox workspace dir: %v", err)
	}

	cfg := config.ToolsConfig{
		EnabledTools: []string{"Read", "Write"},
		Sandbox: config.SandboxConfig{
			WorkspaceDir: sandboxWorkspaceDir,
			AllowedPaths: []string{tempDir}, // Allow entire temp directory for testing
		},
	}

	registry := NewRegistry(cfg)

	// Set up services with config
	services := &types.ToolServices{}
	if workspaceContextDir != "" {
		services.ConfigMgr = &config.Config{
			Workspace: config.WorkspaceConfig{
				ContextDir: workspaceContextDir,
			},
		}
	}

	registry.SetServices(services)

	return registry, tempDir
}

func TestWriteFileTool_AbsolutePath(t *testing.T) {
	registry, tempDir := setupTestRegistry(t, "workspace", "sandbox")

	// Test absolute path - should use the path as-is
	absPath := filepath.Join(tempDir, "absolute_test.txt")
	content := "test content"

	tool := &WriteFileTool{registry: registry}

	args := map[string]interface{}{
		"path":    absPath,
		"content": content,
	}

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if !result.Success {
		t.Fatalf("Tool execution failed: %s", result.Error)
	}

	// Verify file was created at absolute path
	data, err := os.ReadFile(absPath)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}

	if string(data) != content {
		t.Errorf("File content mismatch: got %q, want %q", string(data), content)
	}
}

func TestWriteFileTool_RelativePathWithWorkspaceContext(t *testing.T) {
	registry, tempDir := setupTestRegistry(t, "workspace", "sandbox")

	// Test relative path - should resolve against workspace context directory
	relativePath := "relative_test.txt"
	content := "test content"
	expectedPath := filepath.Join(tempDir, "workspace", relativePath)

	tool := &WriteFileTool{registry: registry}

	args := map[string]interface{}{
		"path":    relativePath,
		"content": content,
	}

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if !result.Success {
		t.Fatalf("Tool execution failed: %s", result.Error)
	}

	// Verify file was created at resolved path
	data, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("Failed to read file at %s: %v", expectedPath, err)
	}

	if string(data) != content {
		t.Errorf("File content mismatch: got %q, want %q", string(data), content)
	}
}

func TestWriteFileTool_RelativePathWithoutWorkspaceContext(t *testing.T) {
	registry, tempDir := setupTestRegistry(t, "", "sandbox")

	// Test relative path with no workspace context - should fallback to sandbox workspace
	relativePath := "fallback_test.txt"
	content := "test content"
	expectedPath := filepath.Join(tempDir, "sandbox", relativePath)

	tool := &WriteFileTool{registry: registry}

	args := map[string]interface{}{
		"path":    relativePath,
		"content": content,
	}

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if !result.Success {
		t.Fatalf("Tool execution failed: %s", result.Error)
	}

	// Verify file was created at fallback path
	data, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("Failed to read file at %s: %v", expectedPath, err)
	}

	if string(data) != content {
		t.Errorf("File content mismatch: got %q, want %q", string(data), content)
	}
}

func TestWriteFileTool_NestedPath(t *testing.T) {
	registry, tempDir := setupTestRegistry(t, "workspace", "sandbox")

	// Test nested relative path - should create directories
	relativePath := "reference/pets.md"
	content := "test pets data"
	expectedPath := filepath.Join(tempDir, "workspace", relativePath)

	tool := &WriteFileTool{registry: registry}

	args := map[string]interface{}{
		"path":    relativePath,
		"content": content,
	}

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if !result.Success {
		t.Fatalf("Tool execution failed: %s", result.Error)
	}

	// Verify file was created with correct directory structure
	data, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("Failed to read file at %s: %v", expectedPath, err)
	}

	if string(data) != content {
		t.Errorf("File content mismatch: got %q, want %q", string(data), content)
	}

	// Verify directory was created
	dirInfo, err := os.Stat(filepath.Dir(expectedPath))
	if err != nil {
		t.Fatalf("Directory was not created: %v", err)
	}

	if !dirInfo.IsDir() {
		t.Error("Expected directory, got file")
	}
}

func TestWriteFileTool_InvalidArgs(t *testing.T) {
	registry, _ := setupTestRegistry(t, "workspace", "sandbox")
	tool := &WriteFileTool{registry: registry}

	tests := []struct {
		name string
		args map[string]interface{}
	}{
		{
			name: "missing path",
			args: map[string]interface{}{
				"content": "test",
			},
		},
		{
			name: "missing content",
			args: map[string]interface{}{
				"path": "test.txt",
			},
		},
		{
			name: "invalid path type",
			args: map[string]interface{}{
				"path":    123,
				"content": "test",
			},
		},
		{
			name: "invalid content type",
			args: map[string]interface{}{
				"path":    "test.txt",
				"content": 123,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tool.Execute(context.Background(), tt.args)
			if err != nil {
				t.Fatalf("Execute failed: %v", err)
			}

			if result.Success {
				t.Error("Expected tool execution to fail")
			}

			if result.Error == "" {
				t.Error("Expected error message")
			}
		})
	}
}

func TestReadFileTool_AbsolutePath(t *testing.T) {
	registry, tempDir := setupTestRegistry(t, "workspace", "sandbox")

	// Create test file
	absPath := filepath.Join(tempDir, "read_test.txt")
	content := "test read content"
	err := os.WriteFile(absPath, []byte(content), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	tool := &ReadFileTool{registry: registry}

	args := map[string]interface{}{
		"path": absPath,
	}

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if !result.Success {
		t.Fatalf("Tool execution failed: %s", result.Error)
	}

	if result.Content != content {
		t.Errorf("Content mismatch: got %q, want %q", result.Content, content)
	}
}

func TestReadFileTool_RelativePathWithWorkspaceContext(t *testing.T) {
	registry, tempDir := setupTestRegistry(t, "workspace", "sandbox")

	// Create test file in workspace context directory
	workspaceDir := filepath.Join(tempDir, "workspace")
	relativePath := "read_relative_test.txt"
	expectedPath := filepath.Join(workspaceDir, relativePath)
	content := "test relative read content"

	err := os.WriteFile(expectedPath, []byte(content), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	tool := &ReadFileTool{registry: registry}

	args := map[string]interface{}{
		"path": relativePath,
	}

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if !result.Success {
		t.Fatalf("Tool execution failed: %s", result.Error)
	}

	if result.Content != content {
		t.Errorf("Content mismatch: got %q, want %q", result.Content, content)
	}
}

func TestReadFileTool_RelativePathWithoutWorkspaceContext(t *testing.T) {
	registry, tempDir := setupTestRegistry(t, "", "sandbox")

	// Create test file in sandbox workspace directory
	sandboxDir := filepath.Join(tempDir, "sandbox")
	relativePath := "read_fallback_test.txt"
	expectedPath := filepath.Join(sandboxDir, relativePath)
	content := "test fallback read content"

	err := os.WriteFile(expectedPath, []byte(content), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	tool := &ReadFileTool{registry: registry}

	args := map[string]interface{}{
		"path": relativePath,
	}

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if !result.Success {
		t.Fatalf("Tool execution failed: %s", result.Error)
	}

	if result.Content != content {
		t.Errorf("Content mismatch: got %q, want %q", result.Content, content)
	}
}

func TestReadFileTool_FileNotFound(t *testing.T) {
	registry, _ := setupTestRegistry(t, "workspace", "sandbox")

	tool := &ReadFileTool{registry: registry}

	args := map[string]interface{}{
		"path": "nonexistent.txt",
	}

	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.Success {
		t.Error("Expected tool execution to fail for nonexistent file")
	}

	if result.Error == "" {
		t.Error("Expected error message")
	}
}

func TestReadFileTool_InvalidArgs(t *testing.T) {
	registry, _ := setupTestRegistry(t, "workspace", "sandbox")
	tool := &ReadFileTool{registry: registry}

	tests := []struct {
		name string
		args map[string]interface{}
	}{
		{
			name: "missing path",
			args: map[string]interface{}{},
		},
		{
			name: "invalid path type",
			args: map[string]interface{}{
				"path": 123,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tool.Execute(context.Background(), tt.args)
			if err != nil {
				t.Fatalf("Execute failed: %v", err)
			}

			if result.Success {
				t.Error("Expected tool execution to fail")
			}

			if result.Error == "" {
				t.Error("Expected error message")
			}
		})
	}
}

func TestPathResolution_Integration(t *testing.T) {
	registry, tempDir := setupTestRegistry(t, "workspace", "sandbox")

	// Write with relative path
	writeContent := "integration test content"
	relativePath := "reference/integration.md"

	writeTool := &WriteFileTool{registry: registry}
	writeArgs := map[string]interface{}{
		"path":    relativePath,
		"content": writeContent,
	}

	writeResult, err := writeTool.Execute(context.Background(), writeArgs)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	if !writeResult.Success {
		t.Fatalf("Write execution failed: %s", writeResult.Error)
	}

	// Read with same relative path
	readTool := &ReadFileTool{registry: registry}
	readArgs := map[string]interface{}{
		"path": relativePath,
	}

	readResult, err := readTool.Execute(context.Background(), readArgs)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if !readResult.Success {
		t.Fatalf("Read execution failed: %s", readResult.Error)
	}

	if readResult.Content != writeContent {
		t.Errorf("Content mismatch: got %q, want %q", readResult.Content, writeContent)
	}

	// Verify file exists at expected location
	expectedPath := filepath.Join(tempDir, "workspace", relativePath)
	if _, err := os.Stat(expectedPath); err != nil {
		t.Errorf("File not found at expected path %s: %v", expectedPath, err)
	}
}
