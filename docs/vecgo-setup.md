# Vecgo Episodic Memory — Setup Guide

Vecgo is Conduit's vector search subsystem, enabling semantic search across workspace memory files and session history. This guide covers setup, configuration, and verification.

## Architecture Overview

```
┌─────────────┐     ┌──────────────┐     ┌─────────────┐
│ Memory Files │────▶│  Indexer     │────▶│   HNSW      │
│ (workspace/) │     │  (file watch)│     │   Index     │
└─────────────┘     └──────────────┘     └──────┬──────┘
                                               │
┌─────────────┐     ┌──────────────┐     ┌──────▼──────┐
│ Session      │────▶│  Session     │────▶│   HNSW      │
│ History      │     │  Indexer     │     │   Index     │
│ (SQLite)     │     │  (on close)  │     └──────┬──────┘
└─────────────┘     └──────────────┘            │
                                               │
                    ┌──────────────┐     ┌──────▼──────┐
                    │ MemorySearch  │◀────│  Search     │
                    │ Tool (hybrid) │     │  Pipeline   │
                    └──────────────┘     └─────────────┘
                                               ▲
                    ┌──────────────┐            │
                    │  Embedder    │────────────┘
                    │ (Ollama/     │
                    │  OpenAI)     │
                    └──────────────┘
```

**Key components:**

| Component | Location | Purpose |
|-----------|----------|---------|
| vecgo library | `vecgo/` | Standalone Go library: Pipeline, HNSW index, chunkers, embedder interface |
| Vecgo service | `internal/vecgo/service.go` | Conduit wrapper — builds the pipeline, exposes Search/Index/Remove |
| File indexer | `internal/vecgo/indexer.go` | Watches workspace dir, content-hash tracking, incremental re-index |
| OpenAI embedder | `internal/vecgo/embedding/openai.go` | OpenAI API adapter (`text-embedding-3-small`, 1536 dims) |
| TF-IDF embedder | `vecgo/embedder/tfidf.go` | Local sparse vectors (needs trained corpus — not recommended) |
| MemorySearch tool | `core/tools/memory_search.go` | Hybrid FTS5 + vector search exposed as a tool |

## Prerequisites

You need an embedding model. Vecgo supports two options:

### Option A: Ollama (local, free, recommended for self-hosting)

```bash
# Install Ollama
curl -fsSL https://ollama.com/install.sh | sh

# Pull a small embedding model
ollama pull nomic-embed-text

# Verify
ollama list
# Should show: nomic-embed-text

# Ollama runs on http://localhost:11434 by default
```

### Option B: OpenAI API (cloud, paid)

```bash
# Set your API key
export OPENAI_API_KEY="sk-..."

# Verify
curl -s https://api.openai.com/v1/embeddings \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"text-embedding-3-small","input":"test"}' | jq .data[0].embedding | head -c 100
```

## Configuration

Add the `vecgo` section to your gateway config:

### Using Ollama

```json
{
  "vecgo": {
    "enabled": true,
    "embedder": "ollama",
    "ollama_host": "http://localhost:11434",
    "ollama_model": "nomic-embed-text",
    "dimensions": 768,
    "db_path": "~/.conduit/vecgo.db",
    "chunk_size": 500,
    "session_indexing": {
      "enabled": true,
      "max_age_days": 90,
      "backfill_limit": 100
    },
    "hnsw": {
      "m": 16,
      "ef_construction": 200,
      "ef_search": 50
    },
    "max_corpus_size": 50000
  }
}
```

### Using OpenAI

```json
{
  "vecgo": {
    "enabled": true,
    "embedder": "openai",
    "openai_model": "text-embedding-3-small",
    "dimensions": 1536,
    "db_path": "~/.conduit/vecgo.db",
    "chunk_size": 500,
    "session_indexing": {
      "enabled": true,
      "max_age_days": 90,
      "backfill_limit": 100
    },
    "hnsw": {
      "m": 16,
      "ef_construction": 200,
      "ef_search": 50
    },
    "max_corpus_size": 50000
  }
}
```

