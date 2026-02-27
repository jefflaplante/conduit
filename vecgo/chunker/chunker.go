package chunker

// Chunk represents a piece of a document.
type Chunk struct {
	Content  string
	Index    int
	Metadata map[string]string
}

// Chunker splits documents into indexable pieces.
type Chunker interface {
	// Chunk splits text into pieces.
	Chunk(text string) []Chunk

	// ChunkWithMetadata includes source metadata on each chunk.
	ChunkWithMetadata(text string, meta map[string]string) []Chunk
}
