package chunker

import "strings"

// Fixed splits text into fixed-size token chunks.
type Fixed struct {
	maxTokens int
	overlap   int
}

// NewFixed creates a fixed-size chunker.
func NewFixed(maxTokens, overlap int) *Fixed {
	if maxTokens <= 0 {
		maxTokens = 500
	}
	if overlap < 0 {
		overlap = 0
	}
	if overlap >= maxTokens {
		overlap = maxTokens / 4
	}
	return &Fixed{maxTokens: maxTokens, overlap: overlap}
}

// Chunk splits text into fixed-size chunks.
func (f *Fixed) Chunk(text string) []Chunk {
	return f.ChunkWithMetadata(text, nil)
}

// ChunkWithMetadata chunks with additional metadata.
func (f *Fixed) ChunkWithMetadata(text string, meta map[string]string) []Chunk {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}

	var chunks []Chunk
	step := f.maxTokens - f.overlap
	if step <= 0 {
		step = 1
	}

	for i := 0; i < len(words); i += step {
		end := i + f.maxTokens
		if end > len(words) {
			end = len(words)
		}

		content := strings.Join(words[i:end], " ")
		chunks = append(chunks, Chunk{
			Content:  content,
			Index:    len(chunks),
			Metadata: copyMeta(meta),
		})

		// Stop if we've reached the end
		if end == len(words) {
			break
		}
	}

	return chunks
}
