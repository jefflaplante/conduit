# Health Monitoring Endpoints

This document describes the health monitoring endpoints implemented in OCGO-016, providing operators with comprehensive gateway health visibility and diagnostic capabilities.

## Overview

The Conduit Gateway provides four health monitoring endpoints:

- `/health` - Enhanced health check with status and uptime
- `/metrics` - Detailed gateway metrics in JSON format
- `/diagnostics` - Real-time diagnostic events with filtering
- `/prometheus` - Prometheus-compatible metrics format

All endpoints are publicly accessible (no authentication required) but are rate-limited to prevent abuse.

## Endpoints

### `/health` - Enhanced Health Check

**Method:** GET  
**Content-Type:** application/json  
**Status Codes:** 200 (healthy), 503 (degraded)

```json
{
  "status": "healthy",
  "timestamp": "2026-02-09T12:34:56Z",
  "version": "0.1.0",
  "uptime": "2h30m45s"
}
```

**Status Values:**
- `healthy` - Gateway is operating normally
- `degraded` - Gateway has issues but is still functional
- `error` - Gateway has serious issues

### `/metrics` - Detailed Gateway Metrics

**Method:** GET  
**Content-Type:** application/json

```json
{
  "active_sessions": 25,
  "processing_sessions": 10,
  "waiting_sessions": 5,
  "idle_sessions": 10,
  "total_sessions": 100,
  "queue_depth": 15,
  "pending_requests": 8,
  "completed_requests": 1543,
  "failed_requests": 12,
  "webhook_connections": 12,
  "active_webhooks": 10,
  "uptime_seconds": 9045,
  "memory_usage_bytes": 134217728,
  "memory_usage_mb": 128.0,
  "goroutine_count": 245,
  "timestamp": "2026-02-09T12:34:56Z",
  "status": "healthy",
  "version": "0.1.0",
  "database": {
    "connected": true,
    "error": ""
  },
  "system_health": {
    "memory_pressure": false,
    "high_load": false,
    "queue_backlog": false
  },
  "last_activity": "2026-02-09T12:34:45Z",
  "is_idle": false
}
```

**System Health Indicators:**
- `memory_pressure`: true if memory usage > 512MB
- `high_load`: true if goroutine count > 1000
- `queue_backlog`: true if queue depth > 100

### `/diagnostics` - Real-time Diagnostic Events

**Method:** GET  
**Content-Type:** application/json

**Query Parameters:**
- `type` - Filter by event type (`heartbeat`, `status_change`, `metric_alert`, `system_event`)
- `severity` - Filter by severity (`info`, `warning`, `error`, `critical`)
- `source` - Filter by event source
- `since` - Filter events since timestamp (RFC3339 format)
- `until` - Filter events until timestamp (RFC3339 format)
- `limit` - Maximum number of events to return (default: 100)

```json
{
  "events": [
    {
      "id": "evt_1707484496123456789",
      "type": "heartbeat",
      "severity": "info",
      "timestamp": "2026-02-09T12:34:56Z",
      "message": "System heartbeat",
      "source": "gateway",
      "metrics": {
        "active_sessions": 25,
        "memory_usage_mb": 128.0
      },
      "metadata": {},
      "context": {}
    }
  ],
  "count": 1,
  "filter": {
    "type": "heartbeat",
    "max_results": 100
  },
  "timestamp": "2026-02-09T12:34:56Z",
  "system_info": {
    "gateway_version": "0.1.0",
    "store_type": "memory",
    "status": "healthy",
    "uptime_seconds": 9045
  }
}
```

**Event Types:**
- `heartbeat` - Regular system health reports
- `status_change` - Gateway status transitions
- `metric_alert` - Metric threshold violations
- `system_event` - Significant system events

**Severity Levels:**
- `info` - Informational events
- `warning` - Potential issues
- `error` - Errors that may affect functionality
- `critical` - Critical errors requiring immediate attention

### `/prometheus` - Prometheus Metrics

**Method:** GET  
**Content-Type:** text/plain; version=0.0.4; charset=utf-8

Returns metrics in Prometheus exposition format:

```
# HELP conduit_uptime_seconds Total uptime in seconds
# TYPE conduit_uptime_seconds counter
conduit_uptime_seconds 9045

# HELP conduit_memory_usage_bytes Memory usage in bytes
# TYPE conduit_memory_usage_bytes gauge
conduit_memory_usage_bytes 134217728

# HELP conduit_sessions_active Number of active sessions
# TYPE conduit_sessions_active gauge
conduit_sessions_active 25

# HELP conduit_requests_completed_total Total completed requests
# TYPE conduit_requests_completed_total counter
conduit_requests_completed_total 1543

# HELP conduit_status Gateway status (1=healthy, 0=unhealthy)
# TYPE conduit_status gauge
conduit_status{status="healthy"} 1
```

