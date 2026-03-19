# MQTT Integration

Conduit can subscribe to an MQTT broker to ingest device events from home automation systems like zigbee2mqtt and Home Assistant. This makes real-time sensor data (temperature, motion, light states, battery levels) available to the AI agent via a tool, and enables automated monitoring through the heartbeat system.

## Overview

```
MQTT Broker (zigbee2mqtt, Home Assistant)
        │
        ▼
┌─────────────────┐
│  mqtt.Service    │  ← long-lived subscriber, auto-reconnects
│  (internal/mqtt) │
└────────┬────────┘
         │ per-message callback
         ▼
┌─────────────────┐
│  EventBuffer     │  ← per-topic ring buffers, pruned by age
│  (in-memory)     │
└────────┬────────┘
         │ queried via MQTTService interface
         ▼
┌─────────────────┐     ┌──────────────────┐
│  MQTT Tool       │     │  HEARTBEAT.md     │
│  (AI agent use)  │     │  (periodic check) │
│  status/recent/  │     │  "check temps,    │
│  history/topics/ │     │   alert if..."    │
│  publish         │     └──────────────────┘
└─────────────────┘
```

MQTT is **not a channel adapter** — it's pub/sub, not conversational. The integration is an event ingest system: Conduit subscribes, buffers events in memory, and exposes them to the AI via the `MQTT` tool.

## Configuration

Add an `mqtt` section to your config JSON:

```json
{
  "mqtt": {
    "enabled": true,
    "broker_url": "tcp://192.168.1.10:1883",
    "client_id": "conduit",
    "username": "${MQTT_USERNAME}",
    "password": "${MQTT_PASSWORD}",
    "topics": ["zigbee2mqtt/#"],
    "qos": 0,
    "buffer_max_age_seconds": 3600,
    "buffer_max_events": 1000,
    "buffer_max_topics": 500,
    "publish_allowed": false
  }
}
```

### Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable MQTT event ingest |
| `broker_url` | string | **required** | Broker address: `tcp://host:1883` or `ssl://host:8883` |
| `client_id` | string | `"conduit"` | MQTT client ID |
| `username` | string | `""` | Broker username (supports `${ENV_VAR}`) |
| `password` | string | `""` | Broker password (supports `${ENV_VAR}`) |
| `topics` | string[] | **required** | Topic subscriptions (wildcards OK: `#`, `+`) |
| `qos` | int | `0` | QoS level for subscriptions (0, 1, or 2) |
| `buffer_max_age_seconds` | int | `3600` | Max age of buffered events before pruning |
| `buffer_max_events` | int | `1000` | Max events per topic (ring buffer) |
| `buffer_max_topics` | int | `500` | Max number of tracked topics |
| `publish_allowed` | bool | `false` | Allow the AI to publish MQTT messages |
| `tls` | object | `null` | Optional TLS configuration (see below) |

### TLS Configuration

```json
{
  "mqtt": {
    "broker_url": "ssl://192.168.1.10:8883",
    "tls": {
      "ca_cert": "/path/to/ca.pem",
      "client_cert": "/path/to/client.pem",
      "client_key": "/path/to/client.key",
      "insecure": false
    }
  }
}
```

### Environment Variables

| Variable | Description |
|----------|-------------|
| `MQTT_USERNAME` | Broker username |
| `MQTT_PASSWORD` | Broker password |

## MQTT Tool

The `MQTT` tool is automatically registered when MQTT is enabled. It uses action-based dispatch with five actions.

### status

Connection info and event counts.

```json
{"action": "status"}
```

Returns:
```json
{
  "connected": true,
  "broker_url": "tcp://192.168.1.10:1883",
  "subscribed_topics": ["zigbee2mqtt/#"],
  "active_topics": 42,
  "total_events": 12847,
  "publish_allowed": false
}
```

### topics

List all active topics with last value and event count.

```json
{"action": "topics"}
```

Returns:
```json
{
  "topics": [
    {
      "topic": "zigbee2mqtt/Living Room Sensor",
      "event_count": 156,
      "last_event": "2026-03-19T10:30:00Z",
      "last_value": {"temperature": 22.1, "humidity": 45, "battery": 87}
    }
  ]
}
```

### recent

Recent events across all topics, optionally filtered by glob pattern.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `limit` | int | 20 | Max events (capped at 100) |
| `topic_pattern` | string | — | Glob filter (e.g. `zigbee2mqtt/*`) |

```json
{"action": "recent", "limit": 10}
{"action": "recent", "topic_pattern": "zigbee2mqtt/*", "limit": 5}
```

### history

Event history for a specific topic.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `topic` | string | Yes | Exact topic path |
| `limit` | int | No | Max events (default 20, max 100) |

```json
{"action": "history", "topic": "zigbee2mqtt/Living Room Sensor", "limit": 10}
```

### publish

Publish a message to a topic. **Gated by `publish_allowed` config** (default `false`).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `topic` | string | Yes | Target topic |
| `payload` | string | Yes | JSON payload |
| `qos` | int | No | QoS level (default 0) |
| `retained` | bool | No | Retain message (default false) |

