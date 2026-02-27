package fts

import (
	"strings"
)

// Chunk represents a piece of a markdown document.
type Chunk struct {
	Heading string // breadcrumb heading context, e.g. "## Configuration > ### Database"
	Content string
	Index   int
}

// ChunkMarkdown splits markdown content using a hybrid strategy:
//  1. Split on ## and ### headings
//  2. Sections exceeding maxTokens are subdivided by paragraph (double newline)
//  3. Each chunk gets heading context (breadcrumb path)
//
// Token estimation uses word count (len(strings.Fields(text))) as a proxy.
func ChunkMarkdown(content string, maxTokens int) []Chunk {
	if maxTokens <= 0 {
		maxTokens = 500
	}

	lines := strings.Split(content, "\n")
	var sections []rawSection
	var currentHeadings []string
	var currentLines []string

	flush := func() {
		if len(currentLines) > 0 {
			text := strings.Join(currentLines, "\n")
			if strings.TrimSpace(text) != "" {
				sections = append(sections, rawSection{
					heading: strings.Join(currentHeadings, " > "),
					content: text,
				})
			}
		}
		currentLines = nil
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isHeading(trimmed) {
			flush()
			level := headingLevel(trimmed)
			headingText := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
			prefix := strings.Repeat("#", level)
			entry := prefix + " " + headingText

			// Update heading breadcrumb stack: keep headings at lower depth, replace at this level
			var newHeadings []string
			for _, h := range currentHeadings {
				if headingLevel(strings.TrimSpace(strings.SplitN(h, " ", 2)[0]+"x")) < level {
					newHeadings = append(newHeadings, h)
				} else {
					break
				}
			}
			// Recalculate: keep only headings with strictly lower level
			newHeadings = nil
			for _, h := range currentHeadings {
				hl := headingLevelFromEntry(h)
				if hl < level {
					newHeadings = append(newHeadings, h)
				}
			}
			newHeadings = append(newHeadings, entry)
			currentHeadings = newHeadings
		} else {
			currentLines = append(currentLines, line)
		}
	}
	flush()

	// Now subdivide large sections by paragraph
	var chunks []Chunk
	idx := 0
	for _, sec := range sections {
		wordCount := len(strings.Fields(sec.content))
		if wordCount <= maxTokens {
			chunks = append(chunks, Chunk{
				Heading: sec.heading,
				Content: strings.TrimSpace(sec.content),
				Index:   idx,
			})
			idx++
		} else {
			// Split by paragraph (double newline)
			paragraphs := splitParagraphs(sec.content)
			var buf strings.Builder
			bufWords := 0
			for _, para := range paragraphs {
				paraWords := len(strings.Fields(para))
				if bufWords+paraWords > maxTokens && bufWords > 0 {
					chunks = append(chunks, Chunk{
						Heading: sec.heading,
						Content: strings.TrimSpace(buf.String()),
						Index:   idx,
					})
					idx++
					buf.Reset()
					bufWords = 0
				}
				if buf.Len() > 0 {
					buf.WriteString("\n\n")
				}
				buf.WriteString(para)
				bufWords += paraWords
			}
			if bufWords > 0 {
				chunks = append(chunks, Chunk{
					Heading: sec.heading,
					Content: strings.TrimSpace(buf.String()),
					Index:   idx,
				})
				idx++
			}
		}
	}

	// Handle case where there are no headings at all
	if len(chunks) == 0 && strings.TrimSpace(content) != "" {
		chunks = append(chunks, Chunk{
			Heading: "",
			Content: strings.TrimSpace(content),
			Index:   0,
		})
	}

	return chunks
}

type rawSection struct {
	heading string
	content string
}

func isHeading(line string) bool {
	return strings.HasPrefix(line, "#")
}

func headingLevel(line string) int {
	level := 0
	for _, ch := range line {
		if ch == '#' {
			level++
		} else {
			break
		}
	}
	return level
}

// headingLevelFromEntry extracts heading level from a breadcrumb entry like "## Config".
func headingLevelFromEntry(entry string) int {
	return headingLevel(strings.TrimSpace(entry))
}

// splitParagraphs splits text on double newlines, preserving paragraph boundaries.
func splitParagraphs(text string) []string {
	// Normalize line endings
	text = strings.ReplaceAll(text, "\r\n", "\n")
	parts := strings.Split(text, "\n\n")
	var result []string
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
