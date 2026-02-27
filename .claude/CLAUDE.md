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

Go 1.24.2 (toolchain 1.24.13), single binary gateway. Pure Go SQLite via modernc.org/sqlite (no CGO). CLI via Cobra (cmd/gateway/main.go). Default port 18789.

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
- types.Tool (internal/tools/types/types.go) — All tools implement Name(), Description(), Parameters(), Execute(ctx, args). Optional interfaces: EnhancedSchemaProvider, ParameterValidator, ParameterDiscoverer, UsageExampleProvider.
- types.GatewayService (internal/tools/types/types.go) — Gateway operations exposed to tools without circular imports. Includes session, channel, config, metrics, and scheduler operations.
- types.ChannelSender (internal/tools/types/types.go) — Channel message sending exposed to tools.
- types.SearchService (internal/tools/types/types.go) — FTS5 full-text search interface over documents and messages.
- agent.AgentSystem (internal/agent/interface.go) — Concrete agent system interface with Name(), BuildSystemPrompt, SetTools, ProcessResponse. Includes SessionStateManager for tracking processing states.

### Dependency Injection Pattern

Tools cannot import gateway directly (circular dependency). Instead, types.ToolServices struct in internal/tools/types/types.go aggregates service interfaces (SessionStore, ConfigMgr, WebClient, SkillsManager, ChannelSender, Gateway, Searcher, SchemaBuilder). The gateway creates services, then calls registry.SetServices() after construction.

## CLI Commands

The binary is `bin/conduit`. Default behavior (no subcommand) starts the server.

- `server` — Start the gateway (default)
- `version` — Show version, git commit, build date
- `token` — Token management (create, list, revoke)
- `pairing` — Telegram user pairing management
- `tools` — Tool discovery (list, describe, schema, examples)
- `tui` — Launch the BubbleTea terminal UI client
- `ssh` — Start the SSH server
- `ssh-keys` — SSH key management (list, add, remove, init)
- `maintenance` — Database maintenance (run, run-task, status, config)
- `backup` — Backup/restore gateway data (create, restore, list)

## Package Layout

- cmd/gateway/ — Entry point and CLI command definitions (main.go, backup.go, maintenance.go, ssh.go, ssh_keys.go, tui.go, tools.go, pairing.go)
- internal/gateway/ — Core gateway orchestration, WebSocket handling, HTTP endpoints, context usage tracking, direct client for TUI, heartbeat integration
- internal/ai/ — AI provider routing, conversation management, tool execution loops, streaming
- internal/agent/ — Agent personality system: interface definition, Conduit agent implementation, prompt builder with section-based prompt construction
- internal/models/ — Anthropic API request/response models and request builder
- internal/tools/ — Tool registry (registry.go ~34KB), execution engine with parallel support, plus top-level tool files:
  - aliases.go — Anthropic tool alias resolution (unversioned name → versioned name) with env override
  - anthropic.go — Anthropic versioned tool name constants (web_search, web_fetch)
  - unifi.go — UniFi Network/Protect API tool
  - web_search.go — Web search tool implementation at registry level
  - execution.go, execution_adapter.go — Tool execution engine and adapter
  - planning_execution.go — Planning-to-execution bridge
  - Tool subdirectories:
    - core/ — Context management, file editing, gateway control, memory search, session management
    - web/ — Web search (Brave/Anthropic), web fetch with HTML parsing
    - communication/ — Message sending to channels, TTS
    - scheduling/ — Cron job tool (includes heartbeat cron integration)
    - vision/ — Image analysis
    - planning/ — Planning engine with dependency resolution, optimization, caching, metrics
    - schema/ — Dynamic schema enhancement and parameter discovery
    - validation/ — Parameter validation
    - errors/ — Tool error types
- internal/tools/types/ — Single source of truth for tool-related types and service interfaces (Tool, ToolServices, GatewayService, ChannelSender, SearchService)
- internal/channels/ — Channel adapter interface + manager; subdirectories:
  - telegram/ — Native Telegram adapter with pairing system (pairing storage, CLI, photo support)
  - tui/ — TUI channel adapter with factory for in-process BubbleTea connections
- internal/sessions/ — SQLite session store with state tracking
- internal/config/ — JSON config loading with ${ENV_VAR} expansion. Config struct includes: port, database, AI, agent, workspace, skills, tools, channels, debug, rate limiting, heartbeat, agent heartbeat, SSH
- internal/database/ — SQLite migration system (4 migrations: sessions/messages, auth tokens, telegram pairings, FTS5 search)
- internal/fts/ — FTS5 full-text search: document chunking, indexing, and search queries (Porter stemming, unicode61 tokenizer)
- internal/search/ — Web search routing: Brave API, Anthropic search, result caching, strategy selection
- internal/auth/ — Token auth (128-bit entropy, Base58, SHA256 hash storage), OAuth support, CLI token management
- internal/backup/ — Backup/restore system: create tar.gz archives of database, config, workspace, SSH keys, skills; restore with dry-run support; list/inspect archives
- internal/middleware/ — HTTP auth, WebSocket auth, rate limiting
- internal/ratelimit/ — Sliding window rate limiter implementation
- internal/monitoring/ — Gateway metrics, event tracking, metric aggregation, heartbeat metrics
- internal/heartbeat/ — HEARTBEAT.md task execution, alert queue with priority routing, severity-based delivery, result processing, task types
- internal/skills/ — Skill discovery from SKILL.md files, loading, validation, tool adaptation, manager
- internal/maintenance/ — Database cleanup and maintenance scheduling
- internal/scheduler/ — Cron job scheduling with interfaces
- internal/ssh/ — SSH server via Wish with key management (server.go, keys.go)
- internal/tui/ — BubbleTea terminal UI: chat view, sidebar, tab bar, status bar, tool activity display, Lipgloss styling, client interface
- internal/version/ — Version info injected via ldflags at build time
- pkg/protocol/ — Message type definitions (messages.go) shared across packages
- pkg/tokens/ — Token generation (generator.go) and formatting (format.go) utilities

## Config Files

JSON config in configs/ directory with ${ENV_VAR} expansion. Key files:
- configs/config.telegram.json — Telegram channel enabled
- configs/config.tools.json — Tools-focused config
- configs/config.skills.json — Skills-focused config
- configs/config-oauth-test.json — OAuth testing config
- configs/examples/ — Example configs for new setups
- config.live.json — Production (gitignored)
- Database path auto-detected from config filename (e.g., config.telegram.json → config.telegram.db)

Config struct covers: port, database path, AI providers (Anthropic with OAuth or API key), agent personality/identity/capabilities, workspace context (core files, memory, security, caching), skills, tools (enabled list, max chains, sandbox, services), channels, debug logging, rate limiting (anonymous/authenticated tiers), heartbeat loop, agent heartbeat (quiet hours, alert targets, retry policy), SSH server.

## Database

SQLite with WAL mode, 5s busy timeout, NORMAL synchronous, foreign keys enabled, 10000 page cache. Connection pool: max 4 open, 2 idle, no lifetime expiry. Four migrations:

1. Sessions and messages tables
2. Auth tokens table (+ schema_migrations table)
3. Telegram pairings table
4. FTS5 virtual tables for document chunks and messages (with sync triggers, backfill)

## Test Patterns

Tests use testing.T with t.TempDir() for isolation. testify assertions available. Integration tests use _integration_test.go suffix. Helper functions like setupTestRegistry() create configured instances with temp directories. No test tags required for standard tests; integration tests in test/integration/ use -tags=integration. Mock provider available at internal/ai/mock_provider.go. Test fixtures in test/fixtures/. Test scripts in test/scripts/.
