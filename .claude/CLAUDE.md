---
# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Test Commands

```bash
make build              # Build binary to bin/conduit (includes version info via ldflags)
make build-prod         # Production build with symbol stripping (-s -w)
make test               # Run all tests: go test -v ./...
make test-coverage      # Tests with HTML coverage report
make lint               # golint ./... && go vet ./...
make format             # gofmt -w . && go mod tidy
make run                # Build and run with config.json
make run-telegram       # Build and run with config.telegram.json (requires TELEGRAM_BOT_TOKEN)
make dev                # Auto-restart on changes (requires 'air')
make clean              # Remove build artifacts, coverage files, conduit.db
make init               # Full new-setup: deps, workspace dir, config from example
make install            # Build and run install.sh for service setup
make health             # Curl localhost:18789/health to check if running
make help               # Show all targets
```

Run a single test:
```bash
go test -v -run TestFunctionName ./internal/package/...
```

Run tests for a specific package:
```bash
go test -v ./internal/tools/...
```

## Architecture

Go 1.25 (`go 1.25.0` in go.mod, no toolchain line), single binary gateway. Pure Go SQLite via modernc.org/sqlite (no CGO). CLI via Cobra (cmd/gateway/main.go). Default port 18789.

### Core Flow

Incoming messages flow: Channel Adapter → Channel Manager → Gateway → AI Router → Provider (Anthropic). The AI Router handles tool execution loops internally, calling tools through the Tool Registry and feeding results back to the provider until the model produces a final response. Streaming responses are supported end-to-end.

### Access Methods

- **WebSocket** — Primary client protocol for browser/app clients
- **SSH + TUI** — BubbleTea terminal UI served over SSH via Wish (internal/ssh/, internal/tui/). Uses a direct in-process client (gateway/direct_client.go) instead of WebSocket loopback. TUI also has a dedicated channel adapter (internal/channels/tui/).
- **Telegram** — Native bot adapter with pairing system (internal/channels/telegram/)

### Key Interfaces

- channels.ChannelAdapter (internal/channels/interface.go) — All channel implementations. Optional interfaces: StreamingAdapter, TypingIndicator. Factory pattern via ChannelFactory.
- ai.Provider (internal/ai/router.go) — AI model providers. Each implements GenerateResponse(ctx, *GenerateRequest) (*GenerateResponse, error).
- ai.ExecutionEngine (internal/ai/router.go) — Tool call flow handling: HandleToolCallFlow processes iterative tool execution.
- ai.AgentSystem (internal/ai/router.go) — Pluggable agent personality: BuildSystemPrompt, GetToolDefinitions, ProcessResponse.
- types.Tool (internal/tools/types/types.go) — All tools implement Name(), Description(), Parameters(), Execute(ctx, args). Optional interfaces: EnhancedSchemaProvider, ParameterValidator, ParameterDiscoverer, UsageExampleProvider, SelfTester.
- types.GatewayService (internal/tools/types/types.go) — Gateway operations exposed to tools without circular imports. Includes session, channel, config, metrics, scheduler operations, `GetContextBudget(ctx, sessionKey)`, and `GetFuelGaugeMap(topN int)`.
- types.ChannelSender (internal/tools/types/types.go) — Channel message sending exposed to tools.
- types.SearchService (internal/tools/types/types.go) — FTS5 full-text search interface over documents and messages.
- types.BrainService (internal/tools/types/types.go) — Tiered cognitive memory: Store, Get, Recall, List, Delete, Push/Pop/Peek (scratchpad), Promote, Consolidate, Status, Close.
- types.BrainFTSSearcher (internal/tools/types/types.go) — FTS5-backed search over brain LTM entries.
- types.VisionAnalyzer (internal/tools/types/types.go) — Narrow single-method interface (`AnalyzeImage`) wiring ImageTool to a multimodal LLM backend.
- agent.AgentSystem (internal/agent/interface.go) — Concrete agent system interface with Name(), BuildSystemPrompt, SetTools, ProcessResponse. Includes SessionStateManager for tracking processing states.

### Dependency Injection Pattern

