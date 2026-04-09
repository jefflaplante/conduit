# Configuration Reference

Complete reference for Conduit Go Gateway configuration.

## Overview

Configuration is loaded from JSON files with support for:
- Environment variable expansion: `${ENV_VAR}`
- Default values: `${ENV_VAR:-default}`
- Multiple config files for different environments

## Full Configuration Example

```json
{
  "port": 18789,
  "database": "./gateway.db",
  "allowed_origins": [],

  "ai": {
    "default_provider": "anthropic",
    "providers": [
      {
        "name": "anthropic",
        "type": "anthropic",
        "api_key": "${ANTHROPIC_API_KEY}",
        "model": "claude-3-5-sonnet-20241022"
      },
      {
        "name": "opus46",
        "type": "anthropic",
        "api_key": "${ANTHROPIC_API_KEY}",
        "model": "claude-opus-4-6"
      }
    ]
  },

  "agent": {
    "name": "Conduit",
    "personality": "helpful assistant",
    "email": {
      "address": "agent@example.com",
      "aliases": ["assistant@example.com"],
      "display_name": "Conduit Assistant"
    },
    "capabilities": ["code", "research", "analysis"]
  },

  "workspace": {
    "context_dir": "./workspace",
    "memory_file": "MEMORY.md",
    "core_files": ["MEMORY.md", "PREFERENCES.md"],
    "cache_ttl_seconds": 300
  },

  "tools": {
    "enabled_tools": [
      "Read", "Write", "Edit", "Bash", "Glob",
      "MemorySearch", "WebSearch", "WebFetch",
      "Message", "Cron", "Chain", "Gateway", "Brain"
    ],
    "max_chain_depth": 10,
    "sandbox": {
      "enabled": true,
      "workspace_dir": "./workspace",
      "allowed_paths": ["./workspace", "/tmp"]
    }
  },

  "search": {
    "default_strategy": "anthropic",
    "brave_api_key": "${BRAVE_API_KEY}",
    "cache_ttl_minutes": 15,
    "max_results": 5
  },

  "channels": [
    {
      "name": "telegram",
      "type": "telegram",
      "enabled": true,
      "config": {
        "bot_token": "${TELEGRAM_BOT_TOKEN}",
        "webhook_mode": false
      }
    }
  ],

  "ssh": {
    "enabled": true,
    "listen_addr": ":2222",
    "host_key_path": "~/.conduit/ssh_host_key",
    "authorized_keys_path": "~/.conduit/authorized_keys"
  },

  "auth": {
    "rate_limits": {
      "anonymous": {
        "requests_per_minute": 100,
        "endpoints": ["/health"]
      },
      "authenticated": {
        "requests_per_minute": 1000,
        "applies_to_all": true
      }
    }
  },

  "heartbeat": {
    "enabled": true,
    "interval_seconds": 30,
    "timeout_seconds": 5
  },

  "agent_heartbeat": {
    "enabled": true,
    "interval_minutes": 5,
    "quiet_hours": {
      "start": "23:00",
      "end": "08:00",
      "timezone": "America/Los_Angeles"
    }
  },

  "brain": {
    "enabled": true,
    "max_ltm_entries": 10000,
    "auto_promote": true
  },

  "skills": {
    "enabled": true,
    "search_paths": [
      "/home/user/.npm-global/lib/node_modules/conduit/skills",
      "/home/user/conduit/skills"
    ],
    "execution": {
      "timeout_seconds": 300
    },
    "cache": {
      "enabled": true,
      "ttl_seconds": 1800
    }
  },

  "debug": {
    "enabled": false,
    "log_level": "info"
  }
}
```

## Configuration Sections

### Server

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `port` | int | 18789 | HTTP/WebSocket server port |
| `database` | string | "./gateway.db" | SQLite database path |
| `allowed_origins` | string[] | `[]` | WebSocket allowed origins. Empty = localhost only. |

### Allowed Origins (WebSocket Security)

Controls which browser origins can establish WebSocket connections. Prevents cross-site WebSocket hijacking (CSWSH).

