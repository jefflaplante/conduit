# Tools Reference

Complete reference for all built-in AI tools available in Conduit Go Gateway.

## Overview

Tools extend the AI's capabilities by allowing it to interact with files, execute commands, search the web, and manage the gateway. All tools respect sandbox configuration and only operate within allowed paths.

## File Operations

### Read

Read file contents from the workspace.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `path` | string | Yes | Path to the file to read |

```json
{"path": "config.json"}
{"path": "src/main.go"}
```

### Write

Write content to a file, creating directories as needed.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `path` | string | Yes | Path to the file to write |
| `content` | string | Yes | Content to write |

```json
{"path": "output.txt", "content": "Hello, World!"}
```

### Edit

Make surgical edits to files by replacing exact text matches.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `path` | string | Yes | Path to the file to edit |
| `old_text` | string | Yes | Exact text to replace |
| `new_text` | string | Yes | Replacement text |

```json
{
  "path": "config.json",
  "old_text": "\"debug\": false",
  "new_text": "\"debug\": true"
}
```

### Glob

List files and directories matching a pattern.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `path` | string | No | Directory path (default: workspace) |
| `pattern` | string | No | Glob pattern to match |

```json
{"path": "src", "pattern": "*.go"}
```

## System Operations

### Bash

Execute shell commands in the sandbox environment.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `command` | string | Yes | Shell command to execute |
| `cwd` | string | No | Working directory |

```json
{"command": "ls -la"}
{"command": "go test ./...", "cwd": "/project"}
```

## Web Operations

### WebSearch

Hybrid web search using Anthropic native search or Brave API fallback.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `query` | string | Yes | Search query |
| `max_results` | int | No | Maximum results (default: 5) |

```json
{"query": "golang best practices 2026"}
{"query": "weather seattle", "max_results": 3}
```

### WebFetch

Fetch and extract content from URLs.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `url` | string | Yes | URL to fetch |
| `selector` | string | No | CSS selector for content extraction |

```json
{"url": "https://example.com/article"}
```

## Memory & Search

### MemorySearch

Search across workspace memory files and session history.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `query` | string | Yes | Search query |
| `scope` | string | No | Search scope: "all", "memory", "sessions" |
| `limit` | int | No | Maximum results (default: 10) |

```json
{"query": "project architecture"}
{"query": "user preferences", "scope": "memory", "limit": 5}
```

Uses FTS5 full-text search with BM25 ranking. Searches MEMORY.md, daily logs, and session history.

### Find

Universal search across memory, sessions, and beads issues.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `query` | string | Yes | Search query |
| `scope` | string | No | "all", "memory", "sessions", "beads" |
| `status` | string | No | For beads: "open", "closed", "all" |
| `limit` | int | No | Maximum results |

```json
{"query": "authentication bug", "scope": "beads", "status": "open"}
{"query": "API design decisions", "scope": "memory"}
```

### Facts

Extract structured facts from memory files.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `category` | string | No | Filter by category |
| `max_facts` | int | No | Maximum facts to return |

```json
{"category": "preferences"}
{"max_facts": 20}
```

Extracts bullet points and key-value pairs from MEMORY.md and related files.

## Session Management

### SessionsList

List all active sessions with metadata.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `status` | string | No | Filter by status |
| `limit` | int | No | Maximum sessions |

```json
{}
{"status": "active", "limit": 10}
```

### SessionsSend

Send a message to another session.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `session_key` | string | Yes | Target session key |
| `message` | string | Yes | Message to send |

```json
{"session_key": "telegram_123456", "message": "Task completed"}
```

### SessionsSpawn

Spawn a new session with specific configuration.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `channel` | string | No | Channel type |
| `user_id` | string | No | User identifier |

```json
{"channel": "tui", "user_id": "background-worker"}
```

### SessionStatus

Get detailed status of the current or specified session.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `session_key` | string | No | Session key (default: current) |

```json
{}
{"session_key": "tui_abc123"}
```

## Communication

### Message

Send messages via configured channels (Telegram, etc.).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `channel` | string | Yes | Target channel |
| `user_id` | string | Yes | Recipient user ID |
| `text` | string | Yes | Message text |

```json
{"channel": "telegram", "user_id": "123456789", "text": "Hello!"}
```

### Tts

Text-to-speech synthesis.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `text` | string | Yes | Text to synthesize |
| `voice` | string | No | Voice selection |

```json
{"text": "Hello, how can I help you today?"}
```

## Scheduling

### Cron

Schedule recurring tasks and manage heartbeat jobs.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | "list", "create", "delete", "run" |
| `name` | string | Conditional | Job name (for create/delete) |
| `schedule` | string | Conditional | Cron expression (for create) |
| `command` | string | Conditional | Command to run (for create) |

```json
{"action": "list"}
{"action": "create", "name": "daily-backup", "schedule": "0 2 * * *", "command": "backup create"}
{"action": "delete", "name": "old-job"}
```

## Workflow

### Chain

Execute saved multi-tool workflows.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | "list", "show", "validate", "run" |
| `name` | string | Conditional | Chain name |
| `variables` | object | No | Variable substitutions (for run) |

```json
{"action": "list"}
{"action": "show", "name": "deploy-pipeline"}
{"action": "validate", "name": "my-workflow"}
{"action": "run", "name": "deploy", "variables": {"env": "production", "version": "1.2.3"}}
```

Chains are JSON files in `workspace/chains/` defining tool sequences with dependencies and variable substitution.

## Gateway Management

### Gateway

Manage gateway operations, status, and configuration.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | "status", "channels", "metrics", "config" |

```json
{"action": "status"}
{"action": "channels"}
{"action": "metrics"}
```

### Context

Get comprehensive context about the current environment.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `section` | string | No | Specific section to retrieve |
| `verbose` | bool | No | Include additional details |

```json
{}
{"section": "workspace"}
{"section": "project", "verbose": true}
```

Sections: workspace, tools, session, beads, project, gateway, channels.

## Vision

### Image

Analyze images using vision models.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `path` | string | Yes | Path to image file |
| `prompt` | string | No | Analysis prompt |

```json
{"path": "screenshot.png"}
{"path": "diagram.jpg", "prompt": "Describe the architecture shown"}
```

## Adding Custom Tools

Implement the `Tool` interface:

```go
type Tool interface {
    Name() string
    Description() string
    Parameters() map[string]interface{}
    Execute(ctx context.Context, args map[string]interface{}) (*ToolResult, error)
}
```

Register in `internal/tools/registry.go` within `registerAllTools()`.