Tools cannot import gateway directly (circular dependency). Instead, types.ToolServices struct in internal/tools/types/types.go aggregates service interfaces (SessionStore, ConfigMgr, WebClient, SkillsManager, ChannelSender, Gateway, Searcher, VectorSearch, VectorIndexer, MQTTService, Brain, BrainFTS, REMCycle, Reflection, Vision, SchemaBuilder, DebugLog). The gateway creates services, then calls registry.SetServices() after construction.

## CLI Commands

The binary is `bin/conduit`. Default behavior (no subcommand) starts the server.

- `server` — Start the gateway (default)
- `version` — Show version, git commit, build date
- `token` — Token management (create, list, revoke, export, info)
- `pairing` — Telegram user pairing management
- `tools` — Tool discovery (list, describe, schema, examples)
- `tui` — Launch the BubbleTea terminal UI client
- `ssh-server` — Start the standalone SSH server for TUI access
- `ssh-keys` — SSH key management (list, add, remove, init)
- `auth` — OAuth authentication for AI providers (login, status, logout, refresh)
- `maintenance` — Database maintenance (run, run-task, status, config)
- `backup` — Backup/restore gateway data (create, restore, list)
- `brain` — Brain LTM graph operations (export)
- `briefing` — Generate and manage session briefings (generate, show, list)
- `chain` — Manage and execute saved tool chains (list, show, create, run, delete, validate)
- `cron` — Scheduler job maintenance for cron_jobs.json (migrate-tz)
- `loadtest` — Load test against the AI provider using a mock backend
- `metrics` — Start the metrics dashboard HTTP server
- `restart` / `stop` / `status` — Signal or check a running Conduit process (signals, not HTTP)

## Package Layout

- cmd/gateway/ — Entry point and CLI command definitions (main.go, auth.go, backup.go, brain.go, briefing.go, chain.go, cron.go, loadtest.go, maintenance.go, metrics.go, pairing.go, process.go, signals.go, ssh.go, ssh_keys.go, tools.go, tui.go). Build-tagged optional_*.go files blank-import the optional tools (with_datadog, with_k8s, with_pagerduty, with_sre, with_mqtt, with_ssh, with_unifi).
- cmd/recall-events-converter/ — One-off converter for brain recall-events logs
- internal/gateway/ — Core gateway orchestration, WebSocket handling, HTTP endpoints, context usage tracking, direct client for TUI, heartbeat integration. Includes ContextBudget (context_budget.go), FuelGauge (fuel_gauge.go), VisionAnalyzer adapter (vision_adapter.go)
- internal/ai/ — AI provider routing, conversation management, tool execution loops, streaming, pricing (PricingResolver in pricing.go)
- internal/agent/ — Agent personality system: interface definition, Conduit agent implementation, prompt builder with section-based prompt construction
- internal/models/ — Anthropic API models: typed Messages API request/response structs (messages.go) and the request builder (anthropic.go)
- internal/tools/ — Tool registry (registry*.go: registration, lookup, enable/sandbox, execute, skills bridge, optional tools, selftest), execution engine with parallel support, plus top-level tool files:
  - aliases.go — Anthropic tool alias resolution (unversioned name → versioned name) with env override
  - anthropic.go — Anthropic versioned tool name constants (web_search, web_fetch)
  - execution*.go, execution_adapter.go — Tool execution engine and adapter (dispatch, parallel/same-path grouping, loop, format, timeout/drain, middleware, events). HandleToolCallFlow is a for-loop over rounds with an explicit turnState (runToolLoop/roundTrip/advance); golden trace in testdata/toolloop_golden.json
  - chain_state.go, failure_tracker.go, pattern_tracker.go, watchdog.go — Per-turn chain state, failure/pattern tracking, stall watchdog
  - exec.go (Bash; bash_policy.go + shell_lexer.go denylist), fileops*.go (Read/Write/Glob/List), google_workspace.go — Top-level tool implementations
  - Tool subdirectories:
    - core/ — Context management, file editing, find, facts, gateway control, memory search, session management, chain, debug log, brain (tiered cognitive memory)
    - web/ — Web search (Brave), web fetch with HTML parsing
    - communication/ — Message sending to channels, status updates, TTS
    - scheduling/ — Cron job tool (includes heartbeat cron integration)
    - vision/ — Image analysis
    - schema/ — Static schema hints plus channel/workspace parameter discovery
    - args/ — Shared argument parsing helpers for tool implementations
    - debuglog/ — In-memory ring buffer backing the DebugLog tool
    - mqtt/ — MQTT tool with action dispatch (status, topics, recent, history, publish) (build tag with_mqtt)
    - datadog/ — Datadog metrics/logs/APM/monitors tool (with_datadog)
    - k8s/ — Kubernetes management tool with security controls (with_k8s)
    - pagerduty/ — PagerDuty incident/on-call tool (with_pagerduty)
    - sre/ — SRE incident correlation engine across tools (with_sre)
    - ssh/ — SSH remote execution tool: pool, fanout, inventory, SCP, tunnels, audit (with_ssh)
    - unifi/ — UniFi Network/Protect API tool (with_unifi)
    - Optional-tool packages have register.go / register_stub.go pairs selected by build tag.
