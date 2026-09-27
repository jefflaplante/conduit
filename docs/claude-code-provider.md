# Claude Code Provider + MCP Bridge

Conduit can route LLM calls through the Claude Code CLI instead of directly calling the Anthropic API. This is useful when direct API access is restricted or when you want to leverage Claude Code's OAuth-based authentication (tied to Pro/Max subscriptions).

## How It Works

```
User → Channel (WebSocket/TUI/Telegram)
  → Conduit Gateway
    → claude-code Provider
      → claude -p --output-format stream-json "message"
        → Claude Code agent loop
          ├── Native tools: Read, Edit, Bash, Glob, Grep, Write
          └── MCP tools → Conduit MCP Server (localhost:18790)
                            → Conduit ToolRegistry
                              → Brain, MQTT, Cron, WebSearch, etc.
        → stream-json stdout parsed by Conduit
      → streamed back to user
```

The claude-code provider spawns `claude -p` (print/non-interactive mode) for each user message, parses the structured JSON output, and delivers the response through Conduit's normal channel pipeline. Streaming is supported end-to-end via `--output-format stream-json`.

A companion MCP server runs on localhost, exposing Conduit's unique tools (Brain, MQTT, scheduling, web search, etc.) to Claude Code. Claude Code discovers these tools via a `.mcp.json` file written to the configured working directory. Tool calls from Claude Code run through Conduit's ExecutionEngine single-call pipeline (per-call timeout, panic recovery, result truncation, reflection capture), exactly like the agent's own tool calls. The endpoint requires a bearer token (see [Authentication](#authentication)).

### Session Continuity

Conduit maps its own session IDs to Claude Code's `--resume` session IDs. On the first message in a session, no `--resume` flag is passed; Claude Code creates a new session and returns a `session_id`. On subsequent messages in the same Conduit session, the provider passes `--resume <cc_session_id>` so Claude Code continues the same conversation.

Session mappings are stored in SQLite alongside the main gateway database.

## Prerequisites

1. **Claude Code CLI installed** and authenticated:
   ```bash
   # Install
   npm install -g @anthropic-ai/claude-code

   # Authenticate (opens browser for OAuth)
   claude auth login

   # Verify
   claude --version
   claude -p "say hello"
   ```

2. **Conduit v0.21.0+** (the `claude-code` provider was added in this version).

## Configuration

Add a `claude-code` provider to your Conduit config:

### Minimal Config

```json
{
  "ai": {
    "default_provider": "claude-code",
    "providers": [
      {
        "name": "claude-code",
        "type": "claude-code"
      }
    ]
  }
}
```

This uses all defaults: `claude` on `$PATH`, MCP on port 18790, 25 max turns, 5-minute timeout.

### Full Config

```json
{
  "ai": {
    "default_provider": "claude-code",
    "providers": [
      {
        "name": "claude-code",
        "type": "claude-code",
        "model": "claude-sonnet-4-6",
        "claude_code": {
          "claude_path": "/usr/local/bin/claude",
          "mcp_port": 18790,
          "allowed_tools": ["Read", "Edit", "Bash", "Glob", "Grep", "Write"],
          "permission_mode": "acceptEdits",
          "max_turns": 25,
          "timeout_seconds": 300,
          "working_dir": "/home/user/projects/myproject"
        }
      }
    ]
  }
}
```

### Alongside Other Providers

The claude-code provider can coexist with direct API providers. Select it explicitly per session (`/provider`, `/model`) or as the default provider:

```json
{
  "ai": {
    "default_provider": "anthropic",
    "providers": [
      {
        "name": "anthropic",
        "type": "anthropic",
        "api_key": "${ANTHROPIC_API_KEY}",
        "model": "claude-sonnet-4-6"
      },
      {
        "name": "claude-code",
        "type": "claude-code",
        "model": "claude-opus-4-6",
        "claude_code": {
          "working_dir": "/home/user/projects",
          "permission_mode": "bypassPermissions"
        }
      }
    ]
  }
}
```

### Config Reference

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `claude_path` | string | `"claude"` | Path to the Claude Code CLI binary |
| `mcp_port` | int | `18790` | Port for Conduit's MCP server (localhost only) |
| `allowed_tools` | string[] | `["Read","Edit","Bash","Glob","Grep","Write"]` | Claude Code native tools to enable |
| `permission_mode` | string | `"acceptEdits"` | Claude Code permission mode (`acceptEdits`, `bypassPermissions`, etc.) |
| `max_turns` | int | `25` | Maximum agent loop iterations per request |
| `timeout_seconds` | int | `300` | Process timeout in seconds |
| `working_dir` | string | _(empty)_ | Directory where `claude -p` runs; also where `.mcp.json` is written |
| `model` | string | _(empty)_ | Model override passed via `--model` (uses Claude Code default if empty) |

