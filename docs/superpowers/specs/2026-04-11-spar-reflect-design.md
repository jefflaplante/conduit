# SPAR Reflect Architecture — Design Spec

**Date:** 2026-04-11
**Author:** Jeff LaPlante (design), Claude (spec)
**Status:** Draft
**Origin:** `spar-architecture.md` (project root)

---

## Context

Conduit's agent loop has strong Act and Plan phases but a broken Reflect phase. Lessons learned during a session evaporate when it ends. Tool failures get diagnosed in-the-moment but never persist across sessions. The model has no way to carry forward compressed experience.

This spec closes the feedback loop: **Reflect → Store → Sense**. Tool outcomes get captured (Go layer), qualitative insights get recorded (model layer), cross-session patterns get mined (REM cycle), and all of it feeds back into the next session's situational awareness (Sense prompt section).

The core thesis: for a frozen model, context is capability. Structured reflection data is the training data for improvement without weight updates.

---

## Approach: Layered (Go Captures, Model Synthesizes)

Two parallel channels for reflection data:

- **System channel (Go):** Deterministic. Every tool outcome logged automatically. Failures and patterns auto-promoted to Brain. Guaranteed to work regardless of model behavior.
- **Model channel (Prompt):** Qualitative. Model reflects on both success and failure, stores nuanced insights (API quirks, effective approaches, format surprises) in Brain. Best-effort — Go captures the baseline if the model forgets.

Neither channel depends on the other. Graceful degradation in both directions.

---

## 1. Reflection Data Model

### ReflectionEntry

```go
// internal/reflection/entry.go

type ReflectionEntry struct {
    ID           string            `json:"id"`             // UUID
    SessionKey   string            `json:"session_key"`    // Source session
    Timestamp    time.Time         `json:"timestamp"`
    Source       string            `json:"source"`         // "system" | "model"
    Type         ReflectionType    `json:"type"`           // tool_outcome, session_summary, pattern, learned
    Tool         string            `json:"tool,omitempty"` // Tool name (if tool-related)
    Outcome      Outcome           `json:"outcome"`        // success, failure, partial, timeout
    RetryCount   int               `json:"retry_count"`    // Retries before resolution
    Duration     time.Duration     `json:"duration"`       // Execution time
    Insight      string            `json:"insight"`        // Human-readable lesson
    Score        int               `json:"score"`          // 1-5 outcome rating (0 = unscored)
    Tags         []string          `json:"tags"`           // Free-form grouping tags
    RelatedKeys  []string          `json:"related_keys"`   // Connected Brain keys
}
```

**Types:** `tool_outcome` (per-tool), `session_summary` (per-session), `pattern` (cross-session), `learned` (actionable insight).

**Outcomes:** `success`, `failure`, `partial`, `timeout`.

### Storage

- **Primary:** `brain_reflections` table in Brain's existing SQLite database (`brain.db`). Added via Brain's migration system.
- **Searchable:** SQL queries for REM phase (GROUP BY, COUNT, AVG). Indexed in searchdb FTS5 for model access via MemorySearch.
- **Retention:** 30 days (configurable). Entries older than retention that have been processed by at least one REM cycle are pruned via REM's pruning phase. Durable insights live in Brain LTM; the reflections table is a staging area.

```sql
CREATE TABLE IF NOT EXISTS brain_reflections (
    id TEXT PRIMARY KEY,
    session_key TEXT NOT NULL,
    timestamp DATETIME NOT NULL,
    source TEXT NOT NULL,          -- "system" | "model"
    type TEXT NOT NULL,            -- "tool_outcome" | "session_summary" | "pattern" | "learned"
    tool TEXT,                     -- Tool name (nullable)
    outcome TEXT NOT NULL,         -- "success" | "failure" | "partial" | "timeout"
    retry_count INTEGER DEFAULT 0,
    duration_ms INTEGER DEFAULT 0,
    insight TEXT,                  -- Human-readable lesson
    score INTEGER DEFAULT 0,      -- 1-5 outcome rating (0 = unscored)
    tags TEXT,                     -- JSON array
    related_keys TEXT,             -- JSON array of Brain keys
    rem_processed INTEGER DEFAULT 0 -- 0 = unprocessed, 1 = processed by REM
);

CREATE INDEX idx_reflections_session ON brain_reflections (session_key);
CREATE INDEX idx_reflections_timestamp ON brain_reflections (timestamp);
CREATE INDEX idx_reflections_tool ON brain_reflections (tool);
CREATE INDEX idx_reflections_type ON brain_reflections (type);
CREATE INDEX idx_reflections_rem ON brain_reflections (rem_processed);
```