- internal/tools/types/ — Single source of truth for tool-related types and service interfaces (Tool, ToolServices, GatewayService, ChannelSender, SearchService, MQTTService, BrainService, BrainFTSSearcher, VisionAnalyzer)
- internal/channels/ — Channel adapter interface + manager; subdirectories:
  - telegram/ — Native Telegram adapter with pairing system (pairing storage, CLI, photo support)
  - tui/ — TUI channel adapter with factory for in-process BubbleTea connections
- internal/sessions/ — SQLite session store with state tracking
- internal/mqtt/ — MQTT event ingest: paho client wrapper, per-topic ring buffers, service with background pruning, adapter to tool-layer interface
- internal/brain/ — Tiered cognitive memory: LTM (SQLite-persisted brain.db), working memory (in-process per-user), scratchpad (LIFO stack). Salience-scored entries with configurable weights: brain_ltm stores base salience and recency is computed at query time (salience.go). Recall-events log with rotation (recall_events.go). Own migration system (migrations.go, brain_migrations table, 9 migrations; runs in brain.New after options are applied). rem/ implements the nightly REM cycle (triage, consolidate, integrate, prune, reflect). Sub-agent WM sharing via parent context.
- internal/reflection/ — SPAR Reflect subsystem: per-tool outcome capture (ReflectionMiddleware), session metrics (SessionReflector), farewell detection (FarewellDetector), ReflectionStore (brain_reflections table). See reference/spar.md.
- internal/config/ — JSON config loading with ${ENV_VAR} expansion. Config struct includes: port, database, AI, agent, workspace, skills, tools, channels, debug, rate limiting, heartbeat, agent heartbeat, SSH, MQTT, brain. `Config.Validate()` (validate.go) is called by `config.Load()` and checks port range, AI credentials, channels, workspace, rate-limit sanity, and tool constraints; credential checks are intentionally soft (warn, not fatal) to allow partial configs.
- internal/database/ — Gateway DB (gateway.db) SQLite migration system (8 migrations: sessions/messages, auth tokens, telegram pairings, FTS5 search, messages_fts sync triggers, token hash versioning, ingest DLQ, alert history). The brain DB has its own separate migrations in internal/brain.
- internal/fts/ — FTS5 full-text search: document chunking, indexing, and search queries (Porter stemming, unicode61 tokenizer)
- internal/ftsquery/ — Turns free-text user queries into safe FTS5 MATCH expressions
- internal/searchdb/ — Dedicated search.db with FTS5 indexes (document chunks, beads, messages, brain LTM). Includes BeadsIndexer, BrainIndexer, MessageSyncer
- internal/auth/ — Token auth (128-bit entropy, Base58, SHA256 hash storage), OAuth support, CLI token management
- internal/backup/ — Backup/restore system: create tar.gz archives of database, config, workspace, SSH keys, skills; restore with dry-run support; list/inspect archives
- internal/middleware/ — HTTP auth, WebSocket auth, rate limiting. RequestID middleware (request_id.go) injects a `request_id` into every request context; slog-based structured logging uses it for correlation throughout auth and rate-limit handlers.
- internal/ratelimit/ — Sliding window rate limiter implementation
- internal/monitoring/ — Gateway metrics, event tracking, metric aggregation, heartbeat metrics. TokenWindowTracker (token_usage.go) records API token usage in rolling hour/day windows.
- internal/heartbeat/ — HEARTBEAT.md task execution, result processing, task types, quiet-hours deferral (deferred.go, SharedAlertQueue-backed deferred.json). All delivery goes through DeliveryRegistry (delivery.go: CircuitBreaker + AlertAuditor → alert_history) with a ChannelSenderDeliverer (delivery_channel.go) and bounded background retries per alert_retry_policy (delivery_dispatch.go).
- internal/skills/ — Skill discovery from SKILL.md files, loading, validation, tool adaptation, manager
- internal/maintenance/ — Database cleanup and maintenance scheduling
- internal/scheduler/ — Cron job scheduling with interfaces
- internal/ssh/ — SSH server via Wish with key management (server.go, keys.go)
- internal/tui/ — BubbleTea terminal UI: chat view, sidebar, tab bar, status bar, tool activity display, Lipgloss styling, client interface
- internal/version/ — Version info injected via ldflags at build time
- internal/protocol/ — Message type definitions (messages.go) shared across packages
- internal/tokens/ — Token generation (generator.go) and formatting (format.go) utilities
- internal/approval/ — Generic human-in-the-loop approval primitive (manager, request origin)
- internal/briefing/ — Session briefing generation (used by the `briefing` CLI; stored in brain LTM)
- internal/chain/ — Saved multi-tool chain definitions (variables, steps) behind the `chain` CLI and Chain tool
- internal/constants/ — Shared constant values
- internal/datadir/ — Single source of truth for data-directory paths (env overrides)
- internal/httpsafe/ — Outbound-HTTP safety helpers: bounded body reads, SSRF checks
- internal/logging/ — slog-based structured logging with request-context fields; stderr goes through redact.NewWriter
- internal/mcp/ — MCP server exposing Conduit's tools to external MCP clients (bearer auth, calls via the ExecutionEngine)
- internal/procutil/ — Process-execution helpers shared by the Bash tool and others (process groups, platform splits)
- internal/redact/ — Scrubs credentials (e.g. Telegram bot tokens) from strings, errors and log output; NewWriter wraps stderr
- internal/sandbox/ — Single file-path containment check shared by file tools
- internal/stt/ — Speech-to-text (Transcriber interface, Whisper backend)
- internal/vecgo/ — Optional VecGo vector/semantic search service and indexer (module replaced from ./vecgo)
- internal/workspace/ — Workspace context loading and caching (core files, daily memory, beads, security, summaries)
- internal/testing/loadtest/ — Load-test harness behind the `loadtest` CLI

