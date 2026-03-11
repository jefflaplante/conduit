# VECTOR.md — Vector Search Setup Guide

Complete guide to configuring and using semantic vector search in Conduit.

---

## Table of Contents

- [Introduction](#introduction)
- [Quick Start](#quick-start)
- [Embedding Providers](#embedding-providers)
- [Configuration Reference](#configuration-reference)
- [What Gets Indexed](#what-gets-indexed)
- [Using Vector Search](#using-vector-search)
- [Verification & Testing](#verification--testing)
- [Troubleshooting](#troubleshooting)
- [Performance & Scaling](#performance--scaling)

---

## Introduction

### What is Vector Search?

Vector search (also called semantic search or embedding search) finds documents by meaning rather than exact keywords. Instead of matching "database configuration" only to documents containing those words, it finds documents about "SQLite settings", "DB setup", or "storage options" because they're semantically similar.

**Keyword matching (FTS5):** "How do I deploy?" matches documents with "deploy"
**Semantic matching (Vector):** "How do I deploy?" matches documents about "deployment", "shipping to production", "release process"

### When to Use It

Vector search excels at:
- **Memory recall** — Finding relevant past context even when you don't remember exact words
- **Concept search** — Finding related ideas across different terminology
- **Natural language queries** — Searching the way you think, not the way docs are written

### How It Integrates with Conduit

Vector search enhances the MemorySearch tool with hybrid search capabilities:

1. **Hybrid mode** (default when enabled): Runs both vector and FTS5 searches in parallel, merges results using Reciprocal Rank Fusion (RRF)
2. **Vector mode**: Semantic search only
3. **FTS5 mode**: Keyword search only
4. **Auto mode**: Uses hybrid when vector is available, falls back to FTS5

The vector service also exposes HTTP REST endpoints for direct programmatic access.

---

## Quick Start

### Minimal Config (TF-IDF, Zero Dependencies)

Add this to your `config.json`:

```json
{
  "vector": {
    "enabled": true
  }
}
```

That's it. This enables vector search using the local TF-IDF embedder with sensible defaults:
- **Embedder:** TF-IDF (no API keys, offline, free)
- **Database:** Auto-derived from gateway DB (e.g., `gateway.db` → `gateway.vector.db`)
- **Dimensions:** 4096
- **Chunk size:** 500 tokens

### Verify It's Working

1. Start the gateway:
   ```bash
   make run
   ```

2. Check vector status:
   ```bash
   curl http://localhost:18789/api/vector/status
   ```

   Expected response:
   ```json
   {"enabled": true}
   ```

3. Test a search (after some .md files are indexed):
   ```bash
   curl -X POST http://localhost:18789/api/vector/search \
     -H "Content-Type: application/json" \
     -d '{"query": "configuration settings", "limit": 5}'
   ```

---

## Embedding Providers

### TF-IDF (Local/Default)

**How it works:** Term Frequency-Inverse Document Frequency creates sparse vectors based on word importance. Words that appear frequently in one document but rarely across all documents get higher weights.

| Pros | Cons |
|------|------|
| No API keys required | No semantic understanding |
| Works offline | Keyword-based matching only |
| Fast (local computation) | Synonyms not recognized |
| Zero cost | Requires exact or similar terms |

**Configuration:**

```json
{
  "vector": {
    "enabled": true,
    "embed_provider": "tfidf",
    "embed_dims": 4096
  }
}
```

**Best for:** Simple setups, offline use, cost-sensitive deployments, when keyword matching is sufficient.

### OpenAI Embeddings

**How it works:** Neural network models encode text into dense vectors that capture semantic meaning. Similar concepts cluster together in vector space regardless of exact wording.

| Pros | Cons |
|------|------|
| True semantic understanding | Requires OpenAI API key |
| Handles synonyms and paraphrases | Requires network connectivity |
| Context-aware matching | Has usage costs |
| Higher quality results | Slightly slower (API call) |

**Configuration:**

```json
{
  "vector": {
    "enabled": true,
    "embed_provider": "openai",
    "embed_dims": 1536,
    "openai": {
      "api_key": "${OPENAI_API_KEY}",
      "model": "text-embedding-3-small"
    }
  }
}
```

**Environment variable:** Set `OPENAI_API_KEY` in your environment or secrets file:

```bash
export OPENAI_API_KEY=sk-...
```

Or in your secrets file (`secrets_file` config option):
```
OPENAI_API_KEY=sk-...
```

**Available models:**

| Model | Dimensions | Use Case |
|-------|------------|----------|
| `text-embedding-3-small` | 1536 | Default, good balance of quality/cost |
| `text-embedding-3-large` | 3072 | Higher quality, higher cost |
| `text-embedding-ada-002` | 1536 | Legacy model |

**Best for:** Production deployments needing high-quality semantic search, natural language queries, concept discovery.

---

## Configuration Reference

### All Vector Config Fields

```json
{
  "vector": {
    "enabled": false,
    "path": "./gateway.vector.db",
    "chunk_size": 500,
    "embed_dims": 4096,
    "embed_provider": "tfidf",
    "openai": {
      "api_key": "${OPENAI_API_KEY}",
      "model": "text-embedding-3-small"
    }
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable vector search service |
| `path` | string | derived | Path to vector database file |
| `chunk_size` | int | `500` | Maximum tokens per document chunk |
| `embed_dims` | int | `4096` | Embedding vector dimensions |
| `embed_provider` | string | `"tfidf"` | Provider: `"tfidf"` or `"openai"` |
| `openai.api_key` | string | — | OpenAI API key (supports `${ENV_VAR}`) |
| `openai.model` | string | `"text-embedding-3-small"` | OpenAI embedding model |

### Path Derivation Logic

If `path` is empty, it's derived from the gateway database path:

| Gateway DB Path | Vector DB Path |
|-----------------|----------------|
| `gateway.db` | `gateway.vector.db` |
| `config.telegram.db` | `config.telegram.vector.db` |
| `./data/conduit.db` | `./data/conduit.vector.db` |

### Embedding Dimensions

Match `embed_dims` to your provider:

| Provider | Recommended `embed_dims` |
|----------|--------------------------|
| TF-IDF | 4096 (default) |
| OpenAI `text-embedding-3-small` | 1536 |
| OpenAI `text-embedding-3-large` | 3072 |

### HNSW Index Parameters

The underlying vecgo library uses HNSW (Hierarchical Navigable Small World) for approximate nearest neighbor search. Default parameters are tuned for typical workloads:

| Parameter | Default | Effect |
|-----------|---------|--------|
| M | 16 | Max connections per node. Higher = better recall, more memory |
| EfConstruction | 200 | Build-time search depth. Higher = better index quality, slower indexing |
| EfSearch | 50 | Query-time search depth. Higher = better recall, slower queries |

These are currently not exposed in config but use sensible defaults.

### Chunk Size Recommendations

| Use Case | Chunk Size | Rationale |
|----------|------------|-----------|
| Short notes, logs | 200-300 | Preserve granularity |
| Documentation (default) | 500 | Balance context vs precision |
| Long-form content | 800-1000 | More context per chunk |

Larger chunks provide more context but may dilute relevance for specific queries.

---

## What Gets Indexed

### Automatic Workspace Indexing

When vector search is enabled, the indexer automatically scans and indexes:

**Indexed files:**
- All `*.md` files in `workspace.context_dir`
- Recursive — includes subdirectories like `memory/`

**Typical indexed files:**
- `MEMORY.md` — Long-term memory
- `memory/*.md` — Daily memory logs
- `SOUL.md`, `USER.md`, `AGENTS.md` — Context files
- Any other markdown in the workspace

### Metadata Attached to Documents

Each indexed document includes metadata:

```json
{
  "source": "workspace",
  "path": "memory/2024-01-15.md",
  "title": "2024-01-15",
  "type": "memory"
}
```

The `type: "memory"` tag is added for `MEMORY.md` and files in `memory/`.

### Change Detection

The indexer uses SHA-256 content hashing to detect changes:

1. On startup, scans all .md files
2. Computes hash of each file's content
3. Only re-indexes files whose hash has changed
4. Removes entries for deleted files

This makes re-indexing efficient even with many files.

### Indexing Schedule

- **Initial:** Full scan on gateway startup
- **Periodic:** Configurable poll interval (if enabled)
- **On-demand:** Via HTTP API

---

## Using Vector Search

### Via MemorySearch Tool

The MemorySearch tool automatically uses vector search when available:

**Hybrid search (default with vector enabled):**
```json
{
  "query": "deployment procedures",
  "searchMode": "auto",
  "maxResults": 10
}
```

**Force vector-only search:**
```json
{
  "query": "how to configure the database",
  "searchMode": "vector",
  "maxResults": 10
}
```

**Force FTS5-only search:**
```json
{
  "query": "SQLite WAL mode",
  "searchMode": "fts5",
  "maxResults": 10
}
```

**Search modes:**

| Mode | Behavior |
|------|----------|
| `auto` | Hybrid when vector available, else FTS5, else grep |
| `hybrid` | Vector + FTS5 merged with RRF |
| `vector` | Semantic search only |
| `fts5` | Keyword search only |

### Via HTTP API

#### Search Documents

```bash
POST /api/vector/search
Content-Type: application/json

{
  "query": "search text",
  "limit": 10
}
```

**Response:**
```json
{
  "results": [
    {
      "id": "memory/2024-01-15.md",
      "score": 0.85,
      "content": "Deployed new version to production...",
      "metadata": {
        "source": "workspace",
        "path": "memory/2024-01-15.md",
        "type": "memory"
      }
    }
  ]
}
```

#### Index a Document

```bash
POST /api/vector/index
Content-Type: application/json

{
  "id": "custom-doc-1",
  "content": "Document content to index",
  "metadata": {
    "source": "manual",
    "category": "notes"
  }
}
```

**Response:**
```json
{
  "status": "indexed"
}
```

#### Delete a Document

```bash
DELETE /api/vector/delete
Content-Type: application/json

{
  "id": "custom-doc-1"
}
```

**Response:**
```json
{
  "status": "deleted"
}
```

#### Check Status

```bash
GET /api/vector/status
```

**Response:**
```json
{
  "enabled": true
}
```

---

## Verification & Testing

### 1. Check Vector Status

```bash
curl http://localhost:18789/api/vector/status
```

Expected: `{"enabled": true}`

If you see `{"enabled": false}`, check:
- `vector.enabled` is `true` in config
- Gateway was restarted after config change

### 2. Verify Indexing

Check gateway logs on startup for indexer output:
```
vecgo indexer: initial scan indexed 15 files (0 skipped, 0 removed) in 234ms
```

### 3. Test Search Query

```bash
curl -X POST http://localhost:18789/api/vector/search \
  -H "Content-Type: application/json" \
  -d '{"query": "test query", "limit": 3}'
```

If you get empty results:
- Verify workspace has .md files
- Check that files have content (not empty)
- For TF-IDF, ensure query terms appear in documents

### 4. Test Semantic Search (OpenAI only)

With OpenAI embeddings, test semantic understanding:

```bash
# Index a document about deployments
curl -X POST http://localhost:18789/api/vector/index \
  -H "Content-Type: application/json" \
  -d '{
    "id": "test-deploy",
    "content": "The release process involves building artifacts, running tests, and pushing to production servers."
  }'

# Search with different terminology
curl -X POST http://localhost:18789/api/vector/search \
  -H "Content-Type: application/json" \
  -d '{"query": "how to deploy", "limit": 3}'
```

With OpenAI, this should find the "release process" document even though "deploy" doesn't appear in it.

---

## Troubleshooting

### "vector search not enabled"

**Cause:** Vector service is disabled or failed to initialize.

**Fix:**
1. Ensure `vector.enabled: true` in config
2. Restart the gateway
3. Check startup logs for initialization errors

### Empty Search Results

**Causes and fixes:**

| Cause | Fix |
|-------|-----|
| No .md files in workspace | Add markdown files to `workspace.context_dir` |
| Files are empty | Add content to files |
| Query doesn't match (TF-IDF) | Use terms that appear in documents |
| Index not loaded | Check for "vecgo: loading persisted state" in logs |

### OpenAI API Errors

**401 Unauthorized:**
- Check `OPENAI_API_KEY` is set correctly
- Verify the key is valid and not expired

**429 Too Many Requests:**
- You've hit rate limits
- The embedder has automatic retry with exponential backoff (1s, 2s, 4s)
- Wait and retry, or reduce indexing batch size

**400 Bad Request:**
- Check `embed_dims` matches the model (1536 for `text-embedding-3-small`)

### Performance Issues

**Slow searches:**
- Reduce `limit` parameter
- Consider using FTS5 for simple keyword queries
- Check if HNSW index is too large for memory

**Slow indexing:**
- Reduce chunk size to index fewer chunks per document
- With OpenAI, indexing is network-bound; expect ~1-2 seconds per document

**High memory usage:**
- HNSW indexes are memory-resident
- Consider reducing corpus size or using TF-IDF (sparser vectors)

### Database Errors

**"failed to open database":**
- Check `vector.path` is writable
- Ensure directory exists
- Check disk space

**"database is locked":**
- Another process may be using the vector database
- SQLite busy timeout is 5 seconds by default

---

## Performance & Scaling

### Memory Usage

| Factor | Impact |
|--------|--------|
| Embedding dimensions | Higher dims = more memory per vector |
| Number of chunks | Linear scaling with corpus size |
| HNSW M parameter | Higher M = more memory for graph structure |

**Rough estimates (TF-IDF, 4096 dims):**
- 100 documents (~500 chunks): ~50MB
- 1000 documents (~5000 chunks): ~500MB

**OpenAI (1536 dims) uses less memory** due to lower dimensionality.

### Query Speed

| Corpus Size | Expected Latency |
|-------------|------------------|
| 100 chunks | <10ms |
| 1000 chunks | <50ms |
| 10000 chunks | <200ms |

HNSW provides sublinear query time, so doubling corpus size doesn't double query time.

### When to Use TF-IDF vs OpenAI

**Use TF-IDF when:**
- You need offline/air-gapped operation
- Cost is a primary concern
- Your queries use consistent terminology matching your documents
- You're doing keyword-style searches

**Use OpenAI when:**
- You need semantic understanding (synonyms, paraphrases)
- Users search with natural language
- Documents use varied terminology
- Query quality matters more than cost

### Hybrid Search Trade-offs

Hybrid mode (vector + FTS5) provides the best of both worlds but has overhead:

| Mode | Latency | Quality |
|------|---------|---------|
| FTS5 only | Fastest | Good for exact matches |
| Vector only | Medium | Good for semantic queries |
| Hybrid | Slowest (2x searches) | Best overall |

For most use cases, hybrid mode's quality improvement is worth the latency cost.

---

## Related Documentation

- [CONFIG.md](CONFIG.md) — Full configuration reference including vector section
- [vecgo/README.md](vecgo/README.md) — VecGo library documentation
