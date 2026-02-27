package chunker

import (
	"regexp"
	"strings"
)

// Markdown chunks text respecting markdown structure.
type Markdown struct {
	maxTokens int
}

// NewMarkdown creates a markdown-aware chunker.
func NewMarkdown(maxTokens int) *Markdown {
	if maxTokens <= 0 {
		maxTokens = 500
	}
	return &Markdown{maxTokens: maxTokens}
}

var headingRegex = regexp.MustCompile(`(?m)^(#{1,6})\s+(.+)$`)
var codeBlockRegex = regexp.MustCompile("(?s)```[^`]*```")

// Chunk splits markdown into chunks respecting headings and code blocks.
func (m *Markdown) Chunk(text string) []Chunk {
	return m.ChunkWithMetadata(text, nil)
}

// ChunkWithMetadata chunks with additional metadata.
func (m *Markdown) ChunkWithMetadata(text string, meta map[string]string) []Chunk {
	var chunks []Chunk

	// Find all headings
	headings := headingRegex.FindAllStringSubmatchIndex(text, -1)

	if len(headings) == 0 {
		// No headings - chunk by size only
		return m.chunkBySize(text, "", meta)
	}

	// Handle content before first heading
	if headings[0][0] > 0 {
		preContent := strings.TrimSpace(text[:headings[0][0]])
		if preContent != "" {
			preChunks := m.chunkBySize(preContent, "", meta)
			chunks = append(chunks, preChunks...)
		}
	}

	// Process each section
	for i, match := range headings {
		start := match[0]
		var end int
		if i+1 < len(headings) {
			end = headings[i+1][0]
		} else {
			end = len(text)
		}

		section := text[start:end]
		heading := text[match[4]:match[5]] // The heading text

		sectionMeta := copyMeta(meta)
		if sectionMeta == nil {
			sectionMeta = make(map[string]string)
		}
		sectionMeta["heading"] = heading

		sectionChunks := m.chunkBySize(section, heading, sectionMeta)
		chunks = append(chunks, sectionChunks...)
	}

	// Reindex all chunks
	for i := range chunks {
		chunks[i].Index = i
	}

	return chunks
}

func (m *Markdown) chunkBySize(text, heading string, meta map[string]string) []Chunk {
	var chunks []Chunk

	// Protect code blocks - replace with placeholders
	codeBlocks := codeBlockRegex.FindAllString(text, -1)
	for i, block := range codeBlocks {
		placeholder := "\x00CODE" + string(rune('0'+i)) + "\x00"
		text = strings.Replace(text, block, placeholder, 1)
	}

	// Split into paragraphs
	paragraphs := strings.Split(text, "\n\n")
	var current strings.Builder
	currentTokens := 0

	flush := func() {
		if current.Len() > 0 {
			content := current.String()
			// Restore code blocks
			for i, block := range codeBlocks {
				placeholder := "\x00CODE" + string(rune('0'+i)) + "\x00"
				content = strings.Replace(content, placeholder, block, 1)
			}
			chunks = append(chunks, Chunk{
				Content:  strings.TrimSpace(content),
				Metadata: copyMeta(meta),
			})
			current.Reset()
			currentTokens = 0
		}
	}

	for _, para := range paragraphs {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}

		paraTokens := CountTokens(para)

		// If single paragraph exceeds limit, split it
		if paraTokens > m.maxTokens {
			flush()
			// Split by sentences
			sentences := splitSentences(para)
			for _, sent := range sentences {
				sentTokens := CountTokens(sent)
				if currentTokens+sentTokens > m.maxTokens && current.Len() > 0 {
					flush()
				}
				if current.Len() > 0 {
					current.WriteString(" ")
				}
				current.WriteString(sent)
				currentTokens += sentTokens
			}
			continue
		}

		if currentTokens+paraTokens > m.maxTokens {
			flush()
		}

		if current.Len() > 0 {
			current.WriteString("\n\n")
		}
		current.WriteString(para)
		currentTokens += paraTokens
	}

	flush()
	return chunks
}

func splitSentences(text string) []string {
	var sentences []string
	var current strings.Builder

	for i, r := range text {
		current.WriteRune(r)
		if (r == '.' || r == '!' || r == '?') && i+1 < len(text) {
			next := rune(text[i+1])
			if next == ' ' || next == '\n' {
				sentences = append(sentences, strings.TrimSpace(current.String()))
				current.Reset()
			}
		}
	}
	if current.Len() > 0 {
		sentences = append(sentences, strings.TrimSpace(current.String()))
	}

	return sentences
}

func copyMeta(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	result := make(map[string]string, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}