## Config Files

JSON config in configs/ directory with ${ENV_VAR} expansion. Key files:
- configs/config.telegram.json — Telegram channel enabled
- configs/config.tools.json — Tools-focused config
- configs/config.skills.json — Skills-focused config
- configs/config-oauth-test.json — OAuth testing config
- configs/examples/ — Example configs for new setups
- config.live.json — Production (gitignored)
- Database path is `database.path` from the loaded config (default `gateway.db`), NOT derived from the config filename. Server, `token`, `pairing` and TUI all use `auth.ResolveDatabasePath(cfg)`; the global `--database` flag overrides it everywhere. `conduit token info` shows the resolved DB and token-secret source (conduit-31jg.48).

Config struct covers: port, database path, AI providers (Anthropic with OAuth or API key), agent personality/identity/capabilities, workspace context (core files, memory, security, caching), skills, tools (enabled list, max chains, sandbox, services), channels, debug logging, rate limiting (anonymous/authenticated tiers), heartbeat loop, agent heartbeat (quiet hours, alert targets, retry policy), SSH server, brain (tiered memory with configurable salience weights).

## Database

SQLite with WAL mode, 5s busy timeout, NORMAL synchronous, foreign keys enabled, 10000 page cache. Connection pool: max 4 open, 2 idle, no lifetime expiry. There are two independent migration systems.

