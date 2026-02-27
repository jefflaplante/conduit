package storage

import (
	"context"
	"testing"
)

func TestMemory_SaveAndLoad(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()
	defer m.Close()

	vectors := []Vector{
		{ID: "1", Embedding: []float32{1, 2, 3}, Metadata: map[string]string{"a": "b"}},
		{ID: "2", Embedding: []float32{4, 5, 6}, Metadata: nil},
	}

	if err := m.Save(ctx, vectors); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := m.Load(ctx)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if len(loaded) != 2 {
		t.Errorf("expected 2 vectors, got %d", len(loaded))
	}
}

func TestMemory_Delete(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()
	defer m.Close()

	vectors := []Vector{
		{ID: "1", Embedding: []float32{1, 2, 3}},
		{ID: "2", Embedding: []float32{4, 5, 6}},
	}
	m.Save(ctx, vectors)

	if err := m.Delete(ctx, []string{"1"}); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	loaded, _ := m.Load(ctx)
	if len(loaded) != 1 {
		t.Errorf("expected 1 vector after delete, got %d", len(loaded))
	}
	if loaded[0].ID != "2" {
		t.Errorf("expected remaining vector ID=2, got %s", loaded[0].ID)
	}
}

func TestMemory_Graph(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()
	defer m.Close()

	data := []byte("test graph data")
	if err := m.SaveGraph(ctx, data); err != nil {
		t.Fatalf("SaveGraph failed: %v", err)
	}

	loaded, err := m.LoadGraph(ctx)
	if err != nil {
		t.Fatalf("LoadGraph failed: %v", err)
	}

	if string(loaded) != "test graph data" {
		t.Errorf("graph data mismatch: got %s", string(loaded))
	}
}
