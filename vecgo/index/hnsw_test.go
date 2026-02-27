package index

import (
	"testing"

	"github.com/jefflaplante/vecgo/storage"
)

func TestHNSW_AddAndLen(t *testing.T) {
	h := NewHNSW(HNSWConfig{})

	vectors := []storage.Vector{
		{ID: "1", Embedding: []float32{1, 0, 0}},
		{ID: "2", Embedding: []float32{0, 1, 0}},
		{ID: "3", Embedding: []float32{0, 0, 1}},
	}

	if err := h.Add(vectors); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	if h.Len() != 3 {
		t.Errorf("expected Len()=3, got %d", h.Len())
	}
}

func TestHNSW_Search(t *testing.T) {
	h := NewHNSW(HNSWConfig{})

	vectors := []storage.Vector{
		{ID: "1", Embedding: []float32{1, 0, 0}},
		{ID: "2", Embedding: []float32{0.9, 0.1, 0}},
		{ID: "3", Embedding: []float32{0, 1, 0}},
		{ID: "4", Embedding: []float32{0, 0, 1}},
	}
	h.Add(vectors)

	// Query similar to vector 1
	results, err := h.Search([]float32{1, 0, 0}, 2)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// First result should be exact match (ID=1)
	if results[0].ID != "1" {
		t.Errorf("expected first result ID=1, got %s", results[0].ID)
	}

	// Second should be ID=2 (most similar)
	if results[1].ID != "2" {
		t.Errorf("expected second result ID=2, got %s", results[1].ID)
	}
}

func TestHNSW_MarshalUnmarshal(t *testing.T) {
	h1 := NewHNSW(HNSWConfig{})
	h1.Add([]storage.Vector{
		{ID: "1", Embedding: []float32{1, 0, 0}},
		{ID: "2", Embedding: []float32{0, 1, 0}},
	})

	data, err := h1.Marshal()
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	h2 := NewHNSW(HNSWConfig{})
	if err := h2.Unmarshal(data); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if h2.Len() != 2 {
		t.Errorf("expected Len()=2 after unmarshal, got %d", h2.Len())
	}

	// Search should work on restored index
	results, _ := h2.Search([]float32{1, 0, 0}, 1)
	if len(results) != 1 || results[0].ID != "1" {
		t.Errorf("search after unmarshal failed: %v", results)
	}
}
