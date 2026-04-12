# Brain Cognitive Memory System

Conduit includes a tiered cognitive memory system called Brain that gives the AI agent persistent, structured recall across sessions. Brain stores facts as key-value entries across three tiers with salience-based ranking, and includes an offline REM Sleep cycle that consolidates, prunes, and grooms memory automatically.

## Architecture

```
                    ┌──────────────────────────────────────────┐
                    │              Brain Tool                    │
                    │  (store/get/recall/list/push/pop/...)     │
                    └────────────────┬─────────────────────────┘
                                     │
                    ┌────────────────▼─────────────────────────┐
                    │              Brain Service                 │
                    │         (internal/brain/brain.go)          │
                    └────┬──────────┬──────────────┬───────────┘
                         │          │              │
              ┌──────────▼──┐ ┌────▼────────┐ ┌──▼──────────┐
              │  Long-Term   │ │   Working    │ │  Scratchpad  │
              │   Memory     │ │   Memory     │ │   (LIFO)     │
              │  (SQLite)    │ │ (in-process) │ │ (in-process) │
              └──────┬───────┘ └─────────────┘ └─────────────┘
                     │
              ┌──────▼───────┐
              │  REM Sleep    │
              │  Cycle        │
              │  (offline)    │
              └──────────────┘
```

### Long-Term Memory (LTM)

SQLite-persisted key-value store. Entries survive restarts and are shared across all users and sessions. Each entry tracks creation time, last access time, access count, salience score, and source provenance.

- Stored in a dedicated `brain.db` file (path derived from gateway DB or configured explicitly)
- Indexed by salience and access time for efficient queries
- Capped at `max_ltm_entries` (default 10,000) -- lowest-salience entries are evicted on insert when over the cap
- Supports archive table for soft-deleted entries (used by REM pruning)
- Supports relationship table for cross-entry links (used by REM integration)

### Working Memory (WM)

In-process per-user key-value store. Scoped to the current user's session context. Entries exist only while the gateway is running.

- Keyed by user ID extracted from request context
- Sub-agents can read (but not write) their parent's working memory via `WithParentUserID` context propagation
- Auto-flushed periodically: entries not accessed for over 1 hour with salience below the evict threshold are removed
- High-salience entries can be promoted to LTM via the `promote` or `consolidate` actions

### Scratchpad

Per-user LIFO stack for temporary notes during multi-step reasoning. Push values on, pop or peek at the top.

- Purely ephemeral -- lost on restart
- Independent per user ID
- No salience scoring or key indexing -- just a string stack

### Sub-Agent Working Memory Sharing

When a sub-agent session is spawned, the parent's user ID is attached to the child context via `WithParentUserID`. The child can then:

- **Read** parent WM entries via `Get` and `Recall` (returns read-only copies, no access count bump on parent)
- **Not write** to parent WM -- the child has its own isolated WM namespace

This enables sub-agents to access the parent's session context without mutation side effects.

## Brain Tool Actions

The `Brain` tool is registered automatically when brain is enabled. All actions use the `action` parameter.

### store

Save a key-value fact to a memory tier.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | `"store"` |
| `key` | string | Yes | Memory key (e.g. `"user.preference.language"`) |
| `value` | string | Yes | Value to store |
| `tier` | string | No | `"working"` (default) or `"longterm"` |
| `source` | string | No | Provenance label (default `"tool"`). Known prefixes: `file`, `skill`, `tool`, `user`, `llm`, `sub-agent`, `system` |

### get

