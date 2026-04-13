# Vecgo (Semantic Vector Search)

Vecgo adds semantic search to Conduit's MemorySearch tool. While FTS5 finds exact keyword matches, vecgo finds conceptually related content — a search for "power setup" returns results mentioning "EG4 18kPV inverter" even without shared keywords.

## Quick Start (Batteries-Included)

If Ollama is running on localhost, vecgo enables automatically. No config changes needed.

```bash
# 1. Install Ollama (https://ollama.com)
curl -fsSL https://ollama.com/install.sh | sh

# 2. Pull the embedding model
ollama pull nomic-embed-text

# 3. Start Conduit — vecgo auto-detects Ollama and enables itself
make run
```

That's it. The gateway probes `http://localhost:11434` at startup. If Ollama responds, vecgo enables with `nomic-embed-text` embeddings (768 dimensions). Workspace `.md` files are indexed automatically.

You'll see this in the startup logs:

```
INFO auto-detected Ollama at localhost host=http://localhost:11434 model=nomic-embed-text
INFO vector search initialized provider=ollama dims=768 path=gateway.vector.db
```

## How It Works

### Search Flow

MemorySearch uses **hybrid mode** by default when vecgo is available:

```
Query: "power setup"
  ├─ FTS5 (keyword): BM25 ranking over workspace .md files
  ├─ Vecgo (semantic): cosine similarity over embedding vectors
  └─ Reciprocal Rank Fusion: merges both lists, normalizes scores to 0-1
```

Results from both searches are deduplicated and merged using RRF. Items appearing in both keyword and semantic results get boosted scores.

### What Gets Indexed

Vecgo indexes all `.md` files in the workspace directory at startup:

- `MEMORY.md` and `memory/*.md` files
- Any other `.md` files in the workspace

Files are chunked by Markdown headings (~500 tokens per chunk), embedded, and stored in an HNSW (Hierarchical Navigable Small World) index backed by SQLite.

The index is incremental — files are SHA-256 hashed and only re-indexed when content changes.

### Search Modes

The MemorySearch tool supports these modes via the `searchMode` parameter:

| Mode | Behavior |
|------|----------|
| `auto` (default) | Hybrid when vecgo available, FTS5 otherwise |
| `hybrid` | Both vector + FTS5 with RRF merge |
| `vector` | Semantic only |
| `fts5` | Keyword only |

### Score Filtering

The `minScore` parameter (default `0.3`) filters low-relevance results. With real embeddings:

- **0.5-0.85**: Strongly related content
- **0.3-0.5**: Tangentially related
- **< 0.3**: Noise (filtered by default)

## Auto-Detection Priority

When `embed_provider` is not set (or set to `"auto"`), the gateway resolves embedders in this order:

1. **`OLLAMA_HOST` env var** — use that Ollama instance
2. **Ollama at localhost:11434** — probe with HTTP GET `/api/version`
3. **`OPENAI_API_KEY` env var** — use OpenAI `text-embedding-3-small`
4. **None available** — vecgo disabled, MemorySearch falls back to FTS5-only

Even without `vector.enabled: true` in config, the gateway will auto-enable vecgo if any embedder is detected. To force-disable vecgo, set `vector.enabled: false` explicitly.

## Configuration

### Zero-Config (Recommended)

Just run Ollama on localhost. No config.json changes needed.

### Explicit Ollama

```json
{
  "vector": {
    "enabled": true,
    "embed_provider": "ollama",
    "ollama": {
      "host": "http://gpu-server:11434",
      "model": "nomic-embed-text"
    }
  }
}
```

### Explicit OpenAI

```json
{
  "vector": {
    "enabled": true,
    "embed_provider": "openai",
    "openai": {
      "api_key": "${OPENAI_API_KEY}",
      "model": "text-embedding-3-small"
    }
  }
}
```

### Full Reference

```json
{
  "vector": {
    "enabled": true,
    "path": "",
    "chunk_size": 500,
    "embed_dims": 0,
    "embed_provider": "auto",
    "ollama": {
      "host": "http://localhost:11434",
      "model": "nomic-embed-text"
    },
    "openai": {
      "api_key": "${OPENAI_API_KEY}",
      "model": "text-embedding-3-small"
    }
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | auto | Explicit enable/disable. Omit to auto-detect |
| `path` | string | `""` | Vector DB path. Empty = derived from gateway DB (e.g., `gateway.vector.db`) |
| `chunk_size` | int | `500` | Max tokens per chunk when splitting documents |
| `embed_dims` | int | `0` | Embedding dimensions. 0 = use embedder default (768 Ollama, 1536 OpenAI) |
| `embed_provider` | string | `"auto"` | `"auto"`, `"ollama"`, `"openai"` |
| `ollama.host` | string | `$OLLAMA_HOST` or `http://localhost:11434` | Ollama API endpoint |
| `ollama.model` | string | `nomic-embed-text` | Ollama embedding model |
| `openai.api_key` | string | `$OPENAI_API_KEY` | OpenAI API key |
| `openai.model` | string | `text-embedding-3-small` | OpenAI embedding model |

## Supported Embedding Models

### Ollama (Local)

| Model | Dimensions | Notes |
|-------|-----------|-------|
| `nomic-embed-text` | 768 | Default. Good quality, fast. ~274MB |
| `mxbai-embed-large` | 1024 | Higher quality, slightly slower. ~670MB |
| `all-minilm` | 384 | Smallest/fastest. Lower quality. ~45MB |

Pull models with `ollama pull <model>`.

### OpenAI (Cloud)

| Model | Dimensions | Notes |
|-------|-----------|-------|
| `text-embedding-3-small` | 1536 | Default. Best cost/quality ratio |
| `text-embedding-3-large` | 3072 | Highest quality. 2x cost |

## Environment Variables

| Variable | Description |
|----------|-------------|
| `OLLAMA_HOST` | Ollama API endpoint (e.g., `http://gpu-server:11434`) |
| `OPENAI_API_KEY` | OpenAI API key for cloud embeddings |

## Storage

Vecgo stores its data in a separate SQLite database:

- **Path**: Derived from gateway DB (e.g., `gateway.db` -> `gateway.vector.db`)
- **Contents**: Document chunks, embedding vectors, HNSW graph state
- **Size**: Typically 5-50MB depending on workspace size and embedding dimensions

The vector DB is independent from the main gateway DB and search DB. Deleting it triggers a full re-index on next startup.

## Troubleshooting

### Vecgo not enabling

Check startup logs for:
- `vector search disabled: no embedding provider available` — no Ollama or OpenAI detected
- `failed to initialize vector search` — embedder found but service failed to start

Verify Ollama is running:
```bash
curl http://localhost:11434/api/version
```

Verify the embedding model is pulled:
```bash
ollama list | grep nomic-embed-text
```

### Poor search quality

- Check that files are being indexed: startup log shows `vector search initialized`
- Try `searchMode: "vector"` to see raw semantic results without FTS5 blending
- Lower `minScore` to `0.1` temporarily to see all results with their scores
- The `[vecgo] MemorySearch:` log line shows score distributions for calibration

### Switching embedding models

If you change the embedding model (e.g., from `nomic-embed-text` to `mxbai-embed-large`), the vector dimensions change. Delete the vector DB to trigger a full re-index:

```bash
rm gateway.vector.db  # or your configured path
```

## See Also

- [Configuration Reference](configuration.md) — Full config reference
- [Brain & REM Sleep](brain.md) — Cognitive memory system
- [Advanced Features](advanced-features.md) — FTS5 search infrastructure
- [Tools Reference](tools-reference.md) — MemorySearch tool documentation
