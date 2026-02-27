# HTTP API & WebSocket Protocol

Reference for the Conduit Gateway HTTP endpoints and WebSocket protocol.

## HTTP Endpoints

| Endpoint | Method | Auth | Description |
|----------|--------|------|-------------|
| `/health` | GET | No | Health check |
| `/metrics` | GET | Yes | Gateway metrics and session stats |
| `/diagnostics` | GET | Yes | Real-time diagnostic events |
| `/ws` | WebSocket | Yes | Real-time WebSocket API |
| `/api/channels/status` | GET | Yes | Channel adapter status |

### Health Check

```bash
curl http://localhost:18789/health
```

Response:
```json
{
  "status": "healthy",
  "version": "1.0.0",
  "uptime": "2h30m15s"
}
```

### Metrics

```bash
curl -H "Authorization: Bearer claw_v1_..." http://localhost:18789/metrics
```

Response includes:
- Active sessions count
- WebSocket connections
- Memory usage
- Request rates
- Tool execution stats

### Diagnostics

Server-sent events stream for real-time diagnostics:

```bash
curl -H "Authorization: Bearer claw_v1_..." http://localhost:18789/diagnostics
```

## WebSocket API

Connect to `ws://localhost:18789/ws` for real-time bidirectional communication.

### Authentication

Send auth token in the handshake:

```javascript
const ws = new WebSocket('ws://localhost:18789/ws', [], {
  headers: { 'Authorization': 'Bearer claw_v1_...' }
});
```

Or include in first message:

```json
{
  "type": "auth",
  "token": "claw_v1_..."
}
```

### Message Types

```go
const (
    TypeIncomingMessage MessageType = "incoming_message"
    TypeOutgoingMessage MessageType = "outgoing_message"
    TypeChannelStatus   MessageType = "channel_status"
    TypeChannelCommand  MessageType = "channel_command"
    TypeAgentRequest    MessageType = "agent_request"
    TypeAgentResponse   MessageType = "agent_response"
    TypeChatMessage     MessageType = "chat_message"
    TypeStreamDelta     MessageType = "stream_delta"
    TypeToolEvent       MessageType = "tool_event"
)
```

### Chat Messages

Send a chat message:

```json
{
  "type": "chat_message",
  "session_key": "tui_abc123",
  "text": "Hello, can you help me?"
}
```

Receive streaming response:

```json
{
  "type": "stream_delta",
  "session_key": "tui_abc123",
  "delta": "Sure, I'd be happy to help! "
}
```

### Tool Events

Tool execution notifications:

```json
{
  "type": "tool_event",
  "session_key": "tui_abc123",
  "tool_name": "WebSearch",
  "status": "started",
  "params": {"query": "golang best practices"}
}
```

```json
{
  "type": "tool_event",
  "session_key": "tui_abc123",
  "tool_name": "WebSearch",
  "status": "completed",
  "result": {"success": true, "content": "..."}
}
```

### Agent Request/Response

For direct AI provider interaction:

Request:
```json
{
  "type": "agent_request",
  "id": "req_123",
  "session_key": "session_abc",
  "message": "Explain quantum computing",
  "metadata": {
    "model": "claude-3-5-sonnet-20241022"
  }
}
```

Response:
```json
{
  "type": "agent_response",
  "id": "req_123",
  "session_key": "session_abc",
  "text": "Quantum computing is...",
  "tool_calls": [],
  "metadata": {
    "tokens_used": 150,
    "model": "claude-3-5-sonnet-20241022"
  }
}
```

### Channel Messages

Incoming message from a channel:

```json
{
  "type": "incoming_message",
  "id": "telegram_abc123",
  "timestamp": "2026-02-05T22:00:00Z",
  "channel_id": "telegram",
  "session_key": "telegram_987654321",
  "user_id": "987654321",
  "text": "Hello!",
  "metadata": {
    "from_username": "user123",
    "from_first_name": "User"
  }
}
```

Outgoing message to a channel:

```json
{
  "type": "outgoing_message",
  "id": "response_xyz789",
  "timestamp": "2026-02-05T22:00:01Z",
  "channel_id": "telegram",
  "session_key": "telegram_987654321",
  "user_id": "987654321",
  "text": "Hi! How can I help?",
  "metadata": {}
}
```

### Session Commands

Create new session:

```json
{
  "type": "session_create",
  "channel": "tui",
  "user_id": "user123"
}
```

List sessions:

```json
{
  "type": "session_list"
}
```

### Error Handling

Errors are returned with type `error`:

```json
{
  "type": "error",
  "code": "auth_failed",
  "message": "Invalid or expired token"
}
```

Common error codes:
- `auth_failed` - Authentication failure
- `rate_limited` - Too many requests
- `session_not_found` - Invalid session key
- `tool_error` - Tool execution failed
- `provider_error` - AI provider error

## Rate Limiting

Rate limits apply per client:

| Tier | Requests/Minute | Applies To |
|------|-----------------|------------|
| Anonymous | 100 | `/health` only |
| Authenticated | 1000 | All endpoints |

Rate limit headers:
```
X-RateLimit-Limit: 1000
X-RateLimit-Remaining: 950
X-RateLimit-Reset: 1709078400
```
