# SRE Tools Reference

Conduit includes four SRE observability tools that integrate PagerDuty and Datadog for incident management, metrics/logs querying, monitor management, and cross-source correlation. All tools follow the action-dispatch pattern with security tiers.

## Tool Ecosystem

```
                ┌──────────────┐
                │  SRE Tool    │  ← orchestrates the others
                │  (correlate) │
                └──┬───────┬───┘
                   │       │
         ┌─────────▼─┐  ┌─▼──────────┐
         │ PagerDuty  │  │  Datadog    │
         │ incidents, │  │  metrics,   │
         │ on-call,   │  │  logs, APM  │
         │ schedules  │  │             │
         └────────────┘  └──────┬──────┘
                                │
                         ┌──────▼──────┐
                         │  Datadog    │
                         │  Monitor    │
                         │  (CRUD)     │
                         └─────────────┘
```

| Tool | Name | Config Section | Read-only | Description |
|------|------|---------------|-----------|-------------|
| PagerDuty | `PagerDuty` | `pagerduty` | No | Incident lifecycle, on-call, schedules, escalation policies |
| Datadog | `Datadog` | `datadog` | Yes | Metrics queries, log search, APM trace search |
| Datadog Monitor | `DatadogMonitor` | `datadog` | No | Monitor list/get/status/mute/unmute |
| SRE | `Sre` | requires both | Yes | Cross-tool triage, correlation, investigation suggestions |

## PagerDuty Tool

Incident management via the PagerDuty REST API v2. Supports incident CRUD, on-call queries, schedule management, and escalation policy inspection.

### Configuration

```json
{
  "pagerduty": {
    "enabled": true,
    "api_token": "${PAGERDUTY_API_TOKEN}",
    "default_service_id": "PXXXXXX",
    "default_escalation_policy_id": "PXXXXXX",
    "base_url": "https://api.pagerduty.com",
    "rate_limit_rps": 5.0
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable PagerDuty integration |
| `api_token` | string | — | REST API v2 token (required). Supports `${ENV_VAR}` expansion. |
| `default_service_id` | string | — | Default service for `trigger` action |
| `default_escalation_policy_id` | string | — | Default escalation policy for `trigger` action |
| `base_url` | string | `https://api.pagerduty.com` | API base URL |
| `rate_limit_rps` | float | `5.0` | Max requests per second |

### Security Tiers

| Tier | Actions | Behavior |
|------|---------|----------|
| **read** | `list_incidents`, `get_incident`, `get_oncall`, `list_schedules`, `get_schedule`, `list_escalation_policies`, `get_escalation_policy` | Auto-approved |
| **modify** | `acknowledge`, `snooze`, `add_note` | Auto-approved |
| **dangerous** | `resolve`, `trigger` | Requires `confirmed=true` |

### Actions

#### list_incidents

List incidents with optional filters.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `status` | string | No | `triggered`, `acknowledged`, or `resolved` |
| `urgency` | string | No | `high` or `low` |
| `service_id` | string | No | Filter by PagerDuty service ID |
| `limit` | int | No | Max results (default 25, max 100) |

```json
{"action": "list_incidents", "status": "triggered"}
{"action": "list_incidents", "urgency": "high", "limit": 10}
```

#### get_incident

Get detailed information about a specific incident including assignments, escalation policy, and body.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `incident_id` | string | Yes | PagerDuty incident ID |

```json
{"action": "get_incident", "incident_id": "P1234567"}
```

#### acknowledge

Acknowledge an incident (stops escalation timer).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `incident_id` | string | Yes | PagerDuty incident ID |

```json
{"action": "acknowledge", "incident_id": "P1234567"}
```

#### resolve

Resolve an incident. Requires confirmation.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `incident_id` | string | Yes | PagerDuty incident ID |
| `confirmed` | bool | Yes | Must be `true` to proceed |

```json
{"action": "resolve", "incident_id": "P1234567", "confirmed": true}
```

#### snooze

Snooze an incident for a specified duration.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `incident_id` | string | Yes | PagerDuty incident ID |
| `snooze_duration` | int | Yes | Duration in seconds |

```json
{"action": "snooze", "incident_id": "P1234567", "snooze_duration": 3600}
```

