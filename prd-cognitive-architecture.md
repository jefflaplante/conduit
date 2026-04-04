---
title: "PRD: Jules Cognitive Architecture"
status: draft
date: 2026-04-04
author: Jules (with Jeff)
tags: [conduit, architecture, memory, prd]
---

# PRD: Jules Cognitive Architecture

## Problem Statement

Jules wakes up every session with amnesia. The current workaround — reading markdown files into context — works but is expensive, slow, and fragile. Key problems:

1. **Redundant file reads.** The same files get read multiple times per session as facts scroll out of context.
2. **No caching layer.** Once a fact is extracted from a file, there's nowhere to store it except the context window itself, which is finite and managed by the LLM provider.
3. **No tiered storage.** A throwaway calculation and a core identity fact occupy the same space (context window) with the same lifetime.
4. **No structured recall.** Finding "what was solar production today?" requires knowing which file to read, or running a search that may return noisy results.
5. **Session boundary is a cliff.** Everything learned in a session is lost unless manually written to a file.

## Vision

A cognitive architecture that mirrors how human memory works: tiered storage with different lifetimes, automatic promotion/demotion between tiers, unified search across all tiers, and a boot sequence that rapidly reconstructs "who I am" without re-reading entire files.

## Architecture Overview

### Memory Tiers

```
+-------------------------------------------------------------------+
|                    JULES COGNITIVE ARCHITECTURE                    |
+-------------------------------------------------------------------+
|                                                                   |
|  TIER 0: IDENTITY (Immutable per session)                        |
|  Source: SOUL.md, USER.md, AGENTS.md                              |
|  Loaded: System prompt injection (existing)                       |
|  Lifetime: Session                                                |
|  Storage: None needed — already in prompt                         |
|                                                                   |
+-------------------------------------------------------------------+
|                                                                   |
|  TIER 1: LONG-TERM MEMORY (LTM)                                  |
|  Source: Populated from MEMORY.md + reference files at boot       |
|  Lifetime: Persists across sessions (backed to disk)              |
|  Access: O(1) key lookup, prefix scan, search                    |
|  Examples: "jeff.birthday=Oct 5", "solar.panel_count=30"          |
|  Eviction: Manual or consolidation sweep                          |
|                                                                   |
+-------------------------------------------------------------------+
|                                                                   |
|  TIER 2: WORKING MEMORY (WM)                                     |
|  Source: Facts extracted during this session                      |
|  Lifetime: Session-scoped, salience-scored                        |
|  Access: O(1) key lookup, prefix scan, search                    |
|  Examples: "solar.today=4.2kWh", "jeff.topic=federation"          |
|  Eviction: Decay on non-access, or session end                    |
|  Promotion: High-salience keys offered for LTM at consolidation  |
|                                                                   |
+-------------------------------------------------------------------+
|                                                                   |
|  TIER 3: SCRATCHPAD (Stack)                                       |
|  Source: Intermediate calculations, temporary values              |
|  Lifetime: Seconds to minutes, auto-evicts                        |
|  Access: Push/pop (LIFO), peek                                   |
|  Examples: "127.5" (intermediate calc), temp API response ID      |
|  Eviction: Pop consumes, or TTL auto-evict (default 60s)         |
|                                                                   |
+-------------------------------------------------------------------+
|                                                                   |
|  CROSS-TIER: UNIFIED SEARCH                                       |
|  Searches all tiers + existing MemorySearch (files, sessions)    |
|  Results ranked by: tier priority + salience + recency            |
|  Integrates with existing FTS5/vector infrastructure              |
|                                                                   |
+-------------------------------------------------------------------+
```

### Boot Sequence (Phases)

