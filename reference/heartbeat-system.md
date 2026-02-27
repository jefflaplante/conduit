# Conduit Go Gateway Heartbeat System

This document describes the diagnostic heartbeat system implemented in OCGO-014.

## Overview

The heartbeat system provides continuous monitoring of the gateway's health and performance through:
- **Background goroutine** that runs on configurable intervals
- **Metrics collection** from sessions, WebSocket connections, and system resources
- **Event emission** for external monitoring systems
- **Idle detection** to prevent unnecessary heartbeat spam
- **Graceful lifecycle management** with proper resource cleanup

## Architecture

### Components

1. **HeartbeatService** (`internal/monitoring/heartbeat.go`)
   - Main orchestrator that manages the heartbeat goroutine
   - Handles start/stop lifecycle with context cancellation
   - Emits diagnostic events and tracks errors

2. **MetricsCollector** (`internal/monitoring/collector.go`)
   - Gathers metrics from gateway components
   - Detects stuck sessions and system health issues
   - Provides thread-safe metric updates

3. **GatewayMetrics** (`internal/monitoring/metrics.go`)
   - Data structure for system health metrics
   - Thread-safe metric storage and snapshots

4. **HeartbeatEvent** (`internal/monitoring/events.go`)
   - Structured diagnostic events
   - Event filtering and storage capabilities

### Integration Points

- **Gateway Lifecycle**: Heartbeat starts/stops with gateway
- **Session Tracking**: Monitors active, processing, waiting, and idle sessions
- **WebSocket Monitoring**: Tracks connection counts
- **Request Queuing**: Monitors active requests and queue depth
- **System Resources**: Memory usage, goroutine counts, uptime

## Configuration

### HeartbeatConfig

```go
type HeartbeatConfig struct {
    Enabled         bool   // Enable/disable heartbeat
    IntervalSeconds int    // Heartbeat interval (10-3600 seconds)
    EnableMetrics   bool   // Collect system metrics
    EnableEvents    bool   // Emit diagnostic events
    LogLevel        string // Logging level (debug, info, warn, error)
    MaxQueueDepth   int    // Maximum queue depth before alerts
}
```

### Default Configuration

```json
{
  "heartbeat": {
    "enabled": true,
    "interval_seconds": 30,
    "enable_metrics": true,
    "enable_events": true,
    "log_level": "info",
    "max_queue_depth": 1000
  }
}
```

## Metrics Collected

### Session Metrics
- **Active Sessions**: Sessions updated within last 2 minutes
- **Processing Sessions**: Sessions currently being processed
- **Waiting Sessions**: Sessions waiting for processing
- **Idle Sessions**: Sessions with no recent activity
- **Total Sessions**: Overall session count

### System Metrics
- **Memory Usage**: Current memory allocation (bytes and MB)
- **Goroutine Count**: Number of active goroutines
- **Uptime**: Gateway uptime in seconds
- **Status**: Gateway health status (healthy, degraded, error)

### Network Metrics
- **WebSocket Connections**: Active WebSocket client connections
- **Active Requests**: Currently processing requests
- **Queue Depth**: Number of queued requests
- **Request Counters**: Completed and failed request counts

## Idle Detection

The heartbeat system implements intelligent idle detection:

- **2-minute threshold**: No heartbeat if system idle >2 minutes
- **Activity tracking**: Updates on message processing, connections, requests
- **Automatic resume**: Heartbeats resume when activity detected

This prevents log spam during quiet periods while ensuring monitoring during active use.

## Event Types

### Heartbeat Events
- **Type**: `heartbeat`
- **Frequency**: Every heartbeat cycle (when not idle)
- **Content**: Full metrics snapshot with system health

### Status Change Events
- **Type**: `status_change`
- **Trigger**: Gateway status transitions (healthy ↔ degraded ↔ error)
- **Content**: Old and new status information

### Metric Alert Events
- **Type**: `metric_alert`
- **Trigger**: Metrics exceed configured thresholds
- **Content**: Metric name, value, and threshold

### System Events
- **Type**: `system_event`
- **Trigger**: System errors, stuck sessions, database issues
- **Content**: Error details and context

## Session Health Detection

### Stuck Session Detection
The system identifies sessions that may be stuck in processing:

