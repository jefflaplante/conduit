package monitoring

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"conduit/internal/sessions"
)

// MetricsCollector gathers system and application metrics
type MetricsCollector struct {
	// Gateway components
	sessionStore   SessionStoreInterface
	gatewayMetrics *GatewayMetrics

	// Tracking metrics
	lastActivityTime time.Time
	startTime        time.Time
	wsConnections    int
	activeRequests   int
	queueDepth       int

	// Heartbeat metrics
	heartbeatJobsEnabled  int
	heartbeatJobsTotal    int
	lastHeartbeatTime     time.Time
	heartbeatSuccessCount int64
	heartbeatErrorCount   int64

	// Synchronization
	mutex sync.RWMutex
}

// CollectorDependencies contains the gateway components needed for metrics collection
type CollectorDependencies struct {
	SessionStore   SessionStoreInterface
	GatewayMetrics *GatewayMetrics
}

// NewMetricsCollector creates a new metrics collector
func NewMetricsCollector(deps CollectorDependencies) *MetricsCollector {
	now := time.Now()

	return &MetricsCollector{
		sessionStore:     deps.SessionStore,
		gatewayMetrics:   deps.GatewayMetrics,
		lastActivityTime: now,
		startTime:        now,
		mutex:            sync.RWMutex{},
	}
}

// UpdateWebSocketConnections updates the WebSocket connection count
func (c *MetricsCollector) UpdateWebSocketConnections(count int) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.wsConnections = count
	c.lastActivityTime = time.Now()
}

// UpdateActiveRequests updates the active request count
func (c *MetricsCollector) UpdateActiveRequests(count int) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.activeRequests = count
	c.lastActivityTime = time.Now()
}

// UpdateQueueDepth updates the request queue depth
func (c *MetricsCollector) UpdateQueueDepth(depth int) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.queueDepth = depth
	c.lastActivityTime = time.Now()
}

// MarkActivity records that some activity occurred
func (c *MetricsCollector) MarkActivity() {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.lastActivityTime = time.Now()
}

// GetLastActivityTime returns when the last activity was recorded
func (c *MetricsCollector) GetLastActivityTime() time.Time {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return c.lastActivityTime
}

// IsIdle returns true if the system has been idle for the given duration
func (c *MetricsCollector) IsIdle(duration time.Duration) bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return time.Since(c.lastActivityTime) > duration
}

// UpdateHeartbeatJobs updates the heartbeat job counts
func (c *MetricsCollector) UpdateHeartbeatJobs(total, enabled int) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.heartbeatJobsTotal = total
	c.heartbeatJobsEnabled = enabled
}

// MarkHeartbeatSuccess records a successful heartbeat execution
func (c *MetricsCollector) MarkHeartbeatSuccess() {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.lastHeartbeatTime = time.Now()
	c.heartbeatSuccessCount++
	c.lastActivityTime = time.Now()
}

// MarkHeartbeatError records a failed heartbeat execution
func (c *MetricsCollector) MarkHeartbeatError() {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.heartbeatErrorCount++
	c.lastActivityTime = time.Now()
}

// GetHeartbeatMetrics returns current heartbeat metrics
func (c *MetricsCollector) GetHeartbeatMetrics() HeartbeatMetrics {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return HeartbeatMetrics{
		TotalJobs:     c.heartbeatJobsTotal,
		EnabledJobs:   c.heartbeatJobsEnabled,
		LastExecution: c.lastHeartbeatTime,
		SuccessCount:  c.heartbeatSuccessCount,
		ErrorCount:    c.heartbeatErrorCount,
		IsHealthy:     c.heartbeatJobsEnabled > 0 && time.Since(c.lastHeartbeatTime) < 10*time.Minute,
	}
}

// CollectMetrics gathers current system metrics
func (c *MetricsCollector) CollectMetrics(ctx context.Context) (*GatewayMetrics, error) {
	c.mutex.RLock()
	wsConns := c.wsConnections
	activeReqs := c.activeRequests
	queueDepth := c.queueDepth
	startTime := c.startTime
	c.mutex.RUnlock()

	// Update the shared metrics object with fresh data
	if c.gatewayMetrics != nil {
		// Update system metrics (memory, goroutines, etc.)
		c.gatewayMetrics.UpdateSystemMetrics()

		// Update WebSocket metrics
		c.gatewayMetrics.UpdateWebhookMetrics(wsConns, wsConns) // connections = active for simplicity

		// Update queue metrics
		c.gatewayMetrics.UpdateQueueMetrics(queueDepth, activeReqs)
	}

	// Collect session metrics from database
	sessionMetrics, err := c.collectSessionMetrics(ctx)
	if err != nil {
		return nil, err
	}

	// Update session counts in gateway metrics
	if c.gatewayMetrics != nil {
		c.gatewayMetrics.UpdateSessionCount(
			sessionMetrics.ActiveSessions,
			sessionMetrics.ProcessingSessions,
			sessionMetrics.WaitingSessions,
			sessionMetrics.IdleSessions,
			sessionMetrics.TotalSessions,
		)

		// Calculate uptime manually to ensure consistency
		uptime := time.Since(startTime)
		c.gatewayMetrics.mutex.Lock()
		c.gatewayMetrics.UptimeSeconds = int64(uptime.Seconds())
		c.gatewayMetrics.Timestamp = time.Now()
		c.gatewayMetrics.mutex.Unlock()

		return &GatewayMetrics{}, nil // Return empty since gatewayMetrics has the data
	}

	// Fallback: create metrics from scratch if no shared object
	metrics := NewGatewayMetrics()
	metrics.startTime = startTime

	// Set session metrics
	metrics.UpdateSessionCount(
		sessionMetrics.ActiveSessions,
		sessionMetrics.ProcessingSessions,
		sessionMetrics.WaitingSessions,
		sessionMetrics.IdleSessions,
		sessionMetrics.TotalSessions,
	)

	// Set system metrics
	metrics.UpdateSystemMetrics()

	// Set network metrics
	metrics.UpdateWebhookMetrics(wsConns, wsConns)
	metrics.UpdateQueueMetrics(queueDepth, activeReqs)

	return metrics, nil
}