| Phase | Action | Current | Proposed |
|-------|--------|---------|----------|
| 1 | Load identity | System prompt injects SOUL/USER/AGENTS | No change |
| 2 | Load context files | Read MEMORY.md, daily logs | No change (but cache extracted facts into WM) |
| 3 | Demand-load references | Manual file reads | Same, but cache into WM on read |
| 4 | Hydrate LTM | N/A | Load persisted LTM from disk at gateway start |
| 5 | Initialize WM | N/A | Empty map, ready for session |
| 6 | Initialize scratchpad | N/A | Empty stack, ready for session |
| 7 | Search index ready | MemorySearch available | Add Brain tiers to search scope |

**Key insight:** Phases 1-3 are unchanged. The Brain tool is additive — it doesn't replace the file system, it caches what's extracted from it.

## Tool Interface

One tool — `Brain` — with actions that span all tiers.

### Actions

```
Brain(action="store", key="solar.today", value="4.2kWh", tier="working")
Brain(action="store", key="jeff.birthday", value="Oct 5", tier="longterm")
Brain(action="get", key="solar.today")                    # returns value + tier + metadata
Brain(action="recall", query="solar production")           # searches all tiers by relevance
Brain(action="list", prefix="solar.")                      # list keys matching prefix, any tier
Brain(action="delete", key="solar.today")                  # explicit removal
Brain(action="push", value="127.5")                        # scratchpad push
Brain(action="pop")                                        # scratchpad pop (returns + removes)
Brain(action="peek")                                       # scratchpad peek (returns, keeps)
Brain(action="promote", key="solar.today", to="longterm")  # move WM key to LTM
Brain(action="consolidate")                                # session-end sweep
Brain(action="status")                                     # tier counts, hottest keys, health
```

### Value Schema

Each stored entry carries metadata:

```go
type Entry struct {
    Key        string      `json:"key"`
    Value      any         `json:"value"`
    Tier       Tier        `json:"tier"`        // "longterm", "working", "scratch"
    CreatedAt  time.Time   `json:"created_at"`
    AccessedAt time.Time   `json:"accessed_at"` // last read time
    AccessCount int        `json:"access_count"`
    Salience   float64     `json:"salience"`    // computed: f(access_count, recency, age)
    TTL        *time.Duration `json:"ttl,omitempty"` // optional explicit TTL
    Source     string      `json:"source,omitempty"` // "file:MEMORY.md", "session", "user"
}
```

### Salience Scoring

```
salience = (access_count * 0.4) + (recency_score * 0.4) + (tier_weight * 0.2)

where:
  recency_score = 1.0 / (1.0 + hours_since_last_access)
  tier_weight   = longterm: 0.8, working: 0.5, scratch: 0.1
```

Keys with salience below a threshold (configurable, default 0.1) are candidates for eviction during consolidation.

### Consolidation (Session End)

When `Brain(action="consolidate")` is called (or triggered by handoff skill):

1. **Scan working memory** for keys with high salience (> 0.6)
2. **Suggest promotions** to LTM — return list for AI confirmation or auto-promote if `auto_promote=true`
3. **Evict low-salience WM keys** (< 0.1)
4. **Flush LTM to disk** — write the persistent store
5. **Clear scratchpad** entirely
6. **Return summary** — promoted count, evicted count, LTM size

## Storage Backend Decision: SQLite vs JSON

### The Question

LTM needs to persist across sessions (and gateway restarts). Two options:

### Option A: JSON File

```json
{
  "version": 1,
  "entries": {
    "jeff.birthday": {
      "value": "Oct 5",
      "created_at": "2026-04-04T12:00:00Z",
      "accessed_at": "2026-04-04T18:30:00Z",
      "access_count": 12,
      "source": "file:MEMORY.md"
    }
  }
}
```

**Pros:**
- Human-readable, easy to debug/inspect
- Trivial to implement (json.Marshal/Unmarshal)
- No dependencies (already have encoding/json)
- Easy to back up, version control, or copy to NAS
- Atomic write with temp-file + rename pattern

**Cons:**
- Full file rewrite on every flush (O(n) where n = total entries)
- No query capability beyond in-memory map scan
- No concurrent access safety at the file level (only one process)
- Grows linearly — at 10K entries, flush becomes measurable

