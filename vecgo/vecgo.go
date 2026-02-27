package vecgo

import (
	"context"
	"fmt"
	"sync"

	"github.com/jefflaplante/vecgo/chunker"
	"github.com/jefflaplante/vecgo/embedder"
	"github.com/jefflaplante/vecgo/index"
	"github.com/jefflaplante/vecgo/storage"
)

// Version of the vecgo library
const Version = "0.1.0"

// Result represents a search result.
type Result struct {
	ID       string
	Score    float32
	Content  string
	Metadata map[string]string
}

// Document represents a document to index.
type Document struct {
	ID       string
	Content  string
	Metadata map[string]string
}

// Pipeline ties all layers together.
type Pipeline struct {
	chunker  chunker.Chunker
	embedder embedder.Embedder
	index    *index.HNSW
	memory   *storage.Memory
	sqlite   *storage.SQLite

	// Track content for results
	contents map[string]string

	mu sync.RWMutex
}

// Builder configures a Pipeline.
type Builder struct {
	chunker    chunker.Chunker
	embedder   embedder.Embedder
	sqlitePath string
	hnswM      int
	hnswEfC    int
	hnswEfS    int
}

// NewBuilder creates a new Pipeline builder.
func NewBuilder() *Builder {
	return &Builder{
		hnswM:   16,
		hnswEfC: 200,
		hnswEfS: 50,
	}
}

// WithChunker sets the chunker.
func (b *Builder) WithChunker(c chunker.Chunker) *Builder {
	b.chunker = c
	return b
}

// WithEmbedder sets the embedder.
func (b *Builder) WithEmbedder(e embedder.Embedder) *Builder {
	b.embedder = e
	return b
}

// WithSQLite enables SQLite persistence.
func (b *Builder) WithSQLite(path string) *Builder {
	b.sqlitePath = path
	return b
}

// WithHNSW configures HNSW parameters.
func (b *Builder) WithHNSW(m, efConstruction, efSearch int) *Builder {
	b.hnswM = m
	b.hnswEfC = efConstruction
	b.hnswEfS = efSearch
	return b
}

// Build creates the Pipeline.
func (b *Builder) Build() (*Pipeline, error) {
	p := &Pipeline{
		contents: make(map[string]string),
		memory:   storage.NewMemory(),
	}

	// Set defaults
	if b.chunker == nil {
		p.chunker = chunker.NewMarkdown(500)
	} else {
		p.chunker = b.chunker
	}

	if b.embedder == nil {
		p.embedder = embedder.NewTFIDF(4096)
	} else {
		p.embedder = b.embedder
	}

	p.index = index.NewHNSW(index.HNSWConfig{
		M:              b.hnswM,
		EfConstruction: b.hnswEfC,
		EfSearch:       b.hnswEfS,
	})

	if b.sqlitePath != "" {
		sqlite, err := storage.NewSQLite(b.sqlitePath)
		if err != nil {
			return nil, fmt.Errorf("failed to open SQLite: %w", err)
		}
		p.sqlite = sqlite
	}

	return p, nil
}

// Quick creates a Pipeline with sensible defaults.
func Quick() (*Pipeline, error) {
	return NewBuilder().Build()
}

// QuickWithPath creates a Pipeline with SQLite persistence.
func QuickWithPath(dbPath string) (*Pipeline, error) {
	return NewBuilder().WithSQLite(dbPath).Build()
}

// Add indexes a document.
func (p *Pipeline) Add(ctx context.Context, id, text string, meta map[string]string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Chunk
	chunks := p.chunker.ChunkWithMetadata(text, meta)

	// Prepare texts for embedding
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Content
	}

	// Embed
	vectors, err := p.embedder.Embed(ctx, texts)
	if err != nil {
		return fmt.Errorf("embedding failed: %w", err)
	}

	// Create storage vectors
	storageVecs := make([]storage.Vector, len(chunks))
	for i, c := range chunks {
		chunkID := fmt.Sprintf("%s#%d", id, i)
		chunkMeta := c.Metadata
		if chunkMeta == nil {
			chunkMeta = make(map[string]string)
		}
		chunkMeta["doc_id"] = id
		chunkMeta["chunk_index"] = fmt.Sprintf("%d", i)

		storageVecs[i] = storage.Vector{
			ID:        chunkID,
			Embedding: vectors[i],
			Metadata:  chunkMeta,
		}

		p.contents[chunkID] = c.Content
	}

	// Add to index
	if err := p.index.Add(storageVecs); err != nil {
		return fmt.Errorf("indexing failed: %w", err)
	}

	// Save to memory storage
	p.memory.Save(ctx, storageVecs)

	return nil
}

// AddBatch indexes multiple documents.
func (p *Pipeline) AddBatch(ctx context.Context, docs []Document) error {
	for _, doc := range docs {
		if err := p.Add(ctx, doc.ID, doc.Content, doc.Metadata); err != nil {
			return err
		}
	}
	return nil
}

// Search finds similar documents.
func (p *Pipeline) Search(ctx context.Context, query string, k int) ([]Result, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// Embed query
	vectors, err := p.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("query embedding failed: %w", err)
	}

	// Search
	results, err := p.index.Search(vectors[0], k)
	if err != nil {
		return nil, err
	}

	// Convert to Result
	out := make([]Result, len(results))
	for i, r := range results {
		out[i] = Result{
			ID:       r.ID,
			Score:    1 - r.Distance, // Convert distance to similarity
			Content:  p.contents[r.ID],
			Metadata: r.Metadata,
		}
	}

	return out, nil
}

// Remove removes documents by ID.
func (p *Pipeline) Remove(ctx context.Context, ids ...string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Find all chunk IDs for these doc IDs
	var chunkIDs []string
	for id := range p.contents {
		for _, docID := range ids {
			if len(id) > len(docID) && id[:len(docID)] == docID && id[len(docID)] == '#' {
				chunkIDs = append(chunkIDs, id)
				delete(p.contents, id)
			}
		}
	}

	p.index.Remove(chunkIDs)
	p.memory.Delete(ctx, chunkIDs)

	return nil
}

// Save persists the index to SQLite.
func (p *Pipeline) Save(ctx context.Context) error {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.sqlite == nil {
		return nil
	}

	// Save vectors
	vectors, _ := p.memory.Load(ctx)
	if err := p.sqlite.Save(ctx, vectors); err != nil {
		return err
	}

	// Save graph
	graph, err := p.index.Marshal()
	if err != nil {
		return err
	}
	return p.sqlite.SaveGraph(ctx, graph)
}

// Load restores the index from SQLite.
func (p *Pipeline) Load(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.sqlite == nil {
		return nil
	}

	// Load vectors
	vectors, err := p.sqlite.Load(ctx)
	if err != nil {
		return err
	}
	p.memory.Save(ctx, vectors)

	// Load graph
	graph, err := p.sqlite.LoadGraph(ctx)
	if err != nil {
		return err
	}
	if graph != nil {
		return p.index.Unmarshal(graph)
	}

	return nil
}

// Close releases resources.
func (p *Pipeline) Close() error {
	if p.sqlite != nil {
		return p.sqlite.Close()
	}
	return nil
}
