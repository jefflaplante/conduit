package monitoring

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"conduit/internal/config"
)

// MetricsCollectorInterface defines the methods needed by HeartbeatService and Gateway
type MetricsCollectorInterface interface {
	IsIdle(duration time.Duration) bool
	MarkActivity()
	GetLastActivityTime() time.Time
	CollectMetrics(ctx context.Context) (*GatewayMetrics, error)
	DetectStuckSessions(ctx context.Context, threshold time.Duration) ([]string, error)
	ValidateDatabase(ctx context.Context) error
	UpdateWebSocketConnections(count int)
	UpdateActiveRequests(count int)
	UpdateQueueDepth(depth int)
	UpdateHeartbeatJobs(total, enabled int)
	GetHeartbeatMetrics() HeartbeatMetrics
}

// HeartbeatService manages the background heartbeat goroutine
type HeartbeatService struct {
	// Configuration
	config config.HeartbeatConfig

	// Dependencies
	collector  MetricsCollectorInterface
	eventStore EventStore

	// State management
	running bool
	mutex   sync.RWMutex
	cancel  context.CancelFunc
	done    chan struct{}

	// Metrics tracking
	heartbeatCount int64
	lastHeartbeat  time.Time
	errors         []error
}

// HeartbeatDependencies contains all dependencies needed for the heartbeat service
type HeartbeatDependencies struct {
	Config     config.HeartbeatConfig
	Collector  MetricsCollectorInterface
	EventStore EventStore
}

// NewHeartbeatService creates a new heartbeat service
func NewHeartbeatService(deps HeartbeatDependencies) *HeartbeatService {
	return &HeartbeatService{
		config:     deps.Config,
		collector:  deps.Collector,
		eventStore: deps.EventStore,
		done:       make(chan struct{}),
	}
}

// Start begins the heartbeat goroutine
func (h *HeartbeatService) Start(ctx context.Context) error {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	if h.running {
		return fmt.Errorf("heartbeat service is already running")
	}

	if !h.config.Enabled {
		log.Println("[Heartbeat] Service is disabled in configuration")
		return nil
	}

	// Validate configuration
	if err := h.config.Validate(); err != nil {
		return fmt.Errorf("invalid heartbeat configuration: %w", err)
	}

	// Create cancellable context for the heartbeat goroutine
	heartbeatCtx, cancel := context.WithCancel(ctx)
	h.cancel = cancel
	h.running = true
	h.done = make(chan struct{}) // Reinitialize for fresh start

	log.Printf("[Heartbeat] Starting service with %d second interval", h.config.IntervalSeconds)

	// Start the heartbeat goroutine
	go h.heartbeatLoop(heartbeatCtx)

	return nil
}

// Stop gracefully stops the heartbeat service
func (h *HeartbeatService) Stop() error {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	if !h.running {
		return nil // Already stopped
	}

	log.Println("[Heartbeat] Stopping service...")

	// Cancel the goroutine
	if h.cancel != nil {
		h.cancel()
	}

	// Wait for goroutine to finish with timeout
	select {
	case <-h.done:
		log.Println("[Heartbeat] Service stopped gracefully")
	case <-time.After(5 * time.Second):
		log.Println("[Heartbeat] Service stop timed out, forcing shutdown")
	}

	h.running = false
	return nil
}

// IsRunning returns true if the heartbeat service is currently running
func (h *HeartbeatService) IsRunning() bool {
	h.mutex.RLock()
	defer h.mutex.RUnlock()

	return h.running
}

// GetStats returns statistics about the heartbeat service
func (h *HeartbeatService) GetStats() map[string]interface{} {
	h.mutex.RLock()
	defer h.mutex.RUnlock()

	var lastError string
	if len(h.errors) > 0 {
		lastError = h.errors[len(h.errors)-1].Error()
	}

	return map[string]interface{}{
		"running":          h.running,
		"enabled":          h.config.Enabled,
		"interval_seconds": h.config.IntervalSeconds,
		"heartbeat_count":  h.heartbeatCount,
		"last_heartbeat":   h.lastHeartbeat,
		"last_error":       lastError,
		"error_count":      len(h.errors),
	}
}

// heartbeatLoop is the main goroutine that performs periodic heartbeats
func (h *HeartbeatService) heartbeatLoop(ctx context.Context) {
	defer close(h.done)

	// Create ticker for heartbeat interval
	ticker := time.NewTicker(h.config.Interval())
	defer ticker.Stop()

	// Immediate first heartbeat
	h.performHeartbeat(ctx)

	for {
		select {
		case <-ctx.Done():
			log.Println("[Heartbeat] Goroutine shutting down")
			return

		case <-ticker.C:
			h.performHeartbeat(ctx)
		}
	}
}

