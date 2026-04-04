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

### Brain

Tiered cognitive memory: store, retrieve, search, and manage facts across long-term memory (persisted), working memory (session), and scratchpad (temporary stack). Requires `brain.enabled: true` in config.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | Operation: store, get, recall, list, delete, push, pop, peek, promote, consolidate, status |
| `key` | string | Varies | Dot-separated key (e.g., `solar.panel_count`) — required for store, get, delete, promote |
| `value` | string | Varies | Value to store or push — required for store and push |
| `tier` | string | No | Memory tier: `longterm` or `working` (default: working) |
| `query` | string | Varies | Search query — required for recall |
| `prefix` | string | No | Key prefix for list action |
| `limit` | int | No | Max results for recall (default: 20) |
| `auto_promote` | bool | No | Auto-promote during consolidation (default: true) |

**Actions:**

| Action | Description | Required Params |
|--------|-------------|-----------------|
| `store` | Save a key-value fact | `key`, `value` |
| `get` | Retrieve a fact by key (checks working memory first, then LTM) | `key` |
| `recall` | Fuzzy search across all tiers, ranked by salience | `query` |
| `list` | List entries matching a key prefix | `prefix` |
| `delete` | Remove a key from all tiers | `key` |
| `push` | Push value onto per-user scratchpad stack | `value` |
| `pop` | Pop top value from scratchpad | — |
| `peek` | View top scratchpad value without removing | — |
| `promote` | Move a working memory key to long-term storage | `key` |
| `consolidate` | Sweep working memory: auto-promote high-salience, evict stale | — |
| `status` | Report entry counts, scratchpad depth, hottest keys | — |

```json
{"action": "store", "key": "solar.panel_count", "value": "30", "tier": "working"}
{"action": "get", "key": "solar.panel_count"}
{"action": "recall", "query": "solar", "limit": 10}
{"action": "push", "value": "TODO: check inverter status"}
{"action": "consolidate", "auto_promote": true}
{"action": "status"}
```

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
| `action` | string | Yes | "status", "channels", "metrics", "config", "debug_prompt", etc. |
| `session` | string | No | Session key for `debug_prompt` (uses current session if omitted) |

```json
{"action": "status"}
{"action": "channels"}
{"action": "metrics"}
{"action": "debug_prompt"}
{"action": "debug_prompt", "session": "default"}
```

The `debug_prompt` action returns a breakdown of the system prompt: each section's name, priority (P1-P4), character count, and whether it was included or dropped due to context window budget constraints. Useful for optimizing prompt size when running local LLMs with limited context windows.

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

## IoT / Home Automation

### MQTT

Query MQTT device data and optionally publish messages. Requires `mqtt.enabled: true` in config. See [MQTT Integration](mqtt.md) for full documentation.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | "status", "topics", "recent", "history", "publish" |
| `topic` | string | Conditional | Exact topic for history/publish |
| `topic_pattern` | string | No | Glob filter for recent (e.g. `zigbee2mqtt/*`) |
| `limit` | int | No | Max events (default 20, max 100) |
| `payload` | string | Conditional | JSON payload for publish |
| `qos` | int | No | QoS for publish (0-2, default 0) |
| `retained` | bool | No | Retain flag for publish (default false) |

```json
{"action": "status"}
{"action": "topics"}
{"action": "recent", "topic_pattern": "zigbee2mqtt/*", "limit": 10}
{"action": "history", "topic": "zigbee2mqtt/Living Room Sensor"}
{"action": "publish", "topic": "zigbee2mqtt/Light/set", "payload": "{\"state\":\"ON\"}"}
```

Publish is gated by `mqtt.publish_allowed` config (default `false`).

## Infrastructure / SRE

### Kubernetes

Multi-cluster Kubernetes management with security tiers. Requires `kubernetes.enabled: true` in config. See [Kubernetes Integration](kubernetes.md) for full documentation.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | string | Yes | Operation to perform (see below) |
| `cluster` | string | Auto | Target cluster (auto-selected if only one) |
| `resource` | string | Conditional | Resource kind (pods, deploy, svc, etc.) |
| `name` | string | Conditional | Resource name |
| `namespace` | string | No | Target namespace |
| `label_selector` | string | No | Label filter for get/watch |
| `container` | string | No | Container for logs/exec |
| `command` | string | Conditional | Command for exec |
| `tail_lines` | int | No | Log lines (default 100) |
| `replicas` | int | Conditional | Replica count for scale |
| `subaction` | string | Conditional | For rollout: restart/status/history |
| `timeout` | int | No | Watch timeout seconds (default 30) |
| `local_port` | int | Conditional | Local port for forwarding |
| `remote_port` | int | Conditional | Remote port for forwarding |
| `forward_id` | string | Conditional | Port forward ID for close |

**Actions:**

| Action | Tier | Description |
|--------|------|-------------|
| `get` | read | Get/list resources |
| `describe` | read | Detailed resource description |
| `logs` | read | Pod container logs |
| `events` | read | Cluster events |
| `watch` | read | Watch resource changes |
| `clusters` | read | List configured clusters |
| `namespaces` | read | List namespaces |
| `scale` | modify | Scale deployment/statefulset |
| `rollout` | modify | Rollout operations |
| `delete` | dangerous | Delete resources |
| `exec` | dangerous | Execute in pod |
| `portforward_create` | read | Create port forward |
| `portforward_close` | read | Close port forward |
| `portforward_list` | read | List port forwards |

```json
{"action": "get", "resource": "pods", "namespace": "app"}
{"action": "describe", "resource": "deploy", "name": "nginx"}
{"action": "logs", "name": "web-abc123", "tail_lines": 50}
{"action": "scale", "resource": "deploy", "name": "nginx", "replicas": 3}
{"action": "exec", "name": "web-abc123", "command": "ls -la"}
{"action": "watch", "resource": "pods", "timeout": 60}
{"action": "portforward_create", "name": "postgres-0", "local_port": 5433, "remote_port": 5432}
```

**Security Model:** Operations are classified into read/modify/dangerous tiers. Each cluster has a `safety_level` that caps allowed operations. Namespace restrictions enforced via `allowed_namespaces`. Secret values always redacted.

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

## Upcoming Tools

The following integrations have config and auth infrastructure in place. Full tool implementations are planned:

| Integration | Config | Client | Tool Status |
|-------------|--------|--------|-------------|
| **PagerDuty** | `pagerduty` | REST v2 | Config + client ready, tool pending |
| **Datadog** | `datadog` | REST v1/v2 | Config + client ready, tool pending |

See [Configuration Reference](configuration.md) for setup details.