### Config Reference

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Master switch for vecgo |
| `embedder` | string | — | `"ollama"` or `"openai"` (required if enabled) |
| `ollama_host` | string | `http://localhost:11434` | Ollama server URL |
| `ollama_model` | string | `nomic-embed-text` | Ollama embedding model |
| `openai_model` | string | `text-embedding-3-small` | OpenAI embedding model |
| `dimensions` | int | varies | Vector dimensions (768 for nomic, 1536 for OpenAI) |
| `db_path` | string | — | SQLite persistence file (empty = in-memory only) |
| `chunk_size` | int | 500 | Max tokens per chunk for static files |
| `session_indexing.enabled` | bool | `false` | Enable session history indexing |
| `session_indexing.max_age_days` | int | 90 | Don't index sessions older than this |
| `session_indexing.backfill_limit` | int | 100 | Max sessions to backfill on first boot |
| `hnsw.m` | int | 16 | HNSW max connections per node |
| `hnsw.ef_construction` | int | 200 | HNSW build-time search depth |
| `hnsw.ef_search` | int | 50 | HNSW query-time search depth |
| `max_corpus_size` | int | 50000 | Prune oldest vectors when exceeded |

### Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| `OLLAMA_HOST` | No | Ollama server URL (overrides config `ollama_host`) |
| `OPENAI_API_KEY` | Yes (OpenAI) | OpenAI API key |

## Startup Behavior

When Conduit starts with `vecgo.enabled: true`:

1. **Embedder init** — Connects to Ollama or OpenAI. If unreachable, vecgo disables itself with a warning log. Gateway still starts.
2. **Load persisted index** — If `db_path` is set, loads the HNSW graph and vectors from SQLite.
3. **File indexer starts** — Walks the workspace directory, content-hashes all files, indexes new/changed ones.
4. **Session indexer starts** — If `session_indexing.enabled`, scans session store for unindexed sessions within `max_age_days`.
5. **Periodic scan** — File indexer re-scans every 5 minutes (configurable) for changes.

Watch for these log lines to confirm:

```
level=INFO  msg="vecgo: starting with ollama embedder (nomic-embed-text, 768d)"
level=INFO  msg="vecgo: loaded 1234 vectors from /path/to/vecgo.db"
level=INFO  msg="vecgo indexer: initial scan complete: 12 indexed, 3 skipped"
level=INFO  msg="vecgo session indexer: backfilled 45 sessions"
```

## Verification

### Check vecgo is running

```bash
# Check gateway logs
journalctl -u conduit | grep vecgo

# Should show:
# vecgo: starting with <embedder>
# vecgo: loaded <N> vectors
# vecgo indexer: initial scan complete
```

### Test vector search via MemorySearch

From a session, use the MemorySearch tool:

```
MemorySearch(query="solar panel discussion", searchMode="hybrid")
```

Results with `source: "vector"` confirm vecgo is contributing. If all results come from `source: "fts5"`, the vector index is empty or disabled.

### Inspect the index

```bash
# SQLite index stats
sqlite3 ~/.conduit/vecgo.db "SELECT COUNT(*) FROM vectors;"

# HNSW graph stats (check log on startup)
journalctl -u conduit | grep "vecgo.*loaded"
```

### Test the embedder directly

```bash
# Ollama
curl http://localhost:11434/api/embeddings \
  -d '{"model":"nomic-embed-text","prompt":"test query"}' | jq .embedding | wc -c
# Should return ~3000+ chars (768 floats)

# OpenAI
curl -s https://api.openai.com/v1/embeddings \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"text-embedding-3-small","input":"test query"}' | jq .data[0].embedding | wc -c
# Should return ~6000+ chars (1536 floats)
```

## How MemorySearch Hybrid Mode Works

The MemorySearch tool has three search modes:

