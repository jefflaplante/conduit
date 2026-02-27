package storage

import "testing"

func TestVectorStruct(t *testing.T) {
	v := Vector{
		ID:        "test-1",
		Embedding: []float32{1.0, 2.0, 3.0},
		Metadata:  map[string]string{"source": "test"},
	}
	if v.ID != "test-1" {
		t.Errorf("unexpected ID: %s", v.ID)
	}
	if len(v.Embedding) != 3 {
		t.Errorf("unexpected embedding length: %d", len(v.Embedding))
	}
}
