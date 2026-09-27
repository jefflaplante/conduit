# SPAR — Sense, Plan, Act, Reflect

SPAR is Conduit's cross-session learning loop. It captures what happened during tool execution and conversations, distills that into patterns, and feeds it back into the agent's system prompt so future sessions start smarter.

The core thesis: for a frozen model, context is capability. Structured reflection data is the training data for improvement without weight updates.

## How It Works

```
 ┌──────────────────────────────────────────────────────────────────┐
 │                        Agent Session                             │
 │                                                                  │
 │  ┌─────────┐    ┌─────────┐    ┌─────────────────────────────┐  │
 │  │  Sense   │───▶│  Plan   │───▶│           Act               │  │
 │  │(prompt)  │    │(model)  │    │  Tool calls → results       │  │
 │  └────▲─────┘    └─────────┘    └──────────┬──────────────────┘  │
 │       │                                     │                    │
 │       │           ┌─────────────────────────▼──────────────┐     │
 │       │           │           Reflect                      │     │
 │       │           │  • Per-tool outcomes (Go, automatic)   │     │
 │       │           │  • Session summaries (Go, on idle/end) │     │
 │       │           │  • Model insights (prompt, on farewell)│     │
 │       │           └──────────┬──────────────────────────────┘     │
 │       │                      │                                   │
 └───────┼──────────────────────┼───────────────────────────────────┘
         │                      │
         │               ┌──────▼──────┐
         │               │brain_        │
         │               │reflections  │  (SQLite)
         │               └──────┬──────┘
         │                      │
         │               ┌──────▼──────┐
         │               │ REM Sleep   │  (nightly, offline)
         │               │ Reflect     │
         │               │ Phase       │
         │               └──────┬──────┘
         │                      │
         │               ┌──────▼──────────────────┐
         │               │ Brain LTM               │
         │               │ reflect.clusters.*      │
         │               │ reflect.patterns.*      │
         │               │ reflect.learned.*       │
         └───────────────┤ sense.tasks.*           │
                         │ sense.alerts.*          │
                         └─────────────────────────┘
```

**Two parallel channels capture reflection data:**

- **System channel (Go):** Deterministic. Every tool outcome logged automatically. Failures and patterns auto-promoted to Brain. Guaranteed to work regardless of model behavior.
- **Model channel (Prompt):** Qualitative. Model reflects on success and failure, stores nuanced insights (API quirks, effective approaches, format surprises) in Brain. Best-effort — Go captures the baseline if the model doesn't cooperate.

Neither channel depends on the other.

## Reflection Capture

### Per-Tool Outcomes (Automatic)

Every tool execution fires an `AfterExecutionHook` on the `ExecutionEngine`. The `ReflectionMiddleware` converts this into a `TypeToolOutcome` entry in `brain_reflections`.

**What's captured:** tool name, session key, success/failure/timeout, error message, duration, retry count.

**Capture levels** (configurable via `reflection.capture_level`):
- `"all"` — every tool execution (default)
- `"failures"` — only failures and timeouts
- `"anomalies"` — only timeouts and high-retry executions

**Key files:**
- `internal/reflection/middleware.go` — ToolOutcomeInfo → ReflectionEntry conversion
- `internal/reflection/entry.go` — entry types and outcome classification
- `internal/tools/execution.go:384` — hook invocation site
- `internal/gateway/gateway.go:751-769` — wiring in gateway init

### Session Summaries (On Idle or End)

When a session ends or goes idle, Go computes aggregate metrics and writes a `TypeSessionSummary` entry.

**Metrics computed:** total tool calls, unique tools, failure count, failure rate, most-used tool, most-failed tool, max chain depth, duration, message count.

**Three trigger paths:**

| Trigger | Confidence | How |
|---------|-----------|-----|
| **Idle timeout** | Low (Go-only) | Background goroutine checks every 5 min for sessions idle >30 min with >5 messages. Writes summary with `score=0`. |
| **Farewell phrase** | High (model-assisted) | Detecting "goodbye", "see ya", "that's all", etc. at message start. Injects reflection prompt so the model reflects before signing off. |
| **Context budget** | High (model-assisted) | When prompt tokens reach 80% of context window. Same reflection prompt injection. |

**Farewell phrases recognized:** goodbye, bye, good night, see ya, see you later, thanks i'm done, thanks that's it, thank you that's all, all done, we're done, that's all, that's it, signing off, logging off, end session, talk later.

**Session-end commands:** `/goodbye`, `/end`, `/reset` all trigger high-confidence reflection.

**Key files:**
- `internal/gateway/gateway_reflect_wiring.go` — idle scanner, session-end reflection, high-confidence pre/post
- `internal/gateway/ws_chat.go:100-127` — farewell/context budget detection and prompt injection
- `internal/gateway/ws_chat.go:374-378` — post-reflection metric write
- `internal/gateway/ws_chat.go:786-853` — `/goodbye` reflective session end
- `internal/reflection/trigger.go` — FarewellDetector with conservative phrase matching
- `internal/reflection/session_reflect.go` — SessionReflector, metrics computation, reflection prompt builder