### Configuration

**Batteries included:** Reflection is ON by default with no config required. All defaults are sensible. Config fields exist only to override defaults or disable the feature.

| Field | Default | Purpose |
|-------|---------|---------|
| `reflection.enabled` | `true` | Set `false` to disable entirely |
| `reflection.capture_level` | `"all"` | `"all"` \| `"failures"` \| `"anomalies"` |
| `reflection.retention_days` | `30` | How long before REM grooms old entries |

No config.json entry needed for default behavior.

---

## 2. Go-Level Auto-Capture (System Channel)

Deterministic capture of tool outcomes via existing middleware infrastructure.

### 2.1 Execution Middleware

**File:** `internal/tools/execution.go` — new `AfterExecution` middleware.

After every tool execution, write a `TypeToolOutcome` entry to `brain_reflections`:

- Tool name, outcome (success/failure/timeout), duration, retry count
- Error string if failed
- Respects `capture_level` config: `"all"` logs everything, `"failures"` skips successes, `"anomalies"` only logs failures + slow executions (>2x average duration)

**Cost:** One JSON marshal + file append per tool call. Negligible.

### 2.2 FailureTracker Auto-Promotion

**File:** `internal/tools/failure_tracker.go` — extend `RecordFailure`.

When `ShouldPivot()` triggers (3+ consecutive failures):
1. Write `TypePattern` entry to `brain_reflections`: "Tool X failed 3+ times consecutively"
2. Store to Brain LTM: `reflect.tools.<toolname>.consecutive_failure` with failure count, error summary, timestamp
3. Source tag: `"system"`

### 2.3 PatternTracker Auto-Promotion

**File:** `internal/tools/pattern_tracker.go` — extend `DetectCircular`.

When circular pattern detected:
1. Write `TypePattern` entry to `brain_reflections` with loop sequence (e.g., "Read → Bash → Read")
2. Store to Brain LTM: `reflect.tools.circular.<signature_hash>` with pattern description
3. Source tag: `"system"`

### 2.4 Session Metrics (Go-Computed)

Computed at session-end trigger (see Section 4). Writes `TypeSessionSummary` to `brain_reflections`:

- Total tool calls, unique tools used
- Failure rate (failures / total)
- Most-used tool, most-failed tool
- Longest chain depth
- Total session duration, message count
- Circular patterns detected (count)
- Source tag: `"system"`, Score: 0 (unscored by Go)
- Session metrics stored as JSON in the `insight` field

---

## 3. Model-Driven Reflection (Prompt Channel)

Prompt additions that guide the model to reflect qualitatively.

### 3.1 Per-Action Reflection (Diff A)

**Location:** System prompt, Tool Strategy section (P3), after existing Error Recovery guidance.

```markdown
### Per-Action Reflection
After receiving any tool result, briefly assess before your next action:
- Did this return what I expected? If not, diagnose before retrying.
- Did this succeed in a way worth noting? (unexpected format, useful pattern, efficient approach)
- If you discover a tool quirk or learned pattern, store it:
  Brain(action="store", key="reflect.learned.<tool>.<finding>", value="<what you learned>", tier="working")
- This prevents repeated failures and captures effective approaches across sessions.
```

**Token cost:** ~80 tokens/session. Captures nuanced insights Go can't detect (e.g., "this API returns paginated results that look complete but aren't").

### 3.2 Session-End Reflection (Diff B)

**Location:** System prompt, new section or appended to Memory Persistence (P2).

Injected only when session-end is detected (high-confidence triggers):

