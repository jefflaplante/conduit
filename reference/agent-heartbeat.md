# Agent Heartbeat System

The agent heartbeat system executes periodic tasks defined in HEARTBEAT.md and processes alerts from a shared queue. This enables automated monitoring, alerting, and scheduled AI-driven tasks.

## Overview

```
                                    ┌─────────────────────────┐
                                    │     External Systems    │
                                    │  (scripts, cron, other  │
                                    │   agents, monitors)     │
                                    └───────────┬─────────────┘
                                                │
                                                ▼
┌─────────────────────────────────────────────────────────────────────┐
│                         Conduit Gateway                             │
│                                                                     │
│  ┌─────────────────┐      ┌─────────────────────────────────────┐  │
│  │  HEARTBEAT.md   │      │  alert_queue_path                   │  │
│  │                 │      │  memory/alerts/pending.json         │  │
│  │  - Check alerts │      │                                     │  │
│  │  - System status│      │  [{"severity": "critical", ...}]    │  │
│  │  - Reports      │      └─────────────────┬───────────────────┘  │
│  └────────┬────────┘                        │                      │
│           │                                 │                      │
│           └─────────────┬───────────────────┘                      │
│                         ▼                                          │
│              ┌─────────────────────┐                               │
│              │   Agent Heartbeat   │                               │
│              │   Loop (every N min)│                               │
│              └──────────┬──────────┘                               │
│                         │                                          │
│           ┌─────────────┼─────────────┐                            │
│           ▼             ▼             ▼                            │
│     ┌──────────┐  ┌──────────┐  ┌──────────┐                       │
│     │ Critical │  │ Warning  │  │   Info   │                       │
│     │ (always) │  │ (quiet   │  │ (quiet   │                       │
│     │          │  │  aware)  │  │  aware)  │                       │
│     └────┬─────┘  └────┬─────┘  └────┬─────┘                       │
│          │             │             │                             │
└──────────┼─────────────┼─────────────┼─────────────────────────────┘
           │             │             │
           ▼             ▼             ▼
      ┌─────────┐   ┌─────────┐   ┌─────────┐
      │Telegram │   │Telegram │   │ Briefing│
      │  (now)  │   │(if awake│   │ (later) │
      └─────────┘   └─────────┘   └─────────┘
```

## Two Heartbeat Systems

Conduit has two separate heartbeat systems:

| System | Config Key | Purpose | Documentation |
|--------|------------|---------|---------------|
| Diagnostic Heartbeat | `heartbeat` | Gateway health metrics, session monitoring, system stats | [heartbeat-system.md](heartbeat-system.md) |
| **Agent Heartbeat** | `agent_heartbeat` | HEARTBEAT.md task execution, alert queue processing | This document |

This document covers the **Agent Heartbeat** system.

## Configuration

```json
{
  "agent_heartbeat": {
    "enabled": true,
    "interval_minutes": 5,
    "timezone": "America/Los_Angeles",
    "quiet_enabled": true,
    "quiet_hours": {
      "start_time": "22:00",
      "end_time": "07:00"
    },
    "alert_queue_path": "memory/alerts/pending.json",
    "heartbeat_task_path": "HEARTBEAT.md",
    "enabled_task_types": ["alerts", "checks", "reports", "maintenance"],
    "alert_targets": [
      {
        "name": "telegram_primary",
        "type": "telegram",
        "config": {
          "chat_id": "123456789"
        },
        "severity": ["critical", "warning", "info"]
      }
    ],
    "alert_retry_policy": {
      "max_retries": 3,
      "retry_interval": 300000000000,
      "backoff_factor": 2.0
    },
    "log_level": "info",
    "verbose_logging": false
  }
}
```

### Configuration Reference

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable/disable agent heartbeat |
| `interval_minutes` | int | `5` | Minutes between heartbeat cycles (1-60) |
| `timezone` | string | `"America/Los_Angeles"` | Timezone for quiet hours |
| `quiet_enabled` | bool | `true` | Enable quiet hours |
| `quiet_hours.start_time` | string | `"22:00"` | Quiet period start (24h format) |
| `quiet_hours.end_time` | string | `"08:00"` | Quiet period end (24h format) |
| `alert_queue_path` | string | `"memory/alerts/pending.json"` | Path to shared alert queue (relative to workspace) |
| `heartbeat_task_path` | string | `"HEARTBEAT.md"` | Path to task definitions (relative to workspace) |
| `enabled_task_types` | array | `["alerts", "checks", "reports"]` | Which task types to execute |
| `alert_targets` | array | `[]` | Where to deliver alerts |
| `alert_retry_policy` | object | see below | Retry behavior for failed deliveries |
| `log_level` | string | `"info"` | Logging verbosity |
| `verbose_logging` | bool | `false` | Extra debug output |

