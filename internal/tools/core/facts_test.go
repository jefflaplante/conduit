package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"conduit/internal/config"
	"conduit/internal/tools/types"
)

func newTestFactsTool(t *testing.T) (*FactsTool, string) {
	t.Helper()
	tmpDir := t.TempDir()
	tool := &FactsTool{
		services:     &types.ToolServices{},
		sandboxCfg:   config.SandboxConfig{WorkspaceDir: tmpDir},
		workspaceDir: tmpDir,
	}
	return tool, tmpDir
}

func TestFactsTool_Name(t *testing.T) {
	tool, _ := newTestFactsTool(t)
	if got := tool.Name(); got != "Facts" {
		t.Errorf("Name() = %q, want %q", got, "Facts")
	}
}

func TestFactsTool_ExtractBulletFacts(t *testing.T) {
	tool, tmpDir := newTestFactsTool(t)

	memoryContent := `# My Memory

## Key Facts
- Go is the primary language
- SQLite is used for storage
- Port 18789 is the default

## Architecture
This section describes architecture but is not a knowledge header.
- This bullet should not be extracted

## Important Notes
- Always run tests before committing
- Use WAL mode for SQLite
`
	err := os.WriteFile(filepath.Join(tmpDir, "MEMORY.md"), []byte(memoryContent), 0644)
	if err != nil {
		t.Fatalf("failed to write MEMORY.md: %v", err)
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("Execute failed: %s", result.Error)
	}

	facts, ok := result.Data["facts"].([]Fact)
	if !ok {
		t.Fatalf("expected facts to be []Fact, got %T", result.Data["facts"])
	}

	// Should extract 5 bullet facts: 3 from "Key Facts" + 2 from "Important Notes"
	// "Architecture" is not a knowledge header, so its bullets are skipped
	if len(facts) != 5 {
		t.Errorf("expected 5 facts, got %d", len(facts))
		for i, f := range facts {
			t.Logf("  fact[%d]: category=%q content=%q", i, f.Category, f.Content)
		}
	}

	total, ok := result.Data["total"].(int)
	if !ok {
		t.Fatalf("expected total to be int, got %T", result.Data["total"])
	}
	if total != len(facts) {
		t.Errorf("total=%d does not match len(facts)=%d", total, len(facts))
	}

	// Verify categories
	categories, ok := result.Data["categories"].([]string)
	if !ok {
		t.Fatalf("expected categories to be []string, got %T", result.Data["categories"])
	}
	if len(categories) != 2 {
		t.Errorf("expected 2 categories, got %d: %v", len(categories), categories)
	}

	// Verify first fact content
	if len(facts) > 0 && facts[0].Content != "Go is the primary language" {
		t.Errorf("first fact content = %q, want %q", facts[0].Content, "Go is the primary language")
	}
	if len(facts) > 0 && facts[0].Category != "key facts" {
		t.Errorf("first fact category = %q, want %q", facts[0].Category, "key facts")
	}
}

func TestFactsTool_ExtractKeyValueFacts(t *testing.T) {
	tool, tmpDir := newTestFactsTool(t)

	memoryContent := `# Profile

## User Info
**Name**: Alice
**Role**: Developer
**Team**: Platform

## Preferences
- Dark mode enabled
- Vim keybindings
`
	err := os.WriteFile(filepath.Join(tmpDir, "MEMORY.md"), []byte(memoryContent), 0644)
	if err != nil {
		t.Fatalf("failed to write MEMORY.md: %v", err)
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("Execute failed: %s", result.Error)
	}

	facts := result.Data["facts"].([]Fact)

	// 3 key-value facts from "User Info" + 2 bullet facts from "Preferences"
	if len(facts) != 5 {
		t.Errorf("expected 5 facts, got %d", len(facts))
		for i, f := range facts {
			t.Logf("  fact[%d]: category=%q content=%q", i, f.Category, f.Content)
		}
	}

	// Verify key-value extraction
	foundKV := false
	for _, fact := range facts {
		if fact.Content == "Name: Alice" {
			foundKV = true
			if fact.Category != "user info" {
				t.Errorf("key-value fact category = %q, want %q", fact.Category, "user info")
			}
			break
		}
	}
	if !foundKV {
		t.Error("expected to find key-value fact 'Name: Alice'")
	}
}