#### add_note

Add a note to an incident timeline.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `incident_id` | string | Yes | PagerDuty incident ID |
| `note` | string | Yes | Note content |

```json
{"action": "add_note", "incident_id": "P1234567", "note": "Investigating root cause - CPU spike on web-01"}
```

#### trigger

Create a new incident. Requires confirmation.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `title` | string | Yes | Incident title |
| `confirmed` | bool | Yes | Must be `true` to proceed |
| `service_id` | string | No | Service ID (uses default if not set) |
| `escalation_policy_id` | string | No | Escalation policy ID (uses default if not set) |
| `details` | string | No | Incident body/details |

```json
{"action": "trigger", "title": "Database connection pool exhausted", "details": "Connection count hit limit on primary DB", "confirmed": true}
```

#### get_oncall

Get who is currently on-call for a schedule or escalation policy.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `schedule_id` | string | No* | PagerDuty schedule ID |
| `escalation_policy_id` | string | No* | Escalation policy ID |
| `timezone` | string | No | Timezone for results (default: UTC) |

*One of `schedule_id` or `escalation_policy_id` is required.

```json
{"action": "get_oncall", "schedule_id": "PXXXXXX"}
{"action": "get_oncall", "escalation_policy_id": "PXXXXXX", "timezone": "America/New_York"}
```

#### list_schedules

List all on-call schedules.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `query` | string | No | Filter by name (partial match) |
| `limit` | int | No | Max results (default 25, max 100) |

```json
{"action": "list_schedules"}
{"action": "list_schedules", "query": "platform"}
```

#### get_schedule

Get detailed schedule information including upcoming shifts (next 7 days).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `schedule_id` | string | Yes | PagerDuty schedule ID |
| `timezone` | string | No | Timezone (default: UTC) |

```json
{"action": "get_schedule", "schedule_id": "PXXXXXX"}
```

#### list_escalation_policies

List all escalation policies.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `query` | string | No | Filter by name (partial match) |
| `limit` | int | No | Max results (default 25, max 100) |

```json
{"action": "list_escalation_policies"}
```

#### get_escalation_policy

Get detailed escalation policy with rules and targets.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `escalation_policy_id` | string | Yes | Escalation policy ID |
| `include_oncall` | bool | No | Include current on-call users |

```json
{"action": "get_escalation_policy", "escalation_policy_id": "PXXXXXX", "include_oncall": true}
```

## Datadog Tool

Read-only observability tool for metrics, logs, and APM traces via the Datadog REST API.

### Configuration

```json
{
  "datadog": {
    "enabled": true,
    "api_key": "${DD_API_KEY}",
    "app_key": "${DD_APP_KEY}",
    "site": "datadoghq.com",
    "rate_limit_rps": 5.0
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable Datadog integration |
| `api_key` | string | — | Datadog API key (required). Supports `${ENV_VAR}`. |
| `app_key` | string | — | Datadog application key (required). Supports `${ENV_VAR}`. |
| `site` | string | `datadoghq.com` | Datadog site (e.g., `datadoghq.eu`, `us5.datadoghq.com`) |
| `rate_limit_rps` | float | `5.0` | Max requests per second |

The API base URL is derived as `https://api.<site>/`.

### Metrics Actions

#### query_metrics

Query time series data using Datadog metric query syntax. Max time range: 7 days.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `query` | string | Yes | Datadog metric query (e.g., `avg:system.cpu.user{*}`) |
| `from` | string | No | Unix timestamp or negative offset in seconds (default: `-3600`) |
| `to` | string | No | Unix timestamp or `0` for now (default: `0`) |

```json
{"action": "query_metrics", "query": "avg:system.cpu.user{*}", "from": "-3600"}
{"action": "query_metrics", "query": "sum:requests.count{service:web} by {host}", "from": "-7200", "to": "-3600"}
```

#### list_metrics

List available metric names from the last 24 hours.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `filter` | string | No | Prefix filter (e.g., `system.cpu`) |

```json
{"action": "list_metrics"}
{"action": "list_metrics", "filter": "system.cpu"}
```

#### get_metric_metadata

Get metadata for a specific metric.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `metric` | string | Yes | Metric name |

