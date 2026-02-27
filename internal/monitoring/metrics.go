package monitoring

import (
	"runtime"
	"sync"
	"time"
)

// GatewayMetrics tracks gateway health and performance metrics
type GatewayMetrics struct {
	// Session metrics
	ActiveSessions     int `json:"active_sessions"`
	ProcessingSessions int `json:"processing_sessions"`
	WaitingSessions    int `json:"waiting_sessions"`
	IdleSessions       int `json:"idle_sessions"`
	TotalSessions      int `json:"total_sessions"`

	// Request metrics
	QueueDepth        int   `json:"queue_depth"`
	PendingRequests   int   `json:"pending_requests"`
	CompletedRequests int64 `json:"completed_requests"`
	FailedRequests    int64 `json:"failed_requests"`

	// WebHook metrics (like TS Conduit)
	WebhookConnections int `json:"webhook_connections"`
	ActiveWebhooks     int `json:"active_webhooks"`

	// System metrics
	UptimeSeconds    int64   `json:"uptime_seconds"`
	MemoryUsageBytes int64   `json:"memory_usage_bytes"`
	MemoryUsageMB    float64 `json:"memory_usage_mb"`
	CPUUsagePercent  float64 `json:"cpu_usage_percent,omitempty"`
	GoroutineCount   int     `json:"goroutine_count"`

	// Gateway state
	Timestamp time.Time `json:"timestamp"`
	Status    string    `json:"status"` // "healthy", "degraded", "error"
	Version   string    `json:"version,omitempty"`

	// Internal fields for tracking
	mutex     sync.RWMutex `json:"-"`
	startTime time.Time    `json:"-"`
}

// SessionState is now defined in sessions package to avoid conflicts

// NewGatewayMetrics creates a new metrics instance
func NewGatewayMetrics() *GatewayMetrics {
	now := time.Now()
	return &GatewayMetrics{
		Timestamp: now,
		startTime: now,
		Status:    "healthy",
	}
}

// UpdateSessionCount updates session counters
func (g *GatewayMetrics) UpdateSessionCount(active, processing, waiting, idle, total int) {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	g.ActiveSessions = active
	g.ProcessingSessions = processing
	g.WaitingSessions = waiting
	g.IdleSessions = idle
	g.TotalSessions = total
	g.Timestamp = time.Now()
}

// UpdateQueueMetrics updates request queue metrics
func (g *GatewayMetrics) UpdateQueueMetrics(queueDepth, pending int) {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	g.QueueDepth = queueDepth
	g.PendingRequests = pending
	g.Timestamp = time.Now()
}

// IncrementCompleted increments the completed requests counter
func (g *GatewayMetrics) IncrementCompleted() {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	g.CompletedRequests++
	g.Timestamp = time.Now()
}

// IncrementFailed increments the failed requests counter
func (g *GatewayMetrics) IncrementFailed() {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	g.FailedRequests++
	g.Timestamp = time.Now()
}

// UpdateWebhookMetrics updates webhook-related metrics
func (g *GatewayMetrics) UpdateWebhookMetrics(connections, active int) {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	g.WebhookConnections = connections
	g.ActiveWebhooks = active
	g.Timestamp = time.Now()
}

// UpdateSystemMetrics updates system-level metrics
func (g *GatewayMetrics) UpdateSystemMetrics() {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	// Calculate uptime
	g.UptimeSeconds = int64(time.Since(g.startTime).Seconds())

	// Get memory statistics
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	g.MemoryUsageBytes = int64(memStats.Alloc)
	g.MemoryUsageMB = float64(memStats.Alloc) / 1024 / 1024

	// Get goroutine count
	g.GoroutineCount = runtime.NumGoroutine()

	g.Timestamp = time.Now()
}

// SetStatus updates the gateway status
func (g *GatewayMetrics) SetStatus(status string) {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	g.Status = status
	g.Timestamp = time.Now()
}

// SetVersion sets the gateway version
func (g *GatewayMetrics) SetVersion(version string) {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	g.Version = version
}

// MetricsSnapshot is a data-only copy of GatewayMetrics, safe to pass by value.
type MetricsSnapshot struct {
	ActiveSessions     int       `json:"active_sessions"`
	ProcessingSessions int       `json:"processing_sessions"`
	WaitingSessions    int       `json:"waiting_sessions"`
	IdleSessions       int       `json:"idle_sessions"`
	TotalSessions      int       `json:"total_sessions"`
	QueueDepth         int       `json:"queue_depth"`
	PendingRequests    int       `json:"pending_requests"`
	CompletedRequests  int64     `json:"completed_requests"`
	FailedRequests     int64     `json:"failed_requests"`
	WebhookConnections int       `json:"webhook_connections"`
	ActiveWebhooks     int       `json:"active_webhooks"`
	UptimeSeconds      int64     `json:"uptime_seconds"`
	MemoryUsageBytes   int64     `json:"memory_usage_bytes"`
	MemoryUsageMB      float64   `json:"memory_usage_mb"`
	CPUUsagePercent    float64   `json:"cpu_usage_percent,omitempty"`
	GoroutineCount     int       `json:"goroutine_count"`
	Timestamp          time.Time `json:"timestamp"`
	Status             string    `json:"status"`
	Version            string    `json:"version,omitempty"`
}

// Snapshot returns a thread-safe copy of the current metrics
func (g *GatewayMetrics) Snapshot() MetricsSnapshot {
	g.mutex.RLock()
	defer g.mutex.RUnlock()

	return MetricsSnapshot{
		ActiveSessions:     g.ActiveSessions,
		ProcessingSessions: g.ProcessingSessions,
		WaitingSessions:    g.WaitingSessions,
		IdleSessions:       g.IdleSessions,
		TotalSessions:      g.TotalSessions,
		QueueDepth:         g.QueueDepth,
		PendingRequests:    g.PendingRequests,
		CompletedRequests:  g.CompletedRequests,
		FailedRequests:     g.FailedRequests,
		WebhookConnections: g.WebhookConnections,
		ActiveWebhooks:     g.ActiveWebhooks,
		UptimeSeconds:      g.UptimeSeconds,
		MemoryUsageBytes:   g.MemoryUsageBytes,
		MemoryUsageMB:      g.MemoryUsageMB,
		CPUUsagePercent:    g.CPUUsagePercent,
		GoroutineCount:     g.GoroutineCount,
		Timestamp:          g.Timestamp,
		Status:             g.Status,
		Version:            g.Version,
	}
}

// IsHealthy returns true if the gateway appears to be healthy
func (g *GatewayMetrics) IsHealthy() bool {
	g.mutex.RLock()
	defer g.mutex.RUnlock()

	return g.Status == "healthy"
}

// GetUptime returns the uptime as a duration
func (g *GatewayMetrics) GetUptime() time.Duration {
	g.mutex.RLock()
	defer g.mutex.RUnlock()

	return time.Since(g.startTime)
}

// Reset resets counters (but preserves startTime)
func (g *GatewayMetrics) Reset() {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	g.ActiveSessions = 0
	g.ProcessingSessions = 0
	g.WaitingSessions = 0
	g.IdleSessions = 0
	g.TotalSessions = 0
	g.QueueDepth = 0
	g.PendingRequests = 0
	g.CompletedRequests = 0
	g.FailedRequests = 0
	g.WebhookConnections = 0
	g.ActiveWebhooks = 0
	g.MemoryUsageBytes = 0
	g.MemoryUsageMB = 0
	g.CPUUsagePercent = 0
	g.GoroutineCount = 0
	g.Status = "healthy"
	g.Timestamp = time.Now()
}