// performHeartbeat executes a single heartbeat cycle
func (h *HeartbeatService) performHeartbeat(ctx context.Context) {
	start := time.Now()

	// Check if system is idle (skip heartbeat if no activity for 2 minutes)
	idleThreshold := 2 * time.Minute
	if h.collector.IsIdle(idleThreshold) {
		h.logDebug("System is idle (>%v), skipping heartbeat", idleThreshold)
		return
	}

	// Collect current metrics
	metrics, err := h.collector.CollectMetrics(ctx)
	if err != nil {
		h.recordError(fmt.Errorf("failed to collect metrics: %w", err))
		return
	}

	// Update heartbeat tracking
	h.mutex.Lock()
	h.heartbeatCount++
	h.lastHeartbeat = time.Now()
	h.mutex.Unlock()

	// Log metrics at debug level
	if h.shouldLog("debug") {
		h.logDebug("Heartbeat #%d: %d active sessions, %d goroutines, %.1f MB memory",
			h.heartbeatCount,
			metrics.ActiveSessions,
			metrics.GoroutineCount,
			metrics.MemoryUsageMB,
		)
	}

	// Emit heartbeat event if enabled
	if h.config.EnableEvents {
		if err := h.emitHeartbeatEvent(ctx, metrics); err != nil {
			h.recordError(fmt.Errorf("failed to emit heartbeat event: %w", err))
		}
	}

	// Check for stuck sessions
	if err := h.checkStuckSessions(ctx); err != nil {
		h.recordError(fmt.Errorf("failed to check stuck sessions: %w", err))
	}

	// Validate database health
	if err := h.collector.ValidateDatabase(ctx); err != nil {
		h.recordError(fmt.Errorf("database health check failed: %w", err))
		h.emitSystemEvent("error", fmt.Sprintf("Database health check failed: %v", err))
	}

	duration := time.Since(start)
	if duration > time.Second {
		log.Printf("[Heartbeat] Cycle took %v (longer than expected)", duration)
	}
}

// emitHeartbeatEvent creates and stores a heartbeat event
func (h *HeartbeatService) emitHeartbeatEvent(ctx context.Context, metrics *GatewayMetrics) error {
	if h.eventStore == nil {
		return nil // No event store configured
	}

	// Create heartbeat event with metrics
	event := NewHeartbeatEventWithMetrics(metrics,
		fmt.Sprintf("Heartbeat #%d", h.heartbeatCount),
		"heartbeat_service")

	// Add context information
	event.AddContext("interval_seconds", fmt.Sprintf("%d", h.config.IntervalSeconds))
	event.AddContext("heartbeat_count", fmt.Sprintf("%d", h.heartbeatCount))

	// Store the event
	return h.eventStore.Store(event)
}

// checkStuckSessions looks for sessions that have been processing too long
func (h *HeartbeatService) checkStuckSessions(ctx context.Context) error {
	stuckThreshold := 2 * time.Minute
	stuckSessions, err := h.collector.DetectStuckSessions(ctx, stuckThreshold)
	if err != nil {
		return err
	}

	// Emit alerts for stuck sessions
	for _, sessionKey := range stuckSessions {
		h.emitSystemEvent("warning",
			fmt.Sprintf("Session %s appears stuck (processing >%v)", sessionKey, stuckThreshold))
	}

	return nil
}

// emitSystemEvent emits a system-level event
func (h *HeartbeatService) emitSystemEvent(severity, message string) {
	if !h.config.EnableEvents || h.eventStore == nil {
		return
	}

	var sev HeartbeatEventSeverity
	switch severity {
	case "info":
		sev = SeverityInfo
	case "warning":
		sev = SeverityWarning
	case "error":
		sev = SeverityError
	case "critical":
		sev = SeverityCritical
	default:
		sev = SeverityInfo
	}

	event := NewSystemEvent(sev, message, "heartbeat_service")
	if err := h.eventStore.Store(event); err != nil {
		log.Printf("[Heartbeat] Failed to store system event: %v", err)
	}
}

// recordError tracks errors from heartbeat operations
func (h *HeartbeatService) recordError(err error) {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	// Log the error
	log.Printf("[Heartbeat] Error: %v", err)

	// Store error (keep only last 10 errors)
	h.errors = append(h.errors, err)
	if len(h.errors) > 10 {
		h.errors = h.errors[len(h.errors)-10:]
	}

	// Emit error event
	h.emitSystemEvent("error", fmt.Sprintf("Heartbeat error: %v", err))
}

// shouldLog determines if a message should be logged at the given level
func (h *HeartbeatService) shouldLog(level string) bool {
	configLevel := h.config.LogLevel
	if configLevel == "" {
		configLevel = "info"
	}

	// Simple level hierarchy: debug < info < warn < error
	levels := map[string]int{
		"debug": 0,
		"info":  1,
		"warn":  2,
		"error": 3,
	}

	configLevelNum, exists := levels[configLevel]
	if !exists {
		configLevelNum = 1 // Default to info
	}

	requestLevelNum, exists := levels[level]
	if !exists {
		return false
	}

	return requestLevelNum >= configLevelNum
}

// logDebug logs a debug message if debug logging is enabled
func (h *HeartbeatService) logDebug(format string, args ...interface{}) {
	if h.shouldLog("debug") {
		log.Printf("[Heartbeat] "+format, args...)
	}
}

// ForceHeartbeat triggers an immediate heartbeat cycle (useful for testing)
func (h *HeartbeatService) ForceHeartbeat(ctx context.Context) error {
	if !h.IsRunning() {
		return fmt.Errorf("heartbeat service is not running")
	}

	h.performHeartbeat(ctx)
	return nil
}

// GetRecentEvents returns recent heartbeat events
func (h *HeartbeatService) GetRecentEvents(limit int) ([]*HeartbeatEvent, error) {
	if h.eventStore == nil {
		return nil, fmt.Errorf("no event store configured")
	}

	filter := EventFilter{
		MaxResults: limit,
	}

	return h.eventStore.Query(filter)
}

// GetRecentAlerts returns recent alert-level events
func (h *HeartbeatService) GetRecentAlerts(limit int) ([]*HeartbeatEvent, error) {
	if h.eventStore == nil {
		return nil, fmt.Errorf("no event store configured")
	}

	filter := EventFilter{
		Severity:   SeverityWarning, // This will get warning and above
		MaxResults: limit,
	}

	return h.eventStore.Query(filter)
}