```json
{"action": "get_metric_metadata", "metric": "system.cpu.user"}
```

Returns: type, unit, per_unit, description, short_name, integration.

### Log Actions

#### search_logs

Search logs with filters and pagination.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `query` | string | No | Datadog log query string |
| `from` | string | No | RFC3339 or relative (e.g., `-1h`, `-15m`) |
| `to` | string | No | RFC3339 (default: now) |
| `limit` | int | No | Max results (default 100, max 1000) |
| `service` | string | No | Filter by service name |
| `host` | string | No | Filter by host name |
| `status` | string | No | `info`, `warn`, `error`, or `debug` |
| `cursor` | string | No | Pagination cursor from previous results |
| `indexes` | array | No | Specific log indexes to search |

```json
{"action": "search_logs", "query": "error timeout", "service": "api", "from": "-1h"}
{"action": "search_logs", "status": "error", "from": "-15m", "limit": 25}
```

#### get_log

Retrieve a single log entry by ID.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `log_id` | string | Yes | Log entry ID |

```json
{"action": "get_log", "log_id": "AQAAAYxxxxxxxx"}
```

#### list_indexes

List all configured log indexes with retention and rate limit info.

```json
{"action": "list_indexes"}
```

### APM Trace Actions

#### search_traces

Search APM traces with filters. Service name is required.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `service` | string | Yes | Service name |
| `operation` | string | No | Operation/span name (e.g., `http.request`, `db.query`) |
| `resource` | string | No | Resource name (e.g., `/api/checkout`) |
| `from` | string | No | RFC3339 or relative (default: `-15m`) |
| `to` | string | No | RFC3339 (default: now) |
| `min_duration` | string | No | Min trace duration (e.g., `1s`, `500ms`, or numeric ms) |
| `status` | string | No | `ok` or `error` |
| `limit` | int | No | Max results (default 20, max 100) |
| `cursor` | string | No | Pagination cursor |

```json
{"action": "search_traces", "service": "api", "min_duration": "1s", "from": "-1h"}
{"action": "search_traces", "service": "api-gateway", "status": "error", "limit": 50}
```

#### get_trace

Get full trace with all spans. Traces with more than 50 spans are summarized.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `trace_id` | string | Yes | Trace ID (hexadecimal string) |

```json
{"action": "get_trace", "trace_id": "1234567890abcdef"}
```

Returns: span timeline with timing, service, operation, resource, errors, and selected metadata (HTTP method/status, error messages, DB statements).

## Datadog Monitor Tool

Dedicated tool for Datadog monitor management. Uses the same `datadog` config section as the Datadog tool.

### Security Tiers

| Tier | Actions | Behavior |
|------|---------|----------|
| **read** | `list_monitors`, `get_monitor`, `get_monitor_status` | Auto-approved |
| **modify** | `mute_monitor`, `unmute_monitor` | Requires `confirmed=true` |

### Actions

#### list_monitors

List all monitors with optional filters. Monitors in Alert/Warn state are sorted first and highlighted.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `name` | string | No | Filter by name (substring match) |
| `tags` | array | No | Filter by tags (e.g., `["env:prod", "team:platform"]`) |
| `status` | string | No | Filter by status: `OK`, `Alert`, `Warn`, `No Data` |

```json
{"action": "list_monitors"}
{"action": "list_monitors", "status": "Alert"}
{"action": "list_monitors", "tags": ["env:prod"], "name": "api"}
```

Returns a summary with counts: total, alerting, warning, OK, no data.

#### get_monitor

Get detailed monitor information including query, thresholds, and creator.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `monitor_id` | int | Yes | Datadog monitor ID |

```json
{"action": "get_monitor", "monitor_id": 12345678}
```

#### get_monitor_status

Get current status with group states and last triggered times.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `monitor_id` | int | Yes | Datadog monitor ID |

```json
{"action": "get_monitor_status", "monitor_id": 12345678}
```

#### mute_monitor

Mute a monitor. Requires confirmation.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `monitor_id` | int | Yes | Datadog monitor ID |
| `confirmed` | bool | Yes | Must be `true` to proceed |
| `scope` | string | No | Scope to mute (e.g., `host:myhost`) |
| `end` | int | No | Unix timestamp when mute ends (omit for indefinite) |

