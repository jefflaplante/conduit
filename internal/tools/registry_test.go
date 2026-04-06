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

func TestIsPathAllowed(t *testing.T) {
	tests := []struct {
		name         string
		allowedPaths []string
		path         string
		expected     bool
	}{
		{
			name:         "path inside allowed dir",
			allowedPaths: []string{"/tmp/work"},
			path:         "/tmp/work/file.txt",
			expected:     true,
		},
		{
			name:         "prefix overlap returns false",
			allowedPaths: []string{"/tmp/work"},
			path:         "/tmp/workspace/file.txt",
			expected:     false,
		},
		{
			name:         "path traversal returns false",
			allowedPaths: []string{"/tmp/work"},
			path:         "/tmp/work/../../etc/passwd",
			expected:     false,
		},
		{
			name:         "exact match returns true",
			allowedPaths: []string{"/tmp/work"},
			path:         "/tmp/work",
			expected:     true,
		},
		{
			name:         "subdirectory returns true",
			allowedPaths: []string{"/tmp/work"},
			path:         "/tmp/work/sub/deep/file",
			expected:     true,
		},
		{
			name:         "completely outside returns false",
			allowedPaths: []string{"/tmp/work"},
			path:         "/etc/passwd",
			expected:     false,
		},
		{
			name:         "multiple allowed paths",
			allowedPaths: []string{"/tmp/work", "/var/data"},
			path:         "/var/data/file.txt",
			expected:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := &Registry{
				sandboxCfg: config.SandboxConfig{
					AllowedPaths: tt.allowedPaths,
				},
			}
			result := registry.isPathAllowed(tt.path)
			if result != tt.expected {
				t.Errorf("isPathAllowed(%q) with allowed=%v: got %v, want %v",
					tt.path, tt.allowedPaths, result, tt.expected)
			}
		})
	}
}

func TestRegistry_CaseInsensitiveToolEnabling(t *testing.T) {
	// Config uses snake_case names (as users write them)
	cfg := config.ToolsConfig{
		EnabledTools: []string{
			"sessions_spawn", // snake_case in config
			"Sessions_List",  // mixed case
			"SESSIONSSEND",   // all caps (weird but should work) - note double 's'
			"read_file",      // snake_case
		},
	}

	registry := NewRegistry(cfg)

	tests := []struct {
		name     string
		toolName string
		want     bool
	}{
		// snake_case config -> PascalCase tool name
		{"PascalCase matches snake_case config", "SessionsSpawn", true},
		{"lowercase matches snake_case config", "sessionsspawn", true},
		{"exact snake_case matches", "sessions_spawn", true},

		// Mixed case config -> various lookups
		{"PascalCase matches mixed config", "SessionsList", true},
		{"lowercase matches mixed config", "sessionslist", true},

		// All caps config -> various lookups
		{"lowercase matches ALLCAPS config", "sessionssend", true},
		{"PascalCase matches ALLCAPS config", "SessionsSend", true},

		// snake_case config for ReadFile
		{"PascalCase matches snake_case", "ReadFile", true},
		{"snake_case matches snake_case", "read_file", true},

		// Not enabled tools
		{"not enabled tool returns false", "WriteFile", false},
		{"not enabled tool lowercase", "writefile", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := registry.isToolEnabled(tt.toolName)
			if got != tt.want {
				t.Errorf("isToolEnabled(%q) = %v, want %v", tt.toolName, got, tt.want)
			}
		})
	}
}

func TestRegistry_EnabledToolsNormalized(t *testing.T) {
	cfg := config.ToolsConfig{
		EnabledTools: []string{
			"SessionsSpawn", // PascalCase in config
			"sessions_list", // snake_case in config
		},
	}

	registry := NewRegistry(cfg)

	// Both should be stored normalized (lowercase, no underscores)
	if !registry.enabledTools["sessionsspawn"] {
		t.Error("PascalCase tool not normalized in storage")
	}
	if !registry.enabledTools["sessionslist"] {
		t.Error("snake_case tool not normalized in storage (underscores should be removed)")
	}

	// Original case/format should not be stored
	if registry.enabledTools["SessionsSpawn"] {
		t.Error("PascalCase key should not exist (should be normalized)")
	}
	if registry.enabledTools["sessions_list"] {
		t.Error("snake_case key should not exist (should be normalized)")
	}
}

