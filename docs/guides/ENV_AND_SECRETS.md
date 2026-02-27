# Environment Files and Secrets

Conduit resolves a **data directory** at startup, loads `.env` files from it, and then expands `${ENV_VAR}` placeholders in your config. This means you can keep secrets out of `config.json` entirely.

## Quick Start

```bash
# 1. Create your data directory (auto-created on first run, but you can do it now)
mkdir -p ~/.conduit

# 2. Put your secrets in a .env file
cat > ~/.conduit/.env << 'EOF'
ANTHROPIC_API_KEY=sk-ant-api03-...
BRAVE_API_KEY=BSA...
TELEGRAM_BOT_TOKEN=7123456789:AAF...
EOF
chmod 600 ~/.conduit/.env

# 3. Reference them in config.json
# {
#   "ai": { "providers": [{ "api_key": "${ANTHROPIC_API_KEY}" }] },
#   "tools": { "services": { "brave": { "api_key": "${BRAVE_API_KEY}" } } }
# }
```

That's it. The gateway loads `.env` before parsing config, so the placeholders resolve automatically.

## How It Works

### Startup Sequence

1. **Resolve data directory** — checks `CONDUIT_DATA_DIR` env var, falls back to `~/.conduit/`
2. **Create subdirectories** — `config/`, `auth/`, `ssh/`, `data/`, `workspace/` (all 0700)
3. **Migrate legacy dir** — copies `~/.conduit/` contents to `~/.conduit/` if the old dir exists, then leaves a backward-compat symlink
4. **Load `.env` files** — into the process environment (never overrides existing vars)
5. **Load `config.json`** — tilde expansion, then secrets file loading, then `${ENV_VAR}` expansion

CLI subcommands (`token`, `pairing`, `ssh-keys`, etc.) also load `.env` files, so env vars are available everywhere.

### `.env` File Search Order

Files are loaded first-write-wins — earlier files take priority:

| Priority | Path | Notes |
|----------|------|-------|
| 1 | `$CONDUIT_ENV_FILE` | If set, **only** this file is loaded |
| 2 | `~/.conduit/.env` | Data directory root |
| 3 | `./.env` | Current working directory |

- Missing files are silently skipped
- Existing shell/systemd env vars are **never** overwritten
- Format: `KEY=VALUE`, one per line. `#` comments and blank lines allowed. Optional quotes around values are stripped.

### Config Fields That Expand `${ENV_VAR}`

| Field | Example |
|-------|---------|
| `ai.providers[*].api_key` | `"${ANTHROPIC_API_KEY}"` |
| `ai.providers[*].auth.oauth_token` | `"${ANTHROPIC_OAUTH_TOKEN}"` |
| `ai.providers[*].auth.refresh_token` | `"${ANTHROPIC_OAUTH_REFRESH_TOKEN}"` |
| `ai.providers[*].auth.client_id` | `"${ANTHROPIC_OAUTH_CLIENT_ID}"` |
| `ai.providers[*].auth.client_secret` | `"${ANTHROPIC_OAUTH_CLIENT_SECRET}"` |
| `channels[*].config.*` | `"${TELEGRAM_BOT_TOKEN}"` |
| `tools.services.*.*` | `"${BRAVE_API_KEY}"` |
| `vector.openai.api_key` | `"${OPENAI_API_KEY}"` |
| `data_dir` | `"${CONDUIT_DATA_DIR}"` |

Expansion uses Go's `os.ExpandEnv` — undefined vars expand to empty string, no error.

## The `secrets_file` Alternative

If you prefer a separate secrets file referenced from config:

```json
{
  "secrets_file": "~/.conduit/secrets.env",
  "ai": {
    "providers": [{ "api_key": "${ANTHROPIC_API_KEY}" }]
  }
}
```

The secrets file is loaded after tilde expansion but before `${ENV_VAR}` expansion, so the placeholders resolve correctly. Same KEY=VALUE format as `.env`, same never-override behavior.

The difference from `.env` loading: `secrets_file` is explicit in config and loaded slightly later in the pipeline. Both approaches work — pick whichever fits your workflow.

## Data Directory Override

Default is `~/.conduit/`. Override with:

| Method | Example |
|--------|---------|
| Environment variable | `CONDUIT_DATA_DIR=/opt/conduit` |
| Config field | `"data_dir": "/opt/conduit"` |

The env var takes precedence (it's resolved before config is loaded).

## Legacy Migration

If you previously used `~/.conduit/`, the gateway automatically:

1. Copies files to `~/.conduit/` (never overwrites existing files)
2. Creates a symlink `~/.conduit` → `~/.conduit` for backward compatibility
3. Logs how many files were migrated

This is a one-time operation. Once the symlink exists, migration is skipped.

## Common Environment Variables

```bash
# AI Providers
ANTHROPIC_API_KEY=sk-ant-api03-...
ANTHROPIC_OAUTH_TOKEN=sk-ant-oat-...
OPENAI_API_KEY=sk-...

# Channels
TELEGRAM_BOT_TOKEN=7123456789:AAF...

# Search
BRAVE_API_KEY=BSA...

# Services
UNVR_URL=https://192.168.1.1
UNVR_API_KEY=...

# Data directory override
CONDUIT_DATA_DIR=/custom/path
```

## Precedence Summary

For any given environment variable, the first source wins:

1. Shell / systemd environment (always wins)
2. `.env` at data dir root (`~/.conduit/.env`)
3. `.env` at working directory (`./.env`)
4. `secrets_file` from config.json

A variable set by any earlier source is never overwritten by a later one.
