package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSQLite_SaveAndLoad(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")

	s, err := NewSQLite(dbPath)
	if err != nil {
		t.Fatalf("NewSQLite failed: %v", err)
	}
	defer s.Close()

	vectors := []Vector{
		{ID: "1", Embedding: []float32{1, 2, 3}, Metadata: map[string]string{"a": "b"}},
		{ID: "2", Embedding: []float32{4, 5, 6}, Metadata: nil},
	}

	if err := s.Save(ctx, vectors); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := s.Load(ctx)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if len(loaded) != 2 {
		t.Errorf("expected 2 vectors, got %d", len(loaded))
	}
}

func TestSQLite_Persistence(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")

	// Save data
	s1, _ := NewSQLite(dbPath)
	s1.Save(ctx, []Vector{{ID: "1", Embedding: []float32{1, 2, 3}}})
	s1.SaveGraph(ctx, []byte("graph data"))
	s1.Close()

	// Reopen and verify
	s2, _ := NewSQLite(dbPath)
	defer s2.Close()

	loaded, _ := s2.Load(ctx)
	if len(loaded) != 1 {
		t.Errorf("expected 1 vector after reopen, got %d", len(loaded))
	}

	graph, _ := s2.LoadGraph(ctx)
	if string(graph) != "graph data" {
		t.Errorf("graph data mismatch: %s", string(graph))
	}
}

func TestSQLite_Delete(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")

	s, _ := NewSQLite(dbPath)
	defer s.Close()

	s.Save(ctx, []Vector{
		{ID: "1", Embedding: []float32{1}},
		{ID: "2", Embedding: []float32{2}},
	})

	s.Delete(ctx, []string{"1"})

	loaded, _ := s.Load(ctx)
	if len(loaded) != 1 || loaded[0].ID != "2" {
		t.Errorf("delete failed: got %v", loaded)
	}
}
