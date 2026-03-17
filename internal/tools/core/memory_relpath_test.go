package core

import (
	"os"
	"path/filepath"
	"testing"

	"conduit/internal/config"
	"conduit/internal/tools/types"
)

func TestSearchInFile_EmptyRelPath(t *testing.T) {
	// When filePath == workspaceDir (no trailing separator), TrimPrefix
	// produces an empty string. Previously this caused an index-out-of-range
	// panic on relPath[0].
	tmpDir := t.TempDir()

	// Create a file at the workspace root whose full path equals workspaceDir
	// after TrimPrefix produces "".
	filePath := filepath.Join(tmpDir, "test.md")
	if err := os.WriteFile(filePath, []byte("hello world\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &MemorySearchTool{
		services:     &types.ToolServices{},
		sandboxCfg:   config.SandboxConfig{WorkspaceDir: filePath}, // workspaceDir == filePath → empty relPath
		workspaceDir: filePath,
	}

	// Should NOT panic
	results, err := tool.searchInFile(filePath, "hello", 0.0)
	if err != nil {
		t.Fatalf("searchInFile returned error: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}

	// relPath should be "" (not panic)
	if results[0].Path != "" {
		t.Errorf("expected empty relPath, got %q", results[0].Path)
	}
}
