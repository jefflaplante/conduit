# Telegram Channel Adapter

Native Go implementation of a Telegram channel adapter for Conduit Go Gateway using the `go-telegram/bot` library.

## Architecture

```
┌─────────────────────────┐
│   Conduit Gateway     │
│                         │
│ ┌─────────────────────┐ │
│ │ Channel Manager     │ │ ← Manages all adapters
│ └─────────────────────┘ │
│           │             │
│ ┌─────────────────────┐ │
│ │ Telegram Adapter    │ │ ← Native Go implementation
│ │                     │ │
│ │ • go-telegram/bot   │ │
│ │ • Goroutines        │ │
│ │ • Direct integration│ │
│ └─────────────────────┘ │
└─────────────────────────┘
```

## Features

### Message Types Supported
- ✅ **Text messages** - Full support for text content
- ✅ **Photo messages** - Images with optional captions  
- ✅ **Callback queries** - Inline button interactions
- 🔄 **Future**: Documents, voice, video, stickers

### Bot Features
- ✅ **Polling mode** - Long polling for development
- ✅ **Webhook mode** - HTTP webhooks for production
- ✅ **Error handling** - Graceful degradation and retry logic
- ✅ **Rate limiting** - Built-in Telegram API rate limit handling
- ✅ **Session management** - Automatic session creation per chat ID

### Monitoring & Health
- ✅ **Status reporting** - Real-time adapter health
- ✅ **Message statistics** - Count tracking and metrics
- ✅ **Uptime tracking** - Adapter runtime monitoring
- ✅ **Bot information** - ID, username, and metadata

## Configuration

### Basic Configuration

```json
{
  \"channels\": [
    {
      \"name\": \"telegram\",
      \"type\": \"telegram\",
      \"enabled\": true,
      \"config\": {
        \"bot_token\": \"${TELEGRAM_BOT_TOKEN}\",
        \"webhook_mode\": false,
        \"debug\": true
      }
    }
  ]
}
```

### Configuration Options

| Option | Type | Description | Default |
|--------|------|-------------|---------|
| `bot_token` | `string` | Bot token from @BotFather | **required** |
| `webhook_mode` | `bool` | Use webhooks vs polling | `false` |
| `webhook_url` | `string` | Webhook URL (webhook mode only) | `\"\"` |
| `debug` | `bool` | Enable debug logging | `false` |

### Environment Variables

```bash
# Required: Get from @BotFather
export TELEGRAM_BOT_TOKEN=\"<YOUR_BOT_TOKEN>\"

# Optional: For AI integration
export ANTHROPIC_API_KEY=\"your_api_key\"
```

## Usage

### Quick Start

```bash
# 1. Get bot token from @BotFather on Telegram
# 2. Set environment variable
export TELEGRAM_BOT_TOKEN=\"your_bot_token\"

# 3. Run the gateway
cd ~/projects/conduit
make run-telegram
```

### Using with AI

```bash
# Set API keys for AI integration
export TELEGRAM_BOT_TOKEN=\"your_bot_token\"
export ANTHROPIC_API_KEY=\"your_api_key\"

# Run with full AI capabilities
./bin/conduit --config config.telegram.json --verbose
```

### Example Code

```go
package main

import (
    \"context\"
    \"conduit/internal/config\"
    \"conduit/internal/gateway\"
)

func main() {
    cfg, _ := config.Load(\"config.telegram.json\")
    gw, _ := gateway.New(cfg)
    gw.Start(context.Background())
}
```

## API Integration

### Message Flow

**Incoming Messages:**
1. Telegram → Bot Webhook/Polling
2. Bot → Adapter handler
3. Adapter → Channel Manager
4. Channel Manager → Gateway processor
5. Gateway → AI Router (optional)
6. Gateway → Session Store

**Outgoing Messages:**
1. AI Response/Tool Output
2. Gateway → Channel Manager  
3. Channel Manager → Telegram Adapter
4. Adapter → Telegram Bot API
5. Bot API → User's Telegram client

### Session Management

Sessions are automatically created and managed:
- **Session Key**: `telegram_{chat_id}`
- **User ID**: Telegram chat ID as string
- **Metadata**: First name, last name, username
- **Persistence**: SQLite storage with message history

## Bot Setup

### 1. Create Bot with BotFather