## MCP Server

When a claude-code provider is configured, Conduit automatically starts an MCP server on `127.0.0.1:<mcp_port>`. This server:

- Exposes Conduit tools to Claude Code via the [Model Context Protocol](https://modelcontextprotocol.io/)
- Binds to localhost only (not accessible from the network); the SDK's DNS-rebinding (Host) and cross-origin protections stay on
- Uses HTTP-based MCP (StreamableHTTP, stateful sessions)
- Requires `Authorization: Bearer <token>` (see [Authentication](#authentication))
- Runs every tool call through the ExecutionEngine: per-call timeout (60s, longer when a Bash `timeout` asks for it), panic recovery, smart truncation to `tools.max_tool_result_chars`, `Structured data` for tools that opt in, and SPAR reflection capture (conduit-31jg.8)
- Treats every call as **non-interactive** for human approvals (`approval.WithNonInteractive(ctx, "mcp")`): owner-account email sends fail closed with `NOT SENT ... (origin: mcp)`
- Writes a `.mcp.json` file to `working_dir` so Claude Code auto-discovers it
- On shutdown, cancels in-flight calls and hanging SSE streams, closes all sessions and force-closes lingering connections after 500ms, so a restart no longer waits 5s (conduit-31jg.78)

### Authentication

conduit-31jg.8. The token is a dedicated secret in `{data_dir}/auth/mcp_token` (default data dir `~/.conduit`; override with `mcp.token_file`), 64 hex chars, mode 0600, generated on first start and stable across restarts. It is compared in constant time (SHA-256 + `subtle.ConstantTimeCompare`). A separate secret is used instead of a gateway auth token because those are stored only as HMAC hashes (the gateway could not give the plaintext to its own `claude -p` child) and because this credential grants code execution (Bash/Write), so it is kept apart from WebSocket client tokens.

The gateway exports the token as `CONDUIT_MCP_TOKEN` in its own environment, so the claude-code provider's `claude -p` subprocess inherits it, and the `.mcp.json` it writes references `${CONDUIT_MCP_TOKEN:-}`.

`mcp.require_auth` (top-level `mcp` block):

| Value | Mode | Behavior |
|-------|------|----------|
| unset (default) | `warn` | Requests without a token are served and logged (at most once a minute); a wrong token gets 401. Transition mode for one release; the default becomes `enforce` afterwards. |
| `true` | `enforce` | Requests without a valid token get 401 (`WWW-Authenticate: Bearer realm="conduit-mcp"`). If the token file is unusable (e.g. group/world-readable), the MCP server does not start. |
| `false` | `disabled` | No auth (logged as a warning at startup). Not recommended. |

```json
{
  "mcp": {
    "require_auth": true,
    "token_file": "~/.conduit/auth/mcp_token"
  }
}
```

**Client configuration** (any Claude Code session outside `working_dir`, e.g. a repo `.mcp.json` or `~/.claude.json`):

```json
{
  "mcpServers": {
    "conduit": {
      "type": "http",
      "url": "http://127.0.0.1:18790/mcp",
      "headers": { "Authorization": "Bearer ${CONDUIT_MCP_TOKEN}" }
    }
  }
}
```

and export the variable in the shell that launches Claude Code (e.g. `~/.bashrc`):

```bash
export CONDUIT_MCP_TOKEN="$(cat ~/.conduit/auth/mcp_token)"
```

Claude Code expands `${VAR}` and `${VAR:-default}` in `headers`. With plain `${VAR}` and the variable unset, Claude Code reports the server config as invalid (missing environment variable); `${CONDUIT_MCP_TOKEN:-}` sends an empty bearer instead, which the gateway treats as "no token" (served in warn mode, 401 in enforce mode).

**Rotation:** delete the token file and restart the gateway (a new token is generated), then re-export `CONDUIT_MCP_TOKEN` and restart Claude Code sessions. A stale token is rejected with 401 in every mode.

### Exposed Tools

All Conduit tools are exposed **except** internal-only tools:

| Excluded Tool | Reason |
|---------------|--------|
| Chain | Internal orchestration |
| DebugLog | Internal debug |
| Gateway | Internal gateway control |
| SessionsList | Internal session management |
| SessionsSend | Internal session management |
| SessionsSpawn | Internal session management |
| SessionStatus | Internal session management |
| StatusUpdate | Internal status updates |
| Context | Internal context/prompt management |

Everything else (Brain, MQTT, Cron, WebSearch, WebFetch, Message, Image, etc.) is available to Claude Code through MCP.

### .mcp.json

When `working_dir` is set, Conduit writes a `.mcp.json` file on startup:

```json
{
  "mcpServers": {
    "conduit": {
      "type": "http",
      "url": "http://127.0.0.1:18790/mcp",
      "headers": { "Authorization": "Bearer ${CONDUIT_MCP_TOKEN:-}" }
    }
  }
}
```

The `headers` entry is omitted when `mcp.require_auth` is `false`. The literal token is never written to this file.

If a `.mcp.json` already exists (e.g., with other MCP servers), Conduit merges the `conduit` entry without disturbing existing entries (all of their fields are preserved). On shutdown, Conduit restores the original file (or deletes it if Conduit created it).

### Without working_dir

If `working_dir` is not set, the MCP server still runs but no `.mcp.json` is written. You can point Claude Code at the MCP server manually via its own configuration.

## Verification

### Check the provider is registered

Look for this log line on startup:
```
level=INFO msg="claude-code provider configured" mcp_port=18790 working_dir=/path/to/project
```

### Check the MCP server

```
level=INFO msg="MCP server started"
[mcp] server starting on 127.0.0.1:18790 (auth: warn)
```

Verify auth (expect `401` without a token in enforce mode, and a non-401 with it):
```bash
curl -s -o /dev/null -w '%{http_code}\n' -X POST http://127.0.0.1:18790/mcp
curl -s -o /dev/null -w '%{http_code}\n' -X POST -H "Authorization: Bearer $(cat ~/.conduit/auth/mcp_token)" http://127.0.0.1:18790/mcp
```

In warn mode, `[mcp] WARNING: served unauthenticated MCP request ...` lines identify clients that still need the header.

### Check .mcp.json

```bash
cat /path/to/working_dir/.mcp.json
```

Should contain a `conduit` entry.

### Test a request

Send a message through any Conduit channel. In the logs you should see:
```
[ClaudeCode] Executing (streaming): claude (session=<session-key>)
```

On subsequent messages in the same session:
```
[ClaudeCode] Executing (streaming): claude (session=<session-key>)
```
With `--resume` in the CLI arguments.

## Permission Modes

The `permission_mode` setting controls how Claude Code handles tool permissions:

| Mode | Behavior |
|------|----------|
| `acceptEdits` | Default. Claude Code asks before writing/executing, auto-accepts edits. |
| `bypassPermissions` | No permission prompts. Use this for fully automated operation. |
| `plan` | Read-only mode. Claude Code cannot write files or execute commands. |

For server/automated use, `bypassPermissions` is typically appropriate since Conduit is already managing the conversation.

## Error Handling

The provider classifies Claude Code CLI errors into Conduit's standard error categories:

| CLI Error | Category | Behavior |
|-----------|----------|----------|
| `authentication`, `unauthorized`, `401` | Authentication | Reported to user |
| `rate_limit`, `429`, `too many requests` | Rate Limit | May trigger retry |
| `overloaded`, `503` | Service Unavailable | May trigger retry |
| `timeout`, `timed out` | Timeout | Reported to user |
| Other | Unknown | Reported to user |

The provider remains functional after errors. Each request spawns a fresh `claude -p` process, so transient failures don't affect subsequent requests.

## Limitations

- **No direct tool call routing**: Claude Code handles its own tool execution internally. Conduit's execution engine only processes tools that Claude Code calls via MCP. Native Claude Code tools (Read, Edit, Bash) are executed by Claude Code, not Conduit.
- **Process-per-request**: Each message spawns a new `claude -p` process. This adds ~200-500ms of overhead compared to direct API calls. Session `--resume` mitigates context loss but not startup latency.
- **No system prompt injection**: The provider does not pass `--append-system-prompt`. Claude Code uses its own system prompt. Conduit's agent personality settings do not apply when using this provider.
- **Single claude-code provider**: Only one claude-code provider is supported per Conduit instance. The gateway uses the first one it finds during initialization.
