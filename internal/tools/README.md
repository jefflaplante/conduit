# Conduit Tools Implementation

This directory contains the complete implementation of Conduit's core tools, providing the full suite of capabilities that power the AI assistant.

## Architecture

The tools are organized into logical packages:

```
internal/tools/
├── registry.go           # Enhanced tool registry with service injection
├── core/                 # Core system tools
│   ├── memory.go         # memory_search, memory_get
│   ├── sessions.go       # session management tools
│   └── gateway.go        # gateway operations
├── web/                  # Web integration tools
│   ├── search.go         # web_search (Brave API)
│   └── fetch.go          # web_fetch (content extraction)
├── communication/        # Communication tools
│   ├── message.go        # message sending via channels
│   └── tts.go           # text-to-speech conversion
├── scheduling/           # Scheduling tools
│   └── cron.go          # cron job management
└── vision/              # Vision tools
    └── image.go         # image analysis
```

## Core Tools

### Memory Management

#### `memory_search`
Semantic search across memory files (MEMORY.md and memory/*.md).

**Parameters:**
- `query` (string, required): Search query
- `maxResults` (integer): Maximum results to return (default: 10)
- `minScore` (float): Minimum relevance score 0.0-1.0 (default: 0.3)

**Example:**
```json
{
  "query": "project deadlines",
  "maxResults": 5,
  "minScore": 0.5
}
```

#### `memory_get`
Retrieve specific content from memory files by path and line range.

**Parameters:**
- `path` (string, required): Path to memory file (MEMORY.md or memory/*.md)
- `from` (integer): Starting line number (default: 1)
- `lines` (integer): Number of lines to retrieve (0 = all, default: 0)

**Example:**
```json
{
  "path": "memory/2024-01-15.md",
  "from": 10,
  "lines": 5
}
```

### Session Management

#### `sessions_list`
List active sessions with metadata and recent activity.

**Parameters:**
- `activeMinutes` (integer): Show sessions active within N minutes (default: 60)
- `limit` (integer): Maximum sessions to return (default: 20)

#### `sessions_send`
Send messages to other sessions by key or label.

**Parameters:**
- `message` (string, required): Message content
- `sessionKey` (string): Target session key
- `label` (string): Target session label (alternative to sessionKey)

#### `sessions_spawn`
Spawn new sub-agent sessions for specific tasks.

**Parameters:**
- `task` (string, required): Task description for sub-agent
- `agentId` (string): Specific agent ID to spawn
- `model` (string): AI model to use
- `label` (string): Label for the session
- `timeoutSeconds` (integer): Session timeout (default: 300)

#### `session_status`
Get detailed status information about sessions.

**Parameters:**
- `sessionKey` (string): Session key (optional, defaults to current)

### Gateway Management

#### `gateway`
Manage gateway operations including status, channels, and configuration.

**Parameters:**
- `action` (string, required): Operation to perform
  - `status`: Get gateway status
  - `restart`: Restart gateway
  - `channels`: Get channel status
  - `enable_channel`: Enable a channel
  - `disable_channel`: Disable a channel
  - `config`: Get configuration
  - `update_config`: Update configuration
  - `metrics`: Get performance metrics
  - `version`: Get version info

## Web Tools

### `web_search`
Search the web using Brave Search API with region-specific support.

**Parameters:**
- `query` (string, required): Search query
- `count` (integer): Number of results 1-10 (default: 10)
- `country` (string): 2-letter country code (default: "US")
- `freshness` (string): Time filter ("pd", "pw", "pm", "py")
- `search_lang` (string): Language code for results

**Example:**
```json
{
  "query": "OpenAI GPT-4 latest updates",
  "count": 5,
  "country": "US",
  "freshness": "pw"
}
```

### `web_fetch`
Fetch and extract readable content from URLs, converting HTML to markdown or text.

**Parameters:**
- `url` (string, required): HTTP/HTTPS URL to fetch
- `extractMode` (string): "markdown" or "text" (default: "markdown")
- `maxChars` (integer): Maximum characters to return (default: 50000)

**Example:**
```json
{
  "url": "https://example.com/article",
  "extractMode": "markdown",
  "maxChars": 25000
}
```

## Communication Tools

### `message`
Send messages via configured channels (Telegram, Discord, etc.).

**Parameters:**
- `action` (string): Action to perform ("send", "broadcast", "react", "delete", "edit", "status")
- `target` (string): Target channel/user ID or name
- `targets` (array): Multiple targets for broadcast
- `message` (string): Message content
- `messageId` (string): Message ID for reactions/edits
- `silent` (boolean): Send silently (default: false)
- `asVoice` (boolean): Send as voice message (default: false)
- `replyTo` (string): Message ID to reply to
- `effectId` (string): Message effect ID

**Examples:**
```json
// Send a message
{
  "action": "send",
  "target": "telegram_chat_123",
  "message": "Hello from Conduit!",
  "silent": false
}

// Broadcast to multiple channels
{
  "action": "broadcast",
  "targets": ["telegram_chat_123", "discord_channel_456"],
  "message": "Important announcement",
  "silent": true
}
```

### `tts`
Convert text to speech and return audio files.

**Parameters:**
- `text` (string, required): Text to convert
- `voice` (string): Voice to use (default: "en-US-AriaNeural")
- `rate` (string): Speech rate (default: "+0%")
- `format` (string): Audio format "mp3", "ogg", "wav" (default: "ogg")
- `channel` (string): Target channel for format optimization

**Example:**
```json
{
  "text": "Hello, this is a test of the text-to-speech system.",
  "voice": "en-US-AriaNeural",
  "rate": "+10%",
  "format": "ogg"
}
```

## Scheduling Tools

### `cron`
Schedule recurring tasks and one-shot wake events using cron syntax.

**Parameters:**
- `action` (string): Operation ("schedule", "list", "cancel", "run", "enable", "disable", "status")
- `schedule` (string): Cron expression (e.g., "0 9 * * 1" for 9 AM Mondays)
- `command` (string): Command/prompt to execute
- `name` (string): Job name/description
- `model` (string): AI model to use
- `target` (string): Channel target for output
- `jobId` (string): Job ID for operations
- `oneshot` (boolean): Run once then delete (default: false)
- `delayMinutes` (integer): Schedule to run in X minutes

**Examples:**
```json
// Schedule a weekly report
{
  "action": "schedule",
  "schedule": "0 9 * * 1",
  "command": "Generate weekly project status report",
  "name": "Weekly Status",
  "target": "telegram_chat_123"
}

// Set a reminder
{
  "action": "schedule",
  "delayMinutes": 30,
  "command": "Remember to check the deployment status",
  "oneshot": true
}
```

## Vision Tools

### `image`
Analyze images with vision models for description, object detection, and text extraction.

**Parameters:**
- `image` (string, required): Image path, URL, or base64 data
- `prompt` (string): Analysis prompt (default: "Describe what you see")
- `model` (string): Vision model to use
- `maxBytesMb` (float): Max image size in MB (default: 5.0)
- `extractText` (boolean): Enable OCR (default: false)
- `detectObjects` (boolean): Enable object detection (default: false)

**Examples:**
```json
// Analyze a local image
{
  "image": "screenshots/dashboard.png",
  "prompt": "What metrics are shown in this dashboard?",
  "extractText": true
}

// Analyze image from URL
{
  "image": "https://example.com/chart.jpg",
  "prompt": "Explain this data visualization",
  "detectObjects": true
}
```

## Configuration

Enable tools in your `config.json`:

```json
{
  "tools": {
    "enabled_tools": [
      "read_file", "write_file", "exec", "list_files",
      "memory_search", "memory_get",
      "sessions_list", "sessions_send", "sessions_spawn", "session_status", 
      "web_search", "web_fetch",
      "message", "tts",
      "cron",
      "gateway",
      "image"
    ],
    "services": {
      "brave": {
        "api_key": "${BRAVE_API_KEY}"
      },
      "tts": {
        "provider": "edge",
        "voice": "en-US-AriaNeural"
      }
    },
    "sandbox": {
      "workspace_dir": "./workspace",
      "allowed_paths": ["./workspace", "/tmp"]
    }
  }
}
```

## Security

### Sandbox Enforcement
- All file operations are restricted to allowed paths
- Memory tools only access MEMORY.md and memory/ directory
- Session tools validate ownership and permissions
- Web tools include URL validation and safety checks

### Access Control
- Tools respect channel access controls
- Session isolation prevents cross-session interference
- Input sanitization and validation on all parameters
- Rate limiting and timeout protection

## Performance

### Optimization Features
- Memory search uses efficient keyword matching (vector search planned)
- Web fetch includes intelligent content extraction
- Tool registration is lazy-loaded and cached
- Session management minimizes database queries

### Monitoring
- Built-in metrics collection for all tool usage
- Performance tracking with response times
- Error rate monitoring and alerting
- Resource usage tracking

## Dependencies

- **Core**: Go standard library, modernc.org/sqlite, google/uuid
- **Web**: PuerkitoBio/goquery for HTML parsing, net/http for requests
- **Scheduling**: Built-in cron expression parsing
- **Communication**: Channel-specific adapters (Telegram, Discord)
- **Vision**: Integration with AI provider vision models

## Future Enhancements

1. **Vector Search**: Replace keyword-based memory search with semantic embeddings
2. **Advanced Scheduling**: Support for complex cron expressions and job dependencies
3. **Multi-modal Analysis**: Support for audio and video analysis
4. **Real-time Collaboration**: Live session sharing and real-time updates
5. **Plugin System**: Support for custom tool extensions
6. **Enhanced Security**: Fine-grained permission system and audit logging

## Testing

Run the validation tests:

```bash
go test ./internal/tools/...
```

The test suite includes:
- Tool registration validation
- Parameter schema validation
- Mock service integration tests
- Performance benchmarks
- Security boundary testing

## Migration from Legacy

The new tool system is backward compatible:
- Existing `read_file`, `write_file`, `exec`, `list_files` tools unchanged
- New tools use same interface pattern as legacy tools
- Configuration is additive - no breaking changes
- Tool execution patterns remain consistent