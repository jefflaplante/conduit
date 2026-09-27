# Agent Heartbeat System

The agent heartbeat system runs the tasks defined in HEARTBEAT.md on a schedule, using the agent (with its tools) to check alerts, system status and reports, and delivers whatever the agent reports to a channel. This enables automated monitoring, alerting, and scheduled AI-driven tasks.

## Overview

```
  External systems (scripts, cron, monitors)
          | append alerts
          v
  memory/alerts/pending.json  <-- read/cleared by the agent (HEARTBEAT.md
                                   prompt) and alert-flush.sh, not by
                                   gateway code
+---------------------------------------------------------------------+
|                         Conduit Gateway                             |
|                                                                     |
|   HEARTBEAT.md --> Agent Heartbeat job (every N min) --> AI turn    |
|                                                            |        |
|                         parsed result (OK / actions) <-----+        |
|                                  |                                  |
|        +-------------------------+----------------------+           |
|        v                         v                      v           |
|  critical / high          quiet-aware, outside    quiet-aware,      |
|  (always now)             quiet hours (now)       quiet hours       |
|        |                         |                      |           |
|        |                         |          memory/alerts/deferred  |
|        |                         |          .json, flushed on the   |
|        |                         |          first cycle after quiet |
|        v                         v                      v           |
|   DeliveryRegistry: circuit breaker, alert_history audit, retries   |
|        |                                                            |
|        v                                                            |
|   ChannelSender (SanitizeOutgoingText) --> Telegram / other channel |
+---------------------------------------------------------------------+
```

## Two Heartbeat Systems

Conduit has two separate heartbeat systems:

| System | Config Key | Purpose | Documentation |
|--------|------------|---------|---------------|
| Diagnostic Heartbeat | `heartbeat` | Gateway health metrics, session monitoring, system stats | [heartbeat-system.md](heartbeat-system.md) |
| **Agent Heartbeat** | `agent_heartbeat` | HEARTBEAT.md task execution and alert delivery | This document |

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
| `alert_queue_path` | string | unset | **Deprecated** (warns at load). The gateway no longer processes this file; if set, only its directory is used to place `deferred.json` |
| `heartbeat_task_path` | string | `"HEARTBEAT.md"` | Path to task definitions (relative to workspace) |
| `enabled_task_types` | array | `["alerts", "checks", "reports"]` | Which task types to execute |
| `alert_targets` | array | `[]` | Validated but not currently used for routing (see below) |
| `alert_retry_policy` | object | see below | Background retries for failed deliveries |
| `log_level` | string | `"info"` | Logging verbosity |
| `verbose_logging` | bool | `false` | Extra debug output |

### Alert Retry Policy

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `max_retries` | int | `3` | Retries after a failed live delivery (0-10) |
| `retry_interval` | duration | `5m` | Wait before the first retry (nanoseconds) |
| `backoff_factor` | float | `2.0` | Multiplier applied to the wait after each retry |

See [Delivery](#delivery) for how retries interact with the circuit breaker.

### Alert Targets

Alert targets are parsed and validated, but the gateway does not currently route by them: heartbeat messages go to the heartbeat job's target (`telegram:<chat_id>`, or an action's own target). The format is kept for compatibility:

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

## Alert Queue File (pending.json)

`memory/alerts/pending.json` (relative to `workspace.context_dir`) is a hand-off file for external systems. **Gateway code does not read or modify it.** The agent reads it during a heartbeat turn because HEARTBEAT.md tells it to (see the example below), and `alert-flush.sh` clears it after delivery. Its format (a JSON array of alert objects) is defined by those two consumers, so external writers should follow them.

The gateway-owned `memory/alerts/deferred.json` (quiet-hours deferral, see below) is a separate file. Never write to it from scripts.

### Writing Alerts from External Systems

```bash
#!/bin/bash
QUEUE="/home/user/conduit/workspace/memory/alerts/pending.json"
[ -s "$QUEUE" ] || echo '[]' > "$QUEUE"
jq --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
   '. += [{"severity": "critical", "title": "Service down",
           "message": "nginx is not responding", "source": "health-check",
           "created_at": $ts}]' "$QUEUE" > "${QUEUE}.tmp" && mv "${QUEUE}.tmp" "$QUEUE"
```

## Delivery

Every heartbeat message (actions, errors, deferred flushes) goes through the gateway's `DeliveryRegistry` using a channel deliverer that wraps the normal channel sender (conduit-31jg.59):