```json
{"action": "publish", "topic": "zigbee2mqtt/Light/set", "payload": "{\"state\":\"ON\"}"}
```

Returns an error if `publish_allowed` is `false`.

## zigbee2mqtt Topic Patterns

Common topic patterns when using zigbee2mqtt:

| Pattern | Description |
|---------|-------------|
| `zigbee2mqtt/<device_name>` | Device state reports (temperature, humidity, battery, etc.) |
| `zigbee2mqtt/bridge/state` | Bridge online/offline status |
| `zigbee2mqtt/bridge/devices` | Full device list |
| `zigbee2mqtt/bridge/groups` | Group definitions |
| `zigbee2mqtt/bridge/logging` | Bridge log messages |
| `zigbee2mqtt/<device>/set` | Command topic for controlling devices |
| `zigbee2mqtt/<device>/get` | Request current state from a device |

### Example Device Payloads

**Temperature/humidity sensor:**
```json
{
  "temperature": 22.1,
  "humidity": 45,
  "battery": 87,
  "linkquality": 156,
  "voltage": 2985
}
```

**Contact sensor:**
```json
{
  "contact": true,
  "battery": 100,
  "linkquality": 112
}
```

**Motion sensor:**
```json
{
  "occupancy": true,
  "illuminance_lux": 42,
  "battery": 95
}
```

**Smart plug:**
```json
{
  "state": "ON",
  "power": 45.2,
  "energy": 12.34,
  "current": 0.19,
  "voltage": 238.1
}
```

## Heartbeat Integration

The most powerful use of MQTT data is through the [Agent Heartbeat](agent-heartbeat.md) system. Add monitoring tasks to your `HEARTBEAT.md`:

```markdown
## MQTT Monitoring

- Check all temperature sensors. Alert if any room is below 15°C or above 30°C.
- Check battery levels on all zigbee devices. Warn if any are below 20%.
- Check if the front door contact sensor has been open for more than 10 minutes.
- Report if any device hasn't reported in the last hour (may be offline).
```

During each heartbeat cycle, the AI reads these tasks, uses the MQTT tool to check sensor data, and sends alerts through configured channels (Telegram, etc.) if anything needs attention.

## Event Buffer

Events are stored in memory only — they do not survive restarts. This is intentional:

- zigbee2mqtt devices report frequently (every few seconds to minutes)
- Persistent storage would grow quickly and isn't needed for real-time awareness
- Historical data should be handled by dedicated systems (InfluxDB, Home Assistant recorder)

### Buffer Behavior

- **Per-topic ring buffers**: Each topic gets its own ring buffer of `buffer_max_events` capacity
- **Age-based pruning**: Events older than `buffer_max_age_seconds` are removed every 60 seconds
- **Topic cap**: If `buffer_max_topics` is reached, new topics are dropped (existing topics still receive events)
- **Total events counter**: Monotonically increasing, counts all events ever received

## Architecture

### Package Layout

```
internal/mqtt/
├── client.go           # Paho MQTT client wrapper with auto-reconnect
├── event_buffer.go     # Per-topic ring buffers with age-based pruning
├── service.go          # Owns client + buffer, provides query API
├── adapter.go          # Bridges internal types to tool-layer interface
├── errors.go           # Sentinel errors
└── event_buffer_test.go

internal/tools/mqtt/
├── tool.go             # MQTT tool with action dispatch
└── tool_test.go
```

### Connection Management

The MQTT client uses `paho.mqtt.golang` with:
- Auto-reconnect with exponential backoff (up to 2 minutes)
- Connect retry on initial connection failure
- 60-second keepalive with 10-second ping timeout
- Automatic re-subscription to all topics on reconnect

### Safety

- **Publish is off by default**: `publish_allowed` must be explicitly set to `true`
- **No persistent state**: Buffer is in-memory only, no database writes
- **No channel adapter**: MQTT does not create conversational sessions
- **Credentials via env vars**: Username/password support `${ENV_VAR}` expansion

## Troubleshooting

### Connection Issues

Check the Conduit logs for `[MQTT]` prefixed messages:
```
[MQTT] Connected to tcp://192.168.1.10:1883
[MQTT] Subscribed to zigbee2mqtt/# (QoS 0)
[MQTT] Connection lost: EOF
[MQTT] Reconnecting to tcp://192.168.1.10:1883...
```

### No Events Appearing

1. Verify the broker is reachable: `mosquitto_sub -h 192.168.1.10 -t "zigbee2mqtt/#" -v`
2. Check topic subscriptions match what you expect
3. Use `MQTT(action="status")` to confirm connection state
4. Use `MQTT(action="topics")` to see which topics have received data

### High Memory Usage

Reduce buffer sizes:
```json
{
  "mqtt": {
    "buffer_max_events": 100,
    "buffer_max_topics": 100,
    "buffer_max_age_seconds": 600
  }
}
```
