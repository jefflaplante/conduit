# vecgo

Pure Go vector search library with HNSW indexing, used by Conduit for semantic document search.

## Features

- **HNSW Index** — Hierarchical Navigable Small World graph for fast approximate nearest neighbor search
- **Multiple Embedders** — TF-IDF (local) and OpenAI embeddings support
- **Markdown Chunker** — Intelligent document chunking that preserves heading context
- **SQLite Persistence** — Optional persistent storage for vectors and metadata
- **Pipeline API** — Fluent builder for configuring search pipelines

## Installation

```bash
go get github.com/jefflaplante/vecgo
```

## Quick Example

```go
import (
    "github.com/jefflaplante/vecgo"
    "github.com/jefflaplante/vecgo/embedder"
)

// Build a pipeline with TF-IDF embeddings
pipeline, _ := vecgo.NewBuilder().
    WithEmbedder(embedder.NewTFIDF(4096)).
    WithHNSW(16, 200, 50).
    WithSQLite("./vectors.db").
    Build()

// Index documents
pipeline.Add(ctx, "doc1", "Document content here", map[string]string{"source": "test"})

// Search
results, _ := pipeline.Search(ctx, "search query", 10)
```

## Conduit Integration

Conduit wraps vecgo through `internal/vecgo/service.go`, providing:

- Configuration via `config.json` vector section
- Automatic workspace file indexing
- HTTP REST API (`/api/vector/*`)
- Integration with MemorySearch tool for hybrid search

See [VECTOR.md](../VECTOR.md) for the complete setup guide.

## Components

### Embedders

| Embedder | Description |
|----------|-------------|
| `embedder.NewTFIDF(dims)` | Local TF-IDF sparse vectors |
| `embedding.NewOpenAIEmbedder(key, model, dims)` | OpenAI API dense vectors |

### Chunkers

| Chunker | Description |
|---------|-------------|
| `chunker.NewMarkdown(maxTokens)` | Splits markdown by headings, preserves context |

### Index

HNSW parameters:
- **M** — Max connections per node (default: 16)
- **EfConstruction** — Build-time search depth (default: 200)
- **EfSearch** — Query-time search depth (default: 50)

## API Reference

### Pipeline Builder

```go
vecgo.NewBuilder().
    WithChunker(chunker).      // Document chunking strategy
    WithEmbedder(embedder).    // Vector embedding provider
    WithHNSW(m, efC, efS).     // HNSW index parameters
    WithSQLite(path).          // Optional persistence
    Build()
```

### Pipeline Operations

```go
// Add or update a document
pipeline.Add(ctx, id, content, metadata)

// Remove a document
pipeline.Remove(ctx, id)

// Search for similar documents
results, _ := pipeline.Search(ctx, query, limit)

// Persistence
pipeline.Save(ctx)
pipeline.Load(ctx)
pipeline.Close()
```

### Search Results

```go
type Result struct {
    ID       string
    Score    float32           // Similarity score (0-1)
    Content  string            // Chunk content
    Metadata map[string]string // Document metadata
}
```

## License

See repository root for license information.
