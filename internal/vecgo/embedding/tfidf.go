package embedding

import "github.com/jefflaplante/vecgo/embedder"

// NewTFIDFFallback creates a TF-IDF embedder for use as the default/fallback provider.
func NewTFIDFFallback(dims int) embedder.Embedder {
	return embedder.NewTFIDF(dims)
}
