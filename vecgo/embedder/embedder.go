package embedder

import "context"

// Embedder converts text to vectors.
type Embedder interface {
	// Embed converts texts to vectors (batched for efficiency).
	Embed(ctx context.Context, texts []string) ([][]float32, error)

	// Dimensions returns the vector dimensionality.
	Dimensions() int

	// Name identifies the embedder.
	Name() string
}