| Feature | Behavior |
|---------|----------|
| **Sanitized output** | `SanitizeOutgoingText` strips reply tags, `MEDIA:` lines and trailing silent tokens; `HEARTBEAT_OK` / `NO_REPLY` responses are not sent at all |
| **Circuit breaker** | Per destination (`telegram:<chat_id>`): 3 consecutive failures open it for 5 minutes; sends while open are skipped (and audited), not retried |
| **Audit trail** | Every attempt (success, failure, breaker skip) is written to the `alert_history` table |
| **Retries** | A failed live send is retried in the background per `alert_retry_policy` (for example 3 retries at 30s, 60s, 120s). Retries never block the heartbeat loop, stop early on success or an open breaker, are capped at 32 pending, and are cancelled on shutdown |
| **Deferred flush** | Not retried in the background: a failed deferred action stays in `deferred.json` and is retried on the next cycle (up to 5 attempts) |

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
| `alerts` | Have the agent check the alert queue file | Critical ignores quiet hours |
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

### Behavior by Action

| Action | During Quiet Hours | Outside Quiet Hours |
|--------|-------------------|---------------------|
| Alert, or critical/high priority | Delivered immediately | Delivered immediately |
| Quiet-aware (normal/low priority) | Deferred to `deferred.json` until quiet hours end | Delivered immediately |
| Other | Delivered immediately | Delivered immediately |

### Evaluation Rules

- Quiet hours are evaluated on the wall clock of `agent_heartbeat.timezone`,
  never the server's local zone (containers and systemd units usually run in UTC).
- The window is `[start_time, end_time)`: the start minute is quiet, the end minute is not.
  `start_time == end_time` means no quiet hours. DST transition days are handled on the
  wall clock (22:00 is 22:00 whether the offset is PST or PDT).
- One implementation serves every caller: `config.AgentHeartbeatConfig.IsQuietTime` /
  `NextQuietEnd` / `NextQuietStart` (`internal/config/quiet_hours.go`).

### Deferred Delivery

Quiet-aware heartbeat actions (non-critical, non-high-priority actions whose text marks them as
quiet-aware) that come up during quiet hours are written to `memory/alerts/deferred.json` (or next
to the deprecated `alert_queue_path`, if set). At the start of every heartbeat cycle
outside quiet hours, the gateway delivers the queued entries, so delivery lands on the first cycle
after quiet hours end (at most `interval_minutes` late). The queue is on disk, so it survives restarts.
A failed delivery stays queued for up to 5 attempts. Entries expire after 72 hours.
`deferred.json` is owned by the gateway. External scripts should keep writing to `pending.json`.

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

Enable `verbose_logging: true` for detailed output. Delivery logs look like:
```
[HeartbeatIntegration] Executing heartbeat job: agent_heartbeat_main
[HeartbeatIntegration] Heartbeat completed: status=alert, actions=1
[HeartbeatIntegration] Delivery retry 1/3 to telegram:123456789 failed: ...
[HeartbeatIntegration] Circuit open for telegram:123456789; abandoning retries after 2 attempt(s)
```

Delivery history is queryable in the `alert_history` table:
```sql
SELECT created_at, alert_type, severity, action_taken, action_result
FROM alert_history ORDER BY created_at DESC LIMIT 20;
```

## Common Use Cases

### External Monitoring Integration

Use the alert queue file as a bridge between monitoring systems and Conduit (the agent reads it on each heartbeat):

```
┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│ Prometheus  │────▶│ alertmanager│────▶│ pending.json│
│   Alerts    │     │  webhook    │     │             │
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

## Troubleshooting

### Alerts Not Delivering

1. Check `agent_heartbeat.enabled` is `true`
2. Check the heartbeat job's target (`telegram:<chat_id>`) and that the channel is connected
3. Look at `alert_history` for failures or `circuit_breaker_open` rows
4. Verify quiet hours aren't deferring it (check timezone and `deferred.json`)
5. Check `log_level: "debug"` for detailed output

### Queue File Issues

1. Ensure directory exists: `mkdir -p workspace/memory/alerts`
2. Check file permissions (readable/writable by gateway process)
3. Verify JSON is valid: `jq . memory/alerts/pending.json`
4. The gateway never edits `pending.json`; if alerts are not cleared, check the HEARTBEAT.md prompt and `alert-flush.sh`

### Tasks Not Executing

1. Verify `heartbeat_task_path` points to valid HEARTBEAT.md
2. Check `enabled_task_types` includes the task type
3. Ensure HEARTBEAT.md uses proper Markdown format
4. Check AI provider is configured and working
