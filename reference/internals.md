# Internal Subsystems Reference

This document covers five internal subsystems that are not directly user-facing but provide foundational capabilities used by other parts of Conduit. These packages live under `internal/` and are not currently wired into the main gateway startup path. They should be considered **experimental** unless noted otherwise.

---

## Table of Contents

- [Learning (Proactive Behavior Prediction)](#learning-proactive-behavior-prediction)
- [Orchestration (Multi-Agent Coordination)](#orchestration-multi-agent-coordination)
- [NLI (Natural Language Intent Parsing)](#nli-natural-language-intent-parsing)
- [Plugins (Plugin Architecture)](#plugins-plugin-architecture)
- [Briefing (Session Summarization)](#briefing-session-summarization)

---

## Learning (Proactive Behavior Prediction)

**Package:** `internal/learning/`

The learning subsystem observes user behavior patterns across sessions and builds predictive models. It tracks tool usage sequences, timing, and outcomes to anticipate what a user is likely to do next. The primary use case is suggesting prefetch targets so that data can be prepared before the user explicitly requests it.

The system is privacy-conscious by design: no raw message content is stored. Only statistical data (word counts, tool names, timing, outcome categories) and a truncated SHA-256 hash for deduplication are retained. All data structures are bounded with configurable caps to prevent unbounded memory growth. Sessions are evicted on an LRU basis when the cap is reached.

### Key Types

| Type | Description |
|------|-------------|
| `Learner` | Top-level engine. Observes interactions, generates predictions, and identifies patterns. Composes `SequenceAnalyzer` and `PreferenceTracker`. |
| `SequenceAnalyzer` | N-gram model (bigrams and trigrams) over action sequences. Detects common tool chains and time-of-day usage patterns using 4-hour buckets. |
| `PreferenceTracker` | Tracks per-session tool preferences by usage frequency and success rate. Detects message length patterns (concise/moderate/detailed). |
| `Prediction` | A predicted next action with confidence score (0.0-1.0), reasoning text, and source label (`sequence`, `time_of_day`, or `preference`). |
| `PrefetchSuggestion` | A resource to prefetch with expected benefit and confidence. |
| `UserPattern` | A recognized behavioral pattern with name, frequency, confidence, action sequence, and optional time-of-day affinity. |

### Key Methods

- `Learner.ObserveInteraction(session, message, tools, outcome)` -- Records an interaction. Message content is hashed, never stored.
- `Learner.PredictNextAction(session, currentMessage)` -- Returns up to 5 predictions sorted by confidence. Combines n-gram, time-of-day, and preference signals.
- `Learner.GetUserPatterns(session)` -- Returns recognized behavioral patterns for a session.
- `Learner.SuggestPrefetch(session)` -- Suggests resources to prefetch based on predicted next actions and time-of-day habits.

### Integration

Not currently wired into the gateway. Intended to be called from the AI router or agent system after each tool execution loop completes. Predictions could feed into the agent's system prompt or into a prefetch pipeline.

### Configuration

All tuning is done via `LearnerOption` functional options at construction time:

| Option | Default | Description |
|--------|---------|-------------|
| `WithMaxInteractions(n)` | 500 | Per-session rolling window cap |
| `WithMaxSessions(n)` | 100 | Maximum concurrent sessions tracked |
| `WithMinPatternOccurrences(n)` | 3 | Minimum occurrences before a pattern is surfaced |
| `WithMinConfidence(c)` | 0.3 | Minimum confidence threshold for predictions |

### Status

**Experimental.** Fully implemented with tests. Not integrated into the gateway runtime.

---

## Orchestration (Multi-Agent Coordination)

**Package:** `internal/orchestration/`

The orchestration subsystem manages multiple concurrent AI agent sessions with shared context. It provides an `Orchestrator` that creates, runs, and monitors agents, and a `SharedContext` that acts as a versioned key-value store visible to all agents. On top of these primitives, the package provides four higher-level coordination patterns: fan-out, pipeline, debate, and map-reduce.

The `AgentRunner` interface decouples the orchestrator from any specific AI provider. Callers supply an implementation that connects to Anthropic (or a mock for testing). Each agent runs asynchronously in its own goroutine, with cancellation propagated via context. The orchestrator tracks agent lifecycle states (idle, running, completed, failed, cancelled) and supports graceful shutdown with a 5-second drain period.

### Key Types

| Type | Description |
|------|-------------|
| `Orchestrator` | Central coordinator. Manages agent creation, message dispatch, result collection, and shutdown. Holds a global `SharedContext` and per-group contexts. |
| `AgentRunner` | Interface with a single `Run(ctx, agentID, config, message, shared)` method. Provided by the caller. |
| `AgentConfig` | Configuration for spawning an agent: role, system prompt, model, allowed tools, max turns. |
| `AgentResult` | Outcome of an agent's work: response text, status, tokens used, duration, error. |
| `SharedContext` | Thread-safe versioned key-value store with change listeners. Supports `ScopeGlobal` and `ScopeGroup`. |
| `ContextChangeEvent` | Describes a set or delete operation on a SharedContext, including old/new values and version number. |

### Coordination Patterns

| Pattern | Function | Description |
|---------|----------|-------------|
| Fan-out | `FanOut(ctx, orch, cfg)` | Sends the same message to multiple agents concurrently and collects all results. |
| Pipeline | `Pipeline(ctx, orch, cfg)` | Chains agents sequentially, feeding each agent's output as input to the next. Supports per-stage input transformation. |
| Debate | `Debate(ctx, orch, cfg)` | Two agents argue opposing sides for N rounds, then a synthesizer agent evaluates and produces a balanced result. |
| Map-Reduce | `MapReduce(ctx, orch, cfg)` | Splits work across map agents (one per input), then a reduce agent combines their outputs. |

### Integration

Not currently wired into the gateway. The `AgentRunner` interface is the integration point: an implementation backed by `ai.Provider` would enable the orchestrator to dispatch real AI work. The existing sub-agent spawning in `internal/sessions/` operates independently of this package.

### Status

**Experimental.** Fully implemented with tests. No gateway-level integration yet. The SharedContext versioning and change notification system is stable and could be used independently.

---

## NLI (Natural Language Intent Parsing)

**Package:** `internal/nli/`

The NLI (Natural Language Intent) subsystem parses free-form user messages into structured intents using regex and keyword analysis. It requires no external NLP dependencies. The parser identifies the action type (query, command, create, modify, delete), extracts typed entities (file paths, URLs, model names, sessions, tool names, channels), and infers a target for the operation.

Beyond single-message parsing, the package provides two additional components: a `ConversationPlanner` that converts sequences of intents into executable plans with dependency resolution and rollback steps, and a `ContextTracker` that maintains a sliding window of conversation turns for pronoun resolution and entity tracking across multi-turn interactions.

### Key Types

| Type | Description |
|------|-------------|
| `IntentParser` | Stateless parser. Extracts action, entities, target, and parameters from a message. Supports multi-step parsing via `ParseMultiStep`. |
| `Intent` | Parsed result: action type, target, parameters map, entities list, confidence score, raw text. |
| `ActionType` | Enum: `query`, `command`, `create`, `modify`, `delete`. |
| `EntityType` | Enum: `file_path`, `url`, `model_name`, `session`, `tool_name`, `search_term`, `channel`, `generic`. |
| `Entity` | A recognized entity with type, value, and character offsets (start/end index). |
| `ConversationPlanner` | Converts intents into an `ExecutionPlan` with ordered steps, tool resolution, dependency inference, cost/duration estimates, and rollback generation. |
| `ExecutionPlan` | Ordered list of `ExecutionStep` entries with estimated duration and optional rollback steps. |
| `ContextTracker` | Sliding-window conversation state tracker. Resolves pronoun references ("it", "that file", "the url") to the most recent matching entity. |

### Intent Parsing

The parser uses keyword matching with position and word-boundary scoring to determine action type. Entity extraction uses compiled regex patterns for file paths, URLs, model names, and session references, plus a known-tool-name dictionary for tool entities and a pattern for channel references (`#name` or `channel name`).

Multi-step messages are split on numbered lists (`1. ... 2. ...`), semicolons, sequential keywords (`first`, `then`, `next`, `finally`), and conjunctions (`and also`, `and then`).

### Execution Planning

The `ConversationPlanner` resolves each intent to a tool name using a layered strategy: explicit tool entity > action+target keyword mapping > entity-type heuristics > action-based defaults. Dependencies between steps are inferred from data-flow analysis (e.g., `web_search` producing URLs consumed by `web_fetch`, or shared file paths). Each plan includes estimated duration and cost, and destructive operations (delete, modify) get rollback steps.

### Integration

Not currently wired into the gateway. Could be used as a pre-processing layer before the AI router to provide structured hints about user intent, or as the backbone of a local (non-LLM) command parser for simple operations.

### Status

**Experimental.** Fully implemented with tests. The keyword and regex approach works for structured commands but is not intended to replace the AI model's natural language understanding for complex queries.

---

## Plugins (Plugin Architecture)

**Package:** `internal/plugins/`

The plugin subsystem defines a formal architecture for extending Conduit with third-party or modular functionality. It is distinct from the Skills system (which loads SKILL.md prompt files): plugins are Go interface implementations that provide tools, lifecycle hooks, and structured metadata. The architecture includes a plugin interface, a registry with dependency management, an on-disk manifest format for discovery, and a marketplace catalog for browsing and managing available plugins.

A plugin implements the `Plugin` interface: it provides metadata (name, version, author, description, tags, capabilities, dependencies), initializes with a `PluginContext` that supplies configuration and a data directory, exposes tools via the standard `types.Tool` interface, and cleans up on shutdown. The registry validates metadata (requiring semver versions), checks dependency ordering, and prevents unregistration of plugins that others depend on.

### Key Types

| Type | Description |
|------|-------------|
| `Plugin` | Interface: `Metadata()`, `Initialize(PluginContext)`, `Tools()`, `Shutdown()`. |
| `PluginMetadata` | Name, version (semver), author, description, tags, capabilities, dependencies, min gateway version. |
| `PluginContext` | Initialization context: config map, logger, data directory. |
| `PluginRegistry` | Manages plugin lifecycle: register, unregister, enable/disable, get all tools from enabled plugins, shutdown all. Thread-safe. |
| `PluginManifest` | On-disk JSON format (`plugin.json`). Extends metadata with entrypoint path and config schema. |
| `ConfigParam` | Describes a configuration parameter in a manifest: type, description, default, required flag. Supported types: `string`, `int`, `float`, `bool`, `[]string`, `map`. |
| `Catalog` | In-memory marketplace directory. Tracks install status, rating, download count, verification status. Supports search by name/description/tag/capability and import from discovered manifests. |

### Plugin Discovery

The `DiscoverPlugins(dirs)` function scans a list of directories for subdirectories containing a `plugin.json` manifest file. Each manifest is loaded, validated, and deduplicated by name. The catalog can import discovered manifests to populate its directory of available plugins.

### Registry Lifecycle

1. Create registry with `NewPluginRegistry(ctx)`
2. Register plugins with `Register(plugin)` -- validates metadata, checks dependencies, calls `Initialize`
3. Query tools with `GetAllTools()` -- returns tools from all enabled plugins
4. Enable/disable individual plugins without unregistering
5. Unregister with dependency checking (blocks if other plugins depend on it)
6. `ShutdownAll()` calls `Shutdown()` on every plugin and clears the registry

### Integration

Not currently wired into the gateway. The `types.Tool` return from `Plugin.Tools()` is compatible with the existing tool registry, so plugins could be registered alongside built-in tools. The marketplace catalog is purely in-memory and does not connect to any external service.

### Status

**Experimental.** Fully implemented with tests. No plugins exist yet. The architecture is in place for future extensibility.

---

## Briefing (Session Summarization)

**Package:** `internal/briefing/`

The briefing subsystem generates structured summaries of session activity by analyzing conversation messages. Given a session ID and its message history, it produces a `Briefing` that includes a high-level summary, key decisions made during the conversation, files changed, tools used (with counts), open questions, and suggested next steps. Briefings are persisted as JSON files and can be listed and loaded from a directory.

The summarization is entirely heuristic-based: it uses keyword matching to identify decisions ("decided to", "let's use"), file changes ("wrote to", "edited"), and next steps ("todo", "need to", "follow up"). Open questions are detected by scanning the last 10 messages for sentences ending with a question mark. No LLM calls are made during briefing generation.

### Key Types

| Type | Description |
|------|-------------|
| `BriefingGenerator` | Stateless generator. Call `Generate(sessionID, messages)` to produce a `Briefing`. |
| `Briefing` | Full summary: ID, session ID, timestamp, summary text, key decisions, files changed, tools used, open questions, next steps, duration, message count. |
| `BriefingSummary` | Lightweight representation for list views (ID, session ID, timestamp, truncated summary, duration). |
| `Message` | Input message type (mirrors `sessions.Message` without importing it): ID, role, content, timestamp, metadata map. |
| `ToolUsage` | Tool name and invocation count. |

### Key Functions

- `Generate(sessionID, messages)` -- Produces a full briefing from message history.
- `Save(briefing, dir)` -- Persists a briefing as `{briefingID}.json` in the given directory.
- `Load(path)` -- Reads a briefing from a JSON file.
- `ListBriefings(dir)` -- Returns summaries of all briefings in a directory, sorted most-recent-first.

### Summary Generation Details

The summary combines message counts (user vs. assistant), the first user message (truncated to 200 chars) as the opening topic, and the last assistant message as the closing state. Tool usage is counted from message metadata (`tool_name` key) and from content pattern matching against known tool names. Files changed are extracted by looking for write/edit/create patterns followed by path-like strings.

### Integration

Not currently wired into the gateway. Could be triggered at session close or on demand to provide session handoff context. The `Message` type is structurally compatible with `sessions.Message`, so adaptation is straightforward.

### Configuration

No configuration options. The generator is stateless and has no tunable parameters.

### Status

**Experimental.** Fully implemented with tests. The heuristic approach produces reasonable summaries for structured tool-heavy sessions but may miss nuance in free-form conversations.
