package fts

import (
	"strings"
	"testing"
)

func TestChunkMarkdown_BasicHeadings(t *testing.T) {
	content := `# Title

Intro paragraph.

## Section One

Content of section one.

## Section Two

Content of section two.
`
	chunks := ChunkMarkdown(content, 500)

	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}

	// First chunk should be the intro under the title
	if !strings.Contains(chunks[0].Content, "Intro paragraph") {
		t.Errorf("first chunk should contain intro, got: %s", chunks[0].Content)
	}

	// Check that section headings appear in heading breadcrumbs
	foundSec1 := false
	foundSec2 := false
	for _, c := range chunks {
		if strings.Contains(c.Heading, "Section One") {
			foundSec1 = true
			if !strings.Contains(c.Content, "Content of section one") {
				t.Errorf("Section One chunk has wrong content: %s", c.Content)
			}
		}
		if strings.Contains(c.Heading, "Section Two") {
			foundSec2 = true
		}
	}
	if !foundSec1 {
		t.Error("expected a chunk with Section One heading")
	}
	if !foundSec2 {
		t.Error("expected a chunk with Section Two heading")
	}
}

func TestChunkMarkdown_LargeSectionSubdivision(t *testing.T) {
	// Create a section with many words to trigger paragraph subdivision
	var paragraphs []string
	for i := 0; i < 10; i++ {
		// Each paragraph is ~100 words
		paragraphs = append(paragraphs, strings.Repeat("word ", 100))
	}
	content := "## Big Section\n\n" + strings.Join(paragraphs, "\n\n")

	chunks := ChunkMarkdown(content, 200)

	if len(chunks) < 3 {
		t.Fatalf("expected large section to be split into 3+ chunks, got %d", len(chunks))
	}

	// All chunks should reference the same heading
	for _, c := range chunks {
		if !strings.Contains(c.Heading, "Big Section") {
			t.Errorf("expected heading to contain 'Big Section', got: %s", c.Heading)
		}
	}
}

func TestChunkMarkdown_NestedHeadings(t *testing.T) {
	content := `## Parent

Parent content.

### Child

Child content.

## Another Parent

Another parent content.
`
	chunks := ChunkMarkdown(content, 500)

	// Find the child chunk
	var childChunk *Chunk
	for i := range chunks {
		if strings.Contains(chunks[i].Content, "Child content") {
			childChunk = &chunks[i]
			break
		}
	}

	if childChunk == nil {
		t.Fatal("expected a chunk with 'Child content'")
	}

	// Heading should show breadcrumb: "## Parent > ### Child"
	if !strings.Contains(childChunk.Heading, "Parent") || !strings.Contains(childChunk.Heading, "Child") {
		t.Errorf("expected nested heading breadcrumb, got: %s", childChunk.Heading)
	}
}

func TestChunkMarkdown_EmptyContent(t *testing.T) {
	chunks := ChunkMarkdown("", 500)
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for empty content, got %d", len(chunks))
	}
}

func TestChunkMarkdown_NoHeadings(t *testing.T) {
	content := "Just some plain text without any headings.\n\nAnother paragraph."
	chunks := ChunkMarkdown(content, 500)

	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk for headingless content, got %d", len(chunks))
	}
	if chunks[0].Heading != "" {
		t.Errorf("expected empty heading, got: %s", chunks[0].Heading)
	}
}

func TestChunkMarkdown_ChunkIndexOrdering(t *testing.T) {
	content := `## A

Content A.

## B

Content B.

## C

Content C.
`
	chunks := ChunkMarkdown(content, 500)

	for i, c := range chunks {
		if c.Index != i {
			t.Errorf("chunk %d has Index=%d, expected %d", i, c.Index, i)
		}
	}
}