// mockSelfTesterTool is a test tool that implements SelfTester
type mockSelfTesterTool struct {
	name   string
	status types.SelfTestStatus
}

func (m *mockSelfTesterTool) Name() string        { return m.name }
func (m *mockSelfTesterTool) Description() string { return "Mock tool for testing" }
func (m *mockSelfTesterTool) Parameters() map[string]interface{} {
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
}
func (m *mockSelfTesterTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	return &types.ToolResult{Success: true}, nil
}
func (m *mockSelfTesterTool) SelfTest(ctx context.Context, opts *types.SelfTestOptions) *types.SelfTestResult {
	return &types.SelfTestResult{
		Status:       m.status,
		Message:      "Mock self-test result",
		Capabilities: []string{"mock_capability"},
	}
}

func TestSelfTestTool_Exists(t *testing.T) {
	registry, _ := setupTestRegistry(t, "workspace", "sandbox")

	// Test SelfTestTool on an enabled tool that doesn't implement SelfTester
	result := registry.SelfTestTool(context.Background(), "Read", nil)
	if result == nil {
		t.Fatal("Expected result for existing tool")
	}
	if result.Status != types.SelfTestStatusOK {
		t.Errorf("Expected OK status for non-SelfTester tool, got %s", result.Status)
	}
	if result.Message == "" {
		t.Error("Expected message for non-SelfTester tool")
	}
}

func TestSelfTestTool_NotExists(t *testing.T) {
	registry, _ := setupTestRegistry(t, "workspace", "sandbox")

	result := registry.SelfTestTool(context.Background(), "NonExistentTool", nil)
	if result != nil {
		t.Error("Expected nil result for non-existent tool")
	}
}

func TestSelfTestAll(t *testing.T) {
	registry, _ := setupTestRegistry(t, "workspace", "sandbox")

	result := registry.SelfTestAll(context.Background(), nil)
	if result == nil {
		t.Fatal("Expected result from SelfTestAll")
	}
	if result.TotalTools == 0 {
		t.Error("Expected at least some tools in registry")
	}
	if result.TestedAt.IsZero() {
		t.Error("Expected TestedAt to be set")
	}
}

func TestSelfTestResult_Helpers(t *testing.T) {
	tests := []struct {
		status       types.SelfTestStatus
		wantOK       bool
		wantFunctional bool
	}{
		{types.SelfTestStatusOK, true, true},
		{types.SelfTestStatusDegraded, false, true},
		{types.SelfTestStatusFailed, false, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			result := &types.SelfTestResult{Status: tt.status}
			if result.IsOK() != tt.wantOK {
				t.Errorf("IsOK() = %v, want %v", result.IsOK(), tt.wantOK)
			}
			if result.IsFunctional() != tt.wantFunctional {
				t.Errorf("IsFunctional() = %v, want %v", result.IsFunctional(), tt.wantFunctional)
			}
		})
	}
}

func TestRegistrySelfTestResult_Summary(t *testing.T) {
	result := &RegistrySelfTestResult{
		TotalTools:    10,
		TestedTools:   8,
		HealthyTools:  5,
		DegradedTools: 2,
		FailedTools:   1,
	}

	summary := result.Summary()
	if summary == "" {
		t.Error("Expected non-empty summary")
	}
	// Check that summary contains key numbers
	if !strContains(summary, "8") || !strContains(summary, "5") || !strContains(summary, "2") || !strContains(summary, "1") {
		t.Errorf("Summary missing expected counts: %s", summary)
	}
}

func TestRegistrySelfTestResult_IsHealthy(t *testing.T) {
	tests := []struct {
		name          string
		failed        int
		degraded      int
		wantHealthy   bool
	}{
		{"all healthy", 0, 0, true},
		{"has failed", 1, 0, false},
		{"has degraded", 0, 1, false},
		{"both issues", 1, 1, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := &RegistrySelfTestResult{
				FailedTools:   tt.failed,
				DegradedTools: tt.degraded,
			}
			if result.IsHealthy() != tt.wantHealthy {
				t.Errorf("IsHealthy() = %v, want %v", result.IsHealthy(), tt.wantHealthy)
			}
		})
	}
}

func strContains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && strContainsHelper(s, substr))
}

func strContainsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
