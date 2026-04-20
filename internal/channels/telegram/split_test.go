package telegram

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSplitMessage_UnderLimit(t *testing.T) {
	text := "Hello, world!"
	chunks := splitMessage(text, 4096)
	assert.Len(t, chunks, 1)
	assert.Equal(t, text, chunks[0])
}

func TestSplitMessage_ExactLimit(t *testing.T) {
	text := strings.Repeat("a", 4096)
	chunks := splitMessage(text, 4096)
	assert.Len(t, chunks, 1)
	assert.Equal(t, text, chunks[0])
}

func TestSplitMessage_OneOverLimit(t *testing.T) {
	text := strings.Repeat("a", 4097)
	chunks := splitMessage(text, 4096)
	assert.Len(t, chunks, 2)
	assert.Equal(t, 4096, len(chunks[0]))
	assert.Equal(t, "a", chunks[1])
}

func TestSplitMessage_ParagraphBreaks(t *testing.T) {
	// Build text with two paragraphs; total must exceed the limit.
	para1 := strings.Repeat("a", 4000)
	para2 := strings.Repeat("b", 200)
	text := para1 + "\n\n" + para2

	chunks := splitMessage(text, 4096)
	assert.Len(t, chunks, 2)
	assert.Equal(t, para1, chunks[0])
	assert.Equal(t, para2, chunks[1])
}

func TestSplitMessage_SentenceBreaks(t *testing.T) {
	// No paragraph breaks; rely on sentence boundaries. Total > limit.
	tail := strings.Repeat("c", 200)
	text := strings.Repeat("a", 4000) + ". " + tail
	chunks := splitMessage(text, 4096)
	assert.Len(t, chunks, 2)
	assert.Equal(t, strings.Repeat("a", 4000)+".", chunks[0])
	assert.Equal(t, tail, chunks[1])
}

func TestSplitMessage_WordBoundary(t *testing.T) {
	// No paragraph or sentence breaks, but spaces
	part1 := strings.Repeat("a", 3000) + " "
	part2 := strings.Repeat("b", 2000)
	text := part1 + part2

	chunks := splitMessage(text, 4096)
	assert.GreaterOrEqual(t, len(chunks), 2)
	// First chunk should be at or under limit
	assert.LessOrEqual(t, len(chunks[0]), 4096)
	// All content should be preserved
	reconstructed := strings.Join(chunks, " ")
	assert.Equal(t, text, reconstructed)
}

func TestSplitMessage_HardCut(t *testing.T) {
	// One giant word with no breaks at all
	text := strings.Repeat("x", 12000)
	chunks := splitMessage(text, 4096)
	assert.Len(t, chunks, 3)
	for i, chunk := range chunks {
		assert.LessOrEqual(t, len(chunk), 4096, "chunk %d exceeds limit", i)
	}
	// All content preserved
	total := 0
	for _, chunk := range chunks {
		total += len(chunk)
	}
	assert.Equal(t, 12000, total)
}

func TestSplitMessage_MultipleParagraphs(t *testing.T) {
	paragraphs := []string{
		strings.Repeat("alpha ", 200), // ~1200 chars
		strings.Repeat("beta ", 200),  // ~1000 chars
		strings.Repeat("gamma ", 500), // ~3000 chars
		strings.Repeat("delta ", 200), // ~1200 chars
	}
	text := strings.Join(paragraphs, "\n\n")
	// Total: ~6400 chars, should split into 2 chunks

	chunks := splitMessage(text, 4096)
	assert.GreaterOrEqual(t, len(chunks), 2)

	for i, chunk := range chunks {
		assert.LessOrEqual(t, len(chunk), 4096, "chunk %d exceeds limit", i)
	}

	// Content preservation: each paragraph's prefix should appear in some chunk.
	joined := strings.Join(chunks, "")
	for _, marker := range []string{"alpha", "beta", "gamma", "delta"} {
		assert.Contains(t, joined, marker)
	}
}

func TestSplitMessage_SmallLimit(t *testing.T) {
	text := "Hello world, this is a test."
	chunks := splitMessage(text, 10)
	assert.GreaterOrEqual(t, len(chunks), 2)
	for i, chunk := range chunks {
		assert.LessOrEqual(t, len(chunk), 10, "chunk %d exceeds limit", i)
	}
}

func TestSplitMessage_EmptyString(t *testing.T) {
	chunks := splitMessage("", 4096)
	assert.Len(t, chunks, 1)
	assert.Equal(t, "", chunks[0])
}

func TestSplitMessage_DefaultLimit(t *testing.T) {
	// Test that limit=0 defaults to TelegramMessageLimit
	text := strings.Repeat("a", 4097)
	chunks := splitMessage(text, 0)
	assert.Len(t, chunks, 2)
}

func TestSplitMessage_MarkdownCodeBlock(t *testing.T) {
	// Text with a code block that spans across what would be a split point
	codeBlock := "```\n" + strings.Repeat("code line\n", 500) + "```"
	prefix := "Here is some code:\n\n"
	text := prefix + codeBlock

	chunks := splitMessage(text, 4096)
	for i, chunk := range chunks {
		assert.LessOrEqual(t, len(chunk), 4096, "chunk %d exceeds limit", i)
	}
	// All non-whitespace content should be preserved across chunks.
	rejoined := strings.Join(chunks, "")
	assert.Equal(t, strings.ReplaceAll(strings.TrimSpace(text), "\n", ""),
		strings.ReplaceAll(strings.TrimSpace(rejoined), "\n", ""))
}

func TestSplitMessage_SentenceEndings(t *testing.T) {
	// Total must exceed limit for a split to occur.
	tail := strings.Repeat("c", 200)
	text := strings.Repeat("a", 4000) + "! " + tail
	chunks := splitMessage(text, 4096)
	assert.GreaterOrEqual(t, len(chunks), 2)
	assert.LessOrEqual(t, len(chunks[0]), 4096)

	text = strings.Repeat("a", 4000) + "? " + tail
	chunks = splitMessage(text, 4096)
	assert.GreaterOrEqual(t, len(chunks), 2)
	assert.LessOrEqual(t, len(chunks[0]), 4096)
}

func TestFindSplitPoint_WithinLimit(t *testing.T) {
	text := "short text"
	idx := findSplitPoint(text, 100)
	assert.Equal(t, len(text), idx)
}

func TestFindSplitPoint_ParagraphBreak(t *testing.T) {
	text := strings.Repeat("a", 100) + "\n\n" + strings.Repeat("b", 100)
	idx := findSplitPoint(text, 150)
	assert.Equal(t, strings.Repeat("a", 100)+"\n\n", text[:idx])
}

func TestFindSplitPoint_SentenceBreak(t *testing.T) {
	text := strings.Repeat("a", 100) + ". " + strings.Repeat("b", 100)
	idx := findSplitPoint(text, 150)
	assert.Equal(t, strings.Repeat("a", 100)+". ", text[:idx])
}

func TestFindSplitPoint_HardCut(t *testing.T) {
	text := strings.Repeat("a", 200)
	idx := findSplitPoint(text, 150)
	assert.Equal(t, 150, idx)
}