### Alert Retry Policy

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `max_retries` | int | `3` | Maximum delivery attempts (0-10) |
| `retry_interval` | duration | `5m` | Wait between retries (nanoseconds) |
| `backoff_factor` | float | `2.0` | Exponential backoff multiplier |

### Alert Targets

Each target specifies where alerts of certain severities should be delivered:

```json
{
  "name": "telegram_jeff",
  "type": "telegram",
  "config": {
    "chat_id": "123456789"
  },
  "severity": ["critical", "warning"]
}
```

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Unique identifier for this target |
| `type` | string | Delivery type: `telegram`, `webhook`, `email` |
| `config` | object | Type-specific configuration |
| `severity` | array | Which severities to route here: `critical`, `warning`, `info` |

## Shared Alert Queue

The alert queue is a JSON file that external systems can write to. The agent heartbeat reads and processes these alerts on each cycle.

### Queue Location

The queue path is relative to `workspace.context_dir`:

```
workspace.context_dir = /home/user/conduit/workspace
alert_queue_path = memory/alerts/pending.json

Full path: /home/user/conduit/workspace/memory/alerts/pending.json
```

### Queue Format

```json
{
  "alerts": [
    {
      "id": "alert-1710123456",
      "severity": "critical",
      "title": "Database connection failed",
      "message": "PostgreSQL connection timed out after 30s",
      "source": "db-monitor",
      "status": "pending",
      "created_at": "2026-03-11T10:30:00Z",
      "retry_count": 0
    }
  ],
  "last_sync": "2026-03-11T10:35:00Z",
  "version": 42
}
```

### Alert Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `id` | string | Yes | Unique identifier |
| `severity` | string | Yes | `critical`, `warning`, or `info` |
| `title` | string | Yes | Short summary |
| `message` | string | Yes | Detailed description |
| `source` | string | Yes | Origin system/script |
| `status` | string | Yes | `pending`, `sent`, `failed`, `expired` |
| `created_at` | string | Yes | ISO 8601 timestamp |
| `retry_count` | int | No | Delivery attempts (default 0) |
| `metadata` | object | No | Additional key-value data |

### Writing Alerts from External Systems

Any process can add alerts by writing to the queue file:

**Python example:**
```python
import json
import time
from pathlib import Path
from datetime import datetime

queue_path = Path("/home/user/conduit/workspace/memory/alerts/pending.json")

# Load existing queue or create empty one
if queue_path.exists():
    queue = json.loads(queue_path.read_text())
else:
    queue = {"alerts": [], "version": 0}

# Add new alert
queue["alerts"].append({
    "id": f"alert-{int(time.time())}",
    "severity": "warning",
    "title": "Disk space low",
    "message": "Server XYZ has only 10% disk space remaining",
    "source": "disk-monitor",
    "status": "pending",
    "created_at": datetime.utcnow().isoformat() + "Z"
})

queue["version"] += 1
queue_path.write_text(json.dumps(queue, indent=2))
```

**Bash example:**
```bash
#!/bin/bash
QUEUE="/home/user/conduit/workspace/memory/alerts/pending.json"
ALERT_ID="alert-$(date +%s)"
TIMESTAMP=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

# Use jq to append alert
jq --arg id "$ALERT_ID" \
   --arg ts "$TIMESTAMP" \
   '.alerts += [{
     "id": $id,
     "severity": "critical",
     "title": "Service down",
     "message": "nginx is not responding",
     "source": "health-check",
     "status": "pending",
     "created_at": $ts
   }] | .version += 1' "$QUEUE" > "${QUEUE}.tmp" && mv "${QUEUE}.tmp" "$QUEUE"
```

### Queue Safety Features

The shared queue implementation provides:

| Feature | Description |
|---------|-------------|
| **Thread-safe** | Mutex locking for concurrent access |
| **File locking** | `flock()` for multi-process safety |
| **Atomic writes** | Write to `.tmp`, then rename |
| **Auto-recovery** | Handles corrupted JSON gracefully |
| **Deduplication** | Prevents duplicate alerts |
| **Expiration** | Old alerts are cleaned up |

## HEARTBEAT.md Format

The `heartbeat_task_path` file defines tasks the agent executes on each cycle.

### Example HEARTBEAT.md

```markdown
# HEARTBEAT.md

## Check shared alert queue
Read `memory/alerts/pending.json`. If it contains any alerts:
- **critical** severity: Deliver to Jeff immediately via Telegram
- **warning** severity: Deliver to Jeff if he's likely awake (8 AM - 10 PM PT)
- **info** severity: Skip — save for the next briefing

After delivering, clear the queue.
If no alerts (or only info-level), reply HEARTBEAT_OK.

## Check system status
Monitor critical systems:
- Database connectivity
- API endpoint health
- Disk space usage

Report any issues immediately.

## Daily briefing
At 8:00 AM PT, compile and deliver:
- Overnight alerts summary
- System health overview
- Scheduled maintenance reminders
```

