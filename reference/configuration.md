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
      "Message", "Cron", "Chain", "Gateway"
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
- `openai` - GPT models

Model aliases (for `/model` command):
- `haiku` - claude-3-haiku-20240307
- `sonnet` - claude-3-5-sonnet-20241022
- `opus` - claude-3-opus-20240229
- `opus46` - claude-opus-4-6

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

Available tools: Read, Write, Edit, Bash, Glob, MemorySearch, Find, Facts, WebSearch, WebFetch, Message, Tts, Cron, Chain, Gateway, Context, Image, SessionsList, SessionsSend, SessionsSpawn, SessionStatus, google_workspace

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

## Environment Variables

Common environment variables:

| Variable | Description |
|----------|-------------|
| `ANTHROPIC_API_KEY` | Anthropic API key |
| `ANTHROPIC_OAUTH_TOKEN` | OAuth token for Claude Code compatibility |
| `TELEGRAM_BOT_TOKEN` | Telegram bot token |
| `BRAVE_API_KEY` | Brave Search API key |
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