- **Threshold**: Sessions processing >2 minutes
- **Detection**: Compares last update time against threshold
- **Action**: Emits warning events for investigation

### Session State Classification
```
Recent (≤2 min ago)    → Active
Processing (30s-2m)    → Processing  
Waiting (2-5 min)      → Waiting
Old (>5 min)           → Idle
```

## Error Handling

### Error Tracking
- Last 10 errors are stored in memory
- Error events are emitted for external monitoring
- Heartbeat continues despite individual errors

### Database Health
- Connection ping tests on each heartbeat
- Simple query validation
- Error events on database connectivity issues

## Performance Considerations

### Resource Usage
- **CPU**: Minimal - runs every 30 seconds by default
- **Memory**: Low overhead - structured data collection
- **I/O**: Light database queries for session metrics

### Scalability
- Thread-safe metric collection
- Non-blocking operations
- Configurable intervals for different load levels

## Integration Example

```go
// Initialize monitoring system
gatewayMetrics := monitoring.NewGatewayMetrics()
eventStore := monitoring.NewMemoryEventStore(1000)

// Create metrics collector
collector := monitoring.NewMetricsCollector(monitoring.CollectorDependencies{
    SessionStore:   sessionStore,
    GatewayMetrics: gatewayMetrics,
})

// Create heartbeat service
heartbeatService := monitoring.NewHeartbeatService(monitoring.HeartbeatDependencies{
    Config:     config.Heartbeat,
    Collector:  collector,
    EventStore: eventStore,
})

// Start with gateway
err := heartbeatService.Start(ctx)
if err != nil {
    log.Printf("Failed to start heartbeat: %v", err)
}
defer heartbeatService.Stop()

// Update metrics during gateway operation
collector.UpdateWebSocketConnections(len(clients))
collector.UpdateActiveRequests(len(activeRequests))
collector.MarkActivity()
```

## Monitoring Integration

### External Systems
- Events can be consumed by external monitoring (Prometheus, Grafana)
- JSON-serializable event format
- Structured logging integration

### Health Endpoints
The gateway can expose heartbeat data via HTTP endpoints:
- `/health` - Basic health check
- `/metrics` - Detailed metrics snapshot  
- `/events` - Recent diagnostic events

## Testing

### Unit Tests
- **collector_test.go**: Metrics collection testing
- **heartbeat_test.go**: Service lifecycle and behavior testing
- **Integration tests**: Complete system testing

### Test Coverage
- Goroutine lifecycle management
- Metrics accuracy
- Event generation
- Error handling
- Concurrency safety
- Resource cleanup

## Troubleshooting

### Common Issues

**Heartbeat not running**
- Check `heartbeat.enabled` configuration
- Verify no startup errors in logs
- Confirm gateway context hasn't been cancelled

**No heartbeat events**  
- Check `heartbeat.enable_events` configuration
- Verify event store is configured
- System may be idle (>2 minutes no activity)

**High heartbeat frequency**
- Check `heartbeat.interval_seconds` setting
- Consider increasing interval for high-load systems
- Review idle detection threshold

**Missing metrics**
- Verify session store connectivity
- Check database health
- Review metric collection errors in logs

### Debug Mode

Enable debug logging for detailed heartbeat information:

```json
{
  "heartbeat": {
    "log_level": "debug"
  }
}
```

This provides detailed logging of:
- Individual heartbeat cycles
- Metric collection details
- Idle detection decisions
- Error context

## Future Enhancements

### Planned Features
- **Custom metric thresholds**: Configurable alerting thresholds
- **Metric persistence**: Long-term metric storage
- **Dashboard integration**: Web UI for real-time monitoring
- **Performance profiling**: CPU and memory profiling integration

### Extension Points
- **Custom collectors**: Plugin architecture for domain-specific metrics
- **Event handlers**: Custom event processing logic
- **External stores**: Database or remote event storage
- **Alerting integrations**: Email, Slack, PagerDuty notifications

---

**Implementation Status**: ✅ Complete (OCGO-014)  
**Dependencies**: OCGO-013 (Configuration & Data Structures)  
**Testing**: ✅ Comprehensive unit and integration tests  
**Documentation**: ✅ Complete