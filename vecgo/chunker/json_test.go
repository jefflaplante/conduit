package chunker

import (
	"strings"
	"testing"
)

func TestJSON_ChunkJSONL(t *testing.T) {
	jsonl := `{"id": "1", "text": "first"}
{"id": "2", "text": "second"}
{"id": "3", "text": "third"}`

	c := NewJSON(500)
	chunks := c.Chunk(jsonl)

	if len(chunks) != 3 {
		t.Errorf("expected 3 chunks for JSONL, got %d", len(chunks))
	}

	// Each chunk should be valid JSON
	for i, chunk := range chunks {
		if !strings.HasPrefix(strings.TrimSpace(chunk.Content), "{") {
			t.Errorf("chunk %d is not valid JSON: %s", i, chunk.Content)
		}
	}
}

func TestJSON_ChunkArray(t *testing.T) {
	arr := `[
		{"id": "1"},
		{"id": "2"},
		{"id": "3"}
	]`

	c := NewJSON(500)
	chunks := c.Chunk(arr)

	if len(chunks) != 3 {
		t.Errorf("expected 3 chunks for array, got %d", len(chunks))
	}
}

func TestJSON_SmallObject(t *testing.T) {
	obj := `{"key": "value"}`

	c := NewJSON(500)
	chunks := c.Chunk(obj)

	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk for small object, got %d", len(chunks))
	}
}