Gateway DB (internal/database, `schema_migrations`), eight migrations:

1. Sessions and messages tables
2. Auth tokens table (+ schema_migrations table)
3. Telegram pairings table
4. FTS5 virtual tables for document chunks and messages (with sync triggers, backfill)
5. Messages FTS sync triggers
6. Token hash version column (SHA256 v1 → HMAC-SHA256 v2)
7. Ingest dead-letter queue (ingest_dlq) for dropped messages
8. Alert history table (alert_history) for SRE audit trail

Brain DB (internal/brain/migrations.go, `brain_migrations`), nine migrations: 1 brain_ltm; 2 REM support (source_hash, brain_archive, brain_relationships); 3 staleness + source indexes; 4 SPAR brain_reflections; 5 expires_at TTL; 6 warmth; 7 edge last_traversed_at; 8 edge access_count; 9 data migration converting stored salience to base salience by subtracting the configured `recency_weight` (conduit-31jg.53). Because migration 9 depends on configuration, brain migrations run in `brain.New()` after options are applied, and every opener of brain.db (gateway, `briefing`, `brain export`) must pass the configured recency weight.

## Test Patterns

Tests use testing.T with t.TempDir() for isolation. testify assertions available. Integration tests use _integration_test.go suffix. Helper functions like setupTestRegistry() create configured instances with temp directories. No test tags required for standard tests; integration tests in test/integration/ use -tags=integration. Mock provider available at internal/ai/mock_provider.go. Test fixtures in test/fixtures/. Test scripts in test/scripts/.

## Tool SelfTest

Tools can implement the optional `SelfTester` interface to provide diagnostic information about their health and capabilities. This allows AI models to verify a tool is functional before relying on it.

### Interface

```go
// internal/tools/types/types.go
type SelfTester interface {
    SelfTest(ctx context.Context, opts *SelfTestOptions) *SelfTestResult
}

type SelfTestOptions struct {
    Verbose           bool  // Include additional diagnostic detail
    IncludeExamples   bool  // Include usage examples in result
    CheckDependencies bool  // Verify dependencies explicitly
}

type SelfTestResult struct {
    Status                  SelfTestStatus          // "ok", "degraded", "failed"
    Message                 string                  // Human-readable summary
    Dependencies            []DependencyStatus      // Required/optional service status
    Capabilities            []string                // Available features
    UnavailableCapabilities []string                // Features currently non-functional
    Examples                []ToolExample           // Usage examples when functional
    Suggestions             []string                // Actionable hints for issues
    TestDuration            time.Duration           // How long the test took
    Details                 map[string]interface{}  // Verbose diagnostic data
}
```

### Status Levels

- **ok** — Tool is fully functional, all dependencies available
- **degraded** — Partial functionality (e.g., MQTTTool connected but publish disabled, SSHTool configured but not connected)
- **failed** — Tool cannot function (missing required dependencies)

### Registry Methods

```go
// Test a single tool by name
result := registry.SelfTestTool(ctx, "Brain", nil)

// Test all enabled tools (5s timeout per tool)
allResults := registry.SelfTestAll(ctx, &types.SelfTestOptions{Verbose: true})
fmt.Println(allResults.Summary()) // "24 tools tested: 22 healthy, 2 degraded, 0 failed"
```

### Example Implementation (BrainTool)

```go
func (t *BrainTool) SelfTest(ctx context.Context, opts *SelfTestOptions) *SelfTestResult {
    result := &SelfTestResult{Status: SelfTestStatusOK, Capabilities: []string{}}

    // Check required dependency
    if t.services.Brain == nil {
        return &SelfTestResult{
            Status:      SelfTestStatusFailed,
            Message:     "Brain service is not enabled",
            Suggestions: []string{"Enable brain in config.json"},
        }
    }

    // Report capabilities
    result.Capabilities = []string{"store", "get", "recall", "list", "delete", "push", "pop", "peek"}

    // Check optional enhancement
    if t.services.REMCycle != nil {
        result.Capabilities = append(result.Capabilities, "rem_cycle")
    } else {
        result.UnavailableCapabilities = []string{"rem_cycle"}
    }

    // Verbose mode: include runtime stats
    if opts.Verbose {
        status, _ := t.services.Brain.Status(ctx)
        result.Details = map[string]interface{}{"brain_status": status}
    }

    return result
}
```

