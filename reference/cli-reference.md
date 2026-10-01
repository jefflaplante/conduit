# CLI Reference

Complete reference for all `conduit` CLI commands.

## Global Flags

```bash
conduit [command] [flags]

Global Flags:
  --config string     Config file path (default "config.json")
  --database string   Database file path (auto-detected if not specified)
  -v, --verbose       Enable verbose logging
  --pidfile string    PID file path (default: $RUNTIME_DIRECTORY/conduit.pid, else {data_dir}/conduit.pid)
  --version           Show version information
```

## Commands

### server

Start the Conduit Gateway server.

```bash
conduit server [flags]

# Examples
conduit server                           # Start with default config
conduit server --config config.live.json # Start with specific config
conduit server --verbose                 # Start with debug logging
```

### token

Manage authentication tokens for API access.

```bash
# Create a new token
conduit token create --client-name "my-app" --role automation --expires-in "1y"
conduit token create --client-name "temp" --role automation --expires-in "7d"

# List all active tokens (ID prefix, client, role, ...)
conduit token list

# Set a token's role: owner (you) or automation (scripts; own sessions only, never approves)
conduit token set-role 3f2a9c1e automation

# Revoke a token
conduit token revoke 3f2a9c1e   # ID prefix from `conduit token list`

# Show which database and token-secret source (never the secret) are used
conduit token info

# Export token for environment variable
conduit token export 3f2a9c1e --format env
```

### tui

Launch the interactive terminal chat client.

```bash
conduit tui [flags]

Flags:
  --url string     Gateway WebSocket URL (default "ws://localhost:18789/ws")
  --token string   Authentication token (saved to ~/.conduit/tui.json)

# Examples
conduit tui                              # Connect with saved token
conduit tui --token "conduit_v1_..."        # Connect with specific token
conduit tui --url "ws://remote:18789/ws" # Connect to remote gateway
```

### ssh-server

Start standalone SSH server for TUI access.

```bash
conduit ssh-server [flags]

Flags:
  --listen string            SSH listen address (default ":2222")
  --host-key string          Path to SSH host key
  --authorized-keys string   Path to authorized_keys file
  --gateway-url string       Gateway WebSocket URL
  --gateway-token string     Gateway authentication token

# Example
conduit ssh-server --listen ":2222" --gateway-token "conduit_v1_..."
```

### ssh-keys

Manage SSH authorized keys.

```bash
# Initialize SSH key infrastructure
conduit ssh-keys init

# List authorized keys with fingerprints
conduit ssh-keys list

# Add a key from file
conduit ssh-keys add ~/.ssh/id_ed25519.pub

# Add a key inline
conduit ssh-keys add "ssh-ed25519 AAAA... user@host"

# Remove a key by fingerprint
conduit ssh-keys remove "SHA256:abc123..."
```

### chain

Manage and execute saved tool chains (multi-step workflows).

```bash
# List all chains
conduit chain list
conduit chain list --json

# Show chain details
conduit chain show my-workflow
conduit chain show deploy-pipeline --json

# Create a new chain
conduit chain create my-workflow                    # Create scaffold
conduit chain create deploy --from=deploy.json     # Create from file

# Validate a chain
conduit chain validate my-workflow

# Execute a chain
conduit chain run my-workflow
conduit chain run deploy --var env=production --var version=1.2.3
conduit chain run build --dry-run                  # Validate without executing
```

Chains are JSON files stored in `workspace/chains/` that define tool sequences with variable substitution and dependency ordering.

### briefing

Generate and manage session briefings.

```bash
# Generate a briefing for recent sessions
conduit briefing generate

# Show briefing for specific session
conduit briefing show <session-key>

# List recent briefings
conduit briefing list
```

### backup

Backup and restore gateway data.

```bash
# Create a backup
conduit backup create
conduit backup create --output /path/to/backup.tar.gz

# List available backups
conduit backup list

# Restore from backup
conduit backup restore backup-2026-02-26.tar.gz
conduit backup restore backup.tar.gz --dry-run     # Preview without restoring
```

Backups include: database, brain database (`brain.path`, or derived from `database.path`), config, workspace files, and optionally SSH keys and skills. Database snapshots use `VACUUM INTO`, so un-checkpointed WAL content is included.