### Task Types

| Type | Description | Quiet Hours |
|------|-------------|-------------|
| `alerts` | Process the shared alert queue | Critical ignores quiet hours |
| `checks` | System health monitoring | Respects quiet hours |
| `reports` | Scheduled summaries/briefings | Respects quiet hours |
| `maintenance` | Cleanup and optimization tasks | Respects quiet hours |

Enable/disable task types via `enabled_task_types` in config.

### Keywords

The parser recognizes keywords to determine priority and behavior:

| Keyword | Effect |
|---------|--------|
| `critical`, `urgent` | Critical priority, ignores quiet hours |
| `alert`, `warning` | High priority |
| `immediate` | Bypasses quiet hours |
| `awake`, `quiet hours` | Respects quiet hours |
| `info`, `routine` | Low priority |

### HEARTBEAT_OK Response

When no action is needed, the AI responds with `HEARTBEAT_OK`. This is detected by:
- Explicit `HEARTBEAT_OK` text
- Phrases like "no alerts", "nothing needs attention", "all clear"
- Short responses indicating no issues

## Quiet Hours

Quiet hours prevent non-critical alerts from disturbing you during sleep/off hours.

### Behavior by Severity

| Severity | During Quiet Hours | Outside Quiet Hours |
|----------|-------------------|---------------------|
| `critical` | Delivered immediately | Delivered immediately |
| `warning` | Queued until quiet hours end | Delivered immediately |
| `info` | Queued until quiet hours end | Delivered immediately |

### Spanning Midnight

Quiet hours can span midnight:
```json
"quiet_hours": {
  "start_time": "22:00",
  "end_time": "07:00"
}
```
This means quiet from 10 PM to 7 AM.

### Timezone

Set timezone for accurate quiet hours:
```json
"timezone": "America/Los_Angeles"
```

Common timezone values:
- `America/New_York` (Eastern)
- `America/Chicago` (Central)
- `America/Denver` (Mountain)
- `America/Los_Angeles` (Pacific)
- `Europe/London`
- `Asia/Tokyo`

## Monitoring and Debugging

### Log Levels

| Level | Output |
|-------|--------|
| `debug` | All execution details |
| `info` | Normal operation logs |
| `warn` | Warnings and recoverable errors |
| `error` | Errors only |

### Verbose Logging

Enable `verbose_logging: true` for detailed output:
```
[AgentHeartbeat] Starting cycle at 2026-03-11T10:00:00-08:00
[AgentHeartbeat] Loading alert queue from memory/alerts/pending.json
[AgentHeartbeat] Found 2 pending alerts (1 critical, 1 warning)
[AgentHeartbeat] Quiet hours active: false
[AgentHeartbeat] Routing critical alert to telegram_primary
[AgentHeartbeat] Delivered alert-123 successfully
[AgentHeartbeat] Routing warning alert to telegram_primary
[AgentHeartbeat] Delivered alert-124 successfully
[AgentHeartbeat] Cycle complete: 2 alerts processed, 0 failures
```

## Common Use Cases

### External Monitoring Integration

Use the alert queue as a bridge between monitoring systems and Conduit:

```
┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│ Prometheus  │────▶│ alertmanager│────▶│ alert_queue │
│   Alerts    │     │  webhook    │     │   .json     │
└─────────────┘     └─────────────┘     └─────────────┘
                                               │
                                               ▼
                                        ┌─────────────┐
                                        │   Conduit   │────▶ Telegram
                                        │  Heartbeat  │
                                        └─────────────┘
```

### Cron Job Alerts

Have cron jobs write to the alert queue:

```bash
# /etc/cron.d/backup-monitor
0 * * * * root /opt/scripts/check-backups.sh || \
  /opt/scripts/queue-alert.sh "Backup check failed" "critical"
```

### Multi-Agent Communication

Multiple Conduit instances can share an alert queue for coordination:

```
Agent A (server1) ──┐
                    ├──▶ shared alert queue ──▶ Agent B (notification hub)
Agent C (server2) ──┘
```

## Troubleshooting

### Alerts Not Delivering

1. Check `agent_heartbeat.enabled` is `true`
2. Verify `alert_targets` has at least one target
3. Check target `severity` array includes the alert's severity
4. Verify quiet hours aren't blocking (check timezone)
5. Check `log_level: "debug"` for detailed output

### Queue File Issues

1. Ensure directory exists: `mkdir -p workspace/memory/alerts`
2. Check file permissions (readable/writable by gateway process)
3. Verify JSON is valid: `jq . memory/alerts/pending.json`
4. Check for `.tmp` files (indicates interrupted writes)

### Tasks Not Executing

1. Verify `heartbeat_task_path` points to valid HEARTBEAT.md
2. Check `enabled_task_types` includes the task type
3. Ensure HEARTBEAT.md uses proper Markdown format
4. Check AI provider is configured and working
