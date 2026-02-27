# Channel System

Conduit Go uses a unified channel system for managing different communication adapters.

## Overview

The channel system provides:
- **Unified lifecycle management** - Start, stop, and monitor all channels
- **Factory pattern** - Pluggable adapter registration
- **Crash isolation** - Channel adapters fail independently
- **Auto-reconnect** - Graceful degradation and recovery

## Available Channels

| Channel | Type | Status | Description |
|---------|------|--------|-------------|
| Telegram | Native Go | Production | Full bot API integration |
| TUI | Native Go | Production | Terminal chat client |
| SSH | Native Go | Production | Remote TUI access via SSH |
| WebSocket | Native Go | Production | Browser/app clients |
| Discord | Native Go | Planned | Discord bot integration |
| Slack | Native Go | Planned | Slack app integration |
| WhatsApp | TypeScript | Legacy | Via Baileys library |
| Signal | TypeScript | Legacy | Via signal-cli |

## Channel Adapter Interface

All channel adapters implement:

```go
type ChannelAdapter interface {
    ID() string
    Name() string
    Type() string
    Start(ctx context.Context) error
    Stop() error
    SendMessage(msg *protocol.OutgoingMessage) error
    ReceiveMessages() <-chan *protocol.IncomingMessage
    Status() ChannelStatus
    IsHealthy() bool
}
```

Optional interfaces:
- `StreamingAdapter` - For streaming responses
- `TypingIndicator` - For typing status updates

## Telegram Adapter

The native Go Telegram adapter uses the `go-telegram/bot` library.

### Features

- Text messages with full bidirectional chat
- Photo handling with captions
- Callback queries for inline buttons
- User metadata tracking
- Automatic session creation per chat ID
- Polling and webhook modes
- Graceful error handling with auto-reconnect

### Configuration

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
        "debug": true
      }
    }
  ]
}
```

### Bot Setup

1. Start chat with [@BotFather](https://t.me/BotFather) on Telegram
2. Send `/newbot` and follow prompts
3. Copy the bot token
4. Set environment variable: `export TELEGRAM_BOT_TOKEN="your_token"`
5. Run: `make run-telegram`

See [TELEGRAM_ADAPTER.md](TELEGRAM_ADAPTER.md) for detailed documentation.

## Channel Manager

The channel manager orchestrates all adapters:

```go
// Create and register adapters
manager := channels.NewManager()
manager.RegisterFactory(telegram.NewFactory())

// Start all configured channels
manager.Start(ctx, channelConfigs)

// Send messages through any channel
manager.SendMessage(outgoingMsg)

// Receive messages from all channels
for msg := range manager.ReceiveMessages() {
    // Process incoming message
}

// Monitor channel health
status := manager.GetStatus()
```

## Adding a New Adapter

1. Create `internal/channels/yourplatform/adapter.go`:

```go
package yourplatform

import (
    "context"
    "conduit/internal/channels"
    "conduit/pkg/protocol"
)

type Adapter struct {
    config   Config
    messages chan *protocol.IncomingMessage
}

func (a *Adapter) ID() string   { return "yourplatform_" + a.config.ID }
func (a *Adapter) Name() string { return "YourPlatform" }
func (a *Adapter) Type() string { return "yourplatform" }

func (a *Adapter) Start(ctx context.Context) error {
    // Initialize connection
    return nil
}

func (a *Adapter) Stop() error {
    close(a.messages)
    return nil
}

func (a *Adapter) SendMessage(msg *protocol.OutgoingMessage) error {
    // Send to platform
    return nil
}

func (a *Adapter) ReceiveMessages() <-chan *protocol.IncomingMessage {
    return a.messages
}

func (a *Adapter) Status() channels.ChannelStatus {
    return channels.ChannelStatus{Connected: true}
}

func (a *Adapter) IsHealthy() bool { return true }
```

2. Create the factory:

```go
type Factory struct{}

func NewFactory() *Factory { return &Factory{} }

func (f *Factory) Type() string { return "yourplatform" }

func (f *Factory) Create(cfg channels.ChannelConfig) (channels.ChannelAdapter, error) {
    return &Adapter{config: parseConfig(cfg)}, nil
}
```

3. Register in `gateway.go`:

```go
channelManager.RegisterFactory(yourplatform.NewFactory())
```

## Channel Status Monitoring

Check channel health via API:

```bash
curl -H "Authorization: Bearer claw_v1_..." \
  http://localhost:18789/api/channels/status
```

Response:
```json
{
  "channels": [
    {
      "id": "telegram_main",
      "name": "telegram",
      "type": "telegram",
      "connected": true,
      "healthy": true,
      "last_activity": "2026-02-26T12:00:00Z"
    }
  ]
}
```

## Hybrid Architecture

Conduit Go supports both native Go adapters and TypeScript process adapters for legacy integrations:

```
                    Native Go              TypeScript Process
                    Adapters               Adapters

                    - Telegram             - WhatsApp (Baileys)
                    - TUI                  - Signal
                    - SSH                  - Other legacy
                    - Discord (planned)
                    - Slack (planned)

                           │                      │
                           └──────────┬───────────┘
                                      │
                              Channel Manager
                         Unified lifecycle management
```

This allows gradual migration while maintaining backward compatibility with existing TypeScript adapters.
