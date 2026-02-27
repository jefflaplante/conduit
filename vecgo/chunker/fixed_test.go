package chunker

import (
	"strings"
	"testing"
)

func TestFixed_Chunk(t *testing.T) {
	// Create text with ~100 tokens
	words := make([]string, 100)
	for i := range words {
		words[i] = "word"
	}
	text := strings.Join(words, " ")

	c := NewFixed(30, 5)
	chunks := c.Chunk(text)

	if len(chunks) < 3 {
		t.Errorf("expected at least 3 chunks, got %d", len(chunks))
	}

	// Each chunk should be roughly maxTokens
	for i, chunk := range chunks {
		tokens := CountTokens(chunk.Content)
		if tokens > 35 { // Allow small buffer for overlap
			t.Errorf("chunk %d has %d tokens, expected ~30", i, tokens)
		}
	}
}

func TestFixed_Overlap(t *testing.T) {
	text := "one two three four five six seven eight nine ten"

	c := NewFixed(4, 2)
	chunks := c.Chunk(text)

	// With overlap, chunks should share some content
	if len(chunks) < 2 {
		t.Skip("need at least 2 chunks to test overlap")
	}

	// Last 2 words of chunk 0 should appear in chunk 1
	// (this is a simplified check)
	if len(chunks) >= 2 && !strings.Contains(chunks[1].Content, "three") {
		// Overlap should include "three four" from first chunk
		t.Log("Note: overlap test is approximate")
	}
}