```json
{
  "allowed_origins": [
    "https://myapp.example.com",
    "https://admin.corp.net:8443"
  ]
}
```

| Behavior | When |
|----------|------|
| Localhost only | `allowed_origins` is empty or omitted (default) |
| Explicit allowlist | `allowed_origins` has entries — only those origins accepted |
| Always allowed | Requests with no `Origin` header (curl, SDKs, non-browser clients) |

Origins are compared case-insensitively. Include scheme and port (if non-standard), no trailing slash.

### AI Providers

```json
{
  "ai": {
    "default_provider": "anthropic",
    "providers": [
      {
        "name": "anthropic",
        "type": "anthropic",
        "api_key": "${ANTHROPIC_API_KEY}",
        "model": "claude-3-5-sonnet-20241022",
        "max_tokens": 4096,
        "temperature": 0.7
      }
    ]
  }
}
```

Supported provider types:
- `anthropic` - Claude models
- `openai` - GPT models and OpenAI-compatible APIs (z.ai, vLLM, Ollama, etc.)

Model aliases (for `/model` command):
- `haiku` - claude-haiku-4-5-20251001
- `sonnet` - claude-sonnet-4-6
- `opus` - claude-opus-4-6
- `glm` - z-ai/glm-5-turbo (requires [z.ai setup](z-ai.md))

### Smart Routing

Automatic model selection based on task complexity. When enabled, Conduit analyzes each request and routes to the most appropriate model tier.

