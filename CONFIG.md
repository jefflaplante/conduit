# CONFIG.md — Conduit-Go Configuration Reference

Complete reference for every `config.json` option, how each option behaves, how options interact, and what you need for each use case.

---

## Table of Contents

- [Minimal Config](#minimal-config)
- [Environment Variable Expansion](#environment-variable-expansion)
- [Path Resolution Rules](#path-resolution-rules)
- [Top-Level Options](#top-level-options)
- [database](#database)
- [ai](#ai)
- [agent](#agent)
- [workspace](#workspace)
- [tools](#tools)
- [channels](#channels)
- [heartbeat](#heartbeat)
- [agent_heartbeat](#agent_heartbeat)
- [rateLimiting](#ratelimiting)
- [ssh](#ssh)
- [debug](#debug)
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

### Provider fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Identifier used by `default_provider` to select this provider |
| `type` | string | yes | Provider type: `"anthropic"` or `"openai"` |
| `api_key` | string | conditional | API key. Required unless using OAuth |
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

---

## `agent`

Controls the AI agent's personality, identity, and capabilities. Optional — if omitted the gateway works but the agent has no personality or special behaviors.

```json
{
  "agent": {
    "name": "Jules",
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

**Interaction with `workspace`:** When `memory_recall` is true, the agent loads MEMORY.md and `memory/*.md` files from `workspace.context_dir`. When `heartbeats` is true, the agent reads HEARTBEAT.md from the same directory.

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
    "name": "Jules",
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
