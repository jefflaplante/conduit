# Claude Code Provider + MCP Bridge Design

**Date:** 2026-04-10
**Status:** Draft

## Context

Anthropic restricts third-party agents from directly using their API in ways that impersonate or compete with Claude Code. Conduit, as an independent AI gateway, faces this restriction when using the direct Anthropic provider. Claude Code CLI has its own OAuth-based authentication (tied to Pro/Max subscriptions) that sidesteps this restriction.

This design adds a new `claude-code` provider to Conduit that routes LLM calls through `claude -p` (Claude Code's non-interactive/print mode), and an MCP server that exposes Conduit's unique tools (Brain, MQTT, scheduling, etc.) to Claude Code. This preserves Conduit's tool execution middleware while leveraging Claude Code's authentication and native filesystem capabilities.

## Architecture

### High-Level Flow

```
User → Channel (WebSocket/TUI/Telegram)
  → Conduit Gateway (session routing)
    → claude-code Provider
      → claude -p --output-format stream-json --resume <session_id> "message"
        → Claude Code agent loop
          ├── Native tools: Read, Edit, Bash, Glob, Grep
          └── MCP tools → Conduit MCP Server (localhost:18790)
                            → Conduit ExecutionEngine
                              → Brain, MQTT, Cron, WebSearch, etc.
        → stream-json stdout
      → StreamCallback → Channel → User
```

### Components

```
Conduit Process
├── Gateway HTTP Server (port 18789)     [existing]
│   ├── WebSocket, health, metrics
│   └── Channel adapters
├── MCP HTTP Server (port 18790)         [NEW]
│   ├── tools/list  → ToolRegistry iteration
│   ├── tools/call  → ExecutionEngine middleware → tool execution
│   └── Bound to 127.0.0.1 only
├── claude-code Provider                 [NEW]
│   ├── Implements Provider + StreamingProvider
│   ├── Spawns claude -p per user message
│   ├── Parses stream-json output
│   └── Maps Conduit sessions ↔ Claude Code sessions
└── Shared Services                      [existing]
    ├── Brain, MQTT, Sessions, Config
    ├── ToolRegistry, ExecutionEngine
    └── FTS5 Search, Scheduler
```

## Component 1: claude-code Provider

**File:** `internal/ai/claude_code.go`

### Provider Interface

Implements both `Provider` and `StreamingProvider`:

```go
type ClaudeCodeProvider struct {
    name           string
    config         ClaudeCodeConfig
    claudePath     string        // path to claude binary
    mcpPort        int           // Conduit's MCP server port
    sessionMapper  SessionMapper // Conduit session → CC session mapping
}

func (p *ClaudeCodeProvider) Name() string
func (p *ClaudeCodeProvider) GenerateResponse(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error)
func (p *ClaudeCodeProvider) GenerateResponseStreaming(ctx context.Context, req *GenerateRequest, onDelta StreamCallback) (*GenerateResponse, error)
```

### Invocation

Each `GenerateResponse` call extracts the latest user message from `req.Messages` and executes a `claude -p` command. The request's `Tools` and history `Messages` are ignored — Claude Code manages its own tool definitions and conversation history via `--resume`.

**Important:** The returned `GenerateResponse.ToolCalls` will always be empty. Claude Code handles the tool execution loop internally (calling Conduit's tools via MCP). Conduit's `ExecutionEngine.HandleToolCallFlow` is never invoked for this provider — the router should check for this and skip the tool loop.

Command built per call:

```bash
claude -p \
  --output-format stream-json \
  --model claude-sonnet-4-6 \
  --resume <claude_code_session_id> \    # omitted for first message
  --allowedTools "Read,Edit,Bash,Glob,Grep" \
  --permission-mode acceptEdits \
  --max-turns 25 \
  "user message text"
```

**Important:** No `--append-system-prompt` or `--system-prompt` flags. Claude Code keeps its own identity. Conduit's personality is expressed through its MCP tools and any `CLAUDE.md` file in the working directory.

### Stream Parsing

**File:** `internal/ai/claude_code_stream.go`

Reads stdout line by line. Each line is a JSON object:

| Event Type | Action |
|---|---|
| `stream_event` with `text_delta` | Forward to `StreamCallback(delta, false)` |
| `message` with `tool_use` content | Log tool event for observability |
| `message` (final, no tool calls) | Extract final text, usage stats |
| `system/api_retry` | Log retry attempt, wait |

Final response assembled from accumulated text + usage stats from the last message event.

### Error Handling

- **Process spawn failure:** Return classified error (e.g., `CategoryServiceUnavailable`)
- **Non-zero exit:** Parse stderr, classify (auth, rate limit, context exceeded, etc.)
- **Timeout:** `context.WithTimeout` kills process, returns `CategoryTimeout`
- **Partial stream:** Set `response.Partial = true` with whatever text was received

### Configuration

Added to `ProviderConfig` (or as a separate struct):

```go
type ClaudeCodeConfig struct {
    ClaudePath     string   `json:"claude_path"`      // default: "claude"
    MCPPort        int      `json:"mcp_port"`         // default: 18790
    AllowedTools   []string `json:"allowed_tools"`    // CC's native tools to enable
    PermissionMode string   `json:"permission_mode"`  // default: "acceptEdits"
    MaxTurns       int      `json:"max_turns"`        // default: 25
    Timeout        int      `json:"timeout_seconds"`  // default: 300
    WorkingDir     string   `json:"working_dir"`      // where claude -p runs
}
```

Example config:

```json
{
  "ai": {
    "default_provider": "claude-code",
    "providers": [
      {
        "name": "claude-code",
        "type": "claude-code",
        "model": "claude-sonnet-4-6",
        "claude_path": "/usr/local/bin/claude",
        "mcp_port": 18790,
        "allowed_tools": ["Read", "Edit", "Bash", "Glob", "Grep"],
        "permission_mode": "acceptEdits",
        "max_turns": 25,
        "timeout_seconds": 300,
        "working_dir": "/home/user/projects"
      }
    ]
  }
}
```

## Component 2: MCP HTTP Server

**Files:** `internal/mcp/server.go`, `internal/mcp/tools.go`

### Server Setup

An HTTP-based MCP server running inside Conduit's process. Uses the official Go SDK (`github.com/modelcontextprotocol/go-sdk`).

- Binds to `127.0.0.1:<mcp_port>` (localhost only, no auth needed)
- JSON-RPC 2.0 over HTTP
- Started when the claude-code provider is configured
- Stopped on gateway shutdown

### Tool Adapter

Maps Conduit's `Tool` interface to MCP tool definitions:

```go
func adaptToolToMCP(tool types.Tool) mcp.ToolDefinition {
    return mcp.ToolDefinition{
        Name:        tool.Name(),
        Description: tool.Description(),
        InputSchema: tool.Parameters(), // already JSON Schema compatible
    }
}
```

### Tool Selection

**Exposed via MCP** (Conduit's unique tools):
- Brain tools (store, get, recall, list, delete, push/pop/peek, promote, consolidate)
- MQTT tool (status, topics, recent, history, publish)
- Cron/scheduling tool
- Communication tools (message sending, TTS)
- Web search and web fetch
- Gateway control/status
- Memory search (FTS5)
- Context management
- Image analysis
- Session management tools

**Not exposed** (Claude Code has native equivalents):
- ReadFile, WriteFile, EditFile → Claude Code's Read, Edit, Write
- Bash → Claude Code's Bash
- Glob, Find → Claude Code's Glob, Grep

### Execution Path

When Claude Code calls a Conduit tool via MCP:

1. MCP server receives `tools/call` JSON-RPC request
2. Extract tool name and arguments
3. Create execution context with Conduit session info
4. Route through Conduit's existing tool execution path:
   - Tool registry lookup
   - Parameter validation (if tool implements `ParameterValidator`)
   - Before-execution middleware
   - `tool.Execute(ctx, args)`
   - After-execution middleware (metrics, logging)
5. Format result as MCP response:
   - Success: `{content: [{type: "text", text: result}], isError: false}`
   - Error: `{content: [{type: "text", text: error_msg}], isError: true}`

### MCP Configuration for Claude Code

On startup, Conduit writes a `.mcp.json` file in the configured working directory:

```json
{
  "mcpServers": {
    "conduit": {
      "type": "http",
      "url": "http://127.0.0.1:18790"
    }
  }
}
```

Claude Code automatically discovers this file when `claude -p` runs in that directory. Conduit manages this file's lifecycle (create on start, clean up on shutdown).

## Component 3: Session Management

**File:** `internal/sessions/claude_code.go`

### Session Mapping

Each Conduit session maps to a Claude Code session:

```go
type ClaudeCodeSessionMap struct {
    ConduitSessionID    string
    ClaudeCodeSessionID string
    CreatedAt           time.Time
    LastUsedAt          time.Time
}
```

Stored in Conduit's SQLite database (new table or session metadata column).

### Flow

1. **First message in session:** Call `claude -p` without `--resume`. Parse `session_id` from JSON response. Store mapping.
2. **Subsequent messages:** Look up Claude Code session ID. Call `claude -p --resume <id>`.
3. **Session expiry:** If Claude Code session can't be resumed (error), start a new one and update mapping.

### Message Storage

Conduit continues to store messages in its own SQLite tables for:
- Display in TUI/WebSocket/Telegram clients
- FTS5 full-text search
- Cross-channel message history

Claude Code manages its own conversation context internally. No history re-sending needed.

### MCP Session Context

When a tool call arrives via MCP, the server needs to know which Conduit session it belongs to. Options:
- **HTTP header:** claude -p passes a session identifier (if supported)
- **Single-user assumption:** If Conduit serves one user, session context is implicit
- **MCP session ID:** Track via the `MCP-Session-Id` header that MCP protocol provides

For the initial implementation, a single-user assumption is reasonable. Multi-user can be added later by correlating the MCP session with the claude -p process that spawned it.

## Dependencies

- `github.com/modelcontextprotocol/go-sdk` — MCP protocol implementation
- No other new dependencies
- Pure Go, no CGO (consistent with Conduit's existing approach)

## Files to Create/Modify

| File | Action | Description |
|---|---|---|
| `internal/ai/claude_code.go` | Create | Provider implementation |
| `internal/ai/claude_code_stream.go` | Create | Stream-JSON parser |
| `internal/ai/claude_code_test.go` | Create | Provider unit tests |
| `internal/mcp/server.go` | Create | MCP HTTP server |
| `internal/mcp/tools.go` | Create | Tool registry → MCP adapter |
| `internal/mcp/server_test.go` | Create | MCP server tests |
| `internal/sessions/claude_code.go` | Create | Session ID mapper |
| `internal/config/config.go` | Modify | Add ClaudeCodeConfig fields |
| `internal/ai/router.go` | Modify | Register claude-code provider type |
| `internal/gateway/gateway.go` | Modify | Start MCP server on init |
| `go.mod` | Modify | Add go-sdk dependency |

## Verification Plan

### Unit Tests
1. **Stream parser:** Feed sample stream-json lines, verify text extraction, usage parsing, tool event detection
2. **MCP tool adapter:** Verify Conduit Tool → MCP ToolDefinition mapping for all exposed tools
3. **Session mapper:** CRUD operations, expiry handling, first-message flow
4. **Config parsing:** ClaudeCodeConfig from JSON, defaults, validation

### Integration Tests
1. **MCP server:** Start server, call `tools/list`, verify all expected tools appear
2. **MCP tool execution:** Call `tools/call` for Brain store/recall, verify data persists
3. **Provider with mock:** Test claude -p invocation with a mock script that returns stream-json

### End-to-End Tests
1. **Full flow:** Configure claude-code provider → send message via WebSocket → verify response streams back
2. **Session continuity:** Send two messages, verify second uses `--resume`
3. **MCP tools in action:** Send a message that triggers Brain storage via Claude Code, verify Brain has the data
4. **Error recovery:** Kill claude -p mid-stream, verify partial response and session recovery

### Manual Verification
1. `claude mcp add --transport http conduit http://127.0.0.1:18790` → verify tools visible
2. In Claude Code interactive mode, call a Conduit tool (e.g., Brain recall) → verify it works
3. `make run` with claude-code provider → send message via TUI → observe full flow
