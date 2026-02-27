package vecgo

import (
	"context"
	"fmt"
	"log"
	"strings"

	"conduit/internal/tools/types"

	"github.com/jefflaplante/vecgo"
	"github.com/jefflaplante/vecgo/chunker"
	"github.com/jefflaplante/vecgo/embedder"
)

// Compile-time interface check.
var _ types.VectorService = (*Service)(nil)

// Config holds configuration for the VecGo vector search service.
type Config struct {
	DBPath    string            // Path to SQLite persistence file (empty = in-memory)
	ChunkSize int               // Max tokens per chunk
	EmbedDims int               // TF-IDF embedding dimensions
	HNSWM     int               // HNSW max connections per node
	HNSWEfC   int               // HNSW construction search depth
	HNSWEfS   int               // HNSW query search depth
	Embedder  embedder.Embedder // Optional: if nil, uses TF-IDF default
}

// resolveEmbedder returns the configured embedder, falling back to TF-IDF.
func (c Config) resolveEmbedder() embedder.Embedder {
	if c.Embedder != nil {
		return c.Embedder
	}
	return embedder.NewTFIDF(c.EmbedDims)
}

// DefaultConfig returns sensible defaults for the vector service.
func DefaultConfig() Config {
	return Config{
		ChunkSize: 500,
		EmbedDims: 4096,
		HNSWM:     16,
		HNSWEfC:   200,
		HNSWEfS:   50,
	}
}

// Service wraps a vecgo.Pipeline and implements types.VectorService.
type Service struct {
	pipeline *vecgo.Pipeline
	cfg      Config
}

// NewService creates a new VecGo vector search service.
func NewService(cfg Config) (*Service, error) {
	if cfg.ChunkSize <= 0 {
		cfg.ChunkSize = DefaultConfig().ChunkSize
	}
	if cfg.EmbedDims <= 0 {
		cfg.EmbedDims = DefaultConfig().EmbedDims
	}
	if cfg.HNSWM <= 0 {
		cfg.HNSWM = DefaultConfig().HNSWM
	}
	if cfg.HNSWEfC <= 0 {
		cfg.HNSWEfC = DefaultConfig().HNSWEfC
	}
	if cfg.HNSWEfS <= 0 {
		cfg.HNSWEfS = DefaultConfig().HNSWEfS
	}

	builder := vecgo.NewBuilder().
		WithChunker(chunker.NewMarkdown(cfg.ChunkSize)).
		WithEmbedder(cfg.resolveEmbedder()).
		WithHNSW(cfg.HNSWM, cfg.HNSWEfC, cfg.HNSWEfS)

	if cfg.DBPath != "" {
		builder = builder.WithSQLite(cfg.DBPath)
	}

	pipeline, err := builder.Build()
	if err != nil {
		return nil, fmt.Errorf("vecgo: build pipeline: %w", err)
	}

	// Attempt to load persisted state; non-fatal if empty or missing.
	if cfg.DBPath != "" {
		if loadErr := pipeline.Load(context.Background()); loadErr != nil {
			log.Printf("vecgo: loading persisted state: %v (starting fresh)", loadErr)
		}
	}

	return &Service{pipeline: pipeline, cfg: cfg}, nil
}

// Search performs a semantic search and returns matching results.
func (s *Service) Search(ctx context.Context, query string, limit int) ([]types.VectorSearchResult, error) {
	if limit <= 0 {
		limit = 10
	}

	results, err := s.pipeline.Search(ctx, query, limit)
	if err != nil {
		// Return empty results for empty-corpus errors instead of failing.
		if strings.Contains(err.Error(), "empty corpus") || strings.Contains(err.Error(), "not trained") {
			return nil, nil
		}
		return nil, fmt.Errorf("vecgo: search: %w", err)
	}

	out := make([]types.VectorSearchResult, len(results))
	for i, r := range results {
		out[i] = types.VectorSearchResult{
			ID:       r.ID,
			Score:    float64(r.Score),
			Content:  r.Content,
			Metadata: r.Metadata,
		}
	}
	return out, nil
}

// Index adds or updates a document in the vector index.
func (s *Service) Index(ctx context.Context, id, content string, metadata map[string]string) error {
	if err := s.pipeline.Add(ctx, id, content, metadata); err != nil {
		return fmt.Errorf("vecgo: index: %w", err)
	}
	return nil
}

// Remove deletes a document from the vector index.
func (s *Service) Remove(ctx context.Context, id string) error {
	if err := s.pipeline.Remove(ctx, id); err != nil {
		return fmt.Errorf("vecgo: remove: %w", err)
	}
	return nil
}

// Save persists the current index state to disk.
func (s *Service) Save(ctx context.Context) error {
	if s.cfg.DBPath == "" {
		return nil
	}
	return s.pipeline.Save(ctx)
}

// Close releases resources held by the pipeline.
func (s *Service) Close() error {
	if s.cfg.DBPath != "" {
		if err := s.pipeline.Save(context.Background()); err != nil {
			log.Printf("vecgo: save on close: %v", err)
		}
	}
	return s.pipeline.Close()
}