func TestFactsTool_CategoryFilter(t *testing.T) {
	tool, tmpDir := newTestFactsTool(t)

	memoryContent := `# Memory

## Key Facts
- Go 1.24 is required
- SQLite is embedded

## Important Preferences
- Dark mode
- Large font size
`
	err := os.WriteFile(filepath.Join(tmpDir, "MEMORY.md"), []byte(memoryContent), 0644)
	if err != nil {
		t.Fatalf("failed to write MEMORY.md: %v", err)
	}

	// Filter by "key facts"
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"category": "key facts",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("Execute failed: %s", result.Error)
	}

	facts := result.Data["facts"].([]Fact)

	if len(facts) != 2 {
		t.Errorf("expected 2 facts for category 'key facts', got %d", len(facts))
		for i, f := range facts {
			t.Logf("  fact[%d]: category=%q content=%q", i, f.Category, f.Content)
		}
	}

	for _, fact := range facts {
		if fact.Category != "key facts" {
			t.Errorf("fact category = %q, want %q", fact.Category, "key facts")
		}
	}

	// Filter by non-existent category
	result2, err := tool.Execute(context.Background(), map[string]interface{}{
		"category": "nonexistent",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result2.Success {
		t.Fatalf("Execute failed: %s", result2.Error)
	}

	facts2 := result2.Data["facts"].([]Fact)
	if len(facts2) != 0 {
		t.Errorf("expected 0 facts for nonexistent category, got %d", len(facts2))
	}
}

func TestFactsTool_MaxFacts(t *testing.T) {
	tool, tmpDir := newTestFactsTool(t)

	memoryContent := `# Memory

## Key Knowledge
- Fact one
- Fact two
- Fact three
- Fact four
- Fact five
- Fact six
- Fact seven
- Fact eight
- Fact nine
- Fact ten
`
	err := os.WriteFile(filepath.Join(tmpDir, "MEMORY.md"), []byte(memoryContent), 0644)
	if err != nil {
		t.Fatalf("failed to write MEMORY.md: %v", err)
	}

	// Request max 3 facts
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"maxFacts": float64(3), // JSON numbers come as float64
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("Execute failed: %s", result.Error)
	}

	facts := result.Data["facts"].([]Fact)
	total := result.Data["total"].(int)

	if len(facts) != 3 {
		t.Errorf("expected 3 facts (maxFacts limit), got %d", len(facts))
	}
	if total != 3 {
		t.Errorf("expected total=3, got %d", total)
	}
}

func TestFactsTool_EmptyWorkspace(t *testing.T) {
	tool, _ := newTestFactsTool(t)

	// No memory files created - workspace is empty
	result, err := tool.Execute(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("Execute failed: %s", result.Error)
	}

	if result.Content != "No memory files found in workspace." {
		t.Errorf("unexpected content: %q", result.Content)
	}

	facts := result.Data["facts"].([]Fact)
	if len(facts) != 0 {
		t.Errorf("expected 0 facts for empty workspace, got %d", len(facts))
	}

	total := result.Data["total"].(int)
	if total != 0 {
		t.Errorf("expected total=0, got %d", total)
	}

	categories := result.Data["categories"].([]string)
	if len(categories) != 0 {
		t.Errorf("expected 0 categories, got %d", len(categories))
	}
}

func TestFactsTool_MemorySubdirectory(t *testing.T) {
	tool, tmpDir := newTestFactsTool(t)

	// Create memory subdirectory with a file
	memDir := filepath.Join(tmpDir, "memory")
	err := os.MkdirAll(memDir, 0755)
	if err != nil {
		t.Fatalf("failed to create memory dir: %v", err)
	}

	subContent := `# Technical Notes

## Key Technical Facts
- Redis is used for caching
- Postgres for persistence
`
	err = os.WriteFile(filepath.Join(memDir, "technical.md"), []byte(subContent), 0644)
	if err != nil {
		t.Fatalf("failed to write technical.md: %v", err)
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("Execute failed: %s", result.Error)
	}

	facts := result.Data["facts"].([]Fact)
	if len(facts) != 2 {
		t.Errorf("expected 2 facts from memory subdirectory, got %d", len(facts))
		for i, f := range facts {
			t.Logf("  fact[%d]: category=%q content=%q source=%q", i, f.Category, f.Content, f.Source)
		}
	}

	// Verify source path includes memory/ prefix
	for _, fact := range facts {
		if fact.Source != "memory/technical.md" {
			t.Errorf("expected source 'memory/technical.md', got %q", fact.Source)
		}
	}
}