// SessionMetrics contains session-related metrics
type SessionMetrics struct {
	ActiveSessions     int
	ProcessingSessions int
	WaitingSessions    int
	IdleSessions       int
	TotalSessions      int
}

// HeartbeatMetrics contains heartbeat loop metrics
type HeartbeatMetrics struct {
	TotalJobs     int
	EnabledJobs   int
	LastExecution time.Time
	SuccessCount  int64
	ErrorCount    int64
	IsHealthy     bool
}

// collectSessionMetrics gathers session metrics from the state tracker and database
func (c *MetricsCollector) collectSessionMetrics(ctx context.Context) (*SessionMetrics, error) {
	if c.sessionStore == nil {
		return &SessionMetrics{}, nil
	}

	// Get metrics from the state tracker (more accurate and real-time)
	stateMetrics := c.sessionStore.GetSessionStateMetrics()

	// Convert to our SessionMetrics format
	// Note: ActiveSessions includes all non-idle sessions
	activeCount := int(stateMetrics.ProcessingSessions + stateMetrics.WaitingSessions)

	return &SessionMetrics{
		ActiveSessions:     activeCount,
		ProcessingSessions: int(stateMetrics.ProcessingSessions),
		WaitingSessions:    int(stateMetrics.WaitingSessions),
		IdleSessions:       int(stateMetrics.IdleSessions),
		TotalSessions:      int(stateMetrics.TotalSessions),
	}, nil
}

// GetDetailedStats returns detailed statistics about the current state
func (c *MetricsCollector) GetDetailedStats(ctx context.Context) (map[string]interface{}, error) {
	// CollectMetrics updates the shared gatewayMetrics object; the return value
	// is empty when the shared object exists, so use a snapshot instead.
	_, err := c.CollectMetrics(ctx)
	if err != nil {
		return nil, err
	}

	var metrics MetricsSnapshot
	if c.gatewayMetrics != nil {
		metrics = c.gatewayMetrics.Snapshot()
	}

	c.mutex.RLock()
	lastActivity := c.lastActivityTime
	c.mutex.RUnlock()

	stats := map[string]interface{}{
		"timestamp":      time.Now(),
		"uptime_seconds": metrics.UptimeSeconds,
		"uptime_human":   time.Duration(metrics.UptimeSeconds) * time.Second,
		"last_activity":  lastActivity,
		"idle_duration":  time.Since(lastActivity),

		// Session stats
		"sessions": map[string]int{
			"active":     metrics.ActiveSessions,
			"processing": metrics.ProcessingSessions,
			"waiting":    metrics.WaitingSessions,
			"idle":       metrics.IdleSessions,
			"total":      metrics.TotalSessions,
		},

		// System stats
		"system": map[string]interface{}{
			"memory_mb":  metrics.MemoryUsageMB,
			"goroutines": metrics.GoroutineCount,
			"status":     metrics.Status,
		},

		// Network stats
		"network": map[string]int{
			"websocket_connections": metrics.WebhookConnections,
			"active_requests":       c.activeRequests,
			"queue_depth":           c.queueDepth,
		},
	}

	return stats, nil
}

// DetectStuckSessions identifies sessions that have been processing for too long
func (c *MetricsCollector) DetectStuckSessions(ctx context.Context, threshold time.Duration) ([]string, error) {
	if c.sessionStore == nil {
		return nil, nil
	}

	// Use the state tracker's stuck session detection
	config := sessions.StuckSessionConfig{
		ProcessingTimeout: threshold,
		WaitingTimeout:    5 * time.Minute,
		ErrorRetryLimit:   3,
	}

	stuckSessions := c.sessionStore.DetectStuckSessions(config)

	var sessionKeys []string
	for _, stuck := range stuckSessions {
		sessionKeys = append(sessionKeys, stuck.SessionKey)
	}

	return sessionKeys, nil
}

// ValidateDatabase checks if the database connection is healthy
func (c *MetricsCollector) ValidateDatabase(ctx context.Context) error {
	if c.sessionStore == nil {
		return nil
	}

	// Simple ping test
	if err := c.sessionStore.DB().PingContext(ctx); err != nil {
		return err
	}

	// Try a simple query
	var count int
	err := c.sessionStore.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM sessions").Scan(&count)
	if err != nil && err != sql.ErrNoRows {
		return err
	}

	return nil
}
