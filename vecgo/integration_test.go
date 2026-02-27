package vecgo

import (
	"context"
	"testing"

	"github.com/jefflaplante/vecgo/chunker"
	"github.com/jefflaplante/vecgo/embedder"
)

func TestIntegration_FullWorkflow(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Create pipeline with all components
	p, err := NewBuilder().
		WithChunker(chunker.NewMarkdown(100)).
		WithEmbedder(embedder.NewTFIDF(1000)).
		WithSQLite(tmpDir+"/test.db").
		WithHNSW(8, 100, 25).
		Build()
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	defer p.Close()

	// Add documents
	docs := []Document{
		{ID: "doc1", Content: "# Machine Learning\n\nMachine learning is a subset of AI.", Metadata: map[string]string{"type": "article"}},
		{ID: "doc2", Content: "# Deep Learning\n\nNeural networks with many layers.", Metadata: map[string]string{"type": "article"}},
		{ID: "doc3", Content: "# Cooking Recipes\n\nHow to make pasta.", Metadata: map[string]string{"type": "recipe"}},
	}

	if err := p.AddBatch(ctx, docs); err != nil {
		t.Fatalf("AddBatch failed: %v", err)
	}

	// Search for ML-related content
	results, err := p.Search(ctx, "machine learning AI", 2)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("expected results")
	}

	// First result should be ML-related
	t.Logf("Top result: %s (score: %.3f)", results[0].ID, results[0].Score)

	// Save and reload
	if err := p.Save(ctx); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Create new pipeline and load
	p2, _ := NewBuilder().WithSQLite(tmpDir + "/test.db").Build()
	defer p2.Close()

	if err := p2.Load(ctx); err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Search should still work
	results2, err := p2.Search(ctx, "neural networks", 1)
	if err != nil {
		t.Fatalf("Search after load failed: %v", err)
	}

	if len(results2) == 0 {
		t.Error("expected results after reload")
	}
}

func TestIntegration_JSONChunker(t *testing.T) {
	ctx := context.Background()

	p, _ := NewBuilder().
		WithChunker(chunker.NewJSON(500)).
		Build()
	defer p.Close()

	jsonl := `{"id": "1", "title": "First item", "content": "Hello world"}
{"id": "2", "title": "Second item", "content": "Goodbye world"}`

	p.Add(ctx, "data", jsonl, nil)

	results, _ := p.Search(ctx, "hello", 1)
	if len(results) == 0 {
		t.Error("expected to find 'hello'")
	}
}