Restore refuses to run while the gateway is live (the `--pidfile` names a running process, or the `--config` port accepts connections); `--force` only skips the confirmation prompt. Databases are written to a temp file and renamed into place, and stale `-wal`/`-shm` files are removed. Archive entries that resolve outside their target directory (e.g. `workspace/../config.json`) are rejected.

### maintenance

Database maintenance: prune old automated sessions and optimise the gateway database.

```bash
# See exactly what would be deleted (changes nothing)
conduit maintenance run --dry-run

# Run all tasks: session_cleanup, then database_maintenance
conduit maintenance run

# Run one task
conduit maintenance run-task session_cleanup
conduit maintenance run-task database_maintenance

# Database size, sessions/messages per key prefix, what a run would prune, and backups (read-only)
conduit maintenance status

# Effective settings (config file "maintenance" section over defaults)
conduit maintenance config
```

| Flag | Commands | Description |
|------|----------|-------------|
| `--dry-run` | run, run-task | Report what would be deleted/optimised; change nothing |
| `--no-backup` | run, run-task | Skip the pre-delete / pre-VACUUM backup |
| `--retention-days N` | run, run-task, status | Override `maintenance.retention_days` (N >= 1) |
| `--json` | all | JSON output |
| `--verbose` | all | Task log lines |
| `--force` | run, run-task | Deprecated, no effect |

**Database.** `database.path` from `--config`, or the global `--database` (same resolution as the server, `token` and `pairing`). The file must exist; the command never creates a database or runs migrations. It is opened with the server's DSN (WAL, 5 s busy timeout), so it can run while the gateway is running.

**session_cleanup** deletes a session, with its messages, only when:

- its key starts with a prunable prefix (default `cron_`, `heartbeat_`, `subagent_`, `test_`: cron jobs, HEARTBEAT.md runs, sub-agents and the `/api/test/message` HTTP endpoint). Matching is on the literal key prefix, and
- its last activity (the later of `sessions.updated_at` and its newest message) is older than the retention window (default 30 days).

