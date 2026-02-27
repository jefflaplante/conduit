package vecgo

import (
	"context"
	"testing"
)

func TestQuick(t *testing.T) {
	p, err := Quick()
	if err != nil {
		t.Fatalf("Quick() failed: %v", err)
	}
	defer p.Close()

	ctx := context.Background()

	// Add some documents
	err = p.Add(ctx, "doc1", "The quick brown fox jumps over the lazy dog", nil)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	err = p.Add(ctx, "doc2", "A fast fox leaps across the sleeping hound", nil)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// Search
	results, err := p.Search(ctx, "quick fox", 2)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(results) == 0 {
		t.Error("expected results, got none")
	}
}

func TestPipeline_Builder(t *testing.T) {
	p, err := NewBuilder().Build()
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	defer p.Close()

	ctx := context.Background()
	p.Add(ctx, "test", "hello world", map[string]string{"source": "test"})

	results, _ := p.Search(ctx, "hello", 1)
	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}
}

func TestPipeline_SaveLoad(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := tmpDir + "/test.db"

	ctx := context.Background()

	// Create, add, save
	p1, _ := QuickWithPath(dbPath)
	p1.Add(ctx, "doc1", "important document", nil)
	p1.Save(ctx)
	p1.Close()

	// Load and verify
	p2, _ := QuickWithPath(dbPath)
	defer p2.Close()
	p2.Load(ctx)

	results, _ := p2.Search(ctx, "important", 1)
	if len(results) != 1 {
		t.Errorf("expected 1 result after reload, got %d", len(results))
	}
}