**Available Metrics:**
- `conduit_uptime_seconds` - Gateway uptime
- `conduit_memory_usage_bytes` - Current memory usage
- `conduit_sessions_active` - Active session count
- `conduit_sessions_total` - Total session count
- `conduit_requests_completed_total` - Completed request count
- `conduit_requests_failed_total` - Failed request count
- `conduit_goroutines` - Current goroutine count
- `conduit_websocket_connections` - WebSocket connection count
- `conduit_queue_depth` - Request queue depth
- `conduit_status` - Gateway health status (1=healthy, 0=unhealthy)

## Rate Limiting

All health endpoints are protected by rate limiting:

- **Anonymous requests**: Limited based on IP address
- **Authenticated requests**: Higher limits based on client credentials
- **Rate exceeded**: Returns HTTP 429 Too Many Requests

Default limits are configured in the gateway's rate limiting settings.

## Integration Examples

### Prometheus Configuration

Add this to your `prometheus.yml`:

```yaml
scrape_configs:
  - job_name: 'conduit-gateway'
    static_configs:
      - targets: ['localhost:8080']
    metrics_path: '/prometheus'
    scrape_interval: 30s
```

### Health Check Script

```bash
#!/bin/bash
GATEWAY_URL="http://localhost:8080"

# Check health
health_status=$(curl -s "$GATEWAY_URL/health" | jq -r '.status')
if [ "$health_status" != "healthy" ]; then
    echo "ALERT: Gateway status is $health_status"
    exit 1
fi

# Check metrics
memory_mb=$(curl -s "$GATEWAY_URL/metrics" | jq '.memory_usage_mb')
if (( $(echo "$memory_mb > 1024" | bc -l) )); then
    echo "WARNING: High memory usage: ${memory_mb}MB"
fi

echo "Gateway is healthy"
```

### Event Webhook Integration

Configure external event emission in your gateway config:

```json
{
  "monitoring": {
    "event_emission": {
      "enabled": true,
      "type": "webhook",
      "endpoint": "https://your-monitoring-system.com/webhooks/conduit",
      "format": "json",
      "timeout_ms": 5000,
      "headers": {
        "Authorization": "Bearer your-token"
      },
      "filters": {
        "min_severity": "warning",
        "types": ["status_change", "metric_alert", "system_event"]
      }
    }
  }
}
```

### Grafana Dashboard

Query examples for Grafana:

- **Active Sessions**: `conduit_sessions_active`
- **Memory Usage**: `conduit_memory_usage_bytes / 1024 / 1024` (MB)
- **Request Rate**: `rate(conduit_requests_completed_total[5m])`
- **Error Rate**: `rate(conduit_requests_failed_total[5m])`
- **Uptime**: `conduit_uptime_seconds`

### Monitoring Alerts

Example alerts for common issues:

1. **Gateway Down**:
   - Check: `up{job="conduit-gateway"} == 0`
   - Action: Immediate notification

2. **High Memory Usage**:
   - Check: `conduit_memory_usage_bytes > 1073741824` (>1GB)
   - Action: Warning notification

3. **High Queue Depth**:
   - Check: `conduit_queue_depth > 100`
   - Action: Investigation needed

4. **Error Rate**:
   - Check: `rate(conduit_requests_failed_total[5m]) > 0.1` (>10%)
   - Action: Error investigation

## Troubleshooting

### Common Issues

1. **503 Service Unavailable on `/health`**:
   - Gateway status is degraded or error
   - Check `/metrics` for specific issues
   - Review `/diagnostics` for recent events

2. **Empty `/diagnostics` response**:
   - No events stored yet (normal on startup)
   - Event store may have been cleared
   - Check `system_info` for store configuration

3. **Rate limiting (429 responses)**:
   - Requests exceeded configured limits
   - Wait for rate limit window to reset
   - Consider authenticating requests for higher limits

4. **Missing metrics in Prometheus**:
   - Verify Prometheus can reach the gateway
   - Check firewall and network connectivity
   - Ensure `/prometheus` endpoint returns data

### Diagnostic Commands

```bash
# Test all endpoints
curl -s http://localhost:8080/health | jq
curl -s http://localhost:8080/metrics | jq
curl -s http://localhost:8080/diagnostics | jq

# Test Prometheus format
curl -s http://localhost:8080/prometheus

# Filter diagnostic events
curl -s "http://localhost:8080/diagnostics?severity=error&limit=10" | jq

# Check recent events
since=$(date -u -d '1 hour ago' +%Y-%m-%dT%H:%M:%SZ)
curl -s "http://localhost:8080/diagnostics?since=$since" | jq '.count'
```

This monitoring system provides comprehensive visibility into gateway health and enables proactive monitoring and alerting for operational environments.