Since conduit-385r, cron and heartbeat runs that give no reply delete their own session when they end (the same deletion: messages, context, `session_summaries` / `claude_code_sessions` rows, the search.db mirror; see [agent-heartbeat.md](agent-heartbeat.md#run-sessions)). gateway.db therefore no longer gains ~200 prompt-only or empty automated sessions a day. The retention prune is mainly a backstop: it removes automated sessions that hold a transcript (runs that replied, sub-agents, test requests) and anything a failed cleanup or an older binary left behind.

Telegram (`telegram_`) and TUI (`tui_`) sessions, and any key with another prefix, are never deleted. `--dry-run` and `status` print what is kept as well: the old sessions and messages that are protected. Timestamps are parsed in Go. Since gateway.db migration 10 (conduit-a636) `messages.timestamp`, `sessions.created_at` and `sessions.updated_at` are stored as canonical UTC text (`2006-01-02 15:04:05.000000000`); older databases mixed Go `time.String()` text with a zone and `m=+…` suffix, RFC 3339 and SQLite `CURRENT_TIMESTAMP`, and the migration leaves any value it cannot parse as it was. A session with an unparseable timestamp is kept.

Before deleting anything a `VACUUM INTO` backup is written next to the database (or to `maintenance.backup_dir`) as `<db>.backup.<UTC timestamp>` with mode 0600, and its path is printed; `--no-backup` skips it. After a successful backup, older backups beyond the newest `maintenance.keep_backups` (default 3; `0` keeps all) are deleted from that directory. Only files named exactly `<db>.backup.<timestamp>` are rotated; other copies next to the database (`*.bak-*`, `*.pre-*`, …) are never touched. `--dry-run` shows which backups the run would remove, and `status` lists the existing backups with the same plan. Deletes run in transactions of `maintenance.batch_size` sessions (default 500). Each session is re-checked inside its transaction and skipped if it changed since the plan. Rows keyed by the session are removed too: messages (gateway.db `messages_fts` follows through its triggers), and `session_summaries` / `claude_code_sessions` rows where those tables exist. If search is enabled and search.db exists, the pruned sessions' rows are removed from its `messages_fts` mirror as well (otherwise the gateway rebuilds the mirror at its next start, when the counts differ).

**database_maintenance** runs `ANALYZE`/`PRAGMA optimize`, and when the file exceeds the vacuum threshold (100 MB) a backup (same rules as above) followed by a WAL checkpoint and `VACUUM`. If the running gateway keeps the database busy, `VACUUM` is skipped. There is no fallback to copying the raw database file.

Tasks run only when invoked: there is no background schedule and no maintenance window. To run maintenance periodically, call `conduit maintenance run` from a system timer (cron, systemd). A run that has a failed task exits non-zero.

### tools

Discover and inspect available tools.

```bash
# List all available tools
conduit tools list

# Show tool details
conduit tools describe Chain
conduit tools describe WebSearch

# Show tool JSON schema
conduit tools schema Chain

# Show usage examples
conduit tools examples WebSearch
```

### pairing

Manage pairing codes for channel authentication.

```bash
# List pending Telegram pairing codes
conduit pairing telegram list [--include-expired]

# Approve a pairing code sent to the bot
conduit pairing telegram approve <CODE>
```

### cron

Scheduler job maintenance for `cron_jobs.json`.

```bash
# Preview rewriting UTC-written Go jobs as CRON_TZ wall-clock jobs
conduit cron migrate-tz --from UTC --to America/New_York --file workspace/cron_jobs.json --dry-run

# Apply (timestamped backup, atomic replace; re-running is a no-op)
conduit cron migrate-tz --from UTC --to America/New_York --file workspace/cron_jobs.json --apply

# Also convert system (crontab) jobs; --apply additionally needs --update-crontab
conduit cron migrate-tz --from UTC --to America/New_York --file workspace/cron_jobs.json --include-system --dry-run
```

### brain

```bash
# Export the Brain LTM graph to JSON
conduit brain export [--db <brain.db>] [--out <file>]
```

### restart / stop / status

Signal a running gateway (found through its PID file) instead of going through an HTTP endpoint.

The gateway writes its PID file to `--pidfile` if given, else `$RUNTIME_DIRECTORY/conduit.pid` (systemd `RuntimeDirectory=conduit`, i.e. `/run/conduit/conduit.pid`), else `{data_dir}/conduit.pid` (`CONDUIT_DATA_DIR`, then config `data_dir`, then `~/.conduit`). These commands and `backup restore` run outside the unit's environment, so without `--pidfile` they search `$RUNTIME_DIRECTORY/conduit.pid`, `/run/conduit/conduit.pid`, `{data_dir}/conduit.pid`, then the legacy `/tmp/conduit.pid`, using the first file that names a live process. An explicit `--pidfile` disables the search.

```bash
conduit restart   # graceful restart (SIGHUP)
conduit stop      # graceful shutdown (SIGTERM)
conduit status    # report whether Conduit is running
```

A graceful restart drains in-flight work for up to 30s (SIGTERM: 15s; a SIGTERM during a SIGHUP drain shortens it), waiting for running turns and scheduler jobs. Tool calls started during the drain are capped to the remaining budget and told to wrap up. Turns still running at the deadline are cancelled; after the restart, the owner is told on the turn's channel which request was cut off (or it is resumed automatically with `restart_resume: "auto"`). See [Restart Resume](configuration.md#restart-resume).

### auth

Manage OAuth authentication for AI providers.

```bash
# Start OAuth device flow
conduit auth login

# Check authentication status
conduit auth status

# Clear stored credentials
conduit auth logout
```

### metrics

Start the metrics dashboard HTTP server.

```bash
conduit metrics [flags]

Flags:
  --port int   Metrics server port (default 9090)
```

### loadtest

Run load tests against the AI provider.

```bash
conduit loadtest [flags]

# Run with mock backend for testing
conduit loadtest --mock --requests 100 --concurrency 10
```

### version

Show version information.

```bash
conduit version

# Output includes:
# - Version string
# - Git commit hash
# - Build date
```

## Makefile Shortcuts

Common operations via Makefile:

```bash
make build          # Build the gateway binary
make build-prod     # Build optimized production binary
make run            # Build and run the gateway
make run-telegram   # Run with Telegram adapter enabled
make test           # Run tests
make test-coverage  # Run tests with coverage report
make lint           # Run linters
make format         # Format code
make clean          # Clean build artifacts
make deps           # Download Go dependencies
make init           # Full initialization for new setup
make health         # Check if gateway is running
make help           # Show all commands
```
