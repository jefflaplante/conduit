package storage

import "context"

// Vector represents a single indexed item.
type Vector struct {
	ID        string
	Embedding []float32
	Metadata  map[string]string
}

// Storage persists vectors and the HNSW graph.
type Storage interface {
	// Vector operations
	Save(ctx context.Context, vectors []Vector) error
	Load(ctx context.Context) ([]Vector, error)
	Delete(ctx context.Context, ids []string) error

	// Graph snapshot operations
	SaveGraph(ctx context.Context, data []byte) error
	LoadGraph(ctx context.Context) ([]byte, error)

	// Lifecycle
	Close() error
}
