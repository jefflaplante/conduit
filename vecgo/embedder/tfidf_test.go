package embedder

import (
	"context"
	"testing"
)

func TestTFIDF_Train(t *testing.T) {
	e := NewTFIDF(100)

	docs := []string{
		"the quick brown fox",
		"the lazy dog",
		"quick quick fox",
	}

	if err := e.Train(docs); err != nil {
		t.Fatalf("Train failed: %v", err)
	}

	if e.Dimensions() == 0 {
		t.Error("expected non-zero dimensions after training")
	}
}

func TestTFIDF_Embed(t *testing.T) {
	e := NewTFIDF(100)
	e.Train([]string{"hello world", "goodbye world"})

	ctx := context.Background()
	vectors, err := e.Embed(ctx, []string{"hello", "goodbye"})
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}

	if len(vectors) != 2 {
		t.Errorf("expected 2 vectors, got %d", len(vectors))
	}

	if len(vectors[0]) != e.Dimensions() {
		t.Errorf("vector dimension mismatch: got %d, want %d", len(vectors[0]), e.Dimensions())
	}
}

func TestTFIDF_SimilarTexts(t *testing.T) {
	e := NewTFIDF(100)
	e.Train([]string{
		"machine learning algorithms",
		"deep learning neural networks",
		"cooking recipes food",
	})

	ctx := context.Background()
	vectors, _ := e.Embed(ctx, []string{
		"machine learning", // Similar to doc 0
		"neural networks",  // Similar to doc 1
	})

	// Vectors should have some non-zero values
	hasNonZero := false
	for _, v := range vectors[0] {
		if v != 0 {
			hasNonZero = true
			break
		}
	}
	if !hasNonZero {
		t.Error("expected non-zero vector for 'machine learning'")
	}
}

func TestTFIDF_Name(t *testing.T) {
	e := NewTFIDF(100)
	if e.Name() != "tfidf" {
		t.Errorf("expected name 'tfidf', got %s", e.Name())
	}
}
