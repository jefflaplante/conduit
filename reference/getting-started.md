# Getting Started

Quick guide to get Conduit Go Gateway running.

## Prerequisites

- **Go 1.22+** - [Install Go](https://golang.org/dl/)
- **Node.js 18+** - For TypeScript adapters (optional)

## Installation

```bash
# Clone the project
git clone https://github.com/yourorg/conduit.git
cd conduit

# Install Go dependencies
go mod tidy

# Build the gateway
make build

# (Optional) Install TypeScript adapter dependencies
make channel-deps
```

## Basic Usage

```bash
# Run with test config (no API keys needed)
./bin/gateway --config config.test.json --verbose

# Run with Telegram adapter
export TELEGRAM_BOT_TOKEN="your_bot_token"
make run-telegram

# Run with full AI integration
export ANTHROPIC_API_KEY="your_api_key"
./bin/gateway --config config.json --verbose
```

## First-Time Setup

For a complete new installation:

```bash
# Full initialization (creates workspace, config from example)
make init

# Create your first authentication token
./bin/gateway token create --client-name "my-client" --expires-in "1y"

# Start the gateway
./bin/gateway server --verbose
```

## Environment Variables

Key environment variables:

| Variable | Description |
|----------|-------------|
| `ANTHROPIC_API_KEY` | Anthropic API key for Claude models |
| `TELEGRAM_BOT_TOKEN` | Telegram bot token (from @BotFather) |
| `BRAVE_API_KEY` | Brave Search API key (optional fallback) |
| `CONDUIT_CONFIG` | Path to config file (default: config.json) |

See [docs/guides/ENV_AND_SECRETS.md](guides/ENV_AND_SECRETS.md) for detailed environment configuration.

## Verify Installation

```bash
# Check if gateway is running
make health

# Or manually
curl http://localhost:18789/health
```

## Next Steps

- [CLI Reference](cli-reference.md) - All available commands
- [Configuration](configuration.md) - Full configuration options
- [Tools Reference](tools-reference.md) - Built-in AI tools
- [TUI & SSH Access](tui-ssh.md) - Terminal interface