### Option B: SQLite (New Table in Existing DB)

```sql
CREATE TABLE brain_ltm (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,         -- JSON-encoded
    tier TEXT NOT NULL DEFAULT 'longterm',
    created_at DATETIME NOT NULL,
    accessed_at DATETIME NOT NULL,
    access_count INTEGER DEFAULT 0,
    salience REAL DEFAULT 0.0,
    source TEXT,
    ttl_seconds INTEGER          -- NULL = no expiry
);

CREATE INDEX idx_brain_ltm_prefix ON brain_ltm(key);
CREATE INDEX idx_brain_ltm_salience ON brain_ltm(salience);
```

**Pros:**
- Already have SQLite infrastructure (modernc.org/sqlite, migrations, ConfigureDatabase)
- Incremental writes — UPDATE one row, not rewrite entire file
- Query capability: `WHERE key LIKE 'solar.%'`, `ORDER BY salience DESC LIMIT 10`
- Concurrent-safe via SQLite's WAL mode (already configured)
- Scales to 100K+ entries without performance concern
- Could share the existing gateway.db or use a dedicated brain.db
- Migration framework already exists for schema evolution
- Future: could add FTS5 index on value column for free-text search within Brain entries

**Cons:**
- Less human-inspectable (need sqlite3 CLI or a dump tool)
- Slightly more complex implementation (~50 more lines)
- One more thing in the DB to back up (but backups already exist)

### Recommendation: SQLite

The codebase already has a mature SQLite infrastructure:
- `internal/database/migrations.go` — migration framework with versioning
- `internal/sessions/store.go` — session store pattern to follow
- `internal/searchdb/database.go` — search DB as a second SQLite file
- WAL mode, busy_timeout, connection pooling all configured
- `database.BuildDSN()` and `database.ConfigureDatabase()` handle setup

**The JSON approach would be the odd one out.** Every other persistent store in Conduit uses SQLite. Adding a JSON file creates a second persistence pattern to maintain, back up, and reason about.

**Performance matters for LTM.** If Jules accumulates 1000+ facts over weeks of use, JSON flush on every write becomes a drag. SQLite handles single-row UPSERTs in microseconds.

**The query capability is the clincher.** `recall` wants to search by prefix, by salience score, by recency. SQL does this natively. With JSON, you'd build all that in Go on top of a map — essentially reimplementing a database.

### Implementation: Dedicated brain.db

Rather than adding tables to gateway.db, use a dedicated file following the search.db pattern:

```go
// In config.go
type BrainConfig struct {
    Enabled bool   `json:"enabled"`
    Path    string `json:"path,omitempty"` // Default: derived from gateway.db path
    // e.g., gateway.db -> gateway.brain.db
}
```

This keeps the brain store independently backupable and doesn't risk bloating the gateway DB.

**Config path derivation** (following existing DeriveVectorDBPath pattern):
- If `brain.path` is set in config.json, use it
- Otherwise, derive from gateway.db: `gateway.db` -> `gateway.brain.db`
- Both resolve relative to `data_dir`

## System Prompt Section

This is the most important part of the design. The Brain tool is useless if the AI doesn't know *when* and *how* to use it.

### Proposed System Prompt Addition

