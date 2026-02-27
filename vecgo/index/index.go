package index

import "github.com/jefflaplante/vecgo/storage"

// SearchResult represents a nearest neighbor match.
type SearchResult struct {
	ID       string
	Distance float32
	Metadata map[string]string
}

// Index provides nearest neighbor search.
type Index interface {
	// Add vectors to the index
	Add(vectors []storage.Vector) error

	// Search returns k nearest neighbors
	Search(query []float32, k int) ([]SearchResult, error)

	// Remove vectors by ID
	Remove(ids []string) error

	// Serialize/deserialize the graph
	Marshal() ([]byte, error)
	Unmarshal(data []byte) error

	// Stats
	Len() int
}