| Mode | Behavior |
|------|----------|
| `fts5` | Keyword-only search via SQLite FTS5. Always available. |
| `vector` | Semantic-only search via vecgo HNSW. Requires vecgo enabled. |
| `auto` (default) | Tries both, merges results by relevance score. Falls back to FTS5-only if vecgo is disabled. |

**Scoring:**
- FTS5 returns BM25 scores (higher = better match)
- Vector returns cosine similarity (0.0–1.0, higher = better match)
- In hybrid mode, scores are normalized to [0,1] and results are ranked by combined score
- `minScore` parameter filters out low-relevance results (default: 0.1)

**Fallback:** If vecgo is disabled or the embedder is unreachable, `auto` mode silently falls back to FTS5-only. No errors, no broken searches.

## Session Backfill

To index existing session history after first setup:

```bash
# Trigger via gateway API (planned — see conduit-2qof.3)
curl http://localhost:8080/api/vector/backfill?limit=100

# Or restart Conduit — the session indexer auto-backfills up to
# session_indexing.backfill_limit sessions on startup
sudo systemctl restart conduit
```

## Troubleshooting

### "vecgo: embedder not configured, vector search disabled"

Vecgo is enabled but no embedder is reachable. Check:
- Is Ollama running? (`curl http://localhost:11434/api/tags`)
- Is `OPENAI_API_KEY` set? (`echo $OPENAI_API_KEY`)
- Is the config `embedder` field correct? (`"ollama"` or `"openai"`)

### "vecgo: loading persisted state: file is not a database"

The SQLite persistence file is corrupted. Delete it and let vecgo re-index:
```bash
rm ~/.conduit/vecgo.db
sudo systemctl restart conduit
```

### Vector search returns no results

1. Check the index has vectors: `sqlite3 ~/.conduit/vecgo.db "SELECT COUNT(*) FROM vectors;"`
2. If 0: the indexer hasn't run or the workspace is empty. Check logs for `vecgo indexer: initial scan`.
3. If vectors exist but no results: your query might be too niche. Try `minScore=0.0` to see all results.
4. Check the embedder is responding: see "Test the embedder directly" above.

### Ollama is slow

Embedding large batches can take seconds. The indexer processes files sequentially to avoid overloading Ollama. For faster indexing:
- Use a smaller model (`nomic-embed-text` is already small)
- Increase `chunk_size` to reduce the number of chunks
- Consider `text-embedding-3-small` via OpenAI if latency is critical

### Corpus is too large

With session indexing enabled, the corpus grows with every session. Watch for:
```
level=WARN msg="vecgo: corpus size (52000) exceeds max_corpus_size (50000), pruning oldest 2000 vectors"
```

Adjust `max_corpus_size` in config. Pruning removes the oldest session chunks first, keeping static memory files intact.

## Embedder Comparison

| Feature | Ollama (nomic-embed-text) | OpenAI (text-embedding-3-small) |
|---------|--------------------------|--------------------------------|
| Cost | Free (local) | ~$0.02/1M tokens |
| Latency | 50-200ms | 200-500ms |
| Dimensions | 768 | 1536 |
| Quality | Good | Very good |
| Privacy | Fully local | Data sent to OpenAI |
| Requirements | Ollama installed, ~500MB RAM | API key, internet |

**Recommendation for self-hosted Conduit:** Start with Ollama. It's free, private, and good enough for episodic memory. Switch to OpenAI if semantic quality isn't meeting expectations.

## Implementation Status

See epic `conduit-2qof` in the issue tracker for the full rework plan:

- `conduit-2qof.1` — Swap TF-IDF for real embedding model (pluggable embedder)
- `conduit-2qof.2` — Add episode-aware chunker for session history
- `conduit-2qof.3` — Build session-to-vector ingestion pipeline
- `conduit-2qof.4` — Wire vecgo into MemorySearch hybrid mode properly
- `conduit-2qof.5` — Add vector corpus size management and persistence tuning
- `conduit-2qof.6` — Audit and clean up vecgo dead code and HTTP endpoints
- `conduit-2qof.7` — Write vecgo setup and configuration guide (this doc)