```markdown
### Session Reflection
[Session Reflection] Before wrapping up, briefly assess:
1. What was the primary ask? Was it satisfied?
2. Any tool failures, unexpected results, or things that took multiple tries?
3. Any patterns worth remembering for future sessions?
4. Rate this session's outcome 1-5 (1=failed, 3=partial, 5=fully satisfied).
Store findings: Brain(action="store", key="reflect.session.<topic>", value="<insight>", tier="working")
Then: Brain(action="consolidate") to promote important items.
```

**Token cost:** ~100 tokens. Fires only at session end, not every turn.

### 3.3 Brain Namespace Convention

All reflection data under `reflect.*` with source tagging:

| Prefix | Writer | Content |
|--------|--------|---------|
| `reflect.tools.<name>.*` | System | Tool metrics, failure patterns, circular detections |
| `reflect.learned.<topic>.*` | Model | Qualitative insights, API quirks, effective approaches |
| `reflect.session.<topic>` | Model | Session-level observations, preferences |
| `reflect.patterns.<finding>` | REM | Cross-session patterns (synthesized from clusters) |
| `reflect.clusters.<id>` | REM (Go) | Pre-filtered clusters awaiting model synthesis |

Source (`"system"` vs `"model"`) is a metadata tag on each Brain entry, not a namespace split.

---

## 4. Session-End Detection

Explicit + heuristic triggers. Any one fires reflection.

### Triggers

| Trigger | Detection Point | Type | Model Available? |
|---------|----------------|------|-----------------|
| `/goodbye`, `/end` | Slash command handler | Explicit | Yes |
| `/reset` | Reset handler (reflect BEFORE clear) | Explicit | Yes |
| Natural language farewell | Message content heuristic | Explicit | Yes |
| Context budget ≥80% | Router token usage check | Heuristic | Yes |
| Idle timeout (15 min default) | Gateway session cleanup loop | Heuristic | No |
| WebSocket disconnect | Gateway WS `onClose` handler | Heuristic | No |

### Natural Language Farewell Patterns

Detected in gateway message handling. Patterns (case-insensitive, matched at message start or as full message):

- goodbye, bye, good night, goodnight
- see you later, see ya, talk later
- that's all, that's it, all done, we're done
- thanks that's it, thanks I'm done, thank you that's all
- signing off, logging off, end session

Conservative matching — only trigger when the message is primarily a farewell, not when "goodbye" appears mid-sentence.

### Reflection Flow by Trigger Type

**High-confidence (model available):**
1. Inject session reflection system message into current conversation
2. Model reflects, stores insights in Brain, scores session
3. Go computes session metrics, writes `TypeSessionSummary` to `brain_reflections`
4. Call `Brain.Consolidate()` to promote high-salience working memory
5. Normal session end / reset proceeds

**Low-confidence (model unavailable):**
1. Go computes session metrics from message log
2. Write `TypeSessionSummary` to `brain_reflections` (Score: 0, no model insight)
3. If session was substantive (>5 messages AND tool calls present), tag for REM review
4. Normal cleanup proceeds

### `/reset` Wiring

`/reset` currently creates a new session and clears context. New flow:
1. Detect `/reset` command
2. Fire high-confidence reflection on the *current* session (model still has context)
3. Wait for reflection to complete
4. Proceed with normal reset (new session, clean context)

---

## 5. REM Reflect Phase (Cross-Session Pattern Mining)

New phase in the existing REM cycle. Runs after `triage`, before `consolidation`.

### Architecture

**File:** `internal/brain/rem/reflect.go`

Follows existing phase pattern (like `triage.go`, `consolidate.go`). Registered in the cycle orchestrator.

### Phase Flow

**Step 1 — Go Pre-Filter:**
- Query `brain_reflections` for entries since last REM run (`WHERE rem_processed = 0`)
- Group by: tool name, error type, outcome (SQL GROUP BY — efficient)
- Compute per-tool: failure rate, avg retry count, avg duration (SQL aggregates)
- Identify clusters: entries sharing tool + error type with 3+ occurrences
- Filter noise: ignore one-off failures, only surface repeated patterns
- Mark processed entries: `UPDATE brain_reflections SET rem_processed = 1`