```
1. Start chat with @BotFather
2. Send: /newbot
3. Choose bot name: \"Conduit Assistant\"  
4. Choose username: \"conduitassistant_bot\"
5. Copy the token: <YOUR_BOT_TOKEN>
```

### 2. Configure Bot (Optional)

```
/setdescription - Set bot description
/setabouttext - Set about text  
/setuserpic - Upload bot profile picture
/setcommands - Set command menu
```

### 3. Test Bot

```
Send any message to your bot
Bot should respond with AI-generated replies
```

## Development

### Adding New Message Types

```go
// In adapter.go handleUpdate method
if update.Message != nil && update.Message.Document != nil {
    // Handle document messages
    incomingMsg := &protocol.IncomingMessage{
        // ... setup message
        Text: \"[Document]: \" + update.Message.Document.FileName,
        Metadata: map[string]string{
            \"type\": \"document\",
            \"file_id\": update.Message.Document.FileID,
            \"file_size\": strconv.Itoa(update.Message.Document.FileSize),
        },
    }
    
    a.incoming <- incomingMsg
}
```

### Custom Message Handling

```go
// Override default handler
func customHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
    // Custom logic here
    
    // Call default handler  
    defaultHandler(ctx, b, update)
}

// Register custom handler
opts := []bot.Option{
    bot.WithDefaultHandler(customHandler),
}
```

### Error Handling

The adapter includes comprehensive error handling:

- **Connection errors**: Auto-reconnect with exponential backoff
- **Rate limiting**: Built-in respect for Telegram limits  
- **Invalid tokens**: Clear error messages and status reporting
- **Message failures**: Graceful degradation, errors logged but don't crash

## HTTP API

### Channel Status

```bash
curl http://localhost:18790/api/channels/status
```

Response:
```json
{
  \"telegram\": {
    \"status\": \"online\",
    \"message\": \"Bot is running\", 
    \"timestamp\": \"2026-02-05T23:30:00Z\"
  }
}
```

### Health Check

```bash
curl http://localhost:18790/health
# Returns: OK
```

## Performance

No benchmark for the adapter lives in this repo, so none is quoted here. Throughput is ultimately bounded by Telegram's Bot API rate limits (about 30 messages/second).

### Optimizations

- **Goroutine pooling**: Efficient concurrency
- **Channel buffering**: Prevents message loss
- **Connection reuse**: HTTP/2 connections to Telegram
- **Memory efficient**: No JavaScript runtime overhead

## Security

### Best Practices

- ✅ **Token protection**: Store in environment variables
- ✅ **Webhook validation**: Secret token verification
- ✅ **Rate limiting**: Respect Telegram API limits  
- ✅ **Input validation**: Sanitize all user inputs
- ✅ **Error handling**: Don't expose internal details

### Production Setup

```json
{
  \"config\": {
    \"webhook_mode\": true,
    \"webhook_url\": \"https://example.com/webhook\",
    \"debug\": false
  }
}
```

## Troubleshooting

### Common Issues

**Bot Token Invalid:**
```
Error: failed to create bot: 401 Unauthorized
Solution: Check TELEGRAM_BOT_TOKEN environment variable
```

**Rate Limiting:**
```
Warning: TooManyRequestsError with retry_after: 30
Solution: Built-in handling, will retry automatically
```

**Connection Issues:**
```
Error: connection refused
Solution: Check internet connectivity and firewall
```

**Messages Not Received:**
```
Check: Bot is started (@BotFather /mybots)
Check: Bot has permission to read messages
Check: Webhook URL is accessible (if using webhooks)
```

### Debug Mode

Enable debug logging:
```json
{\"debug\": true}
```

This will log:
- All API requests/responses  
- Message processing steps
- Connection status changes
- Error details

## Future Enhancements

### Planned Features

- 🔄 **Inline keyboards** - Rich button interfaces
- 🔄 **File uploads** - Document/media handling  
- 🔄 **Group chat support** - Multi-user conversations
- 🔄 **Bot commands** - /start, /help, etc.
- 🔄 **Webhook auto-setup** - Automatic webhook configuration
- 🔄 **Message templates** - Rich message formatting

### Contributing

To add features:

1. Fork the project
2. Create feature branch: `git checkout -b feature/telegram-inline-keyboards`
3. Make changes in `internal/channels/telegram/`
4. Add tests and documentation
5. Submit pull request

Access control is provided by the pairing system (`conduit pairing telegram list|approve`).