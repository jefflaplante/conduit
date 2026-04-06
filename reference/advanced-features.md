# Advanced Features Reference

This document covers Conduit's search infrastructure, prompt caching, and context compaction — power-user features that optimize performance and cost.

---

## Table of Contents

- [SearchDB (FTS5 Search Infrastructure)](#searchdb-fts5-search-infrastructure)
- [Prompt Caching (Anthropic)](#prompt-caching-anthropic)
- [Context Compaction](#context-compaction)

---

## SearchDB (FTS5 Search Infrastructure)

**Package:** `internal/searchdb/`

Conduit maintains a dedicated `search.db` SQLite database separate from the main `gateway.db`. This separation allows independent index rebuilds, graceful degradation if search is unavailable, and keeps the core database lean.

### FTS5 Indexes

SearchDB uses SQLite FTS5 (Full-Text Search 5) with Porter stemming and unicode61 tokenization. Three FTS5 virtual tables are created across three migrations:

| Table | Migration | Source | Description |
|-------|-----------|--------|-------------|
| `document_chunks_fts` | v1 | `document_chunks` table | Workspace file content indexed by chunk, with heading. Content-synced via triggers (insert/update/delete). |
| `beads_fts` | v1 | Standalone | Beads issues indexed by ID, title, description, status, type, and owner. |
| `messages_fts` | v2 | Standalone | Session messages indexed by message ID, session key, role, and content. |
| `brain_ltm_fts` | v3 | Standalone | Brain long-term memory entries indexed by key, value, and source. |

### Indexers

| Indexer | Package | Description |
|---------|---------|-------------|
| `BeadsIndexer` | `internal/searchdb/beads_indexer.go` | Parses `.beads/issues.jsonl` and indexes open/closed issues into `beads_fts`. Supports full rebuild and incremental sync. |
| `BrainIndexer` | `internal/searchdb/brain_indexer.go` | Indexes brain LTM entries into `brain_ltm_fts`. Called after brain operations to keep search in sync. |
| `MessageSyncer` | `internal/searchdb/message_sync.go` | Syncs messages from `gateway.db` into `messages_fts`. Tracks sync position for incremental updates. |

### Document Chunking

**Package:** `internal/fts/`

The FTS package handles document chunking before indexing:

- Files are split into chunks by markdown headings or fixed-size boundaries
- Each chunk includes its heading context for better search relevance
- File hashes track changes — only modified files are re-indexed
- BM25 ranking scores search results by relevance

### Database Operations

```go
// Get search statistics
stats, _ := searchDB.GetStats()
// Returns: document_chunks, messages_indexed, beads_indexed, size_bytes, path

// Rebuild corrupted FTS5 indexes
searchDB.RebuildFTS5Indexes()

// Reclaim space
searchDB.Vacuum()
```

### Configuration

SearchDB path is derived automatically from the gateway database path:

| Gateway DB | Search DB |
|------------|-----------|
| `gateway.db` | `gateway.search.db` |
| `config.telegram.db` | `config.telegram.search.db` |

No explicit configuration is needed — SearchDB initializes automatically when the gateway starts.

---

## Prompt Caching (Anthropic)

**Package:** `internal/ai/cache_config.go`, `internal/ai/anthropic.go`

Anthropic's prompt caching allows reuse of previously processed prompt content across API calls, reducing latency and cost. Conduit automatically applies `cache_control` breakpoints to the Anthropic request.

### How It Works

1. **System prompt** — Marked with `cache_control: {type: "ephemeral"}` so the full system prompt is cached between turns
2. **Tool definitions** — The tool definition block is cached since it rarely changes within a session
3. **Conversation history** — Breakpoints are inserted every N messages (configurable) so older history hits cache

### Minimum Token Requirements

Caching requires a minimum number of tokens per cacheable block. These vary by model:

| Model | Minimum Tokens |
|-------|---------------|
| `claude-opus-4-6` | 4,096 |
| `claude-opus-4-5` | 4,096 |
| `claude-sonnet-4-6` | 2,048 |
| `claude-sonnet-4-5` | 1,024 |
| `claude-sonnet-4` | 1,024 |
| `claude-haiku-4-5` | 4,096 |
| `claude-haiku-3.5` | 2,048 |

### Configuration

Under the `ai` key in config JSON:

```json
{
  "ai": {
    "prompt_caching": {
      "enabled": true,
      "extended_ttl": false,
      "cache_tools": true,
      "cache_system": true,
      "cache_history": true,
      "history_breakpoint_interval": 15
    }
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `true` | Master switch for prompt caching |
| `extended_ttl` | bool | `false` | Use 1-hour TTL (2x write cost) vs 5-minute default |
| `cache_tools` | bool | `true` | Cache tool definitions |
| `cache_system` | bool | `true` | Cache system prompt |
| `cache_history` | bool | `true` | Cache conversation history |
| `history_breakpoint_interval` | int | `15` | Messages between history cache breakpoints |

### Cost Impact

- **Cache write:** 25% more than base input token price (or 2x with extended TTL)
- **Cache read:** 90% cheaper than base input token price
- **Break-even:** After ~2 cache reads per write, caching saves money

---

## Context Compaction

**Package:** `internal/ai/compaction.go`

When a session's context window usage exceeds a configurable threshold, Conduit automatically summarizes older messages and replaces them with a compact summary. This allows long-running sessions to continue indefinitely without hitting context limits.

### How It Works

1. **Trigger check:** After each AI response, the prompt token count is compared against the model's context window. If usage exceeds the threshold (default 70%), compaction begins.
2. **Message split:** Messages are divided into "history" (older, to be summarized) and "recent" (kept as-is, default last 10 messages).
3. **Summarization:** The history is sent to a small, fast model (default Haiku) with instructions to preserve key decisions, active tasks, user preferences, technical context, and unresolved questions.
4. **Replacement:** The original messages are cleared and replaced with `[Context Summary from N previous messages]` followed by the summary, then the recent messages.
5. **Metadata:** Compaction timestamp and original message count are stored in session context for auditing.

### Configuration

Under the `ai` key in config JSON:

```json
{
  "ai": {
    "compaction": {
      "enabled": true,
      "threshold": 0.70,
      "model": "claude-haiku-4-5-20251001",
      "recent_messages_to_keep": 10
    }
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable automatic context compaction |
| `threshold` | float | `0.70` | Context window usage fraction that triggers compaction (0.0-1.0) |
| `model` | string | `claude-haiku-4-5-20251001` | Model for summarization (smaller is faster and cheaper) |
| `recent_messages_to_keep` | int | `10` | Number of recent messages preserved without summarization |

### Context Window Sizes

Compaction uses model-specific context windows to calculate usage:

| Model Family | Context Window |
|-------------|---------------|
| Opus | 200K tokens |
| Sonnet | 200K tokens |
| Haiku | 200K tokens |
| Local/Ollama | Varies by model config |

### Tips

- **Threshold tuning:** 0.70 is conservative. For sessions with heavy tool use, 0.60 may be better to leave room for tool results.
- **Recent messages:** Increase `recent_messages_to_keep` if the AI loses track of recent context after compaction.
- **Summarization model:** Haiku is ideal — fast, cheap, and good at summarization. Using a larger model adds cost without meaningful quality improvement.
