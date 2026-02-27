package chunker

import (
	"encoding/json"
	"strings"
)

// JSON chunks JSON and JSONL content.
type JSON struct {
	maxTokens int
}

// NewJSON creates a JSON-aware chunker.
func NewJSON(maxTokens int) *JSON {
	if maxTokens <= 0 {
		maxTokens = 500
	}
	return &JSON{maxTokens: maxTokens}
}

// Chunk splits JSON content into chunks.
func (j *JSON) Chunk(text string) []Chunk {
	return j.ChunkWithMetadata(text, nil)
}

// ChunkWithMetadata chunks with additional metadata.
func (j *JSON) ChunkWithMetadata(text string, meta map[string]string) []Chunk {
	text = strings.TrimSpace(text)

	// Try JSONL first (newline-delimited)
	if strings.Contains(text, "\n") && !strings.HasPrefix(text, "[") {
		return j.chunkJSONL(text, meta)
	}

	// Try as array
	if strings.HasPrefix(text, "[") {
		return j.chunkArray(text, meta)
	}

	// Single object
	return []Chunk{{
		Content:  text,
		Index:    0,
		Metadata: copyMeta(meta),
	}}
}

func (j *JSON) chunkJSONL(text string, meta map[string]string) []Chunk {
	lines := strings.Split(text, "\n")
	var chunks []Chunk

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		chunkMeta := copyMeta(meta)
		if chunkMeta == nil {
			chunkMeta = make(map[string]string)
		}
		chunkMeta["format"] = "jsonl"

		chunks = append(chunks, Chunk{
			Content:  line,
			Index:    len(chunks),
			Metadata: chunkMeta,
		})
	}

	return chunks
}

func (j *JSON) chunkArray(text string, meta map[string]string) []Chunk {
	var arr []json.RawMessage
	if err := json.Unmarshal([]byte(text), &arr); err != nil {
		// Not valid JSON array, return as single chunk
		return []Chunk{{Content: text, Index: 0, Metadata: copyMeta(meta)}}
	}

	var chunks []Chunk
	for i, item := range arr {
		chunkMeta := copyMeta(meta)
		if chunkMeta == nil {
			chunkMeta = make(map[string]string)
		}
		chunkMeta["format"] = "json_array"

		chunks = append(chunks, Chunk{
			Content:  string(item),
			Index:    i,
			Metadata: chunkMeta,
		})
	}

	return chunks
}