**Step 2 — Write Clusters to Brain:**
- Write cluster summaries to `reflect.clusters.<id>` in Brain
- Each cluster entry includes: tool name, failure count, common error, affected session count, date range
- These are factual summaries, not interpretations — synthesis happens lazily

**Step 3 — Lazy Model Synthesis:**
- Clusters show up in the next interactive session's Sense section (see Section 6)
- The model naturally incorporates them into its reasoning
- If a cluster is relevant to the current task, the model synthesizes it into `reflect.patterns.<finding>`
- No separate model call, no latency hit — synthesis happens in-band during normal conversation

**Step 4 — Promote Confirmed Patterns:**
- When `reflect.patterns.*` entries get accessed 3+ times across sessions (salience rises), they're high-confidence
- High-confidence patterns get tagged for permanent Sense inclusion
- Low-access patterns eventually decay via normal Brain salience eviction

### Reflection Table Grooming

Extended from existing REM `pruning` phase:
- `DELETE FROM brain_reflections WHERE rem_processed = 1 AND timestamp < (now - retention_days)`
- Entries older than `retention_days` (default 30) AND processed by at least one REM cycle → pruned
- Entries promoted to Brain LTM are durable there; the reflections table is a staging area
- Grooming runs as part of REM, not on every write

---

## 6. Enhanced Sense Phase

New prompt section that grounds each session with situational awareness from reflection data and operational state.

### New Prompt Section: "Situation Awareness" (P2)

**File:** `internal/agent/prompt_sections.go` — new section builder.

Pulls from Brain and computed sources:

| Data Source | Brain Namespace | Populated By |
|-------------|----------------|-------------|
| Learned patterns | `reflect.learned.*` | Model (per-action reflection) |
| Pattern clusters | `reflect.clusters.*` | REM reflect phase (Go) |
| Confirmed patterns | `reflect.patterns.*` | Model (synthesized from clusters) |
| Active work items | `sense.tasks.*` | Beads integration (new wiring) |
| Recent alerts | `sense.alerts.*` | Heartbeat system (new wiring) |
| Daily briefing summary | `sense.briefing.*` | Daily briefing cron (new wiring) |
| Time context | N/A (computed) | Prompt builder at build time |

### Token Budget

Configurable, default ~500 tokens. When data exceeds budget, prioritize by:
1. Confirmed patterns (highest salience, most actionable)
2. Active work items (immediate relevance)
3. Recent alerts (time-sensitive)
4. Learned patterns (useful background)
5. Clusters awaiting synthesis (lowest priority — model synthesizes only if relevant)

Brain's salience scoring handles ranking within each category.

### Data Population Wiring (New Work)

Three existing systems need Brain-write additions:

**Heartbeat → `sense.alerts.*`:**
- After heartbeat job execution, if result is non-OK (alert/warning), write to Brain: `sense.alerts.<alert_type>` with severity, message, timestamp
- Clear resolved alerts on next successful heartbeat run

**Daily Briefing → `sense.briefing.*`:**
- Daily briefing system exists (`internal/briefing/briefing.go`). Add Brain write after briefing execution: `sense.briefing.latest` with calendar highlights, email summary, key items
- Overwrites previous briefing (always latest)

**Beads → `sense.tasks.*`:**
- On session start, query active beads (status=open, in_progress) via `br` CLI
- Write summary to Brain: `sense.tasks.active` with count, titles, priorities
- Refreshed per-session, not persisted long-term

### Time Context (Computed)

