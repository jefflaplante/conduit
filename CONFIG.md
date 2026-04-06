# CONFIG.md — Conduit-Go Configuration Reference

Complete reference for every `config.json` option, how each option behaves, how options interact, and what you need for each use case.

---

## Table of Contents

- [Minimal Config](#minimal-config)
- [Environment Variable Expansion](#environment-variable-expansion)
- [Path Resolution Rules](#path-resolution-rules)
- [Top-Level Options](#top-level-options)
- [database](#database)
- [search](#search)
- [ai](#ai)
- [agent](#agent)
- [workspace](#workspace)
- [tools](#tools)
- [channels](#channels)
- [heartbeat](#heartbeat)
- [agent_heartbeat](#agent_heartbeat)
- [rateLimiting](#ratelimiting)
- [ssh](#ssh)
- [vector](#vector)
- [debug](#debug)
- [stt](#stt)
- [brain](#brain)
- [mqtt](#mqtt)
- [kubernetes](#kubernetes)
- [pagerduty](#pagerduty)
- [datadog](#datadog)
- [remote_ssh](#remote_ssh)
- [websocket](#websocket)
- [diagnostics](#diagnostics)
- [logging](#logging)
- [auth](#auth-1)
- [tui](#tui)
- [Use-Case Recipes](#use-case-recipes)

---

## Minimal Config

The absolute minimum to get a running gateway with AI responses over WebSocket:

```json
{
  "port": 18789,
  "database": {
    "path": "./gateway.db"
  },
  "ai": {
    "default_provider": "anthropic",
    "providers": [
      {
        "name": "anthropic",
        "type": "anthropic",
        "api_key": "${ANTHROPIC_API_KEY}",
        "model": "claude-sonnet-4-20250514"
      }
    ]
  },
  "tools": {
    "enabled_tools": [],
    "sandbox": {
      "workspace_dir": "./workspace",
      "allowed_paths": ["./workspace"]
    }
  },
  "channels": []
}
```

This gives you: HTTP health endpoint on `:18789`, WebSocket chat at `/ws`, SQLite database auto-created and migrated, no tools, no channels, no personality. Everything else below builds on top of this.

---

## Environment Variable Expansion

All string values in the config support `${ENV_VAR}` syntax. Variables are expanded at load time. If a variable is not set, it expands to an empty string.

```json
{
  "api_key": "${ANTHROPIC_API_KEY}",
  "bot_token": "${TELEGRAM_BOT_TOKEN}"
}
```

This works in all string fields at any nesting depth, including inside `channels[].config` maps and `tools.services` maps.

---

## Path Resolution Rules

Relative paths in the config are resolved **relative to the config file's directory**, not the working directory. This matters for:

- `database.path`
- `workspace.context_dir`
- `tools.sandbox.workspace_dir`
- `tools.sandbox.allowed_paths`
- `channels[].config.session_dir`

If your config is at `/opt/conduit/configs/config.json` and contains `"path": "./gateway.db"`, the database is created at `/opt/conduit/configs/gateway.db`.

**Database path auto-detection:** When the `--database` CLI flag is not specified, the database filename is derived from the config filename. `config.telegram.json` yields `config.telegram.db`. The default `config.json` yields `gateway.db`.

---

## Top-Level Options

### `port`

| | |
|---|---|
| Type | `int` |
| Default | `18789` |
| CLI override | `--port`, `-p` |

The HTTP/WebSocket server listen port. The health endpoint is at `GET /health` and the WebSocket endpoint is at `/ws`.

### `timezone`

| | |
|---|---|
| Type | `string` |
| Default | `""` (uses system local time) |

IANA timezone identifier (e.g., `"America/Los_Angeles"`, `"UTC"`). Used for scheduling, logging timestamps, and quiet hours. Falls back to `agent_heartbeat.timezone` if empty, then to system local time.

### `data_dir`

| | |
|---|---|
| Type | `string` |
| Default | `""` |

Base directory for data files. Supports `~/` expansion. Optional — most paths can be specified individually.

### `secrets_file`

| | |
|---|---|
| Type | `string` |
| Default | `""` |

Path to a KEY=VALUE secrets file (like `.env`). Loaded before environment variable expansion. Supports `~/` expansion. Lines starting with `#` are comments. Existing environment variables take precedence over file values.

```
# Example secrets file
ANTHROPIC_API_KEY=sk-ant-...
TELEGRAM_BOT_TOKEN=123456:ABC...
BRAVE_API_KEY=BSA...
```

---

## `database`

```json
{
  "database": {
    "path": "./gateway.db"
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `path` | string | `"gateway.db"` | SQLite database file path |

The database is auto-created on first run. Four migrations run automatically:

1. `sessions` + `messages` tables
2. `auth_tokens` table
3. `telegram_pairings` table
4. `document_chunks` + FTS5 virtual tables + sync triggers

SQLite is configured with: WAL journal mode, 5s busy timeout, NORMAL synchronous, foreign keys enabled, 10000 page cache. No external database server or CGO required.

---

## `search`

Dedicated search database for FTS5 indices and beads issue indexing. Separated from the main gateway database for independent index management.

```json
{
  "search": {
    "enabled": true,
    "path": "./gateway.search.db",
    "beads_dir": ".beads"
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `true` | Enable search database. When disabled, falls back to grep-based search |
| `path` | string | derived | Path to search database. Defaults to `<gateway-db>.search.db` |
| `beads_dir` | string | `".beads"` | Directory containing beads issues.jsonl for indexing |

---

## `ai`

```json
{
  "ai": {
    "default_provider": "anthropic",
    "providers": [
      {
        "name": "anthropic",
        "type": "anthropic",
        "api_key": "${ANTHROPIC_API_KEY}",
        "model": "claude-sonnet-4-20250514"
      }
    ]
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `default_provider` | string | `"anthropic"` | Which provider name to use for requests |
| `providers` | array | see below | List of configured providers |
| `model_aliases` | object | see below | Map of alias names to full model identifiers |
| `smart_routing` | object | see below | Intelligent model routing configuration |

### Model aliases

```json
{
  "model_aliases": {
    "haiku": "claude-haiku-4-5-20251001",
    "sonnet": "claude-sonnet-4-6",
    "opus": "claude-opus-4-6",
    "default": "claude-haiku-4-5-20251001"
  }
}
```

Aliases let you reference models by short names. The defaults above are built-in; config values override them.

### Smart routing

```json
{
  "smart_routing": {
    "enabled": true,
    "track_usage": true,
    "cost_budget_daily": 10.00,
    "pricing_overrides": {
      "claude-sonnet-4-6": {
        "input_per_m_token": 3.00,
        "output_per_m_token": 15.00
      }
    }
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable smart routing |
| `track_usage` | bool | `false` | Track token usage and costs |
| `cost_budget_daily` | float | `0` | Daily cost budget in USD (0 = unlimited) |
| `pricing_overrides` | object | `{}` | Override default model pricing |

### Provider fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Identifier used by `default_provider` to select this provider |
| `type` | string | yes | Provider type: `"anthropic"` or `"openai"` |
| `api_key` | string | conditional | API key. Required unless using OAuth |
| `base_url` | string | no | Custom API base URL for local/compatible servers (e.g., Ollama) |
| `model` | string | yes | Model identifier (e.g., `"claude-sonnet-4-20250514"`, `"claude-opus-4-6"`) |
| `auth` | object | no | OAuth configuration (alternative to api_key) |

### Auth (OAuth) fields

| Field | Type | Description |
|-------|------|-------------|
| `auth.type` | string | `"oauth"` or `"api_key"` |
| `auth.oauth_token` | string | OAuth access token (e.g., `"${ANTHROPIC_OAUTH_TOKEN}"`) |
| `auth.refresh_token` | string | OAuth refresh token (optional) |
| `auth.expires_at` | int64 | Token expiry unix timestamp (optional) |
| `auth.client_id` | string | OAuth client ID (optional) |
| `auth.client_secret` | string | OAuth client secret (optional) |

**Choosing API key vs OAuth:** API key is simpler — one environment variable and you're done. OAuth is used for Claude Code integration where tokens are managed by the Claude OAuth flow. If both `api_key` and `auth.oauth_token` are set, OAuth takes precedence. The `agent.identity` config controls which system prompt identity is used for each auth type.

**Multiple providers:** You can define several providers with different names and models. Only the one matching `default_provider` is used. This lets you keep configs for quick model switching:

```json
{
  "providers": [
    { "name": "sonnet", "type": "anthropic", "model": "claude-sonnet-4-20250514", "api_key": "${ANTHROPIC_API_KEY}" },
    { "name": "opus", "type": "anthropic", "model": "claude-opus-4-6", "api_key": "${ANTHROPIC_API_KEY}" }
  ],
  "default_provider": "sonnet"
}
```

### Context compaction

Automatic context compaction for long sessions. When context window usage exceeds the threshold, older messages are summarized to free up context space. Nested under `ai`.

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
| `compaction.enabled` | bool | `false` | Enable automatic context compaction |
| `compaction.threshold` | float | `0.70` | Context window usage fraction that triggers compaction |
| `compaction.model` | string | `"claude-haiku-4-5-20251001"` | Model for generating summaries |
| `compaction.recent_messages_to_keep` | int | `10` | Most recent messages to preserve without summarization |

### Prompt caching

Anthropic prompt caching to reduce costs on repeated requests. Nested under `ai`.

```json
{
  "ai": {
    "prompt_caching": {
      "enabled": true,
      "extended_ttl": true,
      "cache_tools": true,
      "cache_system": true,
      "cache_history": true,
      "history_breakpoint_interval": 10
    }
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `prompt_caching.enabled` | bool | `false` | Master switch for prompt caching |
| `prompt_caching.extended_ttl` | bool | `false` | Use 1-hour cache TTL instead of 5-minute default |
| `prompt_caching.cache_tools` | bool | `false` | Cache tool definitions |
| `prompt_caching.cache_system` | bool | `false` | Cache system prompt |
| `prompt_caching.cache_history` | bool | `false` | Cache conversation history |
| `prompt_caching.history_breakpoint_interval` | int | `0` | Messages between history cache breakpoints |

### Provider context window override

Override the auto-detected context window size for a provider:

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `context_window` | int | `0` (auto-detect) | Override context window size in tokens per provider entry |

---

## `agent`

Controls the AI agent's personality, identity, and capabilities. Optional — if omitted the gateway works but the agent has no personality or special behaviors.

```json
{
  "agent": {
    "name": "Conduit",
    "personality": "conduit",
    "identity": {
      "oauth_identity": "You are Claude Code, Anthropic's official CLI for Claude.",
      "api_key_identity": "You are Jules, an AI assistant powered by Claude."
    },
    "capabilities": {
      "memory_recall": true,
      "tool_chaining": true,
      "skills_integration": false,
      "heartbeats": true,
      "silent_replies": true
    }
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `name` | string | `"Jules"` | Agent display name, used in prompts |
| `personality` | string | `"conduit"` | Personality module to load. Currently only `"conduit"` |
| `identity.oauth_identity` | string | see above | System prompt preamble when using OAuth auth |
| `identity.api_key_identity` | string | see above | System prompt preamble when using API key auth |
| `capabilities.memory_recall` | bool | `true` | Load MEMORY.md and daily memory logs into context |
| `capabilities.tool_chaining` | bool | `true` | Allow multi-step tool use in a single turn |
| `capabilities.skills_integration` | bool | `true` | Enable skills system integration |
| `capabilities.heartbeats` | bool | `true` | Enable heartbeat task processing |
| `capabilities.silent_replies` | bool | `true` | Allow agent to reply with empty content (for background tasks) |

### History settings

Controls how conversation history is retrieved and budgeted.

```json
{
  "agent": {
    "history": {
      "max_tokens": 16000,
      "min_messages": 4,
      "max_messages": 100,
      "chars_per_token": 4
    }
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `history.max_tokens` | int | `16000` | Target token budget for conversation history |
| `history.min_messages` | int | `4` | Minimum recent messages to include (even if over budget) |
| `history.max_messages` | int | `100` | Absolute cap on messages regardless of token budget |
| `history.chars_per_token` | int | `4` | Estimated characters per token for budgeting |

### Prompt scaling settings

Controls dynamic system prompt scaling for small-context models.

```json
{
  "agent": {
    "prompt_scaling": {
      "large_context_threshold": 128000,
      "prompt_budget_percent": 15,
      "chars_per_token": 4
    }
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `prompt_scaling.large_context_threshold` | int | `128000` | Context window size (tokens) above which all prompt sections are included |
| `prompt_scaling.prompt_budget_percent` | int | `15` | Percentage of context window allocated to system prompt for small-context models |
| `prompt_scaling.chars_per_token` | int | `4` | Estimated characters per token for budget math |

**Interaction with `workspace`:** When `memory_recall` is true, the agent loads MEMORY.md and `memory/*.md` files from `workspace.context_dir`. When `heartbeats` is true, the agent reads HEARTBEAT.md from the same directory.

### Agent email identity

```json
{
  "agent": {
    "email": {
      "address": "conduit@example.com",
      "aliases": ["assistant@example.com"],
      "display_name": "Conduit AI"
    }
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `email.address` | string | `""` | Primary email address for the agent |
| `email.aliases` | string array | `[]` | Additional email addresses the agent responds to |
| `email.display_name` | string | `""` | Display name in email headers |

### Operating principles override

Override the default operating principles injected into the system prompt:

```json
{
  "agent": {
    "identity": {
      "operating_principles": [
        "Be concise and direct",
        "Always verify before acting"
      ]
    }
  }
}
```

When omitted, the agent uses built-in defaults. Setting this replaces them entirely.

---

## `workspace`

Controls workspace context files — the agent's personality, memory, and knowledge. These files are loaded into the system prompt at the start of each conversation.

```json
{
  "workspace": {
    "context_dir": "./workspace",
    "files": {
      "core": ["SOUL.md", "USER.md", "AGENTS.md", "TOOLS.md", "IDENTITY.md", "MEMORY.md", "HEARTBEAT.md"],
      "memory": {
        "enabled": true,
        "daily_lookback_days": 2,
        "max_file_size_kb": 512
      }
    },
    "security": {
      "enforce_access_rules": true,
      "memory_main_only": true
    },
    "caching": {
      "enabled": true,
      "ttl_seconds": 300,
      "max_cache_size_mb": 50
    }
  }
}
```

### Core fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `context_dir` | string | `"./workspace"` | Root directory for all context files |
| `files.core` | string array | see above | Filenames to look for in `context_dir` |

### Context files and their roles

| File | Loaded in | Purpose |
|------|-----------|---------|
| `SOUL.md` | All sessions | Agent personality and core identity |
| `USER.md` | All sessions | Information about the human user |
| `AGENTS.md` | All sessions | Operational instructions and behaviors |
| `TOOLS.md` | All sessions | Local tool usage guidance |
| `IDENTITY.md` | All sessions | Additional identity context |
| `MEMORY.md` | Main sessions only | Long-term memory (security-restricted) |
| `HEARTBEAT.md` | Main sessions only | Recurring task instructions (security-restricted) |
| `memory/*.md` | All sessions | Daily memory logs, format: `YYYY-MM-DD.md` |

All files are optional. Missing files are silently skipped. The gateway creates none of these automatically — the agent can create and update them at runtime via file tools if enabled.

### Memory settings

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `files.memory.enabled` | bool | `true` | Load daily memory logs from `memory/` subdirectory |
| `files.memory.daily_lookback_days` | int | `2` | How many days of memory logs to load (today + N-1 previous) |
| `files.memory.max_file_size_kb` | int | `512` | Skip memory files larger than this |

### Security settings

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `security.enforce_access_rules` | bool | `true` | Enable file-level access control |
| `security.memory_main_only` | bool | `true` | Restrict MEMORY.md and HEARTBEAT.md to "main" sessions |

Session types: `"main"` (direct/private conversations), `"shared"` (group chats), `"isolated"` (sub-agents). When `memory_main_only` is true, shared and isolated sessions cannot see MEMORY.md or HEARTBEAT.md — this prevents private information from leaking into group contexts.

### Cache settings

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `caching.enabled` | bool | `true` | Cache loaded context files in memory |
| `caching.ttl_seconds` | int | `300` | Cache time-to-live (5 minutes) |
| `caching.max_cache_size_mb` | int | `50` | Maximum memory for cached files |

### Interaction with `tools.sandbox`

`workspace.context_dir` and `tools.sandbox.workspace_dir` are **separate concepts** that often point to the same directory:

- **`workspace.context_dir`** — Where the agent's context files (SOUL.md, MEMORY.md, etc.) are read from. Also where the scheduler stores `cron_jobs.json` and the FTS5 indexer looks for documents to index.
- **`tools.sandbox.workspace_dir`** — The root directory that file tools (Read, Write, Edit, Glob) are sandboxed to. Tools cannot access files outside `workspace_dir` and `allowed_paths`.

If you want the agent to be able to read and write its own context files, both should point to the same directory (or `context_dir` should be within `allowed_paths`).

### Workspace summarization

AI-powered summarization of workspace files for small-context models. When context budget is tight, files are summarized instead of included in full.

```json
{
  "workspace": {
    "summary": {
      "enabled": true,
      "model": "claude-haiku-4-5-20251001",
      "target_ratio": 0.25,
      "cache_dir": ".summaries",
      "cache_ttl_hours": 168,
      "fallback_to_truncate": true
    }
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `summary.enabled` | bool | `false` | Enable AI-powered summarization |
| `summary.model` | string | `"claude-haiku-4-5-20251001"` | Model for summarization |
| `summary.target_ratio` | float | `0.25` | Default compression ratio (0.25 = keep 25%) |
| `summary.cache_dir` | string | `".summaries"` | Directory for persisted summaries |
| `summary.cache_ttl_hours` | int | `168` | How long cached summaries are valid (7 days) |
| `summary.fallback_to_truncate` | bool | `true` | Use truncation if AI summarization fails |

---

## `tools`

Controls which tools are available to the AI and how they're sandboxed.

```json
{
  "tools": {
    "enabled_tools": [
      "Read", "Write", "Edit", "Bash", "Glob",
      "MemorySearch", "WebSearch", "WebFetch",
      "Message", "Tts", "Cron", "Image",
      "SessionsList", "SessionsSend", "SessionsSpawn", "SessionStatus",
      "Gateway", "UniFi"
    ],
    "max_tool_chains": 25,
    "sandbox": {
      "workspace_dir": "./workspace",
      "allowed_paths": ["./workspace", "/tmp"]
    },
    "services": {
      "brave": {
        "api_key": "${BRAVE_API_KEY}"
      },
      "tts": {
        "provider": "edge",
        "voice": "en-US-AriaNeural"
      }
    }
  }
}
```

### Core fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled_tools` | string array | `[]` | Tool names to enable. Only enabled tools are presented to the AI |
| `max_tool_chains` | int | `25` | Maximum tool calls per single conversation turn |
| `max_tool_result_chars` | int | `8192` | Maximum characters in tool result content. Outputs exceeding this are truncated |

### Available tools

The `enabled_tools` values must match the internal tool names exactly:

| Tool name | Package | Description |
|-----------|---------|-------------|
| `Read` | core | Read file contents from workspace |
| `Write` | core | Write/create files in workspace |
| `Edit` | core | Line-based file editing |
| `Bash` | core | Execute shell commands |
| `Glob` | core | List directory contents and find files |
| `MemorySearch` | core | FTS5 full-text search over workspace documents |
| `SessionsList` | core | List active sessions |
| `SessionsSend` | core | Send messages to other sessions |
| `SessionsSpawn` | core | Spawn sub-agent sessions |
| `SessionStatus` | core | Get session status and metadata |
| `Gateway` | core | Gateway operations (status, restart, config, metrics, channels, scheduler) |
| `WebSearch` | web | Web search via Brave API or Anthropic search |
| `WebFetch` | web | Fetch and parse web pages to markdown |
| `Message` | communication | Send messages to channels (Telegram, etc.) |
| `Tts` | communication | Text-to-speech generation |
| `Cron` | scheduling | Create and manage cron jobs |
| `Image` | vision | Image analysis |
| `UniFi` | infrastructure | UniFi Network/Protect device and camera management |

### Sandbox

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `sandbox.workspace_dir` | string | `"./workspace"` | Root directory for file tool operations |
| `sandbox.allowed_paths` | string array | `["./workspace", "/tmp"]` | Additional paths file tools can access |

File tools (Read, Write, Edit, Glob) are restricted to `workspace_dir` and `allowed_paths`. The Bash tool can execute commands but file operations from tools are sandboxed. If a tool tries to access a path outside these boundaries, it returns an error.

### Services

The `services` map provides tool-specific configuration. Each key is a service name, and the value is a map of settings. Environment variables are expanded in all string values.

| Service | Fields | Used by |
|---------|--------|---------|
| `brave` | `api_key` | WebSearch tool — Brave Search API key |
| `tts` | `provider`, `voice` | Tts tool — TTS engine and voice selection |
| `search` | `provider` | WebSearch tool — search provider preference (`"brave"` or `"anthropic"`) |

**UniFi tool** does not use `services` — it reads `UNVR_URL` and `UNVR_API_KEY` environment variables directly.

### Tool chain limit

`max_tool_chains` limits how many tool calls the AI can make in a single conversation turn before being forced to respond. This prevents runaway loops. Must be > 0. Values below 10 generate a warning — complex tasks may need 15-25 tool calls.

---

## `channels`

Configures external communication channels. Each channel is an adapter that connects the gateway to a messaging platform.

```json
{
  "channels": [
    {
      "name": "telegram",
      "type": "telegram",
      "enabled": true,
      "config": {
        "bot_token": "${TELEGRAM_BOT_TOKEN}"
      }
    }
  ]
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Unique channel identifier |
| `type` | string | yes | Channel type: `"telegram"` or `"whatsapp"` |
| `enabled` | bool | yes | Whether this channel is active |
| `config` | object | yes | Channel-specific configuration (varies by type) |

### Telegram config

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `bot_token` | string | yes | Telegram Bot API token from @BotFather |
| `webhook_mode` | bool | no | Use webhooks instead of long polling (default: `false`) |
| `webhook_url` | string | conditional | Public URL for webhook mode |
| `debug` | bool | no | Enable Telegram API debug logging (default: `false`) |
| `groups` | object | no | Group chat access control. Keys are group chat IDs |
| `groups.<id>.requireMention` | bool | no | If `true`, bot only responds when @mentioned in this group |
| `groupPolicy` | string | no | `"allowlist"` — only respond in listed groups. If not set, responds in all groups |

**Telegram group access control example:**

```json
{
  "config": {
    "bot_token": "${TELEGRAM_BOT_TOKEN}",
    "groups": {
      "-1001234567890": { "requireMention": false },
      "-1009876543210": { "requireMention": true }
    },
    "groupPolicy": "allowlist"
  }
}
```

With `groupPolicy: "allowlist"`, the bot ignores messages from groups not listed. Within listed groups, `requireMention` controls whether every message triggers a response or only @mentions.

### WhatsApp config

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `session_dir` | string | no | Directory for WhatsApp session data (default: `"./sessions/whatsapp"`) |
| `adapter_path` | string | no | Path to the Node.js WhatsApp adapter script |

WhatsApp uses an external TypeScript adapter process. The session directory stores authentication state — losing it requires re-pairing with WhatsApp.

### Telegram photo vision

When a Telegram channel is enabled, incoming photos are automatically downloaded and sent to the AI as image content blocks for vision analysis. No additional configuration is needed.

**Behavior:** When a user sends a photo, Conduit downloads the highest-resolution version (up to 20 MB), detects the MIME type, and passes it inline to the AI provider. The AI can then describe or analyze the image. Captions are included as accompanying text.

- **Anthropic providers** receive native `image` content blocks (requires Claude 3+ vision models)
- **OpenAI / Ollama providers** receive `image_url` content blocks with base64 data URIs (requires vision-capable models like GPT-4o or LLaVA)
- Supported formats: JPEG, PNG, GIF, WebP
- Images are in-memory only — never stored in the database. Session history records `[Photo] caption` or `[Sent a photo]` as a text marker.
- If the AI model does not support vision, it will only see the caption text

### Interaction with tools

The `Message` tool sends messages through enabled channels. If no channels are enabled, the Message tool has no targets. Channel names in the Message tool correspond to the `name` field in the channels config.

---

## `heartbeat`

Infrastructure heartbeat — periodic system health monitoring and metrics collection. This is the low-level system heartbeat, not the agent task heartbeat.

```json
{
  "heartbeat": {
    "enabled": true,
    "interval_seconds": 30,
    "enable_metrics": true,
    "enable_events": true,
    "log_level": "info",
    "max_queue_depth": 1000
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `true` | Enable infrastructure heartbeat |
| `interval_seconds` | int | `30` | How often to collect metrics. Range: 10–3600 |
| `enable_metrics` | bool | `true` | Collect system metrics (memory, goroutines, etc.) |
| `enable_events` | bool | `true` | Track system events |
| `log_level` | string | `"info"` | Log level: `"debug"`, `"info"`, `"warn"`, `"error"` |
| `max_queue_depth` | int | `1000` | Maximum queued events before dropping. Cannot be negative |

---

## `agent_heartbeat`

Agent heartbeat — periodic task processing loop. Every N minutes, the agent reads HEARTBEAT.md for tasks, processes the alert queue, and respects quiet hours. This is distinct from the infrastructure `heartbeat` above.

```json
{
  "agent_heartbeat": {
    "enabled": true,
    "interval_minutes": 5,
    "timezone": "America/Los_Angeles",
    "quiet_enabled": true,
    "quiet_hours": {
      "start_time": "23:00",
      "end_time": "08:00"
    },
    "alert_queue_path": "memory/alerts/pending.json",
    "heartbeat_task_path": "HEARTBEAT.md",
    "enabled_task_types": ["alerts", "checks", "reports"],
    "alert_targets": [],
    "alert_retry_policy": {
      "max_retries": 3,
      "retry_interval": 300000000000,
      "backoff_factor": 2.0
    }
  }
}
```

### Core fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `true` | Enable the agent heartbeat loop |
| `interval_minutes` | int | `5` | How often the agent checks for tasks. Range: 1–60 |
| `timezone` | string | `"America/Los_Angeles"` | IANA timezone for quiet hours and scheduling |
| `heartbeat_task_path` | string | `"HEARTBEAT.md"` | Path to task instructions file (relative to `workspace.context_dir`) |
| `enabled_task_types` | string array | `["alerts", "checks", "reports"]` | Which task types to process. Options: `"alerts"`, `"checks"`, `"reports"`, `"maintenance"` |
| `log_level` | string | `"info"` | Log level: `"debug"`, `"info"`, `"warn"`, `"error"` |
| `verbose_logging` | bool | `false` | Enable verbose debug logging for agent heartbeat |

### Quiet hours

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `quiet_enabled` | bool | `true` | Suppress non-critical alerts during quiet hours |
| `quiet_hours.start_time` | string | `"23:00"` | Quiet period start (HH:MM in configured timezone) |
| `quiet_hours.end_time` | string | `"08:00"` | Quiet period end (HH:MM in configured timezone) |

During quiet hours, only `"critical"` severity alerts are delivered. `"warning"` and `"info"` alerts are held in the queue until quiet hours end. Quiet hours can span midnight (e.g., 23:00 to 08:00).

### Alert system

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `alert_queue_path` | string | `"memory/alerts/pending.json"` | Path to alert queue file (relative to `workspace.context_dir`) |
| `alert_targets` | array | `[]` | Where to deliver alerts |
| `alert_retry_policy.max_retries` | int | `3` | Max delivery attempts per alert. Range: 0–10 |
| `alert_retry_policy.retry_interval` | duration (ns) | `300000000000` (5m) | Wait between retries |
| `alert_retry_policy.backoff_factor` | float | `2.0` | Exponential backoff multiplier. Range: 1.0–5.0 |

### Alert target fields

```json
{
  "alert_targets": [
    {
      "name": "admin-telegram",
      "type": "telegram",
      "config": {
        "chat_id": "123456789"
      },
      "severity": ["critical", "warning"]
    }
  ]
}
```

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Target identifier |
| `type` | string | Delivery method: `"telegram"`, `"email"`, `"slack"`, `"webhook"` |
| `config` | object | Target-specific config (e.g., `chat_id` for Telegram) |
| `severity` | string array | Which severities to route here: `"critical"`, `"warning"`, `"info"` |

### How `heartbeat` and `agent_heartbeat` differ

| | `heartbeat` | `agent_heartbeat` |
|---|---|---|
| Purpose | System health monitoring | Agent task execution |
| Frequency | Every 30 seconds | Every 5 minutes |
| What it does | Collects metrics, tracks events | Reads HEARTBEAT.md, processes alerts, runs checks |
| Requires | Nothing | `workspace.context_dir` with HEARTBEAT.md, optionally channels for alert delivery |

You typically want both enabled. The infrastructure heartbeat feeds into the monitoring system. The agent heartbeat drives the agent's autonomous task loop.

---

## `rateLimiting`

Rate limiting for the HTTP/WebSocket API. Uses a sliding window algorithm per client.

```json
{
  "rateLimiting": {
    "enabled": true,
    "anonymous": {
      "windowSeconds": 60,
      "maxRequests": 100
    },
    "authenticated": {
      "windowSeconds": 60,
      "maxRequests": 1000
    },
    "cleanupIntervalSeconds": 300
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `true` | Enable rate limiting |
| `anonymous.windowSeconds` | int | `60` | Sliding window size for unauthenticated requests |
| `anonymous.maxRequests` | int | `100` | Max requests per window for unauthenticated clients (per IP) |
| `authenticated.windowSeconds` | int | `60` | Sliding window size for authenticated requests |
| `authenticated.maxRequests` | int | `1000` | Max requests per window for authenticated clients (per token) |
| `cleanupIntervalSeconds` | int | `300` | How often to clean up expired rate limit entries |

Authenticated clients (with valid auth tokens) get a higher limit. Anonymous clients are tracked by IP address.

---

## `ssh`

Integrated SSH server that serves the BubbleTea TUI over SSH via Wish. Clients connect with any SSH client and get a full terminal chat interface.

```json
{
  "ssh": {
    "enabled": false,
    "listen_addr": ":2222",
    "host_key_path": "~/.conduit/ssh_host_key",
    "authorized_keys_path": "~/.conduit/authorized_keys"
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable the integrated SSH server |
| `listen_addr` | string | `":2222"` | SSH listen address (host:port) |
| `host_key_path` | string | `""` | Path to SSH host private key. Auto-generated to `~/.conduit/ssh/ssh_host_key` if empty |
| `authorized_keys_path` | string | `""` | Path to authorized_keys file for client authentication |

### SSH setup steps

1. `./bin/gateway ssh-keys init` — generates host key and creates authorized_keys file
2. `./bin/gateway ssh-keys add ~/.ssh/id_ed25519.pub` — authorize a client public key
3. Set `ssh.enabled: true` in config and restart, or run `./bin/gateway ssh-server` standalone

The SSH server uses a direct in-process client (`gateway/direct_client.go`) instead of WebSocket loopback, so it doesn't consume an API token. However, the standalone `ssh-server` command does require a `--gateway-token` flag for WebSocket connection to the gateway.

---

## `vector`

Optional vector/semantic search service for embedding-based document retrieval.

```json
{
  "vector": {
    "enabled": false,
    "path": "./gateway.vector.db",
    "chunk_size": 500,
    "embed_dims": 4096,
    "embed_provider": "tfidf",
    "openai": {
      "api_key": "${OPENAI_API_KEY}",
      "model": "text-embedding-3-small"
    }
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable vector search |
| `path` | string | derived | Path to vector database. Defaults to `<gateway-db>.vector.db` |
| `chunk_size` | int | `500` | Maximum tokens per document chunk |
| `embed_dims` | int | `4096` | Embedding dimensions (4096 for TF-IDF, 1536 for OpenAI) |
| `embed_provider` | string | `"tfidf"` | Embedding provider: `"tfidf"` (local) or `"openai"` |

### OpenAI embeddings

| Field | Type | Description |
|-------|------|-------------|
| `openai.api_key` | string | OpenAI API key (supports `${ENV_VAR}` expansion) |
| `openai.model` | string | Embedding model (default: `"text-embedding-3-small"`) |

---

## `debug`

```json
{
  "debug": {
    "log_message_content": false,
    "verbose_logging": false
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `log_message_content` | bool | `false` | Log full message content to stdout. **Privacy risk** — do not enable in production |
| `verbose_logging` | bool | `false` | Enable verbose debug logging across all subsystems |

The `--verbose` / `-v` CLI flag also enables verbose logging and can be used instead of this config field.

---

## `stt`

Speech-to-text configuration for transcribing incoming voice messages (e.g., Telegram voice notes) into text before passing them to the AI.

```json
{
  "stt": {
    "enabled": true,
    "provider": "whisper",
    "api_key": "${OPENAI_API_KEY}",
    "model": "whisper-1"
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable speech-to-text transcription |
| `provider` | string | `"whisper"` | STT provider. Currently only `"whisper"` (OpenAI Whisper API) is supported |
| `api_key` | string | — | OpenAI API key (supports `${ENV_VAR}` expansion) |
| `model` | string | `"whisper-1"` | Whisper model to use for transcription |

### How it works

When a Telegram user sends a voice message:

1. The adapter downloads the OGG/OPUS audio from Telegram
2. The audio is sent to the OpenAI Whisper API for transcription
3. The transcribed text is passed to the AI as a normal message with `type=voice` metadata

If STT is not enabled, voice messages receive an automatic reply: *"Voice messages are not supported (speech-to-text not configured)."*

### Requirements

- An OpenAI API key with access to the Whisper API
- A Telegram channel configured in `channels`
- Telegram voice messages are OGG/OPUS format and up to 20 MB (within Whisper's 25 MB limit)

---

## `brain`

Tiered cognitive memory: long-term memory (SQLite-persisted), working memory (in-process per-user), and scratchpad (LIFO stack). Includes REM sleep cycle for periodic memory consolidation. See [Brain Reference](reference/brain.md) for architecture details.

```json
{
  "brain": {
    "enabled": true,
    "path": "./gateway.brain.db",
    "max_ltm_entries": 10000,
    "consolidate_threshold": 0.6,
    "evict_threshold": 0.1,
    "auto_promote": true,
    "access_weight": 0.4,
    "recency_weight": 0.4,
    "tier_weight": 0.2,
    "rem_enabled": true,
    "rem_schedule": "0 2 * * *",
    "rem_prune_age_days": 30,
    "rem_groom_with_llm": true,
    "rem_log_path": "memory/rem-log"
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable the brain subsystem |
| `path` | string | derived | Brain database path. Defaults to `<gateway-db>.brain.db` |
| `max_ltm_entries` | int | `10000` | Maximum long-term memory entries |
| `wm_grace_period_seconds` | int | `300` | Seconds to keep working memory after session ends |
| `auto_flush_seconds` | int | `600` | Auto-flush interval for WM to LTM |
| `consolidate_threshold` | float | `0.6` | Salience threshold for auto-promoting WM to LTM |
| `evict_threshold` | float | `0.1` | Salience threshold below which LTM entries are evicted |
| `auto_promote` | bool | `true` | Auto-promote high-salience WM entries |
| `access_weight` | float | `0.4` | Salience weight for access frequency |
| `recency_weight` | float | `0.4` | Salience weight for recency |
| `tier_weight` | float | `0.2` | Salience weight for memory tier |
| `recency_decay_rate` | float | `1.0` | Decay rate for recency scoring |
| `access_count_cap` | int | `100` | Normalization cap for access counts |

### REM sleep cycle

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `rem_enabled` | bool | `true` | Enable REM sleep (requires brain `enabled`) |
| `rem_schedule` | string | `"0 2 * * *"` | Cron schedule for REM cycle |
| `rem_integration_day` | int | `0` | Day of week for deep integration (0=Sunday) |
| `rem_prune_age_days` | int | `30` | Evict memories not accessed in this many days |
| `rem_salience_decay_rate` | float | `0.1` | Salience decay per REM cycle |
| `rem_groom_with_llm` | bool | `true` | Use LLM to consolidate memories during REM |
| `rem_log_path` | string | `"memory/rem-log"` | Directory for REM cycle logs |

---

## `mqtt`

MQTT event ingest. Subscribes to topics and buffers events for the MQTT tool. See [MQTT Reference](reference/mqtt.md).

```json
{
  "mqtt": {
    "enabled": true,
    "broker_url": "tcp://192.168.1.10:1883",
    "client_id": "conduit",
    "username": "${MQTT_USERNAME}",
    "password": "${MQTT_PASSWORD}",
    "topics": ["zigbee2mqtt/#"],
    "qos": 0,
    "buffer_max_age_seconds": 3600,
    "buffer_max_events": 1000,
    "publish_allowed": false
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable MQTT event ingest |
| `broker_url` | string | — | MQTT broker URL. Required. Supports `${ENV_VAR}` |
| `client_id` | string | `"conduit"` | MQTT client identifier |
| `username` | string | `""` | MQTT username. Supports `${ENV_VAR}` |
| `password` | string | `""` | MQTT password. Supports `${ENV_VAR}` |
| `topics` | string array | — | Topic subscriptions (wildcards supported). Required |
| `qos` | int | `0` | QoS level: 0, 1, or 2 |
| `publish_allowed` | bool | `false` | Allow MQTT tool to publish messages |
| `buffer_max_age_seconds` | int | `3600` | Max event age in buffer |
| `buffer_max_events` | int | `1000` | Max events per topic |
| `buffer_max_topics` | int | `500` | Max tracked topics |

---

## `kubernetes`

Kubernetes cluster management. See [Kubernetes Reference](reference/kubernetes.md).

```json
{
  "kubernetes": {
    "enabled": true,
    "clusters": [
      {
        "name": "prod",
        "kubeconfig_path": "~/.kube/config",
        "context": "prod-context",
        "allowed_namespaces": ["default", "app"],
        "safety_level": "read"
      }
    ],
    "defaults": { "namespace": "default", "safety_level": "read" }
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable Kubernetes tools |
| `clusters[].name` | string | required | Unique cluster identifier |
| `clusters[].kubeconfig_path` | string | required | Path to kubeconfig. Supports `~/` |
| `clusters[].context` | string | `""` | Kubeconfig context (empty = current) |
| `clusters[].allowed_namespaces` | string array | `[]` | Restrict to these namespaces (empty = all) |
| `clusters[].safety_level` | string | `"read"` | `"read"`, `"modify"`, or `"dangerous"` |

---

## `pagerduty`

PagerDuty integration. See [SRE Tools Reference](reference/sre-tools.md).

```json
{
  "pagerduty": {
    "enabled": true,
    "api_token": "${PAGERDUTY_API_TOKEN}",
    "default_service_id": "PXXXXXX",
    "default_escalation_policy_id": "PXXXXXX",
    "rate_limit_rps": 5.0
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable PagerDuty integration |
| `api_token` | string | — | PagerDuty API token. Required. Supports `${ENV_VAR}` |
| `default_service_id` | string | `""` | Default service for creating incidents |
| `default_escalation_policy_id` | string | `""` | Default escalation policy |
| `base_url` | string | `"https://api.pagerduty.com"` | API base URL |
| `rate_limit_rps` | float | `5.0` | Max API requests per second |

---

## `datadog`

Datadog integration. See [SRE Tools Reference](reference/sre-tools.md).

```json
{
  "datadog": {
    "enabled": true,
    "api_key": "${DD_API_KEY}",
    "app_key": "${DD_APP_KEY}",
    "site": "datadoghq.com",
    "rate_limit_rps": 5.0
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable Datadog integration |
| `api_key` | string | — | Datadog API key. Required. Supports `${ENV_VAR}` |
| `app_key` | string | — | Datadog app key. Required. Supports `${ENV_VAR}` |
| `site` | string | `"datadoghq.com"` | Datadog site |
| `rate_limit_rps` | float | `5.0` | Max API requests per second |

---

## `remote_ssh`

Remote SSH execution with security tiers. See [Remote SSH Reference](reference/remote-ssh.md).

```json
{
  "remote_ssh": {
    "enabled": true,
    "hosts": [
      {
        "name": "web-1",
        "hostname": "10.0.1.10",
        "user": "deploy",
        "identity_file": "~/.ssh/id_ed25519",
        "groups": ["web-servers"],
        "security_tier": "modify"
      }
    ],
    "defaults": { "port": 22, "user": "deploy", "connect_timeout": "30s" },
    "security": { "default_tier": "dangerous", "allow_subshells": false },
    "audit": { "enabled": true, "log_path": "logs/ssh_audit.jsonl" }
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable remote SSH |
| `hosts[].name` | string | required | Unique host identifier |
| `hosts[].hostname` | string | required | DNS name or IP |
| `hosts[].security_tier` | string | `""` | `"read"`, `"modify"`, `"dangerous"`, `"blocked"` |
| `security.default_tier` | string | `"dangerous"` | Tier for unclassified commands |
| `security.allow_subshells` | bool | `false` | Permit `$()` substitution |
| `audit.enabled` | bool | `true` | Enable command audit logging |

See [Remote SSH Reference](reference/remote-ssh.md) for the complete field list.

---

## `websocket`

```json
{ "websocket": { "max_message_size": 1048576 } }
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `max_message_size` | int64 | `1048576` (1 MB) | Max incoming WebSocket message size in bytes |

---

## `diagnostics`

```json
{ "diagnostics": { "require_auth": true, "health_public": true } }
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `require_auth` | bool | `true` | Require auth for `/metrics`, `/diagnostics`, `/prometheus` |
| `health_public` | bool | `true` | Allow unauthenticated `/health` access |

---

## `logging`

```json
{ "logging": { "level": "info", "format": "text" } }
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `level` | string | `"info"` | Min log level: `"debug"`, `"info"`, `"warn"`, `"error"` |
| `format` | string | `"text"` | Output format: `"text"` or `"json"` |

---

## `auth`

Token authentication. Separate from `rateLimiting.auth`.

```json
{ "auth": { "token_secret": "${CONDUIT_TOKEN_SECRET}" } }
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `token_secret` | string | random | HMAC key for hashing auth tokens. If empty, a random key is generated at startup and tokens won't survive restarts. Generate with: `openssl rand -hex 32` |

---

## `tui`

Terminal UI shell escape configuration.

```json
{
  "tui": {
    "shell_escape": {
      "enabled": true,
      "allow_ssh": false,
      "command_allowlist": ["git ", "ls", "cat "],
      "command_blocklist": [],
      "use_default_blocklist": true
    }
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `shell_escape.enabled` | bool | `true` (local), `false` (SSH) | Enable `!` shell escape in TUI |
| `shell_escape.allow_ssh` | bool | `false` | Allow shell escape over SSH |
| `shell_escape.command_allowlist` | string array | `[]` | If non-empty, only matching prefixes allowed |
| `shell_escape.command_blocklist` | string array | `[]` | Blocked command prefixes |
| `shell_escape.use_default_blocklist` | bool | `true` | Include default dangerous command blocklist |

---

## `allowed_origins`

Top-level WebSocket CORS setting.

```json
{ "allowed_origins": ["https://chat.example.com"] }
```

When empty (default), only same-origin and localhost connections are permitted.

---

## Use-Case Recipes

### Headless AI chatbot (no tools, no channels)

For a minimal WebSocket-only chatbot:

```json
{
  "port": 18789,
  "database": { "path": "./gateway.db" },
  "ai": {
    "default_provider": "anthropic",
    "providers": [{
      "name": "anthropic",
      "type": "anthropic",
      "api_key": "${ANTHROPIC_API_KEY}",
      "model": "claude-sonnet-4-20250514"
    }]
  },
  "tools": { "enabled_tools": [], "sandbox": { "workspace_dir": "./workspace", "allowed_paths": [] } },
  "channels": []
}
```

### AI assistant with file tools + memory

Add workspace context and file tools so the agent has a personality and can read/write files:

```json
{
  "port": 18789,
  "database": { "path": "./gateway.db" },
  "ai": {
    "default_provider": "anthropic",
    "providers": [{
      "name": "anthropic",
      "type": "anthropic",
      "api_key": "${ANTHROPIC_API_KEY}",
      "model": "claude-sonnet-4-20250514"
    }]
  },
  "agent": {
    "name": "Conduit",
    "personality": "conduit",
    "capabilities": { "memory_recall": true, "tool_chaining": true, "heartbeats": false, "skills_integration": false, "silent_replies": true }
  },
  "workspace": {
    "context_dir": "./workspace"
  },
  "tools": {
    "enabled_tools": ["Read", "Write", "Edit", "Bash", "Glob", "MemorySearch"],
    "sandbox": {
      "workspace_dir": "./workspace",
      "allowed_paths": ["./workspace", "/tmp"]
    }
  },
  "channels": []
}
```

Then create `./workspace/SOUL.md` with the agent's personality.

### Telegram bot

Add a Telegram channel to the above:

```json
{
  "channels": [
    {
      "name": "telegram",
      "type": "telegram",
      "enabled": true,
      "config": {
        "bot_token": "${TELEGRAM_BOT_TOKEN}"
      }
    }
  ]
}
```

Requires: `TELEGRAM_BOT_TOKEN` environment variable. Create a bot via @BotFather on Telegram.

To enable voice message transcription, add the [`stt`](#stt) section alongside channels:

```json
{
  "stt": {
    "enabled": true,
    "api_key": "${OPENAI_API_KEY}",
    "model": "whisper-1"
  }
}
```

To restrict which groups the bot responds in:

```json
{
  "config": {
    "bot_token": "${TELEGRAM_BOT_TOKEN}",
    "groups": {
      "-1001234567890": { "requireMention": false }
    },
    "groupPolicy": "allowlist"
  }
}
```

### Web search enabled

Add web search and fetch tools with Brave API:

```json
{
  "tools": {
    "enabled_tools": ["...", "WebSearch", "WebFetch"],
    "services": {
      "brave": {
        "api_key": "${BRAVE_API_KEY}"
      }
    }
  }
}
```

Requires: `BRAVE_API_KEY` environment variable. WebSearch can also use Anthropic's built-in search as a fallback if no Brave key is configured.

### SSH access

Enable the integrated SSH server:

```json
{
  "ssh": {
    "enabled": true,
    "listen_addr": ":2222",
    "host_key_path": "~/.conduit/ssh_host_key",
    "authorized_keys_path": "~/.conduit/authorized_keys"
  }
}
```

Before starting, run:
```bash
./bin/gateway ssh-keys init
./bin/gateway ssh-keys add ~/.ssh/id_ed25519.pub
```

### Agent heartbeat with Telegram alerts

Enable the agent task loop with alert delivery to Telegram:

```json
{
  "agent_heartbeat": {
    "enabled": true,
    "interval_minutes": 5,
    "timezone": "America/New_York",
    "quiet_enabled": true,
    "quiet_hours": { "start_time": "22:00", "end_time": "07:00" },
    "alert_queue_path": "memory/alerts/pending.json",
    "heartbeat_task_path": "HEARTBEAT.md",
    "enabled_task_types": ["alerts", "checks", "reports"],
    "alert_targets": [
      {
        "name": "admin",
        "type": "telegram",
        "config": { "chat_id": "${TELEGRAM_ADMIN_CHAT_ID}" },
        "severity": ["critical", "warning", "info"]
      }
    ],
    "alert_retry_policy": {
      "max_retries": 3,
      "retry_interval": 300000000000,
      "backoff_factor": 2.0
    }
  }
}
```

Requires: Telegram channel enabled, `HEARTBEAT.md` in workspace directory.

### UniFi device management

Enable the UniFi tool for network/camera management:

```json
{
  "tools": {
    "enabled_tools": ["...", "UniFi"]
  }
}
```

Requires environment variables (not config):
```bash
export UNVR_URL="https://192.168.1.1"
export UNVR_API_KEY="your-unifi-api-key"
```

### Full production config

See the [New Instance Setup](README.md#new-instance-setup) section in README.md for a complete production-ready config with every option set.

### Skills system

Enable custom skills from SKILL.md files:

```json
{
  "agent": {
    "capabilities": { "skills_integration": true }
  },
  "skills": {
    "enabled": true,
    "search_paths": ["./workspace/skills"],
    "execution": {
      "timeout_seconds": 30,
      "environment": {},
      "allowed_actions": {}
    },
    "cache": {
      "enabled": true,
      "ttl_seconds": 3600
    }
  }
}
```

Each skill directory must contain a `SKILL.md` file. The skills system discovers executable scripts (.sh, .py, .js) and reference files in each skill directory. Skills are exposed to the AI as additional tools.

Note: Skills integration is currently disabled in the tool registry pending a refactor (`registerAllTools` has the skill adapter registration commented out). The config infrastructure is in place for when it's re-enabled.

### Vector/semantic search

Enable vector-based document search with TF-IDF (local, no API):

```json
{
  "vector": {
    "enabled": true,
    "embed_provider": "tfidf",
    "chunk_size": 500
  }
}
```

Or with OpenAI embeddings for higher quality:

```json
{
  "vector": {
    "enabled": true,
    "embed_provider": "openai",
    "embed_dims": 1536,
    "openai": {
      "api_key": "${OPENAI_API_KEY}",
      "model": "text-embedding-3-small"
    }
  }
}
```

### Using a secrets file

Keep API keys out of your config by using a secrets file:

```json
{
  "secrets_file": "~/.conduit/secrets",
  "ai": {
    "providers": [{
      "api_key": "${ANTHROPIC_API_KEY}"
    }]
  }
}
```

Then in `~/.conduit/secrets`:
```
ANTHROPIC_API_KEY=sk-ant-...
TELEGRAM_BOT_TOKEN=123456:ABC...
```

### Local/compatible API servers

Use a local model server (like Ollama) with the OpenAI-compatible endpoint:

```json
{
  "ai": {
    "providers": [{
      "name": "local",
      "type": "openai",
      "base_url": "http://localhost:11434/v1",
      "model": "llama3.2"
    }]
  }
}
```