Retrieve a specific fact by key. Checks working memory first (own, then parent's), then LTM. Bumps access count and recalculates salience on hit.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | `"get"` |
| `key` | string | Yes | Exact key to retrieve |

### recall

Fuzzy search across all tiers. Tokenizes the query (strips stopwords, splits on delimiters), matches against keys and values, and ranks results by a blended score: 60% match relevance + 40% salience.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | `"recall"` |
| `query` | string | Yes | Natural language search query |
| `limit` | int | No | Max results (default 20) |

### list

List entries matching a key prefix, optionally filtered by source prefix.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | `"list"` |
| `key` | string | No | Key prefix to filter (empty = all) |
| `source_prefix` | string | No | Filter by source prefix (e.g. `"file:"`, `"skill:"`) |

### delete

Remove a key from working memory and LTM.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | `"delete"` |
| `key` | string | Yes | Key to delete |

### push

Push a value onto the per-user scratchpad stack.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | `"push"` |
| `value` | string | Yes | Value to push |

### pop

Pop the top value from the scratchpad stack. Returns an error if the stack is empty.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | `"pop"` |

### peek

View the top scratchpad value without removing it.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | `"peek"` |

### promote

Move a working memory key to long-term storage. The key is removed from WM and inserted into LTM.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | `"promote"` |
| `key` | string | Yes | WM key to promote |

### consolidate

Sweep working memory: promote entries with salience above the consolidate threshold, evict entries below the evict threshold.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | `"consolidate"` |
| `auto_promote` | bool | No | Whether to auto-promote high-salience keys (default `true`) |

Returns a report with promoted keys, evicted keys, and current LTM size.

### status

Report entry counts, scratchpad depth, average WM salience, and the 5 hottest LTM keys.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | `"status"` |

### rem_cycle

Run the REM Sleep consolidation cycle (or a subset of phases). Requires `rem_enabled: true` in config.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | `"rem_cycle"` |
| `phases` | string[] | No | Phases to run (default: all five). Accepts short forms: `triage`, `consolidate`, `prune`, `integrate`, `groom` |
| `dry_run` | bool | No | Preview changes without applying (default `false`) |

## Salience Scoring

Every entry has a salience score between 0.0 and 1.0 that determines its importance. Salience controls which entries are promoted, evicted, or pruned.

### Formula

```
salience = (access_score * access_weight) + (recency_score * recency_weight) + (tier_score * tier_weight)
```

Where:

- **access_score** = `min(access_count / access_count_cap, 1.0)` -- how frequently the entry is accessed, normalized to [0, 1]
- **recency_score** = `1 / (1 + hours_since_last_access * recency_decay_rate)` -- exponential decay from last access
- **tier_score** = fixed value per tier: LTM = 0.8, Working = 0.5, Scratch = 0.1

### Default Weights

| Weight | Default | Effect |
|--------|---------|--------|
| `access_weight` | 0.4 | How much access frequency matters |
| `recency_weight` | 0.4 | How much recent access matters |
| `tier_weight` | 0.2 | How much the storage tier matters |

Weights should sum to 1.0. Adjusting them changes which entries float to the top:

- Increase `access_weight` to favor frequently-used facts
- Increase `recency_weight` to favor recently-touched facts
- Increase `tier_weight` to favor LTM entries over WM

### Thresholds

| Threshold | Default | Purpose |
|-----------|---------|---------|
| `consolidate_threshold` | 0.6 | WM entries above this are auto-promoted to LTM during consolidation |
| `evict_threshold` | 0.1 | WM entries below this are evicted during consolidation or auto-flush |

## REM Sleep Cycle

The REM (Replay, Evaluate, Maintain) Sleep cycle runs offline -- typically via a cron schedule during quiet hours -- to maintain memory health. It has five phases that run in order.

### Phase 1: Triage

Scans daily logs and working memory to identify what needs processing.

- Reads the daily log file at `memory/YYYY-MM-DD.md` looking for patterns: `Learned:`, `Noted:`, `Remembered:`, `Updated:`
- Counts unpromoted working memory keys
- Queries LTM for stale candidates (entries not accessed in more than `rem_prune_age_days`)

Output: lists of new facts, updated facts, and stale candidates.

### Phase 2: Consolidation

Promotes, merges, decays, and boosts entries.

1. **Promote high-salience WM entries** to LTM (salience >= `consolidate_threshold`)
2. **Merge duplicate keys** in LTM by normalized key comparison (lowercase, collapse whitespace). Keeps the higher-salience entry, archives the other.
3. **Apply salience decay** to entries not accessed in 7+ days (subtracts `rem_salience_decay_rate` from salience, floored at 0.0). Skipped when LTM count is below `max_ltm_entries`.
4. **Boost recently accessed entries** -- adds 0.05 salience to entries accessed in the last 24 hours (capped at 1.0).

### Phase 3: Pruning

Moves low-value entries to the archive. Two modes based on LTM size:

- **Under `max_ltm_entries`**: Only detects orphaned entries (file-path sources where the source file no longer exists on disk)
- **Over `max_ltm_entries`**: Full salience-based eviction (entries below evict threshold and older than `rem_prune_age_days`) plus orphan detection

Pruned entries are moved to `brain_archive`, not deleted. This is a safe, reversible operation.

### Phase 4: Integration

Detects relationships between stored memories. Runs only on the configured integration day (default: Sunday).

1. **Namespace relationships**: Keys sharing a namespace prefix (e.g. `user.preference.language` and `user.preference.editor`) are linked with 0.7 confidence
2. **Token overlap relationships**: Entries whose values share 30%+ Jaccard token similarity are linked with 0.3-0.6 confidence
3. **Pattern detection**: Reports clusters by namespace, high-salience counts, and frequently-accessed entries

Relationships are stored in `brain_relationships` with key pair, relationship type, and confidence.

### Phase 5: Grooming

Checks source files for changes and marks entries as stale.

**File sources** (`file:` prefix or absolute paths):
- Computes SHA-256 hash of the source file
- Compares against the stored `source_hash` in LTM
- If the hash changed, marks all entries from that source as `stale = 1`

**Non-file sources** (age-based staleness):
- `user:` sources are never stale (authoritative)
- `file:` sources use hash-based detection (not age)
- `llm:` sources become stale after 14 days
- `skill:`, `sub-agent:`, `tool:` sources become stale after 30 days

### REM Report

Each cycle produces a markdown report written to `rem_log_path` (default `memory/rem-log/`) with per-phase results.

## Source Provenance

Every brain entry can have a `source` label tracking where the fact came from. Sources use a prefix convention:

| Prefix | Description | Staleness |
|--------|-------------|-----------|
| `file:` | Extracted from a file (e.g. `file:MEMORY.md`) | Hash-based |
| `skill:` | Produced by a skill | 30 days |
| `tool:` | Produced by a tool | 30 days |
| `user:` | Directly from user input | Never stale |
| `llm:` | Generated by the AI model | 14 days |
| `sub-agent:` | From a sub-agent session | 30 days |
| `system:` | Generated by internal subsystems (beads wiring, SPAR reflection) | 14 days |

Unknown source prefixes generate a log warning but do not block storage.

## Configuration Reference

All fields live under the `brain` key in the config JSON.

### Core Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable the brain memory system |
| `path` | string | derived | Path to brain.db file. If empty, derived from the gateway DB path (e.g. `config.telegram.brain.db`) |
| `max_ltm_entries` | int | `10000` | Maximum LTM entries. Over this cap, lowest-salience entries are evicted on insert |
| `wm_grace_period_seconds` | int | `300` | Seconds to keep working memory after session end |
| `auto_flush_seconds` | int | `600` | Interval for background auto-flush of stale WM entries |
| `consolidate_threshold` | float | `0.6` | Salience threshold for WM-to-LTM promotion |
| `evict_threshold` | float | `0.1` | Salience threshold for WM eviction |
| `auto_promote` | bool | `true` | Auto-promote high-salience WM keys during consolidation |

### Salience Weight Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `access_weight` | float | `0.4` | Weight for access frequency in salience formula |
| `recency_weight` | float | `0.4` | Weight for recency in salience formula |
| `tier_weight` | float | `0.2` | Weight for tier in salience formula |
| `recency_decay_rate` | float | `1.0` | Recency decay: `1/(1 + hours * rate)`. Higher = faster decay |
| `access_count_cap` | int | `100` | Access count normalization cap. Entries with >= this many accesses score 1.0 on the access component |

### REM Sleep Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `rem_enabled` | bool | `true` | Enable REM sleep cycle scheduling |
| `rem_schedule` | string | `"0 2 * * *"` | Cron schedule for REM cycle (default: 2 AM daily) |
| `rem_integration_day` | int | `0` | Day of week for integration phase (0 = Sunday, 6 = Saturday) |
| `rem_prune_age_days` | int | `30` | Entries not accessed in this many days are prune candidates |
| `rem_salience_decay_rate` | float | `0.1` | Amount subtracted from salience during consolidation decay |
| `rem_groom_with_llm` | bool | `true` | Use LLM for re-extraction during grooming (reserved for future use) |
| `rem_log_path` | string | `"memory/rem-log"` | Directory for REM cycle report logs (relative to workspace dir) |

## Configuration Example

```json
{
  "brain": {
    "enabled": true,
    "path": "",
    "max_ltm_entries": 10000,
    "wm_grace_period_seconds": 300,
    "auto_flush_seconds": 600,
    "consolidate_threshold": 0.6,
    "evict_threshold": 0.1,
    "auto_promote": true,

    "access_weight": 0.4,
    "recency_weight": 0.4,
    "tier_weight": 0.2,
    "recency_decay_rate": 1.0,
    "access_count_cap": 100,

    "rem_enabled": true,
    "rem_schedule": "0 2 * * *",
    "rem_integration_day": 0,
    "rem_prune_age_days": 30,
    "rem_salience_decay_rate": 0.1,
    "rem_groom_with_llm": true,
    "rem_log_path": "memory/rem-log"
  }
}
```

## Database Schema

Brain uses its own SQLite database with three tables:

### brain_ltm

Primary storage for long-term memory entries.

| Column | Type | Description |
|--------|------|-------------|
| `key` | TEXT PRIMARY KEY | Unique entry key |
| `value` | TEXT | Entry value |
| `source` | TEXT | Provenance label |
| `source_hash` | TEXT | SHA-256 hash of source file (for groom change detection) |
| `created_at` | DATETIME | When the entry was created |
| `accessed_at` | DATETIME | Last access time |
| `access_count` | INTEGER | Total access count |
| `salience` | REAL | Current salience score |
| `stale` | INTEGER | 1 if marked stale by grooming, 0 otherwise |

### brain_archive

Soft-delete destination for pruned or merged entries.

| Column | Type | Description |
|--------|------|-------------|
| `key` | TEXT PRIMARY KEY | Original entry key |
| `value` | TEXT | Original value |
| `source` | TEXT | Original source |
| `tier` | TEXT | Original tier |
| `salience` | REAL | Salience at time of archival |
| `archived_at` | DATETIME | When archived |
| `reason` | TEXT | Why archived (`low_salience`, `orphaned`, `merged into <key>`) |

### brain_relationships

Cross-entry links discovered by the integration phase.

| Column | Type | Description |
|--------|------|-------------|
| `key_a` | TEXT | First key in pair |
| `key_b` | TEXT | Second key in pair |
| `relationship` | TEXT | Relationship type (currently always `related`) |
| `confidence` | REAL | Confidence score (0.0 to 1.0) |
| `created_at` | DATETIME | When discovered |

## Package Layout

```
internal/brain/
  brain.go            # Core Brain struct: Store, Get, Recall, List, Delete, Push/Pop/Peek, Promote, Consolidate, Status
  migrations.go       # SQLite schema migrations (4 versions, includes brain_reflections)
  source.go           # Source provenance: prefix parsing, validation, staleness thresholds
  tokenize.go         # Query tokenizer: stopword removal, delimiter splitting, dedup
  rem/
    cycle.go          # REMCycle orchestrator: Run() dispatches phases in order
    triage.go         # Phase 1: daily log scanning, stale candidate detection
    consolidate.go    # Phase 2: promote, merge, decay, boost
    prune.go          # Phase 3: salience-based eviction, orphan detection
    integrate.go      # Phase 4: namespace/token-overlap relationship detection
    groom.go          # Phase 5: file hash change detection, age-based staleness
    report.go         # Result structs and markdown report writer

internal/tools/core/
  brain.go            # BrainTool: action dispatch, parameter parsing, SelfTest

internal/tools/types/
  types.go            # BrainService interface, BrainEntry, BrainTier, REMCycleRunner
```

## See Also

- **[SPAR Reflect](spar.md)** — Cross-session learning loop built on top of Brain. Captures per-tool outcomes and session summaries into `brain_reflections`, clusters patterns via the REM Reflect phase, and feeds them back into the agent's Situation Awareness prompt section.