```markdown
## Brain (Cognitive Architecture)

You have a tiered memory system beyond the context window. USE IT.

### Lookup-First Pattern (MANDATORY)
Before reading any file for a fact you've accessed before this session:
1. `Brain(action="get", key="likely.key.name")` — check if it's cached
2. Hit? Use it. Done. Miss? Read the file, then cache the key fact:
   `Brain(action="store", key="solar.panel_count", value="30", tier="working")`

### What Goes Where

| Tier | What | Examples | Lifetime |
|------|------|----------|----------|
| **longterm** | Core facts, stable preferences, learned patterns | jeff.birthday, solar.panel_count, pets.theo.breed | Survives restarts |
| **working** | Session-extracted facts, current task state | solar.today.production, email.unread_count, current.topic | Session only (promote if important) |
| **scratch** | Intermediate calculations, temp values | push/pop only | Seconds |

### Key Naming Convention
Use dot-separated namespaces: `domain.subject.attribute`
- `jeff.birthday`, `jeff.favorite_color`
- `solar.today.production`, `solar.panel_count`
- `pets.theo.breed`, `pets.theo.vet_overdue`
- `session.current_topic`, `session.start_time`

### When to Store
- After reading a file: cache the 2-3 key facts you extracted
- After a tool call returns useful data: cache the summary
- When the user states a fact or preference: store immediately
- When you compute something you might need again: working memory

### When to Promote (working -> longterm)
- Facts that are true across sessions (birthdays, counts, preferences)
- Learned patterns ("Jeff prefers X over Y")
- Infrastructure facts ("solar system has 30 panels")

### When NOT to Store
- Entire file contents (that's what files are for)
- Conversational context (that's what the context window is for)
- One-time responses (just respond, don't cache)

### Consolidation
At session end or handoff, call `Brain(action="consolidate")` to:
- Auto-promote high-salience working memory to longterm
- Flush longterm changes to disk
- Report what was promoted/evicted

### Searching
`Brain(action="recall", query="solar")` searches all tiers by key name and value content.
Results return with tier and salience so you know how fresh/reliable each fact is.
```

## In-Memory Architecture (Go)

### Core Structs

```go
package brain

// Tier represents a memory tier
type Tier string

const (
    TierLongTerm Tier = "longterm"
    TierWorking  Tier = "working"
)

// Entry is a single fact stored in the brain
type Entry struct {
    Key         string        `json:"key"`
    Value       string        `json:"value"` // always string — JSON-encode complex values
    Tier        Tier          `json:"tier"`
    CreatedAt   time.Time     `json:"created_at"`
    AccessedAt  time.Time     `json:"accessed_at"`
    AccessCount int           `json:"access_count"`
    Salience    float64       `json:"salience"`
    TTL         time.Duration `json:"ttl,omitempty"`
    Source      string        `json:"source,omitempty"`
}

// Brain is the cognitive architecture store
type Brain struct {
    mu        sync.RWMutex
    working   map[string]*Entry  // session-scoped
    scratch   []string           // LIFO stack
    db        *sql.DB            // SQLite for LTM persistence
    sessionID string
}
```

### Key Operations

```go
// Get checks working memory first (hot cache), then LTM (SQLite)
func (b *Brain) Get(key string) (*Entry, error)

// Store writes to the specified tier
// working: in-memory map only
// longterm: SQLite UPSERT
func (b *Brain) Store(key, value string, tier Tier, opts ...StoreOption) error

// Recall searches all tiers by key prefix and value content
func (b *Brain) Recall(query string, limit int) ([]*Entry, error)

// List returns keys matching a prefix across all tiers
func (b *Brain) List(prefix string) ([]*Entry, error)

// Push/Pop/Peek for scratchpad
func (b *Brain) Push(value string) error
func (b *Brain) Pop() (string, error)
func (b *Brain) Peek() (string, error)

// Consolidate runs session-end sweep
func (b *Brain) Consolidate(autoPromote bool) (*ConsolidationReport, error)

// Close flushes and closes
func (b *Brain) Close() error
```

## Integration Points

### Gateway Lifecycle

```go
// In gateway.go — at startup
brain, err := brain.New(brainDBPath)

// Passed to tool registry like other services
registry.RegisterTool(NewBrainTool(services, brain))

// At session end or gateway shutdown
brain.Consolidate(true)
brain.Close()
```

### Handoff Skill Integration

The existing `handoff` skill should call `Brain(action="consolidate")` as part of its workflow, ensuring working memory is swept before the session context is written.

### Existing Tool Integration (Future)

Once Brain exists, other tools could auto-cache:
- Solar skill stores `solar.today.production` after generating a report
- Email skill stores `email.unread_count` after checking
- Weather script stores `weather.current.temp` after a check