```json
{
  "ai": {
    "smart_routing": {
      "enabled": true,
      "track_usage": true,
      "cost_budget_daily": 10.0,
      "pricing_overrides": {
        "claude-opus-4-6": {
          "input_per_m_token": 15.0,
          "output_per_m_token": 75.0
        }
      }
    }
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable smart routing |
| `track_usage` | bool | `false` | Track usage metrics |
| `cost_budget_daily` | float | `0` | Daily cost budget (0 = unlimited) |
| `pricing_overrides` | map | `{}` | Override default model pricing |

**Model Tiers:**
- **Haiku** — Simple queries, greetings, straightforward tasks
- **Sonnet** — Standard complexity, 2-3 tool calls, moderate reasoning
- **Opus** — Complex multi-step tasks, 5+ tool calls, deep analysis

Use `/smartroute` command to toggle per-session or check status.

### Context Compaction

Automatic summarization of long sessions when context usage exceeds a threshold.

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
| `enabled` | bool | `false` | Enable auto-compaction |
| `threshold` | float | `0.70` | Context usage ratio that triggers compaction |
| `model` | string | `"claude-haiku-4-5-20251001"` | Model for summarization |
| `recent_messages_to_keep` | int | `10` | Messages to preserve verbatim |

When triggered, older messages are summarized and replaced with a compact summary, preserving key decisions, context, and recent exchanges. Use `/compact` to trigger manually.

### Agent Email

Optional email identity configuration for the agent. When configured, the agent's email address is automatically included in the system prompt and available to tools. See the [Agent Email Guide](guides/agent-email.md) for detailed documentation.

```json
{
  "agent": {
    "email": {
      "address": "agent@example.com",
      "aliases": ["assistant@example.com", "bot@example.com"],
      "display_name": "Conduit"
    }
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `address` | string | `""` | Primary email address for the agent |
| `aliases` | array | `[]` | Additional email addresses the agent recognizes as its own |
| `display_name` | string | Agent name | Display name for outgoing emails |

**Behavior:**
- When `address` is empty, the email section is omitted from the system prompt
- When `display_name` is empty, falls back to the agent's `name` field
- Tools like `google_workspace` use this config for sending emails and validating aliases

### Workspace

```json
{
  "workspace": {
    "context_dir": "./workspace",
    "memory_file": "MEMORY.md",
    "core_files": ["MEMORY.md", "PREFERENCES.md"],
    "cache_ttl_seconds": 300,
    "security": {
      "max_file_size_bytes": 1048576,
      "allowed_extensions": [".md", ".txt", ".json"]
    }
  }
}
```

### Tools

```json
{
  "tools": {
    "enabled_tools": ["Read", "Write", "Bash", "WebSearch"],
    "max_chain_depth": 10,
    "sandbox": {
      "enabled": true,
      "workspace_dir": "./workspace",
      "allowed_paths": ["./workspace", "/tmp", "/home/user/projects"]
    },
    "services": {
      "brave_api_key": "${BRAVE_API_KEY}"
    }
  }
}
```

Available tools: Read, Write, Edit, Bash, Glob, MemorySearch, Find, Facts, WebSearch, WebFetch, Message, Tts, Cron, Chain, Gateway, Context, Image, Brain, SessionsList, SessionsSend, SessionsSpawn, SessionStatus, google_workspace

#### Google Workspace Tool

Optional integration with Gmail and Calendar via the `gws` CLI. See [Google Workspace Setup Guide](guides/google-workspace-setup.md) for detailed instructions.

```json
{
  "tools": {
    "enabled_tools": ["google_workspace"],
    "services": {
      "google_workspace": {
        "gws_path": "gws",
        "user_id": "me"
      }
    }
  }
}
```

| Field | Default | Description |
|-------|---------|-------------|
| `gws_path` | `"gws"` | Path to gws CLI binary |
| `user_id` | `"me"` | Gmail/Calendar user ID |

The tool requires `gws` to be installed separately (`npm install -g @googleworkspace/cli`) and authenticated (`gws auth login`). Without gws, the tool returns a helpful error message and Conduit continues to function normally

### Channels

```json
{
  "channels": [
    {
      "name": "telegram",
      "type": "telegram",
      "enabled": true,
      "config": {
        "bot_token": "${TELEGRAM_BOT_TOKEN}",
        "webhook_mode": false,
        "webhook_url": "https://example.com/webhook",
        "debug": false
      }
    }
  ]
}
```

#### Telegram Photo Vision

When a Telegram channel is enabled, incoming photos are automatically downloaded and sent to the AI as image content blocks for vision analysis. No additional configuration is required — the feature works out of the box with any vision-capable model.

**How it works:**
- User sends a photo in Telegram (with or without a caption)
- Conduit downloads the highest-resolution version from Telegram's servers
- The image is base64-encoded and sent inline to the AI provider as an image content block
- The AI describes or analyzes what it sees in the image

**Supported providers:**
- **Anthropic** — Native `image` content blocks (Claude 3+ models with vision)
- **OpenAI / Ollama / compatible** — `image_url` content blocks with base64 data URIs (GPT-4o, LLaVA, etc.)

**Constraints:**
- Maximum photo size: 20 MB (Telegram's file size limit)
- Supported formats: JPEG, PNG, GIF, WebP
- Images are carried in-memory only and are **never persisted** to the database. Session history stores a text marker (`[Photo] caption` or `[Sent a photo]`) so the AI knows an image was shared earlier in conversation, even though it can no longer see it.
- Unsupported formats or download failures result in a user-facing error message in Telegram

**Prerequisites:**
- A Telegram channel must be enabled (see above)
- The configured AI provider/model must support vision (e.g., `claude-sonnet-4-20250514`, `gpt-4o`, `llava`)
- Text-only models will receive the caption but not the image data

### SSH Server

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

### Authentication

```json
{
  "auth": {
    "rate_limits": {
      "anonymous": {
        "requests_per_minute": 100,
        "endpoints": ["/health"]
      },
      "authenticated": {
        "requests_per_minute": 1000,
        "applies_to_all": true
      }
    }
  }
}
```

### Heartbeat

Gateway health monitoring:

```json
{
  "heartbeat": {
    "enabled": true,
    "interval_seconds": 30,
    "timeout_seconds": 5,
    "stuck_threshold_seconds": 120
  }
}
```

Agent heartbeat for automated tasks. See [agent-heartbeat.md](agent-heartbeat.md) for full documentation.

```json
{
  "agent_heartbeat": {
    "enabled": true,
    "interval_minutes": 5,
    "timezone": "America/Los_Angeles",
    "quiet_enabled": true,
    "quiet_hours": {
      "start_time": "22:00",
      "end_time": "07:00"
    },
    "alert_queue_path": "memory/alerts/pending.json",
    "heartbeat_task_path": "HEARTBEAT.md",
    "enabled_task_types": ["alerts", "checks", "reports", "maintenance"],
    "alert_targets": [
      {
        "name": "telegram_primary",
        "type": "telegram",
        "config": { "chat_id": "123456789" },
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

### Skills

```json
{
  "skills": {
    "enabled": true,
    "search_paths": [
      "/home/user/.npm-global/lib/node_modules/conduit/skills",
      "/home/user/conduit/skills",
      "/opt/conduit/skills"
    ],
    "execution": {
      "timeout_seconds": 300,
      "environment": {
        "PATH": "/usr/bin:/bin"
      },
      "allowed_actions": {
        "*": ["read", "write", "exec"]
      }
    },
    "cache": {
      "enabled": true,
      "ttl_seconds": 1800
    }
  }
}
```

### Search

```json
{
  "search": {
    "path": "./gateway.search.db",
    "beads_dir": ".beads",
    "enabled": true,
    "default_strategy": "anthropic",
    "brave_api_key": "${BRAVE_API_KEY}",
    "cache_ttl_minutes": 15,
    "max_results": 5,
    "timeout_seconds": 10
  }
}
```

### MQTT

Optional MQTT event ingest for IoT/home automation. See [MQTT Integration](mqtt.md) for full documentation.

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
    "buffer_max_topics": 500,
    "publish_allowed": false,
    "tls": {
      "ca_cert": "/path/to/ca.pem",
      "insecure": false
    }
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable MQTT event ingest |
| `broker_url` | string | required | Broker address (`tcp://` or `ssl://`) |
| `client_id` | string | `"conduit"` | MQTT client ID |
| `username` | string | `""` | Broker username (supports `${ENV_VAR}`) |
| `password` | string | `""` | Broker password (supports `${ENV_VAR}`) |
| `topics` | string[] | required | Topic subscriptions (wildcards: `#`, `+`) |
| `qos` | int | `0` | QoS level (0, 1, or 2) |
| `buffer_max_age_seconds` | int | `3600` | Max event age before pruning |
| `buffer_max_events` | int | `1000` | Max events per topic |
| `buffer_max_topics` | int | `500` | Max tracked topics |
| `publish_allowed` | bool | `false` | Allow AI to publish messages |

### Brain (Cognitive Memory)

Tiered cognitive memory system that gives the AI agent persistent memory across interactions. Stores facts in three tiers: long-term memory (SQLite-persisted), working memory (in-process per session), and a scratchpad stack (temporary LIFO).

#### Minimal Setup

```json
{
  "brain": {
    "enabled": true
  },
  "tools": {
    "enabled_tools": ["Brain"]
  }
}
```

That's all you need. The database path auto-derives from your gateway DB (e.g., `gateway.db` → `gateway.brain.db`), and all other settings have sensible defaults.

#### Full Configuration

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
    "access_count_cap": 100
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable the Brain subsystem |
| `path` | string | `""` | Path to brain.db file. Empty = derived from gateway DB path |
| `max_ltm_entries` | int | `10000` | Maximum long-term memory entries. Lowest-salience entries evicted when exceeded |
| `wm_grace_period_seconds` | int | `300` | Seconds to keep working memory entries after session ends |
| `auto_flush_seconds` | int | `600` | Interval for background working memory cleanup |
| `consolidate_threshold` | float | `0.6` | Salience score above which working memory entries are auto-promoted to LTM |
| `evict_threshold` | float | `0.1` | Salience score below which working memory entries are evicted |
| `auto_promote` | bool | `true` | Automatically promote high-salience working memory to LTM during consolidation |

**Salience Formula Tuning:**

Salience determines which facts are important. The formula is: `(access_score × access_weight) + (recency_score × recency_weight) + (tier_score × tier_weight)`.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `access_weight` | float | `0.4` | Weight for access frequency (how often a fact is read/written) |
| `recency_weight` | float | `0.4` | Weight for recency (how recently a fact was accessed) |
| `tier_weight` | float | `0.2` | Weight for storage tier (LTM=0.8, working=0.5, scratch=0.1) |
| `recency_decay_rate` | float | `1.0` | How fast recency decays. Formula: `1/(1 + hours × rate)`. Higher = faster decay |
| `access_count_cap` | int | `100` | Access count at which access score saturates to 1.0 |

> **Note:** `access_weight + recency_weight + tier_weight` must sum to 1.0.

**How It Works:**
1. The agent stores facts in working memory during a session (e.g., `solar.production = 5000W`)
2. Facts accessed frequently get higher salience scores
3. On consolidation (session end), high-salience facts are promoted to LTM (persisted in SQLite)
4. Low-salience facts are evicted from working memory
5. On next session, the agent can recall persisted facts from LTM without re-querying tools
6. Sub-agents can read the parent session's working memory (read-only sharing)

**Integration with Skills:**

Skills can declare brain keys they produce via the `produces` field in their SKILL.md metadata:

```yaml
conduit:
  produces:
    - solar.production
    - solar.panel_count
```

After a skill executes successfully, matching keys from the result data are auto-stored in working memory.

**Integration with Search:**

When both Brain and SearchDB are enabled, brain LTM entries are indexed into an FTS5 virtual table (`brain_ltm_fts` in search.db) for BM25-ranked full-text search via MemorySearch. This provides better recall quality than the default LIKE-based search.

#### REM Sleep Cycle

The REM (Replay, Evaluate, Maintain) Sleep cycle runs offline to consolidate, prune, and groom memory. See [Brain & REM Sleep Reference](brain.md#rem-sleep-cycle) for full architecture details.

```json
{
  "brain": {
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

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `rem_enabled` | bool | `true` | Enable REM sleep cycle scheduling |
| `rem_schedule` | string | `"0 2 * * *"` | Cron schedule (default: 2 AM daily) |
| `rem_integration_day` | int | `0` | Day of week for integration phase (0=Sunday) |
| `rem_prune_age_days` | int | `30` | Days without access before entry becomes prune candidate |
| `rem_salience_decay_rate` | float | `0.1` | Salience subtracted during consolidation decay |
| `rem_groom_with_llm` | bool | `true` | Use LLM for re-extraction during grooming (reserved) |
| `rem_log_path` | string | `"memory/rem-log"` | Directory for REM cycle report logs |

### Kubernetes

Multi-cluster Kubernetes configuration for the K8s tool. Kubeconfig paths support environment variable expansion.

```json
{
  "kubernetes": {
    "enabled": true,
    "clusters": [
      {
        "name": "production",
        "kubeconfig_path": "${HOME}/.kube/config",
        "context": "prod-cluster",
        "default_namespace": "default",
        "allowed_namespaces": ["default", "app", "monitoring"],
        "safety_level": "read"
      },
      {
        "name": "staging",
        "kubeconfig_path": "${HOME}/.kube/staging.yaml",
        "context": "staging-cluster",
        "safety_level": "modify"
      }
    ],
    "defaults": {
      "namespace": "default",
      "safety_level": "read"
    }
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable Kubernetes integration |
| `clusters` | array | `[]` | Cluster configurations |
| `defaults.namespace` | string | `"default"` | Default namespace |
| `defaults.safety_level` | string | `"read"` | Default safety level |

**Safety Levels:**
- `read` — get, list, describe, logs, watch, events, top, clusters, namespaces (auto-approved)
- `modify` — scale, rollout, label, annotate, cordon, uncordon (recommended confirmation)
- `dangerous` — delete, apply, create, edit, drain, exec, patch (requires approval)

See [Kubernetes Integration](kubernetes.md) for full tool documentation.

### PagerDuty

PagerDuty REST API v2 integration for incident management.

```json
{
  "pagerduty": {
    "enabled": true,
    "api_token": "${PAGERDUTY_API_TOKEN}",
    "default_service_id": "PXXXXXX",
    "default_escalation_policy_id": "PXXXXXX",
    "base_url": "https://api.pagerduty.com",
    "rate_limit_rps": 5.0
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable PagerDuty integration |
| `api_token` | string | required | API token (supports `${ENV_VAR}`) |
| `default_service_id` | string | `""` | Default service for new incidents |
| `default_escalation_policy_id` | string | `""` | Default escalation policy |
| `base_url` | string | `"https://api.pagerduty.com"` | API base URL |
| `rate_limit_rps` | float | `5.0` | Requests per second limit |

### Datadog

Datadog API integration for metrics, logs, and monitors.

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
| `api_key` | string | required | API key (supports `${ENV_VAR}`) |
| `app_key` | string | required | Application key (supports `${ENV_VAR}`) |
| `site` | string | `"datadoghq.com"` | Datadog site (e.g., `us5.datadoghq.com`, `datadoghq.eu`) |
| `rate_limit_rps` | float | `5.0` | Requests per second limit |

### Debug

```json
{
  "debug": {
    "enabled": false,
    "log_level": "info",
    "log_requests": false,
    "log_responses": false
  }
}
```

### Speech-to-Text (STT)

Enables transcription of incoming voice messages (e.g., Telegram voice notes) via OpenAI Whisper API. Transcribed text is passed to the AI as a normal message with voice metadata.

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
| `provider` | string | `"whisper"` | STT provider (`"whisper"` is currently the only option) |
| `api_key` | string | — | OpenAI API key (supports `${ENV_VAR}` expansion) |
| `model` | string | `"whisper-1"` | Whisper model to use |

## Environment Variables

Common environment variables:

| Variable | Description |
|----------|-------------|
| `ANTHROPIC_API_KEY` | Anthropic API key |
| `ANTHROPIC_OAUTH_TOKEN` | OAuth token for Claude Code compatibility |
| `TELEGRAM_BOT_TOKEN` | Telegram bot token |
| `OPENAI_API_KEY` | OpenAI API key (used for STT/Whisper and vector embeddings) |
| `BRAVE_API_KEY` | Brave Search API key |
| `MQTT_USERNAME` | MQTT broker username |
| `MQTT_PASSWORD` | MQTT broker password |
| `PAGERDUTY_API_TOKEN` | PagerDuty REST API token |
| `DD_API_KEY` | Datadog API key |
| `DD_APP_KEY` | Datadog application key |
| `CONDUIT_CONFIG` | Config file path |
| `CONDUIT_DATABASE` | Database file path |

## Config File Locations

The gateway looks for config in this order:
1. `--config` flag value
2. `CONDUIT_CONFIG` environment variable
3. `config.json` in current directory
4. `~/.conduit/config.json`

## Database Path Auto-Detection

Database path is auto-detected from config filename:
- `config.json` → `gateway.db`
- `config.telegram.json` → `config.telegram.db`
- `config.live.json` → `config.live.db`

Override with `--database` flag or `database` config field.

## Example Configs

The `configs/` directory contains example configurations:
- `configs/config.example.json` - Full example with comments
- `configs/config.telegram.json` - Telegram-focused
- `configs/config.tools.json` - Tools-focused
- `configs/config.skills.json` - Skills-focused

Copy and customize:
```bash
cp configs/config.example.json config.json
# Edit config.json with your settings
```

## See Also

- [z.ai Provider](z-ai.md) — Setup guide for Zhipu AI's GLM models
- [Brain & REM Sleep](brain.md) — Full architecture reference for cognitive memory
- [SRE Tools](sre-tools.md) — PagerDuty, Datadog, and incident correlation
- [Google Workspace](google-workspace.md) — Gmail and Calendar integration
- [Remote SSH](remote-ssh.md) — Multi-host SSH execution
- [Advanced Features](advanced-features.md) — SearchDB, prompt caching, context compaction
- [Tools Reference](tools-reference.md) — All built-in tool documentation
