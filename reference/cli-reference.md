# CLI Reference

Complete reference for all `conduit` CLI commands.

## Global Flags

```bash
conduit [command] [flags]

Global Flags:
  --config string     Config file path (default "config.json")
  --database string   Database file path (auto-detected if not specified)
  -v, --verbose       Enable verbose logging
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
conduit token create --client-name "my-app" --expires-in "1y"
conduit token create --client-name "temp" --expires-in "7d"

# List all active tokens
conduit token list

# Revoke a token
conduit token revoke conduit_v1_abc123

# Export token for environment variable
conduit token export conduit_v1_abc123 --format env
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

Backups include: database, config, workspace files, SSH keys, and skills.

### maintenance

Database maintenance operations.

```bash
# Run maintenance tasks
conduit maintenance run

# Run specific task
conduit maintenance run-task vacuum
conduit maintenance run-task cleanup

# Check maintenance status
conduit maintenance status

# View maintenance configuration
conduit maintenance config
```

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
# Generate a pairing code
conduit pairing create --channel telegram

# List active pairing codes
conduit pairing list

# Revoke a pairing code
conduit pairing revoke <code>
```

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