### Tools with SelfTest

All ~30 tools implement SelfTest: ExecTool (Bash), ReadFileTool, WriteFileTool, ListFilesTool (Glob), EditTool, FindTool, FactsTool, MemorySearchTool, BrainTool, GatewayTool, ContextTool, ChainTool, DebugLogTool, SessionsListTool, SessionsSendTool, SessionsSpawnTool, SessionStatusTool, MessageTool, StatusUpdateTool, TTSTool, CronTool, WebFetchTool, WebSearchTool, ImageTool, MQTTTool, DatadogTool, PagerDutyTool, K8sTool, SSHTool, SRETool, UniFiTool, GoogleWorkspaceTool.

## Agent Gotchas

### "File was modified" system-reminder is NOT a revert (conduit-358j)

When you `Edit` or `Write` a file, you may later see a system-reminder like:

> `Note: /path/to/file.go was modified, either by the user or by a linter. This change was intentional...`

**This is NOT your edit being reverted.** This is Claude Code's built-in file-change-detection telling you the on-disk content differs from the version you last `Read`. It commonly fires when:

- Another process (another subagent in a sibling worktree, a `make build` run, a git operation) touched the file.
- The Wave orchestrator merged a feature branch into `main` while your subagent was running in a different worktree.
- You ran `go build` / `go mod tidy` which touched generated files.

**It does NOT mean a format-on-save hook rewrote your change.** This project has no `PostToolUse` format hook. The pre-commit hook (`.git/hooks/pre-commit`) only runs `br sync --flush-only` at commit time — it never touches `.go` files. There is no `.pre-commit-config.yaml`, no `.air.toml` in the worktree, no `.editorconfig`, no `.vscode/settings.json`.

**Correct response** when you see the reminder:

1. Run `git diff <path>` (or `Read` the file) — compare actual on-disk content to what you intended.
2. If your edit is present: ignore the reminder, keep going.
3. If it is genuinely missing: don't blindly re-Edit. First check `git log -p -- <path>` or look at sibling worktrees (`git worktree list`) to see who else touched the file. Re-applying a half-applied change on top of a concurrent merge is how bad merge states happen.

**Do NOT:**

- Assume a format hook is eating your edits — there isn't one.
- Switch to `Bash` + `cat <<EOF` or `python -c` atomic writes as a workaround; they have the same observation semantics and just hide the real problem.
- Run `go build` "to settle the hook"; the build doesn't touch source files, it just happens to not re-trigger the reminder because no further reads occur.

### Parallel subagents share the repo but not worktrees

During orchestrated swarms, each subagent runs in its own git worktree under `.claude/worktrees/agent-*/`. The worktrees share the same `.git/` object store but have independent working trees. Files like `internal/tools/types/types.go`, `internal/gateway/gateway.go`, and `internal/config/validate.go` are frequently edited by multiple Wave agents — if your ticket touches one of these, expect system-reminders when other agents merge.

### File organization convention (conduit-2clx)

Large files are split within the same package into focused domain files named `<primary>_<aspect>.go` (e.g. `router_turn_lock.go`, `store_messages.go`, `tool_session.go`), with the primary type and constructor kept in the original file. Aim for ~150–500 LOC per file; nothing exceeds ~750. Split files keep the source file's `//go:build` line. When splitting, prove it's a pure move by comparing per-declaration normalized-source fingerprints before/after (the refactor used a small go/ast `declsig` tool); keep structural changes in separate commits. The full system prompt is pinned by `internal/agent/prompt_golden_test.go` (regenerate with `-update-prompt-golden` only for intentional prompt changes).