Injected by prompt builder, not Brain:
- Current time (already in Runtime section — reference, don't duplicate)
- Day of week, quiet hours status
- "Morning briefing window" / "end of day" / "weekend" contextual hints

---

## 7. Outcome Scoring

Lightweight. No separate infrastructure.

### How It Works

- **High-confidence session end:** Model rates session 1-5 as part of session reflection (Diff B prompt). Score written as `Score` field on `TypeSessionSummary` reflection entry.
- **Low-confidence session end:** Score defaults to 0 (unscored).
- **REM optional backfill:** When REM processes unscored sessions with sufficient reflection data, it can assign a heuristic score (e.g., 0 failures + completed task = 4, multiple failures = 2).

### Score Definitions

| Score | Meaning |
|-------|---------|
| 1 | Failed — primary ask not addressed |
| 2 | Poor — attempted but significantly incomplete |
| 3 | Partial — main ask addressed with notable gaps |
| 4 | Good — primary ask satisfied, minor issues |
| 5 | Excellent — fully satisfied, efficient execution |
| 0 | Unscored (system-computed session end) |

### Usage

Queryable corpus: "show sessions where score < 3" identifies struggle areas. REM can aggregate: "average score for sessions involving tool X" to detect chronically problematic tools or task types.

---

## Files to Create/Modify

### New Files

| File | Purpose |
|------|---------|
| `internal/reflection/entry.go` | ReflectionEntry struct, types, constants |
| `internal/reflection/store.go` | SQLite store (insert, query, groom) using Brain's DB |
| `internal/reflection/trigger.go` | Session-end trigger detection (farewell patterns, slash commands, heuristics) |
| `internal/reflection/session_reflect.go` | Session reflection orchestrator (metrics + model injection + Brain consolidate) |
| `internal/reflection/config.go` | Reflection configuration struct |
| `internal/brain/rem/reflect.go` | REM reflect phase (Go pre-filter + cluster writing) |

### Modified Files

| File | Change |
|------|--------|
| `internal/brain/migrations.go` | Add migration for `brain_reflections` table |
| `internal/tools/execution.go` | Add AfterExecution middleware for tool outcome capture |
| `internal/tools/failure_tracker.go` | Auto-promote to Brain + JSONL on pivot threshold |
| `internal/tools/pattern_tracker.go` | Auto-promote to Brain + JSONL on circular detection |
| `internal/agent/prompt_sections.go` | Add Per-Action Reflection to Tool Strategy; add Situation Awareness section |
| `internal/agent/prompt_builder.go` | Register new Situation Awareness section at P2 |
| `internal/brain/rem/cycle.go` | Register reflect phase in cycle orchestrator |
| `internal/brain/rem/prune.go` | Extend pruning to groom `brain_reflections` table |
| `internal/gateway/gateway.go` | Wire session-end triggers (idle timeout, WS disconnect) |
| `internal/gateway/ws_chat.go` | Wire farewell detection in message handling |
| `internal/config/config.go` | Add reflection config struct |
| `internal/heartbeat/processor.go` | Add Brain write for `sense.alerts.*` |
| `internal/workspace/context.go` | Add beads query for `sense.tasks.*` |

### Slash Command Additions

| Command | Action |
|---------|--------|
| `/goodbye` | Trigger high-confidence session reflection, then end session |
| `/end` | Alias for `/goodbye` |
| `/reset` | Trigger high-confidence session reflection, then reset (existing behavior) |

---

## Verification Plan

### Unit Tests

- `internal/reflection/`: Entry serialization, SQLite store insert/query/groom, trigger pattern matching, farewell detection
- `internal/brain/rem/reflect.go`: Cluster computation from `brain_reflections` query, threshold filtering
- Execution middleware: Verify entries written for success, failure, timeout outcomes
- FailureTracker/PatternTracker: Verify Brain promotion on threshold events

### Integration Tests

- Full session lifecycle: start session → tool calls → explicit goodbye → verify `brain_reflections` entries + Brain LTM entries + session summary
- `/reset` flow: verify reflection fires before context clear
- Idle timeout: verify Go-only metrics written (no model call)
- REM cycle with reflect phase: seed `brain_reflections` with test data, run cycle, verify clusters in Brain

### Manual Verification

- Start a session, use several tools, say "goodbye" — check `brain_reflections` table for entries
- Deliberately fail a tool 3+ times — verify `reflect.tools.<name>.consecutive_failure` appears in Brain
- Run REM cycle after accumulating reflection data — verify `reflect.clusters.*` entries in Brain
- Start a new session — verify Situation Awareness section includes learned patterns and clusters
- Use `/reset` — verify reflection output before context clears

### Metrics to Track Post-Ship

- `brain_reflections` row count — validates grooming keeps table bounded
- Brain `reflect.*` entry count — validates promotion without bloat
- Session score distribution — validates scoring is happening and useful
- Model Brain store calls per session — validates prompt guidance is working