```json
{"action": "mute_monitor", "monitor_id": 12345678, "confirmed": true}
{"action": "mute_monitor", "monitor_id": 12345678, "scope": "host:web-01", "end": 1717200000, "confirmed": true}
```

#### unmute_monitor

Unmute a monitor. Requires confirmation.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `monitor_id` | int | Yes | Datadog monitor ID |
| `confirmed` | bool | Yes | Must be `true` to proceed |
| `scope` | string | No | Scope to unmute |

```json
{"action": "unmute_monitor", "monitor_id": 12345678, "confirmed": true}
```

## SRE Correlation Tool

The SRE tool is an orchestration layer that calls the PagerDuty and Datadog tools to provide unified incident context. It does not make direct API calls. Requires both PagerDuty and Datadog to be enabled. Optionally leverages Kubernetes and SSH tools for deeper investigation.

### Prerequisites

- PagerDuty: **required**
- Datadog: **required**
- Kubernetes: optional (adds pod/event context to triage)
- SSH: optional (adds host-level investigation suggestions)

### Actions

#### triage_incident

Gather unified context from PagerDuty, Datadog metrics/logs, and optionally Kubernetes for a specific incident. Produces a structured report with timeline, metrics summary, error logs, pod status, and suggested next steps.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `incident_id` | string | Yes | PagerDuty incident ID |
| `include_k8s` | bool | No | Include K8s context (default: true if K8s configured) |
| `include_logs` | bool | No | Include Datadog logs (default: true) |
| `namespace` | string | No | K8s namespace (auto-detected from service name) |
| `cluster` | string | No | K8s cluster name |

```json
{"action": "triage_incident", "incident_id": "P123ABC"}
{"action": "triage_incident", "incident_id": "P123ABC", "include_k8s": true, "namespace": "production"}
```

Triage gathers:
1. PagerDuty incident details (title, status, urgency, service, assignments)
2. Datadog metrics for the service (CPU, memory, request rate, error rate, p99 latency)
3. Datadog error logs from the last 30 minutes
4. Kubernetes pod status and events (if enabled)
5. Suggested next steps based on findings

#### correlate

Cross-reference PagerDuty incidents, Datadog monitors, and log patterns for a service over a time range.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `service` | string | Yes | Service name to correlate |
| `time_range` | string | No | Time range (e.g., `30m`, `1h`, `6h`; default: `1h`) |

```json
{"action": "correlate", "service": "api-gateway", "time_range": "1h"}
```

Returns: matched incidents, triggered monitors, error log patterns, identified correlations (e.g., "3 incidents and 2 monitors detected -- likely related"), and a summary.

#### suggest_investigation

Get recommended investigation steps based on an incident type or a specific PagerDuty incident. Returns categorized suggestions with tool names and arguments.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `incident_id` | string | No* | PagerDuty incident ID (type auto-inferred from title) |
| `incident_type` | string | No* | Type: `high_cpu`, `oom`, `5xx_errors`, `latency`, `disk_full`, `crashloop`, `connectivity` |

*One of `incident_id` or `incident_type` is required.

```json
{"action": "suggest_investigation", "incident_type": "high_cpu"}
{"action": "suggest_investigation", "incident_id": "P123ABC"}
```

Suggestions are grouped by category (Kubernetes, Datadog, SSH) with ready-to-use tool arguments.

#### status

Show SRE tool configuration and which integrations are available.

```json
{"action": "status"}
```

## Full Configuration Example

```json
{
  "pagerduty": {
    "enabled": true,
    "api_token": "${PAGERDUTY_API_TOKEN}",
    "default_service_id": "PXXXXXX",
    "default_escalation_policy_id": "PXXXXXX",
    "rate_limit_rps": 5.0
  },
  "datadog": {
    "enabled": true,
    "api_key": "${DD_API_KEY}",
    "app_key": "${DD_APP_KEY}",
    "site": "datadoghq.com",
    "rate_limit_rps": 5.0
  }
}
```

Both PagerDuty and Datadog must be enabled for the SRE correlation tool to register. The Datadog Monitor tool registers whenever the `datadog` section is enabled.