This is Phase 2 — start with the explicit Brain tool first.

## Implementation Plan

### Phase 1: Core Brain (MVP)
- `internal/brain/brain.go` — Brain struct, in-memory working map, SQLite LTM
- `internal/brain/brain_test.go` — full test coverage
- `internal/brain/migrations.go` — SQLite schema
- `internal/tools/core/brain.go` — Brain tool handler
- `internal/config/config.go` — BrainConfig addition
- System prompt section
- **Estimate: 600-800 lines of Go, 1-2 days**

### Phase 2: Search Integration
- Wire Brain tiers into `MemorySearch` / `Find` tool results
- Brain results ranked alongside file/session results
- **Estimate: 200 lines, half day**

### Phase 3: Auto-Caching
- Skills auto-store key facts in working memory after tool calls
- Convention: skill declares what keys it produces
- **Estimate: varies per skill, ongoing**

### Phase 4: Salience Tuning
- Monitor actual usage patterns
- Tune salience formula weights
- Add configurable decay rates
- **Estimate: ongoing iteration**

## Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Over-caching (store everything) | Noise drowns signal | System prompt teaches judgment; consolidation evicts low-salience |
| Under-caching (forget to store) | No benefit over status quo | System prompt makes lookup-first mandatory; measure cache hit rate |
| Key collision across sessions | Stale data from old session | Working memory clears per session; LTM keys are explicitly long-lived |
| SQLite contention with gateway.db | Latency | Dedicated brain.db file, separate connection |
| Brain tool adds latency to every interaction | Slower responses | In-memory working memory is sub-microsecond; SQLite reads are <1ms |

## Success Metrics

1. **File read reduction:** 30-40% fewer file reads per session (measurable via debug ring)
2. **Cache hit rate:** >50% of Brain(get) calls should hit within 10 sessions of use
3. **Session cold-start time:** Reduction in tool calls needed before Jules is "oriented"
4. **Consolidation adoption:** >80% of sessions should end with consolidation

## Open Questions

1. **Should working memory survive across sessions for the same user?** Current design says no — it's session-scoped. But if Jeff has a 4-hour session, disconnects, reconnects 5 minutes later, losing working memory feels wrong. Could key by user_id + session proximity.

2. **Should Brain entries be included in the existing backup system?** Probably yes — brain.db should be part of the regular backup sweep.

3. **Maximum LTM size?** Need a cap to prevent unbounded growth. 10K entries? 50K? Configurable with a default.

4. **Should sub-agents share the parent's working memory?** Currently sub-agents are isolated. Sharing working memory would let a spawned research agent cache facts the parent can use. But it adds complexity.

## Related Tickets

- `conduit-2a14` — Session Memory Map (original ticket, now subsumed by this PRD)
- `conduit-cz8e` — Tool call dry-run / preview mode
- `conduit-3hme` — Sub-agent result routing (callback to parent)
- `conduit-9ek6` — Session-level scratchpad (subsumed — scratchpad is Tier 3)
- `conduit-1hdg` — Skill dependency auto-loading

## Appendix A: Existing Codebase Patterns

The following existing patterns inform this design:

- **Ring Buffer** (`internal/tools/debuglog/ringbuffer.go`): sync.Mutex-guarded circular buffer, 180 lines. Good for ordered events, wrong for K/V lookup.
- **Session Store** (`internal/sessions/store.go`): SQLite-backed, uses database.BuildDSN/ConfigureDatabase, migration framework. Direct pattern to follow.
- **Search DB** (`internal/searchdb/database.go`): Separate SQLite file from gateway.db. Uses same migration and config patterns. Precedent for dedicated DB files.
- **Vector DB Path Derivation** (`config.go:DeriveVectorDBPath`): Pattern for deriving a DB file path from the gateway DB path. Brain should follow this exactly.
- **DebugLog Tool** (`internal/tools/core/debuglog.go`): Action-based tool handler pattern. Brain tool follows same structure.
