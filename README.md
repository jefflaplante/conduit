# Conduit Go Gateway

A clean, high-performance rewrite of the Conduit gateway core in Go, with native channel adapters, vector database integration, and support for legacy TypeScript integrations.

## Architecture

```
                  ┌────────────┐  ┌────────────┐  ┌────────────┐
                  │  Telegram  │  │  TUI Chat  │  │ SSH (Wish) │
                  │   Client   │  │   Client   │  │   Client   │
                  └─────┬──────┘  └─────┬──────┘  └─────┬──────┘
                        │               │               │
┌───────────────────────┼───────────────┼───────────────┼───────┐
│                         Conduit Go Gateway                    │
│                       │               │               │       │
│  ┌────────────────────┴───────────────┴───────────────┴────┐  │
│  │              Channel Manager / WebSocket API            │  │
│  │           Unified adapter lifecycle management          │  │
│  └─────────────────────────────────────────────────────────┘  │
│                               │                               │
│               ┌───────────────┴───────────────┐               │
│               ▼                               ▼               │
│  ┌─────────────────────────┐    ┌─────────────────────────┐   │
│  │   Native Go Adapters    │    │   TypeScript Adapters   │   │
│  │                         │    │                         │   │
│  │  • Telegram             │    │  • WhatsApp (Baileys)   │   │
│  │    (go-telegram/bot)    │    │  • Signal               │   │
│  │  • Discord (planned)    │    │  • Other legacy         │   │
│  │  • Slack (planned)      │    │                         │   │
│  └─────────────────────────┘    └─────────────────────────┘   │
│                                                               │
│  ┌─────────────────────────────────────────────────────────┐  │
│  │  Core Services                                          │  │
│  │  • Session Store (SQLite)   • AI Router (Anthropic/OAI) │  │
│  │  • Tool Registry            • WebSocket API             │  │
│  │  • Config Management        • HTTP Endpoints            │  │
│  │  • Authentication System    • Web Search Integration    │  │
│  │  • Heartbeat Monitoring     • Alert Processing          │  │
│  │  • SSH Server (Wish)        • TUI (BubbleTea)           │  │
│  │  • MQTT Event Ingest        • Kubernetes (client-go)    │  │
│  └─────────────────────────────────────────────────────────┘  │
└───────────────────────────────────────────────────────────────┘
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

See [Getting Started](reference/getting-started.md) for detailed setup instructions.

## Features

### Core Infrastructure
- **Gateway Architecture** — Channel manager with unified lifecycle management
- **Session Management** — SQLite-based persistent session storage
- **AI Provider Routing** — Anthropic and OpenAI with automatic fallback
- **Smart Routing** — Automatic model selection based on task complexity (haiku/sonnet/opus)
- **Context Compaction** — Automatic summarization of long sessions to free context space
- **Tool Registry** — 18 built-in tools with sandbox execution
- **Tool SelfTest** — All built-in tools expose a `SelfTest` capability, allowing the gateway to programmatically verify tool health at startup or on demand

### Access Methods
- **Terminal UI (TUI)** — Full-featured chat client with streaming responses
- **SSH Access** — Remote TUI access via Wish library
- **Telegram Bot** — Native Go adapter with full Bot API support
- **WebSocket API** — Real-time bidirectional communication

### Search & Memory
- **[Brain (Cognitive Memory)](reference/configuration.md#brain-cognitive-memory)** — Tiered memory system: long-term (SQLite), working (per-session), and scratchpad (LIFO stack). Salience-scored with configurable weights, auto-promotion, and sub-agent working memory sharing. **Eliminates context window poisoning** — instead of dumping entire files into the prompt to retrieve a single fact, Brain returns just the fact (30 bytes vs. 12KB+). This dramatically reduces token waste, lowers cost, and keeps the context window clear for actual reasoning — especially critical for smaller models (Haiku, local quantized) where every token counts
- **[REM Sleep Cycle](reference/configuration.md#rem-sleep)** — A 5-phase memory consolidation process inspired by biological sleep. Phases: **Triage** (identify valuable working memory), **Consolidation** (promote high-value entries to LTM), **Pruning** (evict stale/low-value entries), **Integration** (cross-reference with workspace files), and **Grooming** (staleness tracking across all sources). Runs on a configurable schedule to keep long-term memory lean and relevant
- **FTS5 Full-Text Search** — SQLite-based document, message, and brain LTM search
- **Memory Search** — Semantic search across MEMORY.md, session history, and brain entries
- **Web Search** — Hybrid Anthropic native + Brave API fallback

### Automation
- **Chain Workflows** — Multi-tool sequences with dependencies and variables
- **Cron Scheduling** — Recurring task execution
- **[Agent Heartbeat](reference/agent-heartbeat.md)** — Automated HEARTBEAT.md task processing with shared alert queue
- **Skills System** — Extensible AI capabilities via SKILL.md files

### IoT & Home Automation
- **[MQTT Integration](reference/mqtt.md)** — Subscribe to MQTT topics (zigbee2mqtt, Home Assistant) for real-time device data
- **Event Buffering** — In-memory per-topic ring buffers with age-based pruning
- **Heartbeat Monitoring** — Natural language rules in HEARTBEAT.md to check sensors and alert on anomalies

### SRE & Infrastructure
- **[Kubernetes Tool](reference/kubernetes.md)** — Native client-go integration with multi-cluster support, security tiers, pod exec, port forwarding, resource watch
- **PagerDuty** — REST API v2 client with rate limiting for incident management (config ready)
- **Datadog** — Metrics/logs/monitors API client (config ready)

## Providers

Conduit supports multiple AI providers. Configure them in the `ai.providers` array in your config JSON.

### Anthropic (default)

```json
{
  "name": "anthropic",
  "type": "anthropic",
  "model": "claude-sonnet-4-6",
  "auth": {
    "type": "oauth",
    "oauth_token": "${ANTHROPIC_OAUTH_TOKEN}"
  }
}
```

Or with an API key:

```json
{
  "name": "anthropic",
  "type": "anthropic",
  "model": "claude-sonnet-4-6",
  "auth": {
    "type": "api_key",
    "oauth_token": "${ANTHROPIC_API_KEY}"
  }
}
```

### Ollama

```json
{
  "name": "ollama",
  "type": "ollama",
  "base_url": "http://localhost:11434/v1",
  "model": "llama3.1"
}
```

> **Important:** The `base_url` must include `/v1`. Ollama's OpenAI-compatible endpoint is at `/v1/chat/completions`, and Conduit appends `/chat/completions` to the base URL automatically.

No authentication is needed for local Ollama instances.

### OpenAI-compatible

Any OpenAI-compatible API can be used with `type: "openai"`:

```json
{
  "name": "my-provider",
  "type": "openai",
  "base_url": "https://api.example.com/v1",
  "api_key": "${MY_API_KEY}",
  "model": "my-model"
}
```

### Switching providers at runtime

Use `/provider` to list available providers and `/provider <name>` to switch. The model automatically updates to the new provider's default when switching.

### Model aliases

Map short names to full model IDs across providers:

```json
"model_aliases": {
  "haiku": "claude-haiku-4-5-20251001",
  "sonnet": "claude-sonnet-4-6",
  "opus": "claude-opus-4-6",
  "default": "claude-haiku-4-5-20251001"
}
```

Use `/model <alias>` to switch models. The provider auto-resolves based on the model name.

## Documentation

| Document | Description |
|----------|-------------|
| [Getting Started](reference/getting-started.md) | Installation and first-time setup |
| [CLI Reference](reference/cli-reference.md) | All CLI commands and options |
| [Tools Reference](reference/tools-reference.md) | Built-in AI tools documentation |
| [Configuration](reference/configuration.md) | Full configuration reference |
| [TUI & SSH](reference/tui-ssh.md) | Terminal UI and SSH access |
| [Channels](reference/channels.md) | Channel system and adapters |
| [API & Protocol](reference/api-protocol.md) | HTTP endpoints and WebSocket protocol |
| [Skills System](reference/skills.md) | Creating and using skills |
| [Authentication](reference/authentication.md) | Token and OAuth setup |
| [Kubernetes](reference/kubernetes.md) | Multi-cluster K8s tool with security tiers |
| [MQTT Integration](reference/mqtt.md) | MQTT event ingest for IoT/home automation |
| [Security](reference/security.md) | Security considerations |

### Guides
- [Environment & Secrets](reference/guides/ENV_AND_SECRETS.md) — Environment configuration
- [OAuth Setup](reference/guides/OAUTH_SETUP_GUIDE.md) — OAuth device flow setup
- [Telegram Adapter](reference/TELEGRAM_ADAPTER.md) — Telegram bot configuration

### Development
- [Tool Execution Integration](reference/development/TOOL_EXECUTION_INTEGRATION.md) — Tool execution system details

## Project Structure

```
conduit/
├── cmd/gateway/           # CLI entry point and commands
├── internal/
│   ├── gateway/           # Core gateway orchestration
│   ├── ai/                # AI provider routing
│   ├── tools/             # Tool registry and implementations
│   │   ├── k8s/           # Kubernetes tool (client-go)
│   │   ├── ssh/           # SSH remote execution tool
│   │   └── mqtt/          # MQTT tool
│   ├── channels/          # Channel adapters (Telegram, TUI)
│   ├── mqtt/              # MQTT event ingest (zigbee2mqtt, HA)
│   ├── sessions/          # SQLite session storage
│   ├── tui/               # BubbleTea terminal UI
│   ├── ssh/               # Wish SSH server
│   ├── auth/              # Token authentication
│   ├── config/            # Configuration management
│   └── ...
├── vecgo/                 # Vector database library (standalone)
├── reference/             # Documentation
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
