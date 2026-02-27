# Conduit Go Gateway

A clean, high-performance rewrite of the Conduit gateway core in Go, with native channel adapters, vector database integration, and support for legacy TypeScript integrations.

## Architecture

```
                    ┌──────────┐  ┌──────────┐  ┌──────────────┐
                    │ Telegram │  │ TUI Chat │  │ SSH (Wish)   │
                    │  Client  │  │  Client  │  │   Client     │
                    └────┬─────┘  └────┬─────┘  └──────┬───────┘
                         │             │               │
┌────────────────────────┼─────────────┼───────────────┼──────┐
│                   Conduit Go Gateway                       │
│                        │             │               │      │
│  ┌─────────────────────┴─────────────┴───────────────┴──┐   │
│  │            Channel Manager / WebSocket API            │   │
│  │         Unified adapter lifecycle management          │   │
│  └───────────────────────────────────────────────────────┘   │
│                           │                                  │
│           ┌───────────────┴───────────────┐                  │
│           ▼                               ▼                  │
│  ┌─────────────────────┐    ┌─────────────────────────┐      │
│  │  Native Go          │    │  TypeScript Process     │      │
│  │     Adapters        │    │      Adapters           │      │
│  │                     │    │                         │      │
│  │ • Telegram          │    │ • WhatsApp (Baileys)    │      │
│  │   (go-telegram/bot) │    │ • Signal                │      │
│  │ • Discord (planned) │    │ • Other legacy          │      │
│  │ • Slack (planned)   │    │                         │      │
│  └─────────────────────┘    └─────────────────────────┘      │
│                                                              │
│  ┌───────────────────────────────────────────────────────┐   │
│  │  Core Services                                        │   │
│  │  • Session Store (SQLite)  • AI Router (Anthropic/OAI)│   │
│  │  • Tool Registry           • WebSocket API            │   │
│  │  • Config Management       • HTTP Endpoints           │   │
│  │  • Authentication System   • Web Search Integration   │   │
│  │  • Heartbeat Monitoring    • Alert Processing         │   │
│  │  • SSH Server (Wish)       • TUI (BubbleTea)          │   │
│  └───────────────────────────────────────────────────────┘   │
└──────────────────────────────────────────────────────────────┘
```

## Performance

| Metric | TypeScript | Go | Improvement |
|--------|------------|----|-----------|
| Memory (idle) | 150MB | 15MB | **90% less** |
| Memory (1000 sessions) | 300MB | 60MB | **80% less** |
| Startup time | 8s | 2s | **75% faster** |
| Message latency | 150ms | 50ms | **67% faster** |
| Concurrent connections | 1,000 | 10,000+ | **10x more** |

## Quick Start

```bash
# Build
make build

# Create auth token
./bin/gateway token create --client-name "my-client"

# Start server
./bin/gateway server

# Launch terminal UI
./bin/gateway tui --token "conduit_v1_..."
```

See [Getting Started](docs/getting-started.md) for detailed setup instructions.

## Features

### Core Infrastructure
- **Gateway Architecture** — Channel manager with unified lifecycle management
- **Session Management** — SQLite-based persistent session storage
- **AI Provider Routing** — Anthropic and OpenAI with automatic fallback
- **Tool Registry** — 18 built-in tools with sandbox execution

### Access Methods
- **Terminal UI (TUI)** — Full-featured chat client with streaming responses
- **SSH Access** — Remote TUI access via Wish library
- **Telegram Bot** — Native Go adapter with full Bot API support
- **WebSocket API** — Real-time bidirectional communication

### Search & Memory
- **FTS5 Full-Text Search** — SQLite-based document and message search
- **Memory Search** — Semantic search across MEMORY.md and session history
- **Web Search** — Hybrid Anthropic native + Brave API fallback

### Automation
- **Chain Workflows** — Multi-tool sequences with dependencies and variables
- **Cron Scheduling** — Recurring task execution
- **Heartbeat Loop** — Automated HEARTBEAT.md task processing
- **Skills System** — Extensible AI capabilities via SKILL.md files

## Documentation

| Document | Description |
|----------|-------------|
| [Getting Started](docs/getting-started.md) | Installation and first-time setup |
| [CLI Reference](docs/cli-reference.md) | All CLI commands and options |
| [Tools Reference](docs/tools-reference.md) | Built-in AI tools documentation |
| [Configuration](docs/configuration.md) | Full configuration reference |
| [TUI & SSH](docs/tui-ssh.md) | Terminal UI and SSH access |
| [Channels](docs/channels.md) | Channel system and adapters |
| [API & Protocol](docs/api-protocol.md) | HTTP endpoints and WebSocket protocol |
| [Skills System](docs/skills.md) | Creating and using skills |
| [Authentication](docs/authentication.md) | Token and OAuth setup |
| [Security](docs/security.md) | Security considerations |

### Guides
- [Environment & Secrets](docs/guides/ENV_AND_SECRETS.md) — Environment configuration
- [OAuth Setup](docs/guides/OAUTH_SETUP_GUIDE.md) — OAuth device flow setup
- [Telegram Adapter](docs/TELEGRAM_ADAPTER.md) — Telegram bot configuration

### Development
- [Architecture](docs/development/ARCHITECTURE.md) — System design details
- [Core Tools Implementation](docs/development/CORE_TOOLS_IMPLEMENTATION.md)
- [Agent System](docs/development/AGENT_SYSTEM_IMPLEMENTATION.md)

## Project Structure

```
conduit/
├── cmd/gateway/           # CLI entry point and commands
├── internal/
│   ├── gateway/           # Core gateway orchestration
│   ├── ai/                # AI provider routing
│   ├── tools/             # Tool registry and implementations
│   ├── channels/          # Channel adapters (Telegram, TUI)
│   ├── sessions/          # SQLite session storage
│   ├── tui/               # BubbleTea terminal UI
│   ├── ssh/               # Wish SSH server
│   ├── auth/              # Token authentication
│   ├── config/            # Configuration management
│   └── ...
├── vecgo/                 # Vector database library (standalone)
├── docs/                  # Documentation
├── configs/               # Example configurations
└── Makefile
```

## Development

```bash
make build          # Build binary
make test           # Run tests
make lint           # Run linters
make dev            # Auto-restart on changes (requires 'air')
make health         # Check if running
```

Run specific tests:
```bash
go test -v -run TestFunctionName ./internal/package/...
go test -v ./internal/tools/...
```

## Task Management

Uses [beads-rust](https://github.com/yourorg/beads-rust) (`br` command) for task tracking:

```bash
br ready                    # Show actionable tasks
br create "task title"      # Create task
br close br-abc123          # Complete task
br sync --flush-only        # Export to git
```

## License

MIT License - see [LICENSE](LICENSE) for details.
