package chunker

import (
	"strings"
	"testing"
)

func TestMarkdown_ChunkByHeadings(t *testing.T) {
	md := `# Title

Intro paragraph.

## Section One

Content for section one.

## Section Two

Content for section two.
`

	c := NewMarkdown(500)
	chunks := c.Chunk(md)

	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}

	// Check that chunks have heading metadata
	found := false
	for _, chunk := range chunks {
		if chunk.Metadata != nil && chunk.Metadata["heading"] != "" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected chunks to have heading metadata")
	}
}

func TestMarkdown_RespectsTokenLimit(t *testing.T) {
	// Create content that exceeds token limit
	md := "# Title\n\n"
	for i := 0; i < 100; i++ {
		md += "This is a sentence with several words in it. "
	}

	c := NewMarkdown(50) // Small limit
	chunks := c.Chunk(md)

	for _, chunk := range chunks {
		tokens := CountTokens(chunk.Content)
		if tokens > 100 { // Allow some buffer
			t.Errorf("chunk exceeds token limit: %d tokens", tokens)
		}
	}
}

func TestMarkdown_PreservesCodeBlocks(t *testing.T) {
	md := "# Code Example\n\n```go\nfunc main() {\n    println(\"hello\")\n}\n```\n\nAfter code."

	c := NewMarkdown(500)
	chunks := c.Chunk(md)

	// Code block should not be split
	foundCode := false
	for _, chunk := range chunks {
		if strings.Contains(chunk.Content, "func main()") && strings.Contains(chunk.Content, "println") {
			foundCode = true
			break
		}
	}
	if !foundCode {
		t.Error("code block was split across chunks")
	}
}