## REM Reflect Phase

The REM Sleep cycle (documented in [brain.md](brain.md#rem-sleep-cycle)) includes a **Reflect phase** that mines cross-session patterns from the accumulated `brain_reflections` entries.

### What It Does

Reflect is part of the default cycle, including the nightly `rem_cycle` job (the Brain tool's default phase list omitted it until conduit-31jg.54, so the live store had never been processed).

1. **Snapshot** the unprocessed entries (`rem_processed = 0`) by rowid high-water mark. Everything below uses aggregate SQL, so a large backlog is never loaded into memory or bound as one giant `IN (...)` list. Rows that arrive during the phase wait for the next cycle.
2. **Aggregate** tool stats grouped by `tool + outcome` (e.g., "WebFetch + failure", "Bash + success"), counting from the earliest unprocessed entry but no further back than `rem_prune_age_days` (default 30). `pattern` rows are excluded because they record threshold crossings, not executions.
3. **Cluster** groups with 3+ occurrences — these represent real patterns, not noise
4. **Write** cluster summaries to Brain LTM under `reflect.clusters.<tool>.<outcome>` keys
5. **Consolidate SPAR patterns** into `reflect.tools.*` (see below)
6. **Backfill** heuristic scores on unscored session summaries:
   - 0 failures → score 4
   - Some failures → score 2
   - All failures → score 1
7. **Mark** the snapshot `rem_processed = 1` in one statement. Pruning later grooms processed rows older than the retention window.

### Pattern Promotion (`reflect.tools.*`)

The execution engine's per-turn trackers fire two SPAR hooks. **Pivot** fires when a tool fails 3 times in a row within one turn, and **circular** fires when the same 2-3 call sequence repeats 3 times. Each hook writes a `pattern` row to `brain_reflections` whose related key is `reflect.tools.<tool>.consecutive_failure` or `reflect.tools.circular.<signature-hash>`. The hooks never write to LTM directly, because one bad turn is noise.

Reflect consolidates these rows (conduit-31jg.54):

- Rows are grouped by their related key. Occurrences, distinct sessions, first/last seen and the latest error are recomputed each run over every pattern row in the window (`rem_prune_age_days`), processed or not. Each pattern therefore stays **one** LTM entry that is refreshed in place, with counts that are never appended.
- A key is written only when the current snapshot has new evidence for it **and** it recurred in **≥ 2 distinct sessions** and **≥ 2 turns**. A single occurrence, or a repeat confined to one session, never reaches LTM.
- The value is one Situation Awareness line of at most 200 bytes, with source `system:rem-reflect` (grooming marks it stale after 14 days without refresh). For example:

```
Key:   reflect.tools.WebFetch.consecutive_failure
Value: WebFetch failed 3+ times in a row in 4 turns across 3 sessions (Sep 20–27). Last error: dial tcp: i/o timeout
```

### Example Cluster Output

After several sessions where `WebFetch` fails repeatedly, the Reflect phase writes:

```
Key:   reflect.clusters.WebFetch.failure
Value: Tool WebFetch has 7 failures since 2026-04-10 08:15. Avg duration: 275ms.
```

This entry is then visible to the Situation Awareness prompt section in every future session.

**Key files:**
- `internal/brain/rem/reflect.go` — RunReflect, clustering, score backfill
- `internal/brain/rem/cycle.go` — REMCycle orchestration (Reflect is phase 2)

## Situation Awareness (The Sense Loop)

Reflection data feeds back into every session via the **Situation Awareness** prompt section. This is built by the agent's `PromptBuilder` and injected into the system prompt.

### What Gets Injected

The prompt builder queries Brain LTM for these prefixes, in priority order:

| Priority | Prefix | Header | What It Contains |
|----------|--------|--------|-----------------|
| 1 | `reflect.patterns.*` | Confirmed Patterns | Manually validated patterns |
| 2 | `sense.tasks.*` | Active Work | Beads task summaries (auto-refreshed) |
| 3 | `sense.alerts.*` | Recent Alerts | Heartbeat alert context |
| 4 | `reflect.learned.*` | Learned Patterns | Model-written insights from reflection |
| 5 | `reflect.tools.*` | Tool Pitfalls | Cross-session SPAR pivot/circular patterns promoted by REM Reflect |
| 6 | `reflect.clusters.*` | Pattern Clusters | REM-discovered tool outcome clusters |
| 7 | `sense.briefing.*` | Daily Briefing | Daily briefing context |

Entries are sorted by effective salience (recency computed at query time, see [brain.md](brain.md#salience-scoring)) within each category. The entire section is capped at ~500 tokens (~2000 chars). Categories are rendered in priority order; if budget runs out, later categories are truncated or dropped.

### Example System Prompt Section

```markdown
## Situation Awareness
Time context: Friday afternoon

### Active Work
- 3 open tasks: fix auth timeout, update API docs, review PR #42

### Pattern Clusters
- Tool WebFetch has 7 failures since 2026-04-10. Avg duration: 275ms.
- Tool Bash has 12 successes since 2026-04-08. Avg duration: 45ms.
```

**Key files:**
- `internal/agent/prompt_sections.go:535-638` — buildSituationAwareness, querySituationCategories
- `internal/workspace/beads.go:183-205` — sense.tasks.active auto-refresh from beads

## Database

Reflection entries are stored in the `brain_reflections` table within `brain.db` (created by Brain migration 4).

| Column | Type | Description |
|--------|------|-------------|
| `id` | TEXT PK | UUID |
| `session_key` | TEXT | Source session |
| `timestamp` | DATETIME | When captured |
| `source` | TEXT | `"system"` (Go) or `"model"` (LLM) |
| `type` | TEXT | `tool_outcome`, `session_summary`, `pattern`, `learned` |
| `tool` | TEXT | Tool name (nullable, for tool_outcome entries) |
| `outcome` | TEXT | `success`, `failure`, `partial`, `timeout` |
| `retry_count` | INTEGER | Retries before resolution |
| `duration_ms` | INTEGER | Execution time in milliseconds |
| `insight` | TEXT | Human-readable lesson (error message on failure) |
| `score` | INTEGER | 1-5 rating (0 = unscored) |
| `tags` | TEXT | JSON array of grouping tags |
| `related_keys` | TEXT | JSON array of related Brain keys |
| `rem_processed` | INTEGER | 0 = unprocessed, 1 = processed by REM Reflect |

## Configuration

### Reflection Config

Lives under the `reflection` key in config JSON.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `true` | Master switch for all reflection capture |
| `capture_level` | string | `"all"` | `"all"`, `"failures"`, or `"anomalies"` |
| `retention_days` | int | `30` | REM grooms entries older than this |

### Brain Config (REM-related)

Lives under the `brain` key. See [brain.md](brain.md#rem-sleep-fields) for the full list. The relevant fields for SPAR:

| Field | Default | Effect on SPAR |
|-------|---------|---------------|
| `rem_enabled` | `true` | Enables the REM cycle (including Reflect phase) |
| `rem_schedule` | `"0 2 * * *"` | When clustering runs (default 2 AM) |

### Example Config

```json
{
  "brain": {
    "enabled": true,
    "rem_enabled": true,
    "rem_schedule": "0 2 * * *"
  },
  "reflection": {
    "enabled": true,
    "capture_level": "all",
    "retention_days": 30
  }
}
```

## Package Layout

```
internal/reflection/
  entry.go              # ReflectionEntry, types, outcomes
  config.go             # ReflectionConfig, capture level policy
  store.go              # ReflectionStore: Insert, Query, MarkProcessed, Groom, ToolStats
  middleware.go          # ReflectionMiddleware: tool execution → reflection entry
  session_reflect.go    # SessionReflector: session metrics, summary writer, reflection prompt
  trigger.go            # FarewellDetector: farewell phrases and session-end commands

internal/brain/rem/
  reflect.go            # REM Reflect phase: clustering, score backfill

internal/gateway/
  gateway.go            # Reflection init + hook wiring (lines 738-770)
  gateway_reflect_wiring.go  # Idle scanner, session-end reflection helpers
  ws_chat.go            # Farewell detection, context budget trigger, prompt injection

internal/agent/
  prompt_sections.go    # Situation Awareness builder (lines 535-638)

internal/workspace/
  beads.go              # sense.tasks.active auto-refresh
```

## Data Flow Summary

```
Tool Execution
  → AfterExecutionHook (Go, every call)
  → ReflectionMiddleware.recordOutcome()
  → brain_reflections (TypeToolOutcome)

Session End (idle/farewell/context budget)
  → SessionReflector.ComputeMetrics()
  → brain_reflections (TypeSessionSummary)

Pivot / circular detection (per turn)
  → SPAR hooks → brain_reflections (TypePattern, related key reflect.tools.*)

REM Cycle (nightly)
  → Reflect phase snapshots unprocessed entries
  → Clusters tool+outcome groups (≥3 occurrences) → reflect.clusters.*
  → Consolidates patterns recurring in ≥2 sessions → reflect.tools.*
  → Marks entries as processed

Next Session
  → PromptBuilder.buildSituationAwareness()
  → Queries reflect.patterns.*, reflect.tools.*, reflect.clusters.*, sense.*
  → Injects into system prompt as Situation Awareness section
```
